package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.nWasm != 10000 || cfg.nDocker != 200 || cfg.coldRuns != 5 || cfg.image != "alpine:3.20" {
		t.Errorf("defaultConfig = %+v", cfg)
	}
	if cfg.skipDock {
		t.Error("defaultConfig.skipDock should be false")
	}
	if cfg.outPath != "bench/wasm-vs-docker/results.txt" {
		t.Errorf("defaultConfig.outPath = %q", cfg.outPath)
	}
}

func TestParseFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-n", "42", "-docker-n", "7", "-cold-runs", "3", "-image", "busybox", "-skip-docker", "-out", "/tmp/out.txt"})
	if err != nil {
		t.Fatalf("parseFlags: unexpected error: %v", err)
	}
	want := config{nWasm: 42, nDocker: 7, coldRuns: 3, image: "busybox", skipDock: true, outPath: "/tmp/out.txt"}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}

	cfg, err = parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): unexpected error: %v", err)
	}
	if cfg != defaultConfig() {
		t.Errorf("parseFlags(nil) = %+v, want defaults %+v", cfg, defaultConfig())
	}

	if _, err := parseFlags([]string{"-not-a-real-flag"}); err == nil {
		t.Error("parseFlags(bad flag): expected error, got nil")
	}

	cfg, err = parseFlags([]string{"-rss-only", "tinygo"})
	if err != nil {
		t.Fatalf("parseFlags(-rss-only): unexpected error: %v", err)
	}
	if cfg.rssOnly != "tinygo" {
		t.Errorf("cfg.rssOnly = %q", cfg.rssOnly)
	}
}

func TestRun_RSSOnlyBranch(t *testing.T) {
	orig := goGuestDir
	goGuestDir = "/does/not/exist" // forces the "go" guest to skip, taking the cheap rssOnlyMain path
	defer func() { goGuestDir = orig }()

	var out bytes.Buffer
	err := run([]string{"-rss-only=go"}, &out, &fakeRunner{}, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("run(-rss-only): unexpected error: %v", err)
	}
	if out.String() != "RSSDELTA_MIB=0.0\n" {
		t.Errorf("out = %q", out.String())
	}
}

func TestRun_ParseFlagsError(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"-does-not-exist"}, &out, &fakeRunner{}, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err == nil {
		t.Error("run(bad flag): expected error, got nil")
	}
}

func TestRun_DelegatesToRunBench(t *testing.T) {
	orig := goGuestDir
	goGuestDir = "/does/not/exist"
	defer func() { goGuestDir = orig }()
	origRust := rustGuestDir
	rustGuestDir = "/does/not/exist"
	defer func() { rustGuestDir = orig; rustGuestDir = origRust }()
	t.Setenv("RUSTC", "")

	outFile := filepath.Join(t.TempDir(), "results.txt")
	var out bytes.Buffer
	r := &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "", errors.New("nothing on PATH") },
	}
	err := run([]string{"-skip-docker", "-n=1", "-docker-n=1", "-cold-runs=1", "-out=" + outFile}, &out, r, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("run: unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "wasm-vs-docker benchmark") {
		t.Errorf("out missing header: %q", out.String())
	}
	if _, err := os.Stat(outFile); err != nil {
		t.Errorf("results file not written: %v", err)
	}
}

