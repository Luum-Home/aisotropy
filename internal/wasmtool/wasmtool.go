// Package wasmtool runs UNTRUSTED community tool code as a memory-isolated,
// capability-gated WebAssembly module and exposes it as an ordinary Aisotropy
// tool — the same []tools.Tool + typed dispatch handler shape native and MCP
// tools produce, so a wasm tool plugs into the identical registry and double
// scope-gate (ADR-0016's "grow the toolset without editing the core", taken to
// its strongest isolation posture).
//
// # Why WASM
//
// KindMCP runs a tool's behavior in a separate OS process (isolated, but the
// process has whatever the OS grants it). KindGoImport runs it in-process with
// the runtime's FULL trust. A community tool we do not trust needs something in
// between: it must run IN-PROCESS (µs dispatch, no subprocess, no ports) yet be
// unable to touch the host — no filesystem, no network, no clock, no ambient
// authority — reachable ONLY through host functions we explicitly hand it. That
// is exactly WebAssembly's sandbox. This package uses wazero
// (github.com/tetratelabs/wazero, Apache-2.0, pure Go, zero CGO) as the runtime:
// a guest module gets its own linear memory and can call NOTHING except the
// imports the host chooses to satisfy.
//
// # The ABI (host <-> guest contract)
//
// A wasm tool module MUST be a wasip1 *reactor* (or any module) that exports:
//
//   - a memory (conventionally "memory");
//   - aisotropy_alloc(size i32) -> (ptr i32): allocate `size` bytes of the
//     guest's OWN linear memory and return the offset. The host writes the JSON
//     argument bytes at that offset. The guest owns its allocator and must keep
//     the buffer alive until the matching aisotropy_run call reads it.
//   - aisotropy_run(ptr i32, len i32) -> (packed i64): read the JSON args at
//     [ptr,ptr+len), compute, and return a single i64 that packs the result
//     region as (resultPtr<<32)|resultLen. The host reads resultLen bytes at
//     resultPtr out of guest memory and treats them as the tool's UTF-8 result.
//
// Both directions are JSON, matching how native/typed and MCP tools already
// carry arguments and results. The entry-point names are configurable
// (Config.EntryAlloc/EntryRun) but default to the two above.
//
// # Capabilities (default: none)
//
// The guest's authority is whatever host imports the runner satisfies — nothing
// more. By default the aisotropy host namespace is EMPTY: a module that imports
// no host functions runs as pure compute. The only optional host function is
// `aisotropy.log` (Capabilities.Log); a guest that wants it must import it, and
// a guest that imports it CANNOT instantiate unless the deployer enabled it —
// that failure is the capability gate.
//
// wazero's WASI preview1 shim is instantiated by default (Capabilities.AllowWASI,
// on) because real modules produced by Go/TinyGo/Rust import
// wasi_snapshot_preview1 to initialize. This is NOT a capability escape: the
// runner configures WASI with no filesystem preopens, no environment, no args,
// and a deterministic (fake) clock, and wazero implements no networking at all.
// So even with WASI present the guest cannot read a host file, resolve an env
// var, open a socket, or read the real wall clock. The isolation test proves
// this by having the guest attempt a host file read and asserting it is denied.
//
// # Isolation & concurrency
//
// Each dispatch instantiates a FRESH guest instance with its own linear memory
// and closes it when the call returns, so no state — and no tenant's data —
// bleeds between calls (the constitutional multi-tenant isolation gate: two
// tenants' data never share an in-memory buffer). The request context is passed
// straight into the guest call and the runtime is built WithCloseOnContextDone,
// so a cancelled request (e.g. an aborted SSE stream) aborts the guest execution
// rather than letting it run on. tenant_id continues to travel ONLY via
// context.Context; the guest never sees it unless a host function the deployer
// wired chooses to surface it.
//
// # Dependency direction
//
// This package imports internal/tools (the tool shape), internal/api (the
// TypedToolHandler type and ToolArgsError), and internal/llm (schema
// validation) to reuse existing runtime shapes. It never imports the public
// aisotropy package — the public builder wires wasm tools in, not the reverse
// (the same rule internal/plugin follows).
package wasmtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/tetratelabs/wazero"
	wazeroapi "github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// Default ABI entry-point names. A module may override them via Config, but
