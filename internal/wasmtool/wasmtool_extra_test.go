package wasmtool_test

// Error-branch coverage for wasmtool: the load-time guards (required fields,
// schema compile, ABI verification, the capability gate) and the per-dispatch
// containment of a malformed/hostile guest (a bad allocator, a trapping run, an
// out-of-bounds result region). The wasm fixtures are compiled at test time via
// the same buildGuest helper the headline test uses, so they skip under -short.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/wasmtool"
)

// These two need no wasm build — they fail before the module is ever compiled.

func TestNew_RequiresName(t *testing.T) {
	if _, err := wasmtool.New(context.Background(), wasmtool.Config{Module: []byte("x")}); err == nil {
		t.Fatal("expected New to require a Name")
	}
}

func TestNew_RequiresModule(t *testing.T) {
	if _, err := wasmtool.New(context.Background(), wasmtool.Config{Name: "x"}); err == nil {
		t.Fatal("expected New to require Module bytes")
	}
}

func TestNew_RejectsBadParameterSchema(t *testing.T) {
	// Schema compilation happens before module compilation, so junk Module bytes
	// are fine — the invalid schema is what must fail New.
	_, err := wasmtool.New(context.Background(), wasmtool.Config{
		Name:       "x",
		Module:     []byte("not-wasm-but-unreached"),
		Parameters: json.RawMessage(`{ this is not json`),
	})
	if err == nil {
		t.Fatal("expected New to reject an unparseable parameters schema")
	}
	if !strings.Contains(err.Error(), "parameters schema") {
		t.Fatalf("error should point at the schema: %v", err)
	}
}

func TestNew_ABIMissingEntryPoints(t *testing.T) {
	ctx := context.Background()
	mod := buildGuest(t, "echomod") // exports aisotropy_alloc / aisotropy_run

	// Overriding the allocator entry name to one the module does not export makes
	// verifyABI fail with a clear, load-time error.
	_, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "echo",
		Module:       mod,
		EntryAlloc:   "nonexistent_alloc",
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err == nil || !strings.Contains(err.Error(), "nonexistent_alloc") {
		t.Fatalf("expected missing-allocator ABI error, got %v", err)
	}

	// Likewise for the run entry point.
	_, err = wasmtool.New(ctx, wasmtool.Config{
		Name:         "echo",
		Module:       mod,
		EntryRun:     "nonexistent_run",
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err == nil || !strings.Contains(err.Error(), "nonexistent_run") {
		t.Fatalf("expected missing-run ABI error, got %v", err)
	}
}

func TestNew_WASICapabilityDenied(t *testing.T) {
	// echomod (any Go wasip1 module) imports wasi_snapshot_preview1. Withholding
	// the WASI capability makes the load-time gate refuse it.
	_, err := wasmtool.New(context.Background(), wasmtool.Config{
		Name:         "echo",
		Module:       buildGuest(t, "echomod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: false},
	})
	if err == nil {
		t.Fatal("expected New to refuse a WASI-importing module when AllowWASI is false")
	}
	if !strings.Contains(err.Error(), "WASI capability is not granted") {
		t.Fatalf("error should name the WASI capability: %v", err)
	}
}

func TestNew_UngrantedHostModuleDenied(t *testing.T) {
	// evilmod imports env.evil — a host module the sandbox never exposes. Even
	// with WASI granted, the capability gate refuses the whole module.
	_, err := wasmtool.New(context.Background(), wasmtool.Config{
		Name:         "evil",
		Module:       buildGuest(t, "evilmod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err == nil {
		t.Fatal("expected New to refuse a module importing an ungranted host module")
	}
	if !strings.Contains(err.Error(), "ungranted host module") {
		t.Fatalf("error should name the ungranted host module: %v", err)
	}
}

func TestDispatch_BadAllocatorWriteContained(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "badalloc",
		Module:       buildGuest(t, "badallocmod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	_, err = tool.Handler()(ctx, json.RawMessage(`{"n":1}`))
	if err == nil {
		t.Fatal("expected dispatch to fail when the guest allocator returns an out-of-bounds offset")
	}
	if !strings.Contains(err.Error(), "guest memory") {
		t.Fatalf("error should describe the memory bounds failure: %v", err)
	}
}

func TestDispatch_RunTrapContained(t *testing.T) {
	ctx := context.Background()
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:         "misbehave",
		Module:       buildGuest(t, "misbehavemod"),
		Capabilities: wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tool.Close(ctx)

	// A trapping guest surfaces as a run-call error, not an in-process crash.
	if _, err := tool.Handler()(ctx, json.RawMessage(`{"mode":"trap"}`)); err == nil {
		t.Fatal("expected dispatch to return an error when the guest traps")
	}

	// A result region pointing outside guest memory is refused by the bounds check.
	_, err = tool.Handler()(ctx, json.RawMessage(`{"mode":"badread"}`))
	if err == nil {
		t.Fatal("expected dispatch to fail on an out-of-bounds result region")
	}
	if !strings.Contains(err.Error(), "out of guest memory bounds") {
		t.Fatalf("error should describe the out-of-bounds result region: %v", err)
	}
}