// buildAllRunner returns a fakeRunner that makes every external probe
// runBench issues succeed, so the full happy path (all 3 guests build, the
// CompilationCache benchmark runs on real-but-trivial wasm bytes, and the
// docker candidate is measured) is reachable without any real toolchain.
func buildAllRunner() *fakeRunner {
	return &fakeRunner{
		lookPathFn: func(file string) (string, error) {
			switch file {
			case "tinygo":
				return "/opt/homebrew/bin/tinygo", nil
			case "docker":
				return "/usr/local/bin/docker", nil
			}
			return "", fmt.Errorf("buildAllRunner: LookPath(%q) not stubbed", file)
		},
		combinedOutputFn: func(spec cmdSpec) ([]byte, error) {
			for _, a := range spec.Args {
				if strings.HasSuffix(a, ".wasm") {
					if err := os.WriteFile(a, emptyWasmModule, 0o644); err != nil {
						return nil, err
					}
				}
			}
			return nil, nil
		},
		outputFn: func(spec cmdSpec) ([]byte, error) {
			for _, a := range spec.Args {
				if strings.HasPrefix(a, "-rss-only=") {
					return []byte("RSSDELTA_MIB=1.2\n"), nil
				}
			}
			if spec.Name == "docker" {
				switch {
				case len(spec.Args) > 0 && spec.Args[0] == "version":
					return []byte("24.0.7"), nil
				case len(spec.Args) > 0 && spec.Args[0] == "stats":
					return []byte("3.9MiB / 512MiB"), nil
				}
			}
			if len(spec.Args) == 1 && spec.Args[0] == "version" {
				return []byte("tinygo version 0.31.0"), nil
			}
			if len(spec.Args) == 1 && spec.Args[0] == "--version" {
				return []byte("rustc 1.80.0"), nil
			}
			if spec.Name == "ps" {
				return []byte("102400"), nil
			}
			return nil, fmt.Errorf("buildAllRunner: Output(%+v) not stubbed", spec)
		},
		runFn: func(spec cmdSpec) error {
			if spec.Name == "docker" {
				return nil
			}
			return fmt.Errorf("buildAllRunner: Run(%+v) not stubbed", spec)
		},
	}
}

// setupGuestDirs points goGuestDir/rustGuestDir at real, existing fixtures so
// the os.Stat checks in buildGoGuest/buildRustGuest succeed, and sets RUSTC so
// resolveRustc short-circuits to a fixed value instead of probing rustup/rustc
// on PATH.
func setupGuestDirs(t *testing.T) {
	t.Helper()
	origGo, origRust := goGuestDir, rustGuestDir
	goGuestDir = t.TempDir()
	rustGuestDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(rustGuestDir, "guest.rs"), []byte("// stub guest"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUSTC", "/usr/bin/rustc")
	t.Cleanup(func() { goGuestDir, rustGuestDir = origGo, origRust })
}

func TestRunBench_FullSuccess_WithDocker(t *testing.T) {
	setupGuestDirs(t)
	outFile := filepath.Join(t.TempDir(), "results.txt")
	cfg := config{nWasm: 3, nDocker: 3, coldRuns: 1, image: "alpine:3.20", outPath: outFile}

	var out bytes.Buffer
	dockerProv := &fakeDockerProvider{}
	err := runBench(context.Background(), cfg, &out, buildAllRunner(), &fakeComputeProvider{}, dockerProv)
	if err != nil {
		t.Fatalf("runBench: unexpected error: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"go      ", "tinygo  ", "rust    ",
		"CompilationCache",
		"docker    |", // the docker candidate's result row (name padded)
		"THE VERDICT",
		"docker warm ops/s is over N=3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("runBench output missing %q; full output:\n%s", want, got)
		}
	}
	if b, err := os.ReadFile(outFile); err != nil || len(b) == 0 {
		t.Errorf("results file: err=%v len=%d", err, len(b))
	}
}

func TestRunBench_SkipDocker(t *testing.T) {
	setupGuestDirs(t)
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", skipDock: true, outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, buildAllRunner(), &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("runBench: unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "docker candidate SKIPPED (-skip-docker)") {
		t.Errorf("missing skip-docker note: %s", got)
	}
	if !strings.Contains(got, "docker    | (skipped") {
		t.Errorf("missing skipped table row: %s", got)
	}
	if strings.Contains(got, "docker warm ops/s is over N=") {
		t.Error("should not emit the docker-N note when docker was skipped")
	}
}