// these are the convention the reference guest and docs use.
const (
	// DefaultEntryAlloc is the exported allocator: alloc(size i32) -> ptr i32.
	DefaultEntryAlloc = "aisotropy_alloc"
	// DefaultEntryRun is the exported entry point: run(ptr i32, len i32) ->
	// packed i64 = (resultPtr<<32)|resultLen.
	DefaultEntryRun = "aisotropy_run"

	// hostModule is the wasm module name under which capability-gated host
	// functions are exported to the guest.
	hostModule = "aisotropy"
	// hostLogFunc is the one optional host function: log(ptr i32, len i32).
	hostLogFunc = "log"
)

// Capabilities is the authority a wasm tool is granted. The zero value is the
// most restrictive USABLE posture the loader applies via defaults (see New):
// WASI present but powerless, and no aisotropy host functions. Every field only
// ever ADDS authority, so a reviewer can read a Capabilities value as the exact
// blast radius the guest can reach.
type Capabilities struct {
	// AllowWASI instantiates wazero's WASI preview1 host module so modules that
	// import wasi_snapshot_preview1 (anything the Go/TinyGo/Rust toolchains emit)
	// can instantiate. It grants NO filesystem (no preopens), NO environment, NO
	// program args, NO networking (wazero has none), and only a deterministic
	// clock — it is a load-time necessity, not a capability escape. New defaults
	// this to true; set it false to require a fully freestanding module with zero
	// host imports.
	AllowWASI bool

	// Log, when non-nil, exports the single host function aisotropy.log to the
	// guest. The guest passes a (ptr,len) pointing at a UTF-8 message in its own
	// memory; the runner reads it and calls this callback with the REQUEST
	// context (so the deployer can attribute the log to the ctx-carried tenant —
	// the guest cannot forge identity, it only supplies the message text). Nil
	// (the default) means the guest has no host functions at all: a module that
	// imports aisotropy.log will fail to instantiate, which is the capability
	// gate working as intended.
	Log func(ctx context.Context, message string)
}

// Config describes one wasm module to expose as a tool.
type Config struct {
	// Name is the tool name the model sees and the registry keys on (required).
	Name string
	// Description is the human/model-facing description.
	Description string
	// RequiredScope gates the tool exactly as tools.Tool.RequiredScope does:
	// empty means always available; otherwise the granted scopes must include it
	// or the tool is never injected for the model.
	RequiredScope string
	// Parameters is the OPTIONAL JSON Schema for the tool's argument object. When
	// present the runner validates the model's raw args against it and returns a
	// ToolArgsError (reflection-eligible, like any typed/MCP tool) on violation
	// before the args ever cross into the guest.
	Parameters json.RawMessage
	// Module is the raw .wasm bytes (required).
	Module []byte
	// EntryAlloc / EntryRun override the ABI entry-point names. Empty means the
	// DefaultEntry* constants.
	EntryAlloc string
	EntryRun   string
	// Capabilities is the authority granted to the guest.
	Capabilities Capabilities
}

// Tool is a compiled, ready-to-dispatch wasm tool. It owns a wazero.Runtime (the
// compiled module plus any host modules) and MUST be Closed to release it. A
// single Tool is safe for concurrent dispatch: each call gets its own guest
// instance.
type Tool struct {
	spec       tools.Tool
	runtime    wazero.Runtime
	compiled   wazero.CompiledModule
	validator  *llm.SchemaValidator
	entryAlloc string
	entryRun   string
}

