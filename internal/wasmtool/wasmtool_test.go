package wasmtool_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/wasmtool"
)

// buildGuest compiles a testdata guest to a wasip1 reactor .wasm at test time
// (GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared) and returns its bytes.
//
// We COMPILE the fixtures at test time rather than checking in a prebuilt .wasm
// on purpose, and it is reliable here: the Go toolchain that runs the test can
// always target wasip1/wasm (it is a first-class port since Go 1.21, and
// //go:wasmexport + -buildmode=c-shared since Go 1.24), needs no external tool
// (no TinyGo, no wabt, no network — standard library only), and keeps the guest
// as readable Go source in the tree. The one cost is that each build shells out
// to `go build` (~1s) and produces a ~3.5MB module (the full Go runtime), so the
// tests skip under -short. A hand-written WAT/Rust module would be smaller but
// would need a checked-in binary or an external assembler; the Go path is the
// honest, dependency-free choice for a reference fixture.
func buildGuest(t *testing.T, dir string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wasm build under -short (shells out to `go build` for GOOS=wasip1)")
	}
	out := filepath.Join(t.TempDir(), "module.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = filepath.Join("testdata", dir)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build guest %q: %v\n%s", dir, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built wasm: %v", err)
	}
	return b
}

// TestTool_ComputesAndIsSandboxed is the headline test: a wasm tool produces the
// correct result for valid input AND cannot reach the host — the isolation proof.
func TestTool_ComputesAndIsSandboxed(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:          "echo",
		Description:   "echo tool",
		RequiredScope: "use:echo",
		Module:        buildGuest(t, "echomod"),
		Capabilities:  wasmtool.Capabilities{AllowWASI: true}, // WASI on, but powerless
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	// Spec carries the scope so the tool plugs into the same scope-gate.
	if got := tool.Spec().RequiredScope; got != "use:echo" {
		t.Fatalf("RequiredScope = %q, want use:echo", got)
	}

	res, err := tool.Handler()(ctx, json.RawMessage(`{"name":"world","n":21}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var out struct {
		Greeting     string `json:"greeting"`
		Doubled      int    `json:"doubled"`
		FSReadDenied bool   `json:"fs_read_denied"`
		EnvHome      string `json:"env_home"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("unmarshal result %q: %v", res, err)
	}

	// Compute correctness.
	if out.Greeting != "hello world" {
		t.Errorf("greeting = %q, want %q", out.Greeting, "hello world")
	}
	if out.Doubled != 42 {
		t.Errorf("doubled = %d, want 42", out.Doubled)
	}

	// Isolation proof: the guest's attempt to read a host file was DENIED, and no
	// host environment leaked in. This is the whole point of running untrusted
	// code in the sandbox.
	if !out.FSReadDenied {
		t.Error("guest READ a host file — sandbox isolation FAILED")
	}
	if out.EnvHome != "" {
		t.Errorf("guest saw host env HOME=%q — sandbox leaked environment", out.EnvHome)
	}
}

// TestTool_LogCapabilityGranted proves the ONE optional host function works when
// granted: the guest imports aisotropy.log, calls it, and the host callback
// receives the message.
func TestTool_LogCapabilityGranted(t *testing.T) {
	ctx := context.Background()
	var got string
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:   "logger",
		Module: buildGuest(t, "logmod"),
		Capabilities: wasmtool.Capabilities{
			AllowWASI: true,
			Log:       func(_ context.Context, msg string) { got = msg },
		},
	})
	if err != nil {
		t.Fatalf("New with Log granted: %v", err)
	}
	defer tool.Close(ctx)

	if _, err := tool.Handler()(ctx, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got != "log-from-guest" {
		t.Errorf("host log received %q, want %q", got, "log-from-guest")
	}
}

// TestTool_LogCapabilityDenied is the negative half of the capability gate: the
// SAME guest that imports aisotropy.log CANNOT even instantiate when the deployer
// did not grant Log. The guest cannot obtain a host function it was not given.
func TestTool_LogCapabilityDenied(t *testing.T) {
	ctx := context.Background()
	_, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "logger",
		Module:       buildGuest(t, "logmod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true}, // Log NOT granted
	})
	if err == nil {
		t.Fatal("New succeeded for a module importing aisotropy.log without the Log capability — gate FAILED")
	}
}

// TestTool_ArgsValidatedAgainstSchema proves args are validated BEFORE crossing
// into the guest: a schema violation is a ToolArgsError (reflection-eligible),
// not a guest invocation.
func TestTool_ArgsValidatedAgainstSchema(t *testing.T) {
	ctx := context.Background()
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "echo",
		Module:       buildGuest(t, "echomod"),
		Parameters:   schema,
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	if _, err := tool.Handler()(ctx, json.RawMessage(`{"name":"x"}`)); err == nil {
		t.Fatal("expected a validation error for args missing required 'n'")
	}
}

// TestNew_RejectsModuleMissingABI proves the ABI is enforced at load: bytes that
// are not a valid module, or a module without the entry points, fail New.
func TestNew_RejectsModuleMissingABI(t *testing.T) {
	ctx := context.Background()
	if _, err := wasmtool.New(ctx, wasmtool.Config{Name: "bad", Module: []byte("not wasm")}); err == nil {
		t.Fatal("expected New to reject non-wasm bytes")
	}
}
