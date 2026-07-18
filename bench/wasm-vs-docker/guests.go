package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Guests: the constant-work echo tool, built three ways.
// ---------------------------------------------------------------------------

// guestBuild is one compiled guest module plus provenance for the report. A
// guest that could not be built (missing toolchain) carries skipped=true and a
// reason, so the harness reports "SKIPPED: …" instead of faking a row.
type guestBuild struct {
	name       string // "go" | "tinygo" | "rust"
	toolchain  string // human provenance, e.g. "Go/wasip1 — bundles the Go runtime"
	module     []byte
	sizeBytes  int
	allowWASI  bool // go/tinygo import WASI; the rust no_std guest imports nothing
	skipped    bool
	skipReason string
}

// guestNames is the fixed order the guests are built and reported in: heaviest
// toolchain first (Go), leanest last (Rust).
var guestNames = []string{"go", "tinygo", "rust"}

// createTemp is a seam over os.CreateTemp so tests can force the "cannot
// create a scratch file for the compiler's -o" branch of each guest builder
// without needing a full disk or read-only temp dir.
var createTemp = os.CreateTemp

// buildGuestByName dispatches to the per-toolchain builder. Used both by the main
// run (all three) and by the RSS-isolation subprocess (one at a time).
func buildGuestByName(r cmdRunner, name string) guestBuild {
	switch name {
	case "go":
		return buildGoGuest(r)
	case "tinygo":
		return buildTinyGoGuest(r)
	case "rust":
		return buildRustGuest(r)
	default:
		return guestBuild{name: name, skipped: true, skipReason: "unknown guest name"}
	}
}

// goGuestDir is relative to the repo root (the documented invocation is
// `go run ./bench/wasm-vs-docker` from there). It is a var, not a const, so
// tests can point it at a fixture directory reachable from the package's own
// directory (the working directory `go test` uses) without touching the
// production default.
var goGuestDir = "bench/wasm-vs-docker/testdata/guest"

// buildGoGuest compiles the testdata guest to a wasip1 reactor .wasm — the
// existing baseline that bundles the whole Go runtime.
func buildGoGuest(r cmdRunner) guestBuild {
	g := guestBuild{name: "go", toolchain: "Go/wasip1 — bundles the Go runtime", allowWASI: true}
	if _, err := os.Stat(goGuestDir); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("guest dir %q not found (run from repo root)", goGuestDir)
		return g
	}
	out, err := createTemp("", "guest-go-*.wasm")
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	out.Close()
	defer os.Remove(out.Name())
	spec := cmdSpec{
		Name: "go",
		Args: []string{"build", "-buildmode=c-shared", "-o", out.Name(), "."},
		Dir:  goGuestDir,
		Env:  []string{"GOOS=wasip1", "GOARCH=wasm"},
	}
	if b, err := r.CombinedOutput(spec); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("go build: %v\n%s", err, b)
		return g
	}
	return finishGuest(g, out.Name())
}

// buildTinyGoGuest compiles the SAME source with TinyGo's LLVM backend, which
// does not bundle the full Go runtime — isolating the toolchain variable.
func buildTinyGoGuest(r cmdRunner) guestBuild {
	g := guestBuild{name: "tinygo", toolchain: "TinyGo/wasip1 — same source, LLVM backend, no full Go runtime", allowWASI: true}
	tinygo, err := r.LookPath("tinygo")
	if err != nil {
		g.skipped, g.skipReason = true, "`tinygo` not on PATH (install: brew install tinygo-org/tools/tinygo)"
		return g
	}
	out, err := createTemp("", "guest-tinygo-*.wasm")
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	out.Close()
	defer os.Remove(out.Name())
	// -target=wasip1 sets GOOS=wasip1, satisfying the guest's //go:build wasip1
	// tag. -buildmode=c-shared emits a REACTOR module exporting `_initialize`
	// (which the wasmtool host calls before dispatch); TinyGo's default wasip1
	// build is a COMMAND module whose runtime guard traps any //go:wasmexport
	// call made before `_start` runs, so the reactor mode is required for the ABI.
	spec := cmdSpec{
		Name: tinygo,
		Args: []string{"build", "-target=wasip1", "-buildmode=c-shared", "-o", out.Name(), "./" + goGuestDir},
	}
	if b, err := r.CombinedOutput(spec); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("tinygo build: %v\n%s", err, b)
		return g
	}
	return finishGuest(g, out.Name())
}

// rustGuestDir mirrors goGuestDir's test-override rationale.
var rustGuestDir = "bench/wasm-vs-docker/testdata/guest-rust"

// buildRustGuest compiles the #![no_std], dependency-free Rust guest with
// rustc's wasm32-wasip1 target — a few KiB, imports nothing.
func buildRustGuest(r cmdRunner) guestBuild {
	g := guestBuild{name: "rust", toolchain: "Rust/wasm32-wasip1 — #![no_std], -C opt-level=z, panic=abort, import-free", allowWASI: false}
	rustc := resolveRustc(r)
	if rustc == "" {
		g.skipped, g.skipReason = true, "no rustc with a wasm32-wasip1 target (set RUSTC=, or `rustup target add wasm32-wasip1`)"
		return g
	}
	src := filepath.Join(rustGuestDir, "guest.rs")
	if _, err := os.Stat(src); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("rust guest %q not found", src)
		return g
	}
	out, err := createTemp("", "guest-rust-*.wasm")
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	out.Close()
	defer os.Remove(out.Name())
	spec := cmdSpec{
		Name: rustc,
		Args: []string{
			"--target", "wasm32-wasip1",
			"-C", "opt-level=z", "--crate-type", "cdylib",
			"-C", "panic=abort", "-C", "strip=symbols",
			"-o", out.Name(), src,
		},
	}
	if b, err := r.CombinedOutput(spec); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("rustc (target wasm32-wasip1 std may be missing): %v\n%s", err, b)
		return g
	}
	return finishGuest(g, out.Name())
}

// finishGuest reads the built module bytes into the guestBuild.
func finishGuest(g guestBuild, path string) guestBuild {
	mod, err := os.ReadFile(path)
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	g.module, g.sizeBytes = mod, len(mod)
	return g
}

// resolveRustc finds a rustc that can target wasm32-wasip1. Order: $RUSTC, then
// `rustup which rustc` (the rustup shim resolves a toolchain that carries the
// cross-target's std), then a bare `rustc` on PATH (which — e.g. Homebrew's —
// may know the target triple but lack its std, in which case the build fails and
// the guest is skipped with the compiler error shown).
func resolveRustc(r cmdRunner) string {
	if v := strings.TrimSpace(os.Getenv("RUSTC")); v != "" {
		return v
	}
	if rustup, err := r.LookPath("rustup"); err == nil {
		if out, err := r.Output(cmdSpec{Name: rustup, Args: []string{"which", "rustc"}}); err == nil {
			if p := strings.TrimSpace(string(out)); p != "" {
				return p
			}
		}
	}
	if p, err := r.LookPath("rustc"); err == nil {
		return p
	}
	return ""
}

func wasiNote(allowWASI bool) string {
	if allowWASI {
		return "; imports WASI"
	}
	return "; imports nothing"
}