// New compiles the module, verifies it satisfies the ABI (exports a memory and
// the two entry points), instantiates the granted host modules (WASI and/or the
// log function), and returns a Tool. It does NOT run any guest code — behavior
// only executes per dispatch, in a fresh instance. On any failure it closes the
// runtime it created so New never leaks resources.
func New(ctx context.Context, cfg Config) (*Tool, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("wasmtool: Name is required")
	}
	if len(cfg.Module) == 0 {
		return nil, fmt.Errorf("wasmtool %q: Module (wasm bytes) is required", cfg.Name)
	}
	entryAlloc := cfg.EntryAlloc
	if entryAlloc == "" {
		entryAlloc = DefaultEntryAlloc
	}
	entryRun := cfg.EntryRun
	if entryRun == "" {
		entryRun = DefaultEntryRun
	}

	validator, err := llm.CompileSchema(cfg.Parameters)
	if err != nil {
		return nil, fmt.Errorf("wasmtool %q: compile parameters schema: %w", cfg.Name, err)
	}

	// WithCloseOnContextDone: a cancelled request context aborts an in-flight
	// guest call instead of letting untrusted code run past the client hang-up.
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))

	// Any error past this point must close the runtime we just created.
	fail := func(err error) (*Tool, error) {
		_ = rt.Close(ctx)
		return nil, err
	}

	if cfg.Capabilities.AllowWASI {
		if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
			return fail(fmt.Errorf("wasmtool %q: instantiate WASI: %w", cfg.Name, err))
		}
	}
	if cfg.Capabilities.Log != nil {
		if err := instantiateHostModule(ctx, rt, cfg.Capabilities.Log); err != nil {
			return fail(fmt.Errorf("wasmtool %q: instantiate host module: %w", cfg.Name, err))
		}
	}

	compiled, err := rt.CompileModule(ctx, cfg.Module)
	if err != nil {
		return fail(fmt.Errorf("wasmtool %q: compile module: %w", cfg.Name, err))
	}
	if err := verifyABI(compiled, entryAlloc, entryRun); err != nil {
		return fail(fmt.Errorf("wasmtool %q: %w", cfg.Name, err))
	}
	// Capability gate at LOAD time: a module may only import host functions the
	// deployer actually granted. wazero would otherwise reject an unsatisfied
	// import at instantiation (per dispatch); checking here turns "the guest
	// wants authority it wasn't given" into a clear load-time refusal instead of
	// a request-time failure — and refuses BEFORE any guest code can run.
	if err := verifyImports(compiled, cfg.Capabilities); err != nil {
		return fail(fmt.Errorf("wasmtool %q: %w", cfg.Name, err))
	}

	return &Tool{
		spec: tools.Tool{
			Name:          cfg.Name,
			Description:   cfg.Description,
			RequiredScope: cfg.RequiredScope,
			Parameters:    cfg.Parameters,
		},
		runtime:    rt,
		compiled:   compiled,
		validator:  validator,
		entryAlloc: entryAlloc,
		entryRun:   entryRun,
	}, nil
}

// Spec returns the tool descriptor to merge into a tools.Registry — carrying the
// RequiredScope so the same scope gate native and MCP tools get applies to the
// wasm tool unchanged.
func (t *Tool) Spec() tools.Tool { return t.spec }

// Handler returns the typed dispatch handler to register under Spec().Name. It
// has the exact signature of every other typed/MCP tool handler, so a wasm tool
// is indistinguishable to the agent loop from any other tool.
func (t *Tool) Handler() api.TypedToolHandler { return t.dispatch }

// Close releases the runtime (compiled module and host modules). After Close the
// Tool must not be dispatched.
func (t *Tool) Close(ctx context.Context) error { return t.runtime.Close(ctx) }

// dispatch is the TypedToolHandler: validate args, instantiate a fresh isolated
// guest, run the ABI, and return the result. Fresh-instance-per-call keeps every
// dispatch (and every tenant) memory-isolated from every other.
func (t *Tool) dispatch(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	payload := rawArgs
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = json.RawMessage("{}")
	}
	if err := t.validator.Validate(payload); err != nil {
		return "", &api.ToolArgsError{Tool: t.spec.Name, Err: err}
	}

	// Anonymous module (WithName("")) so concurrent dispatches don't collide in
	// the runtime namespace. WithStartFunctions() disables auto-start; we call
	// the reactor initializer explicitly below so both reactor and freestanding
	// modules load the same way.
	mod, err := t.runtime.InstantiateModule(ctx, t.compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return "", fmt.Errorf("wasm tool %q: instantiate: %w", t.spec.Name, err)
	}
	defer mod.Close(ctx)

	if init := mod.ExportedFunction("_initialize"); init != nil {
		if _, err := init.Call(ctx); err != nil {
			return "", fmt.Errorf("wasm tool %q: _initialize: %w", t.spec.Name, err)
		}
	}

	mem := mod.Memory()
	if mem == nil {
		return "", fmt.Errorf("wasm tool %q: module exports no memory", t.spec.Name)
	}

	// 1) ask the guest to allocate room for the args and 2) write them there.
	argBytes := []byte(payload)
	allocRes, err := mod.ExportedFunction(t.entryAlloc).Call(ctx, uint64(len(argBytes)))
	if err != nil {
		return "", fmt.Errorf("wasm tool %q: %s: %w", t.spec.Name, t.entryAlloc, err)
	}
	if len(allocRes) != 1 {
		return "", fmt.Errorf("wasm tool %q: %s returned %d results, want 1", t.spec.Name, t.entryAlloc, len(allocRes))
	}
	inPtr := uint32(allocRes[0])
	if !mem.Write(inPtr, argBytes) {
		return "", fmt.Errorf("wasm tool %q: writing %d arg bytes at %d exceeds guest memory", t.spec.Name, len(argBytes), inPtr)
	}

	// 3) run; the result is a single packed i64 = (ptr<<32)|len.
	runRes, err := mod.ExportedFunction(t.entryRun).Call(ctx, uint64(inPtr), uint64(len(argBytes)))
	if err != nil {
		return "", fmt.Errorf("wasm tool %q: %s: %w", t.spec.Name, t.entryRun, err)
	}
	if len(runRes) != 1 {
		return "", fmt.Errorf("wasm tool %q: %s returned %d results, want 1 (packed ptr/len)", t.spec.Name, t.entryRun, len(runRes))
	}
	packed := runRes[0]
	outPtr := uint32(packed >> 32)
	outLen := uint32(packed)

	out, ok := mem.Read(outPtr, outLen)
	if !ok {
		return "", fmt.Errorf("wasm tool %q: result region [%d,%d) out of guest memory bounds", t.spec.Name, outPtr, outLen)
	}
	// Copy out of guest memory before the deferred Close reclaims the buffer.
	res := make([]byte, len(out))
	copy(res, out)
	return string(res), nil
}

