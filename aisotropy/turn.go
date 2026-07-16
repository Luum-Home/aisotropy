// Package aisotropy: the in-process (library-first) chat surface. Turn runs one
// chat turn against the App's runtime WITHOUT an HTTP hop, at full parity with
// POST /v1/tenant/{tenant_id}/user/{user_id}/chat — same isolation, quota gate,
// guardrails, gateway model selection, memory hydration, fail-closed reflection,
// and cost accounting. It exists so a consumer can embed Aisotropy directly
// (their own trusted code calls Turn) instead of standing up the HTTP server via
// Run when they don't need the network boundary.
package aisotropy

import (
	"context"

	"github.com/Luum-Home/aisotropy/internal/api"
)

// ChatInput is the in-process turn request — the library-first equivalent of the
// HTTP chat body (ChatRequest) plus the path identity (tenant_id/user_id).
//
// Under App.Turn the caller's OWN trusted code supplies the identity: there is
// no HTTP auth middleware in-process, so no credential is verified here. Scopes
// therefore gate tools directly (they flow to ChatCore's effectiveScopes as the
// body scopes, the fallback used when no verified principal is in ctx). This is
// the deliberate trust model of the embedded surface: the process that calls
// Turn is inside the trust boundary, exactly as the code that mints a JWT for
// the HTTP path is. Isolation between tenants still holds by construction —
// tenant_id/user_id enter ctx via ChatCore's chatIdentity and never travel any
// other way, and every memory tier is scoped from that ctx.
type ChatInput struct {
	// TenantID/UserID are the isolation identity. They are validated by
	// ChatCore (memory.Key.Validate + the same charset rules the HTTP path
	// enforces): an empty/invalid identity returns an error, never a silent
	// cross-tenant read.
	TenantID string
	UserID   string
	// Prompt is the user message for this turn.
	Prompt string
	// SessionID is the client-chosen working-memory conversation key. Reusing it
	// across Turn calls is what lets working memory persist between turns.
	SessionID string
	// ProductID selects the per-product persona (WithPersona); empty falls back
	// to the built-in default persona.
	ProductID string
	// ModelPreference is an OPTIONAL gateway target name to prefer for this turn
	// (e.g. "cheap"); empty leaves routing to the gateway's policy.
	ModelPreference string
	// Scopes are the caller's granted permission scopes. They gate which tools
	// are offered to the model — a tool whose RequiredScope is not covered never
	// enters the payload.
	Scopes []string
	// Images is an OPTIONAL presence signal that routes the turn to a
	// vision/premium gateway target (ImageRule). As on the HTTP path the image
	// bytes are not yet transmitted to the model — this wires routing only.
	Images []string
}

// ChatOutput is the in-process turn result — the flat equivalent of the HTTP
// ChatOutput.Body: the schema-enforced reply, the tenant that served it
// (isolation echo), and whether the resolved LLM client was the credential-less
// stub.
type ChatOutput struct {
	Reply    string
	TenantID string
	Stub     bool
}

// Turn runs one chat turn in-process against the App's runtime. The runtime is
// built once, lazily, on the first Turn call (mirroring the semanticOnce idiom
// in knowledge.go) and reused across calls, so working memory persists between
// turns and the background consolidation/maintenance workers boot exactly once.
//
// It is full parity with POST /v1/.../chat: Turn maps ChatInput onto the same
// api.ChatInput the HTTP handler builds and calls api.ChatCore — the identical
// pipeline (identity→ctx, pre-spend quota gate, input guardrail, gateway model
// selection, memory hydration, transactional-outbox user-turn persist,
// fail-closed reflection loop, output guardrail, cost record). There is no HTTP
// hop, so no HTTP auth middleware runs; the caller's supplied identity is
// trusted (see ChatInput) and Scopes gate tools directly.
//
// Safe for concurrent use across tenants: isolation is by ctx + per-key scoping
// (ChatCore derives every memory scope from the ctx it injects tenant/user
// into), and the shared runtime's stores are themselves concurrency-safe. The
// returned error is the ChatCore error unchanged — a huma.StatusError carrying
// an HTTP status code, usable off the HTTP path as an ordinary error (inspect it
// with huma.GetErrorStatus if the status matters).
//
// Building the runtime on first call can fail (e.g. a misconfigured store); that
// error is returned and memoized, so a subsequent Turn returns the same error
// without rebuilding.
func (a *App) Turn(ctx context.Context, in ChatInput) (ChatOutput, error) {
	rt, err := a.ensureRuntime()
	if err != nil {
		return ChatOutput{}, err
	}

	// Map the flat in-process request onto the HTTP contract type ChatCore
	// consumes. Identity goes on the top-level fields (ChatCore.chatIdentity
	// reads TenantID/UserID from here); everything else is the body, exactly as
	// the HTTP handler populates it from the JSON request.
	apiIn := &api.ChatInput{
		TenantID: in.TenantID,
		UserID:   in.UserID,
		Body: api.ChatRequest{
			Prompt:          in.Prompt,
			ProductID:       in.ProductID,
			SessionID:       in.SessionID,
			Scopes:          in.Scopes,
			ModelPreference: in.ModelPreference,
			Images:          in.Images,
		},
	}

	out, err := api.ChatCore(ctx, rt.chatDeps(), apiIn)
	if err != nil {
		return ChatOutput{}, err
	}
	return ChatOutput{
		Reply:    out.Body.Reply,
		TenantID: out.Body.TenantID,
		Stub:     out.Body.Stub,
	}, nil
}

// ensureRuntime lazily boots the App's shared in-process runtime exactly once
// (turnOnce), memoizing the result — the same build path Run uses, but built
// against a background context so the runtime and its workers outlive any single
// Turn call's ctx. A build error is memoized alongside so repeated Turn calls do
// not retry a doomed construction.
func (a *App) ensureRuntime() (*appRuntime, error) {
	a.turnOnce.Do(func() {
		// context.Background(): the runtime (its MCP clients, its cold-boot
		// reconciliation, its stores) must not be tied to the lifetime of the
		// first Turn's ctx — it is shared across every later Turn and torn down
		// only by Close. Per-call cancellation still works: each Turn passes its
		// own ctx to ChatCore, which honors it for that turn.
		a.runtime, a.turnErr = a.startRuntime(context.Background())
	})
	return a.runtime, a.turnErr
}

// chatDeps projects the shared runtime onto the api.ChatDeps bundle ChatCore
// consumes. It is the single place the in-process caller assembles the pipeline
// dependencies, one-for-one the same values Register passes on the HTTP path —
// so HTTP and in-process run ChatCore over identical deps.
func (rt *appRuntime) chatDeps() api.ChatDeps {
	return api.ChatDeps{
		Client:  rt.client,
		WM:      rt.wm,
		Store:   rt.factsStore,
		Limiter: rt.limiter,
		ToolReg: rt.toolRegistry,
		Persona: rt.persona,
		Meter:   rt.costMeter,
		GW:      rt.gateway,
		Guard:   rt.guard,
	}
}
