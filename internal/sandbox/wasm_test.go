package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildComputeGuest compiles a testdata guest to a wasip1 reactor .wasm at test
// time (GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared) and returns its
// bytes — the same dependency-free approach internal/wasmtool's tests use (the
// Go toolchain can always target wasip1; no TinyGo/wabt needed). It shells out
// to `go build` (~1s, ~3.5MB module), so it skips under -short.
func buildComputeGuest(t *testing.T, dir string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wasm build under -short (shells out to `go build` for GOOS=wasip1)")
	}
	out := filepath.Join(t.TempDir(), "module.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = filepath.Join("testdata", dir)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build compute guest %q: %v\n%s", dir, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built wasm: %v", err)
	}
	return b
}

// TestWASMProvider_LoadDispatch is the headline: a pure-compute tool loaded via
// the WASM compute provider produces the right result AND is sandboxed (its host
// file read is denied).
func TestWASMProvider_LoadDispatch(t *testing.T) {
	ctx := context.Background()
	p := NewWASMProvider()
	if p.Name() != "wasm" {
		t.Fatalf("Name = %q, want wasm", p.Name())
	}

	inst, err := p.Load(ctx, ComputeSpec{
		Name:          "echo",
		Description:   "trivial compute tool",
		RequiredScope: "use:echo",
		Module:        buildComputeGuest(t, "computeecho"),
		AllowWASI:     true, // powerless WASI shim so the Go guest can initialize
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer inst.Close(ctx)

	if inst.Name() != "echo" {
		t.Fatalf("instance Name = %q, want echo", inst.Name())
	}

	res, err := inst.Dispatch(ctx, json.RawMessage(`{"name":"world","n":21}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var got struct {
		Greeting     string `json:"greeting"`
		Doubled      int    `json:"doubled"`
		FSReadDenied bool   `json:"fs_read_denied"`
	}
	if err := json.Unmarshal([]byte(res), &got); err != nil {
		t.Fatalf("unmarshal result %q: %v", res, err)
	}
	if got.Greeting != "hello world" {
		t.Errorf("greeting = %q, want %q", got.Greeting, "hello world")
	}
	if got.Doubled != 42 {
		t.Errorf("doubled = %d, want 42", got.Doubled)
	}
	if !got.FSReadDenied {
		t.Errorf("fs_read_denied = false, want true (sandbox must deny host file reads)")
	}
}

// TestWASMProvider_CanServe pins the routing boundary: only pure-compute tools
// may run on the WASM tier; shell/git/filesystem tools must route to a container
// Provider. This is the executable form of the design boundary (WASI has no
// fork/exec).
func TestWASMProvider_CanServe(t *testing.T) {
	p := NewWASMProvider()
	if !p.CanServe(NeedPureCompute) {
		t.Errorf("CanServe(NeedPureCompute) = false, want true")
	}
	if p.CanServe(NeedShellExec) {
		t.Errorf("CanServe(NeedShellExec) = true, want false (no fork/exec in WASI)")
	}
	if p.CanServe(NeedFilesystem) {
		t.Errorf("CanServe(NeedFilesystem) = true, want false (no ambient filesystem)")
	}
}

// TestWASMProvider_LoadValidation covers the cheap Load guards that do not need a
// wasm toolchain (so they run under -short too).
func TestWASMProvider_LoadValidation(t *testing.T) {
	ctx := context.Background()
	p := NewWASMProvider()

	if _, err := p.Load(ctx, ComputeSpec{Module: []byte{0}}); err == nil {
		t.Errorf("Load with empty Name: want error, got nil")
	}
	if _, err := p.Load(ctx, ComputeSpec{Name: "x"}); err == nil {
		t.Errorf("Load with empty Module: want error, got nil")
	}
}