// verifyABI fails fast at load if the compiled module does not satisfy the ABI:
// it must export a memory and both entry-point functions. Catching this at New
// turns a malformed plugin into a load-time error, not a request-time panic.
func verifyABI(compiled wazero.CompiledModule, entryAlloc, entryRun string) error {
	if len(compiled.ExportedMemories()) == 0 {
		return fmt.Errorf("module exports no memory (ABI requires an exported memory)")
	}
	fns := compiled.ExportedFunctions()
	if _, ok := fns[entryAlloc]; !ok {
		return fmt.Errorf("module does not export %q (the allocator entry point)", entryAlloc)
	}
	if _, ok := fns[entryRun]; !ok {
		return fmt.Errorf("module does not export %q (the run entry point)", entryRun)
	}
	return nil
}

// wasiModule is the WASI preview1 host-module name every Go/TinyGo/Rust wasip1
// module imports.
const wasiModule = "wasi_snapshot_preview1"

// verifyImports enforces the capability gate at load: every host function the
// module imports must belong to a module the deployer granted. WASI imports are
// allowed only when AllowWASI is set; the single aisotropy.log import is allowed
// only when Log is set. Any other import — a different host module, or a
// second aisotropy function we never export — is refused with the offending
// import named, so a guest cannot smuggle in authority it was not handed.
func verifyImports(compiled wazero.CompiledModule, caps Capabilities) error {
	for _, def := range compiled.ImportedFunctions() {
		mod, name, isImport := def.Import()
		if !isImport {
			continue
		}
		switch mod {
		case wasiModule:
			if !caps.AllowWASI {
				return fmt.Errorf("module imports %s.%s but the WASI capability is not granted", mod, name)
			}
		case hostModule:
			if caps.Log == nil || name != hostLogFunc {
				return fmt.Errorf("module imports %s.%s which is not a granted host function", mod, name)
			}
		default:
			return fmt.Errorf("module imports %s.%s from an ungranted host module (sandbox exposes no such capability)", mod, name)
		}
	}
	return nil
}

// instantiateHostModule wires the single capability-gated host function
// aisotropy.log into the runtime. The host function reads the guest's message
// bytes out of guest memory and forwards them to the deployer callback with the
// request context; a read that falls outside guest memory is dropped rather than
// trusted.
func instantiateHostModule(ctx context.Context, rt wazero.Runtime, log func(context.Context, string)) error {
	_, err := rt.NewHostModuleBuilder(hostModule).
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod wazeroapi.Module, ptr, length uint32) {
			b, ok := mod.Memory().Read(ptr, length)
			if !ok {
				return
			}
			// Copy before handing to the callback: the guest buffer is only
			// valid during this call.
			msg := make([]byte, len(b))
			copy(msg, b)
			log(ctx, string(msg))
		}).
		Export(hostLogFunc).
		Instantiate(ctx)
	return err
}
