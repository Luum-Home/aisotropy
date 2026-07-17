// wasm.go adds the WASM pure-compute sibling of the container "hands" tier: a
// [WASMProvider] that runs an untrusted JSON->JSON tool as a wazero-sandboxed
// WebAssembly module (reusing internal/wasmtool and ITS memory + deadline
// bounds), giving µs-scale create/dispatch instead of container-ms for the
// subset of tools that need NO shell, git, or filesystem.
//
// # Why this is a SEPARATE seam, not a drop-in [Provider]
//
// The container [Provider]/[Sandbox] pair is the agent's HANDS: its defining
// method is Exec(argv) — "run this argv against a real filesystem". A WASM guest
// fundamentally CANNOT honor that contract: WASI (any preview) offers no
// fork/exec, no process creation, and no general filesystem — that is a design
// boundary of the WebAssembly Component Model, not a temporary gap (see
// docs/research/wasm-vs-docker-sandbox-2026-07-17.md §2b, and the ADR-0035
// package doc on internal/wasmtool). So a WASM backend that pretended to
// implement [Sandbox].Exec would be a broken Provider whose only honest reply to
// any argv is "cannot exec." Instead this file models WASM as what it actually
// is — a pure-compute dispatch tier — via the [ComputeProvider]/[ComputeInstance]
// pair, the JSON->JSON sibling of the argv-shaped [Provider]/[Sandbox]. Both are
// on the same execution-isolation axis (ADR-0038); they serve different
// workloads and neither replaces the other.
//
// Routing is the caller's job: a tool whose capability need is pure computation
// ([NeedPureCompute]) may run here for the cheap fast path; a tool that needs a
// shell, git, or the filesystem ([NeedShellExec]/[NeedFilesystem]) MUST route to
// a container [Provider] (Docker/Podman). [WASMProvider.CanServe] makes that
// boundary executable and testable so a mis-route is a checked refusal, not a
// silent failure.
//
// # What it reuses (no new dependency, no new runtime bounds)
//
// [WASMProvider.Load] delegates straight to wasmtool.New, so the guest inherits
// the exact isolation and DoS posture the plugin sandbox already ships: fresh
// instance per dispatch (multi-tenant isolation), a hard linear-memory cap
// (WithMemoryLimitPages) and a per-call wall-clock deadline (the CPU bound wazero
// can offer — it has no fuel/epoch metering). The broker invariant still holds:
// like a container [Provider], a [WASMProvider] is never handed and never reads a
// provider API key — a wasm guest's only authority is the host imports the
// deployer granted (default: none).
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Luum-Home/aisotropy/internal/wasmtool"
)

// ErrNoShellExec is returned by helpers that are asked to run shell/argv or
// filesystem work on the WASM compute tier. It names the design boundary — WASI
// has no fork/exec — so a caller that reaches it knows to route the tool to a
// container [Provider] (Docker/Podman) instead of treating it as a transient
// error.
var ErrNoShellExec = errors.New("sandbox: the WASM compute tier cannot run shell/argv/filesystem work (WASI has no fork/exec); route this tool to a container Provider")

// ToolNeed classifies the host authority a tool call requires, so a router can
// decide which isolation tier can serve it. It is the single fact that
// determines WASM-eligibility: only pure computation stays in-process on wazero;
// everything else needs a real OS process in a container.
type ToolNeed int

const (
	// NeedPureCompute is a JSON->JSON tool with no side effects beyond the
	// granted host imports (default none). This is the ONLY need the WASM
	// compute tier can serve.
	NeedPureCompute ToolNeed = iota
	// NeedShellExec is a tool that must run argv / a shell / git — a real OS
	// process. WASI has no fork/exec, so this MUST route to a container Provider.
	NeedShellExec
	// NeedFilesystem is a tool that must read or write the host filesystem
	// outside a sandboxed, capability-scoped handle. WASI preview 1 (wazero's
	// surface) grants no ambient filesystem, so this routes to a container
	// Provider too.
	NeedFilesystem
)

// ComputeSpec is the NON-SECRET recipe a [ComputeProvider] compiles into a
// [ComputeInstance]. It mirrors the container [Spec]'s "everything the backend
// needs, and nothing it must not have" discipline, but for a WASM guest: the
// module bytes plus its capability grant and DoS bounds. Like [Spec] it carries
// no provider API key — the broker invariant holds for the compute tier too.
type ComputeSpec struct {
	// Name is the tool name the registry keys on and the model sees (required).
	Name string
	// Description is the human/model-facing description.
	Description string
	// RequiredScope gates the tool exactly as tools.Tool.RequiredScope does:
	// empty means always available; otherwise the granted scopes must include it.
	RequiredScope string
	// Parameters is the OPTIONAL JSON Schema for the argument object; when present
	// the runner validates the model's raw args against it before they cross into
	// the guest.
	Parameters json.RawMessage
	// Module is the raw .wasm bytes of a wasip1 reactor exporting the ABI
	// (aisotropy_alloc / aisotropy_run) — required. See internal/wasmtool.
	Module []byte

	// AllowWASI instantiates wazero's powerless WASI preview-1 shim so modules
	// produced by Go/TinyGo/Rust can initialize (no filesystem, no env, no net,
	// deterministic clock). Nearly every real guest needs this; it is NOT a
	// capability escape. Default recipes set it true.
	AllowWASI bool
	// Log, when non-nil, exports the single optional host function aisotropy.log
	// to the guest (message text only; identity stays host-side). Nil means the
	// guest has no host functions at all.
	Log func(ctx context.Context, message string)

	// MemoryLimitPages caps the guest's linear memory (1 page = 64 KiB). Zero
	// applies wasmtool.DefaultMemoryLimitPages (256 MiB) — the same hard memory
	// bound the plugin sandbox uses.
	MemoryLimitPages uint32
	// CallTimeout bounds a single dispatch's wall-clock time (the CPU bound, since
	// wazero has no metering). Zero applies wasmtool.DefaultCallTimeout (5s).
	CallTimeout time.Duration
}