func TestRunBench_DockerCLINotFound(t *testing.T) {
	setupGuestDirs(t)
	r := buildAllRunner()
	r.lookPathFn = func(file string) (string, error) {
		if file == "tinygo" {
			return "/opt/homebrew/bin/tinygo", nil
		}
		return "", errors.New("docker not on PATH")
	}
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, r, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("runBench: unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "docker` CLI not found on PATH") {
		t.Errorf("missing docker-not-found note: %s", out.String())
	}
}

func TestRunBench_ImageNotAvailable(t *testing.T) {
	setupGuestDirs(t)
	r := buildAllRunner()
	r.runFn = func(spec cmdSpec) error { return errors.New("no such image") }
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, r, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("runBench: unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "not present locally") {
		t.Errorf("missing image-not-available note: %s", out.String())
	}
}

func TestRunBench_DockerBenchReturnsNil(t *testing.T) {
	setupGuestDirs(t)
	dockerProv := &fakeDockerProvider{createFn: func(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
		return nil, errors.New("create always fails")
	}}
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, buildAllRunner(), &fakeComputeProvider{}, dockerProv)
	if err != nil {
		t.Fatalf("runBench: unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "docker    | (skipped") {
		t.Errorf("expected the skipped docker row when benchDocker returns nil: %s", out.String())
	}
}

func TestRunBench_AllGuestsSkipped(t *testing.T) {
	origGo, origRust := goGuestDir, rustGuestDir
	goGuestDir = "/does/not/exist/go"
	rustGuestDir = "/does/not/exist/rust"
	t.Cleanup(func() { goGuestDir, rustGuestDir = origGo, origRust })
	t.Setenv("RUSTC", "")

	r := &fakeRunner{
		lookPathFn: func(file string) (string, error) { return "", errors.New("nothing on PATH") },
	}
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", skipDock: true, outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, r, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("runBench: unexpected error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"go      SKIPPED", "tinygo  SKIPPED", "rust    SKIPPED"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRunBench_BenchWASMError(t *testing.T) {
	setupGuestDirs(t)
	wasmProv := &fakeComputeProvider{loadFn: func(ctx context.Context, spec sandbox.ComputeSpec) (sandbox.ComputeInstance, error) {
		return nil, errors.New("wasm load boom")
	}}
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", skipDock: true, outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, buildAllRunner(), wasmProv, &fakeDockerProvider{})
	if err == nil || !strings.Contains(err.Error(), "Load") {
		t.Errorf("err = %v, want a wasm Load error", err)
	}
}

func TestRunBench_BenchCompileCacheError(t *testing.T) {
	setupGuestDirs(t)
	r := buildAllRunner()
	r.combinedOutputFn = func(spec cmdSpec) ([]byte, error) {
		for _, a := range spec.Args {
			if strings.HasSuffix(a, ".wasm") {
				// Invalid wasm bytes: benchWASM's fake provider ignores the
				// module content, but benchCompileCache calls the REAL
				// wazero CompileModule on it, so this forces that stage's
				// error branch inside runBench.
				if err := os.WriteFile(a, []byte{0x00, 0x00, 0x00, 0x00}, 0o644); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", skipDock: true, outPath: filepath.Join(t.TempDir(), "out.txt")}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, r, &fakeComputeProvider{}, &fakeDockerProvider{})
	if err == nil || !strings.Contains(err.Error(), "CompileModule") {
		t.Errorf("err = %v, want a CompileModule error", err)
	}
}

func TestRunBench_ResultsFileWriteFailure(t *testing.T) {
	setupGuestDirs(t)
	// A path under a nonexistent directory: os.WriteFile fails, and runBench
	// must still return nil (the write failure is a WARN, not a fatal error).
	badPath := filepath.Join(t.TempDir(), "no-such-subdir", "results.txt")
	cfg := config{nWasm: 1, nDocker: 1, coldRuns: 1, image: "alpine:3.20", skipDock: true, outPath: badPath}

	var out bytes.Buffer
	err := runBench(context.Background(), cfg, &out, buildAllRunner(), &fakeComputeProvider{}, &fakeDockerProvider{})
	if err != nil {
		t.Fatalf("runBench: unexpected error despite the write failure being non-fatal: %v", err)
	}
	if _, statErr := os.Stat(badPath); statErr == nil {
		t.Error("expected the results file to NOT exist given the missing parent dir")
	}
}
