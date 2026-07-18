// wasmhost.go runs a tool body inside a wazero WebAssembly sandbox, reproducing
// aisotropy's internal/wasmtool ABI and isolation posture. The arena adapter is a
// SEPARATE Go module, so it cannot import github.com/Luum-Home/aisotropy/internal/
// wasmtool (Go forbids cross-module internal/ imports). This file therefore honors
// the SAME host<->guest contract that package documents, byte-for-byte:
//
//   - the guest exports linear memory, aisotropy_alloc(size)->ptr, and
//     aisotropy_run(ptr,len)->packed i64 = (resultPtr<<32)|resultLen;
//   - a FRESH guest instance per dispatch (its own linear memory, closed after),
//     so no state bleeds between calls;
//   - memory is HARD-bounded (WithMemoryLimitPages) and CPU is bounded by a
//     deadline (WithCloseOnContextDone) — the exact DoS bounds wasmtool documents;
//   - WASI preview1 is instantiated but POWERLESS: no filesystem preopens, no env,
//     no args, no networking (wazero has none). So even with WASI present the guest
//     cannot read a host file, resolve an env var, or open a socket — which is the
//     honest reason the guest can never see the agnes API key the host holds.
//
// wazero (github.com/tetratelabs/wazero, Apache-2.0, pure Go, zero CGO) is already
// a transitive dependency of the aisotropy module the adapter embeds.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	wasmEntryAlloc = "aisotropy_alloc"
	wasmEntryRun   = "aisotropy_run"

	// wasmMemoryLimitPages caps the guest's linear memory at 256 MiB
	// (4096 pages * 64 KiB), down from wazero's 4 GiB ceiling — the same default
	// wasmtool.DefaultMemoryLimitPages applies.
	wasmMemoryLimitPages uint32 = 4096
	// wasmCallTimeout bounds a single dispatch's wall-clock CPU time, matching
	// wasmtool.DefaultCallTimeout. wazero has no CPU metering, so this deadline
	// (via WithCloseOnContextDone) is the only bound on a spinning guest.
	wasmCallTimeout = 5 * time.Second
)

// wasmTool is a compiled, ready-to-dispatch WASM tool. Construct with newWASMTool
// and Close it when done. A single wasmTool is safe for sequential dispatch here.
type wasmTool struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
}

// newWASMTool compiles the module, instantiates the powerless WASI host, and
// verifies the ABI (an exported memory plus the two entry points). It runs no
// guest code — behavior only executes per dispatch, in a fresh instance.
func newWASMTool(ctx context.Context, module []byte) (*wasmTool, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(wasmMemoryLimitPages))

	fail := func(err error) (*wasmTool, error) {
		_ = rt.Close(ctx)
		return nil, err
	}

	// WASI present but powerless (no preopens/env/args, wazero has no net): a
	// load-time necessity for Go/TinyGo/Rust modules, NOT a capability escape.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return fail(fmt.Errorf("wasmhost: instantiate WASI: %w", err))
	}

	compiled, err := rt.CompileModule(ctx, module)
	if err != nil {
		return fail(fmt.Errorf("wasmhost: compile module: %w", err))
	}
	if len(compiled.ExportedMemories()) == 0 {
		return fail(fmt.Errorf("wasmhost: module exports no memory (ABI requires one)"))
	}
	fns := compiled.ExportedFunctions()
	if _, ok := fns[wasmEntryAlloc]; !ok {
		return fail(fmt.Errorf("wasmhost: module does not export %q", wasmEntryAlloc))
	}
	if _, ok := fns[wasmEntryRun]; !ok {
		return fail(fmt.Errorf("wasmhost: module does not export %q", wasmEntryRun))
	}
	return &wasmTool{runtime: rt, compiled: compiled}, nil
}

func (t *wasmTool) Close(ctx context.Context) error { return t.runtime.Close(ctx) }

// dispatch instantiates a FRESH isolated guest, hands it the JSON args through
// the ABI (alloc -> write -> run), reads the JSON result back out of guest
// memory, and returns it. Fresh-instance-per-call keeps every dispatch
// memory-isolated. The per-call deadline is the CPU bound.
func (t *wasmTool) dispatch(ctx context.Context, argsJSON []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	// Anonymous module so sequential dispatches don't collide in the namespace;
	// WithStartFunctions() disables auto-start — we call _initialize explicitly.
	mod, err := t.runtime.InstantiateModule(ctx, t.compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("wasmhost: instantiate: %w", err)
	}
	defer mod.Close(ctx)

	if init := mod.ExportedFunction("_initialize"); init != nil {
		if _, err := init.Call(ctx); err != nil {
			return nil, fmt.Errorf("wasmhost: _initialize: %w", err)
		}
	}

	mem := mod.Memory()
	if mem == nil {
		return nil, fmt.Errorf("wasmhost: module exports no memory at runtime")
	}

	// 1) ask the guest to allocate room for the args, 2) write them there.
	allocRes, err := mod.ExportedFunction(wasmEntryAlloc).Call(ctx, uint64(len(argsJSON)))
	if err != nil {
		return nil, fmt.Errorf("wasmhost: %s: %w", wasmEntryAlloc, err)
	}
	if len(allocRes) != 1 {
		return nil, fmt.Errorf("wasmhost: %s returned %d results, want 1", wasmEntryAlloc, len(allocRes))
	}
	inPtr := uint32(allocRes[0])
	if !mem.Write(inPtr, argsJSON) {
		return nil, fmt.Errorf("wasmhost: writing %d arg bytes at %d exceeds guest memory", len(argsJSON), inPtr)
	}

	// 3) run; result is a single packed i64 = (ptr<<32)|len.
	runRes, err := mod.ExportedFunction(wasmEntryRun).Call(ctx, uint64(inPtr), uint64(len(argsJSON)))
	if err != nil {
		return nil, fmt.Errorf("wasmhost: %s: %w", wasmEntryRun, err)
	}
	if len(runRes) != 1 {
		return nil, fmt.Errorf("wasmhost: %s returned %d results, want 1", wasmEntryRun, len(runRes))
	}
	packed := runRes[0]
	outPtr := uint32(packed >> 32)
	outLen := uint32(packed)

	out, ok := mem.Read(outPtr, outLen)
	if !ok {
		return nil, fmt.Errorf("wasmhost: result region [%d,%d) out of guest memory bounds", outPtr, outLen)
	}
	// Copy out before the deferred Close reclaims the buffer.
	res := make([]byte, len(out))
	copy(res, out)
	return res, nil
}

// wasmCalcResult is the guest's JSON result shape (see testdata/wasmguest).
type wasmCalcResult struct {
	Engine       string   `json:"engine"`
	Result       *float64 `json:"result"`
	Error        string   `json:"error"`
	FSReadDenied bool     `json:"fs_read_denied"`
}

// runWASMCalc dispatches the arithmetic expression into the sandboxed guest and
// decodes its result.
func (t *wasmTool) runWASMCalc(ctx context.Context, expression string) (wasmCalcResult, error) {
	args, err := json.Marshal(map[string]string{"expression": expression})
	if err != nil {
		return wasmCalcResult{}, err
	}
	raw, err := t.dispatch(ctx, args)
	if err != nil {
		return wasmCalcResult{}, err
	}
	var res wasmCalcResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return wasmCalcResult{}, fmt.Errorf("wasmhost: decode guest result %q: %w", string(raw), err)
	}
	return res, nil
}
