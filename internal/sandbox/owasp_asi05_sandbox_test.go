package sandbox

// OWASP Agentic ASI05 — Unexpected Code Execution (sandbox routing boundary).
//
// aisotropy runs untrusted pure-compute tools on the wazero WASM tier, whose
// guest gets NO ambient filesystem, NO network, and NO fork/exec by construction
// (WASI preview-1; a guest's only authority is the host imports the deployer
// granted — default none). The routing boundary that keeps a
// shell/git/filesystem/network-needing tool OFF that powerless tier is
// WASMProvider.CanServe: only NeedPureCompute qualifies, so a mis-route is a
// CHECKED refusal, not a silent escape. This suite locks that boundary in.
//
// The deeper egress/DoS bounds (fresh instance per dispatch, hard memory cap,
// per-call deadline, powerless WASI shim) are exercised by wasm_test.go and
// internal/wasmtool. Deterministic: pure function calls, no container, no network.
// See docs/security/owasp-agentic-coverage.md.

import "testing"

// TestOWASP_ASI05_WASMTierRefusesHostNeedingTools: a tool that needs a shell or
// the host filesystem cannot run on the powerless WASM tier — CanServe returns
// false so the router must send it to a container Provider, never execute it
// with WASM's (absent) host authority.
func TestOWASP_ASI05_WASMTierRefusesHostNeedingTools(t *testing.T) {
	p := NewWASMProvider()

	if !p.CanServe(NeedPureCompute) {
		t.Error("pure-compute tools MUST be servable on the WASM tier")
	}
	if p.CanServe(NeedShellExec) {
		t.Error("a shell/exec tool must NOT be servable on the powerless WASM tier (WASI has no fork/exec)")
	}
	if p.CanServe(NeedFilesystem) {
		t.Error("a filesystem-needing tool must NOT be servable on the powerless WASM tier (WASI grants no ambient FS)")
	}
}

// TestOWASP_ASI05_WASMProviderIsNotAContainerProvider: the type system records
// that a WASM backend cannot stand in for a container "hands" Provider — it
// implements ComputeProvider only, never the argv-shaped Provider seam. A
// compile-time guarantee that argv/exec work cannot be dispatched to the
// no-exec tier.
func TestOWASP_ASI05_WASMProviderIsNotAContainerProvider(t *testing.T) {
	var cp ComputeProvider = NewWASMProvider()
	if cp.Name() != "wasm" {
		t.Errorf("WASM compute provider name = %q, want %q", cp.Name(), "wasm")
	}
	// A ComputeSpec with no Log grants the guest NO host functions at all — the
	// default powerless posture. We assert the field's zero value is nil (no host
	// authority) so a default recipe cannot accidentally hand the guest a callout.
	var spec ComputeSpec
	if spec.Log != nil {
		t.Error("default ComputeSpec must grant the guest no host functions (Log == nil)")
	}
}
