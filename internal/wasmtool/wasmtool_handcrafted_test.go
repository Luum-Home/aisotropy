package wasmtool_test

// Load-time and dispatch-time edge cases the Go wasip1 toolchain cannot
// produce, because Go's own reactor output always exports a memory, always
// has a working _initialize, and only imports WASI functions the shim
// actually provides. Covering them needs hand-assembled WASM binaries instead
// of a testdata/*mod Go guest — three tiny (<150 byte) modules built directly
// with the WASM binary format's LEB128/section encoding, checked in under
// testdata/handcrafted/. Being pre-built (no `go build GOOS=wasip1`), these
// tests do NOT skip under -short.
//
//   - nomem.wasm: exports aisotropy_alloc/aisotropy_run (satisfying the ABI's
//     function requirement) but NO memory at all — verifyABI's "module exports
//     no memory" check requires a module that clears every OTHER ABI gate.
//   - initfail.wasm: exports memory + alloc/run + an "_initialize" function
//     whose body is a single `unreachable` instruction — proves a trapping
//     reactor initializer surfaces as a dispatch error, not a crash.
//   - badimport.wasm: exports memory + alloc/run and IMPORTS
//     wasi_snapshot_preview1.bogus_func, a name the real WASI shim never
//     exports. verifyImports' capability gate only checks the imported
//     MODULE name (wasi_snapshot_preview1) is granted — it has no way to know
//     the granted module doesn't implement this particular function name — so
//     New succeeds; the mismatch surfaces only when wazero actually resolves
//     imports at InstantiateModule time, at dispatch.
import (
	"context"
	"embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/wasmtool"
)

//go:embed testdata/handcrafted/*.wasm
var handcrafted embed.FS

func readHandcrafted(t *testing.T, name string) []byte {
	t.Helper()
	b, err := handcrafted.ReadFile("testdata/handcrafted/" + name)
	if err != nil {
		t.Fatalf("read embedded fixture %q: %v", name, err)
	}
	return b
}

// TestNew_RejectsModuleExportingNoMemory exercises verifyABI's memory check:
// a module can export both ABI functions and still be refused for exporting
// no memory at all.
func TestNew_RejectsModuleExportingNoMemory(t *testing.T) {
	_, err := wasmtool.New(context.Background(), wasmtool.Config{
		Name:   "nomem",
		Module: readHandcrafted(t, "nomem.wasm"),
	})
	if err == nil {
		t.Fatal("expected New to reject a module that exports no memory")
	}
	if !strings.Contains(err.Error(), "exports no memory") {
		t.Fatalf("error should name the missing memory export: %v", err)
	}
}

// TestDispatch_TrappingInitializeContained exercises dispatch's _initialize
// error branch: a reactor whose initializer traps must surface as a dispatch
// error, never an in-process crash, exactly like a trapping run call does.
func TestDispatch_TrappingInitializeContained(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:   "initfail",
		Module: readHandcrafted(t, "initfail.wasm"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	_, err = tool.Handler()(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected a trapping _initialize to surface as a dispatch error")
	}
	if !strings.Contains(err.Error(), "_initialize") {
		t.Fatalf("error should name _initialize: %v", err)
	}
}

// TestDispatch_UnsatisfiedGrantedImportFailsAtInstantiate exercises
// dispatch's InstantiateModule error branch: verifyImports' capability gate
// checks only that the IMPORTED MODULE (wasi_snapshot_preview1) is granted,
// not that the specific imported function name exists in it — a module
// importing a nonexistent WASI function name passes New but fails to
// instantiate on every dispatch.
func TestDispatch_UnsatisfiedGrantedImportFailsAtInstantiate(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "badimport",
		Module:       readHandcrafted(t, "badimport.wasm"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		t.Fatalf("New: %v (the capability gate only checks the module name, so this must succeed)", err)
	}
	defer tool.Close(ctx)

	_, err = tool.Handler()(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected instantiate to fail for an unsatisfied (if granted) import")
	}
	if !strings.Contains(err.Error(), "instantiate") {
		t.Fatalf("error should name the instantiate step: %v", err)
	}
}

// TestDispatch_AllocResultArityEnforced and
// TestDispatch_RunResultArityEnforced use the ordinary Go-toolchain
// testdata/*mod fixtures (built via buildGuest, so they DO skip under
// -short): a guest whose exported ABI function returns the wrong NUMBER of
// results (not just a bad value, which badallocmod/misbehavemod already
// cover) must be rejected at dispatch, not indexed out of bounds.

func TestDispatch_AllocResultArityEnforced(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "badallocresult",
		Module:       buildGuest(t, "badallocresultmod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	_, err = tool.Handler()(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected dispatch to reject an allocator exporting zero results")
	}
	if !strings.Contains(err.Error(), "want 1") {
		t.Fatalf("error should describe the arity mismatch: %v", err)
	}
}

func TestDispatch_RunResultArityEnforced(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "badrunresult",
		Module:       buildGuest(t, "badrunresultmod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	_, err = tool.Handler()(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected dispatch to reject a run entry point exporting zero results")
	}
	if !strings.Contains(err.Error(), "want 1") {
		t.Fatalf("error should describe the arity mismatch: %v", err)
	}
}

// TestTool_LogHostFunctionDropsOutOfBoundsRead exercises
// instantiateHostModule's own bounds check: a guest that calls aisotropy.log
// with a (ptr,len) outside its linear memory must have the call silently
// dropped (never surfaced to the deployer callback, and never a host-side
// out-of-bounds read), distinct from TestTool_LogCapabilityGranted's
// well-behaved call.
func TestTool_LogHostFunctionDropsOutOfBoundsRead(t *testing.T) {
	ctx := context.Background()
	var got string
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:   "badlog",
		Module: buildGuest(t, "badlogmod"),
		Capabilities: wasmtool.Capabilities{
			AllowWASI: true,
			Log:       func(_ context.Context, msg string) { got = msg },
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	res, err := tool.Handler()(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v (the out-of-bounds log call must be dropped, not fail the whole call)", err)
	}
	if got != "" {
		t.Fatalf("host log callback fired with %q from an out-of-bounds read — bounds check FAILED", got)
	}
	if !strings.Contains(res, "logged") {
		t.Fatalf("guest result = %q, want it to still complete normally", res)
	}
}
