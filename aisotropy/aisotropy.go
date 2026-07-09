// Package aisotropy is the public adoption surface (Phase 8) of the Aisotropy
// multi-tenant LLM-OS runtime: it lets a third party build their own agent/bot
// on top of Aisotropy WITHOUT forking the repository or editing the composition
// root (cmd/aisotropyd/main.go).
//
// The reusable engine lives under internal/ (not importable by another module).
// This package is the stable façade over it: a small set of type aliases that
// hand back the real internal types (so a consumer works with the genuine
// contracts, not shadow copies) plus a builder — New(opts...) returning an *App
// whose Run boots the full runtime. A consumer registers tools, a ToolExecutor,
// and per-product personas declaratively through functional options instead of
// hand-wiring them into main.go.
//
// Surface stability contract: the identifiers exported here are the supported
// extension points. They are deliberately MINIMAL — only what a consumer needs
// to register a tool, implement a ToolExecutor, define a persona, and boot the
// runtime. Deliberately NOT exposed (so isolation and anti-magic invariants
// cannot be bypassed from outside):
//   - the tool Registry (built internally from WithTool),
//   - the memory Scope / its constructors / any cross-tenant export
//     (facts.Scope, facts.NewScope, Store.ExportAll): every scope is built by
//     the runtime from ctx, so tenant isolation holds by construction
//     (ADR-0011). A consumer can only READ identity from ctx (TenantFromContext
//     / UserFromContext), never construct a scope or read across tenants,
//   - any llm client decorator (circuit breaker, OTel, degradation): those are
//     runtime wiring, not consumer surface.
package aisotropy

import (
	"context"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// Tool is an agent capability a consumer registers via WithTool. RequiredScope
// gates it: the tool is offered to the model only when the request's granted
// scopes include RequiredScope (empty means always available). It is an alias
// for the real internal type, so a Tool a consumer builds is the exact value
// the runtime's scope-gated registry stores.
type Tool = tools.Tool

// ToolExecutor runs a tool a consumer registered, by name, with string-keyed
// arguments, returning the tool's textual result. The host supplies the concrete
// implementation via WithToolExecutor; the runtime never executes tool logic
// itself, it only decides — safely, under the caller's scopes — which tool may
// run. The Execute signature is fixed and MUST NOT change:
//
//	Execute(ctx context.Context, name string, args map[string]string) (string, error)
type ToolExecutor = agentloop.ToolExecutor

// LLMClient is the narrow streaming LLM contract (a single StreamChat method).
// Supply a custom one via WithLLM; the default is llm.FromEnv() (the real
// provider when ANTHROPIC_API_KEY is set, otherwise the credential-less stub).
type LLMClient = llm.Client

// Token is one incremental fragment of a streamed model response, as delivered
// on the channel an LLMClient returns.
type Token = llm.Token

// Usage carries provider-reported token counts for a completed stream, surfaced
// on the terminal Token.
type Usage = llm.Usage

// Persona is a per-product base system prompt. It is a literal string the
// consumer writes — there is no templating engine behind it (anti-magic gate,
// ADR-0002). Register one for a product_id via WithPersona; the runtime appends
// memory and tool context to this literal at request time.
//
// Persona is a distinct value type (not an alias) so the public surface does not
// leak the internal PersonaProvider interface: a consumer states WHAT the prompt
// is, the runtime owns HOW it is provided.
type Persona struct {
	// BasePrompt is the literal base system prompt for the product.
	BasePrompt string
}

// TenantFromContext returns the tenant id the runtime injected into ctx for the
// in-flight request, or ("", false) if absent. This is a READ-ONLY identity
// accessor: a ToolExecutor uses it to scope its own side effects to the calling
// tenant. There is no consumer-facing way to CONSTRUCT tenant identity or a
// memory scope — the runtime derives those from the validated request path, so
// tenant isolation holds by construction (ADR-0011).
func TenantFromContext(ctx context.Context) (string, bool) {
	return api.TenantFromContext(ctx)
}

// UserFromContext returns the user id the runtime injected into ctx for the
// in-flight request, or ("", false) if absent. Read-only, like TenantFromContext.
func UserFromContext(ctx context.Context) (string, bool) {
	return api.UserFromContext(ctx)
}