// ComputeInstance is one compiled, ready-to-dispatch WASM tool — the pure-compute
// analogue of a live [Sandbox], but its unit of work is a JSON->JSON dispatch,
// not an argv Exec. A single instance is safe for concurrent Dispatch: each call
// gets its own fresh guest instance (the multi-tenant isolation gate). It MUST be
// Closed to release the underlying wazero runtime.
type ComputeInstance interface {
	// Dispatch validates argsJSON against the tool's schema, instantiates a fresh
	// isolated guest, runs the ABI, and returns the tool's JSON result string. A
	// non-nil error is a validation failure, a guest trap (e.g. hitting the memory
	// cap or the call deadline), or a cancelled ctx — never a shell exit code,
	// because there is no shell.
	Dispatch(ctx context.Context, argsJSON json.RawMessage) (string, error)

	// Name is the tool name this instance serves.
	Name() string

	// Close releases the compiled module and its runtime. Idempotent per the
	// underlying runtime; after Close the instance must not be dispatched.
	Close(ctx context.Context) error
}

// ComputeProvider is a factory for [ComputeInstance]s over a pure-compute
// isolation backend — the JSON->JSON sibling of [Provider]. It exists so a
// router depends on the *capability* (cheap in-process isolated compute), not on
// wazero specifically. Like [Provider] it is NEVER handed and MUST NOT read a
// provider API key.
type ComputeProvider interface {
	// Load compiles spec.Module, verifies it satisfies the ABI and its capability
	// grant, and returns a reusable [ComputeInstance]. It runs no guest code —
	// behavior executes only per Dispatch, in a fresh instance.
	Load(ctx context.Context, spec ComputeSpec) (ComputeInstance, error)

	// Name is a short, stable backend identifier ("wasm"), used in logs and errors.
	Name() string
}

// WASMProvider is the wazero-backed [ComputeProvider]. It is a thin, honest
// wrapper over internal/wasmtool: all isolation, capability-gating, and DoS
// bounding live there and are reused unchanged, so this provider adds a routing
// seam and a boundary contract, not a second runtime.
type WASMProvider struct{}

// Compile-time assurances: WASMProvider is a ComputeProvider (the pure-compute
// seam) and DELIBERATELY not a [Provider] (the argv "hands" seam) — the type
// system records that a WASM backend cannot stand in for a container. The
// container providers implement [Provider]; this one implements [ComputeProvider].
var (
	_ ComputeProvider = (*WASMProvider)(nil)
	_ Provider        = (*DockerProvider)(nil)
	_ Provider        = (*PodmanProvider)(nil)
)

// NewWASMProvider returns a ready [WASMProvider]. It holds no state: each Load
// builds an independent, self-contained instance.
func NewWASMProvider() *WASMProvider { return &WASMProvider{} }

// Name implements [ComputeProvider].
func (p *WASMProvider) Name() string { return "wasm" }

// CanServe reports whether a tool with the given capability need can run on the
// WASM compute tier. Only [NeedPureCompute] qualifies; a shell/git/filesystem
// tool must route to a container [Provider]. Making the boundary a function keeps
// mis-routing a checked decision, not a runtime surprise — a caller can assert
// CanServe(need) before choosing a tier, and treat a false as "use Docker/Podman."
func (p *WASMProvider) CanServe(need ToolNeed) bool { return need == NeedPureCompute }

// Load implements [ComputeProvider] by delegating to wasmtool.New, so the guest
// inherits the plugin sandbox's exact isolation and DoS bounds (fresh instance
// per dispatch, hard memory cap, per-call deadline). It runs no guest code.
func (p *WASMProvider) Load(ctx context.Context, spec ComputeSpec) (ComputeInstance, error) {
	if spec.Name == "" {
		return nil, errors.New("sandbox: ComputeSpec.Name is required")
	}
	if len(spec.Module) == 0 {
		return nil, errors.New("sandbox: ComputeSpec.Module (wasm bytes) is required")
	}
	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:          spec.Name,
		Description:   spec.Description,
		RequiredScope: spec.RequiredScope,
		Parameters:    spec.Parameters,
		Module:        spec.Module,
		Capabilities: wasmtool.Capabilities{
			AllowWASI: spec.AllowWASI,
			Log:       spec.Log,
		},
		MemoryLimitPages: spec.MemoryLimitPages,
		CallTimeout:      spec.CallTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &wasmInstance{tool: tool, name: spec.Name}, nil
}

// wasmInstance is a [ComputeInstance] backed by one compiled wasmtool.Tool.
type wasmInstance struct {
	tool *wasmtool.Tool
	name string
}

// Dispatch implements [ComputeInstance] via the tool's typed handler — the exact
// same dispatch path (validate → fresh instance → ABI → copy result out) the
// plugin registry uses, so a compute-provider call is byte-for-byte the plugin
// sandbox's behavior.
func (i *wasmInstance) Dispatch(ctx context.Context, argsJSON json.RawMessage) (string, error) {
	return i.tool.Handler()(ctx, argsJSON)
}

// Name implements [ComputeInstance].
func (i *wasmInstance) Name() string { return i.name }

// Close implements [ComputeInstance].
func (i *wasmInstance) Close(ctx context.Context) error { return i.tool.Close(ctx) }
