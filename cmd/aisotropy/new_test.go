package main

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewScaffold exercises the acceptance path: `new <dir>` writes the full
// skeleton into a temp directory, every expected file lands, and the generated
// main.go references the real public symbols a developer relies on.
func TestNewScaffold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mybot")

	var out, errb bytes.Buffer
	if code := run([]string{"new", dir}, &out, &errb); code != 0 {
		t.Fatalf("new exit=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "scaffolded") {
		t.Fatalf("stdout missing scaffold summary: %q", out.String())
	}
	// The next-steps hint must point the developer at the running dev chat.
	for _, want := range []string{"go mod tidy", "go run .", "http://localhost:8080/ui"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("stdout missing next-step %q:\n%s", want, out.String())
		}
	}

	// Every scaffold file must exist and be non-empty.
	for _, name := range []string{"main.go", "go.mod", "README.md", ".gitignore"} {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected scaffold file %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("scaffold file %s is empty", name)
		}
	}

	// The generated main.go must reference the real public API — the whole point
	// of a runnable skeleton. Assert on the exact symbols the task names.
	mainSrc, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(mainSrc)
	for _, sym := range []string{
		`"github.com/Luum-Home/aisotropy/aisotropy"`,
		"aisotropy.New(",
		"aisotropy.WithPersona(",
		"aisotropy.WithTypedTool(",
		"aisotropy.WithDevUI()",
		"aisotropy.WithAuthenticator(aisotropy.NoAuth())",
		"app.Run(",
	} {
		if !strings.Contains(src, sym) {
			t.Fatalf("generated main.go missing reference to %q:\n%s", sym, src)
		}
	}

	// The generated go.mod must carry the module derived from the directory base.
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module mybot") {
		t.Fatalf("go.mod missing derived module line:\n%s", string(gomod))
	}
}

// TestNewGeneratedMainIsGofmt guarantees the scaffolded main.go is already
// gofmt-clean, so a developer's editor/CI does not immediately flag the file the
// tool just produced. It formats the generated source and asserts it is a no-op.
func TestNewGeneratedMainIsGofmt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fmtbot")
	var out, errb bytes.Buffer
	if code := run([]string{"new", dir}, &out, &errb); code != 0 {
		t.Fatalf("new exit=%d stderr=%q", code, errb.String())
	}

	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(src)
	if err != nil {
		t.Fatalf("generated main.go does not parse: %v", err)
	}
	if !bytes.Equal(src, formatted) {
		t.Fatalf("generated main.go is not gofmt-clean; diff between generated and gofmt'd output")
	}
}

// TestNewModuleFlag confirms --module overrides the directory-derived name.
func TestNewModuleFlag(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "somedir")
	var out, errb bytes.Buffer
	if code := run([]string{"new", dir, "--module", "github.com/me/coolbot"}, &out, &errb); code != 0 {
		t.Fatalf("new exit=%d stderr=%q", code, errb.String())
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module github.com/me/coolbot") {
		t.Fatalf("go.mod missing overridden module line:\n%s", string(gomod))
	}
}

// TestNewRefusesNonEmptyTarget confirms `new` will not clobber an existing,
// non-empty directory.
func TestNewRefusesNonEmptyTarget(t *testing.T) {
	dir := t.TempDir() // t.TempDir already exists and we drop a file in it
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{"new", dir}, &out, &errb)
	if code != 1 {
		t.Fatalf("expected exit 1 for non-empty target, got %d (stderr=%q)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "not empty") {
		t.Fatalf("expected not-empty error, got stderr=%q", errb.String())
	}
}

// TestNewMissingDir confirms the usage error (exit 2) when no directory is given.
func TestNewMissingDir(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"new"}, &out, &errb)
	if code != 2 {
		t.Fatalf("expected exit 2 for missing dir, got %d", code)
	}
	if !strings.Contains(errb.String(), "Usage:") {
		t.Fatalf("expected usage on missing dir, got stderr=%q", errb.String())
	}
}
