package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/lang"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// ChatDeps bundles the dependencies the non-streaming chat pipeline needs,
// so ChatCore can be driven from both the HTTP handler and an in-process
// caller (aisotropy.App.Turn) without duplicating the pipeline.
//
// The fields mirror Register's parameters one-for-one — this is deliberate:
// the two entry points (HTTP and in-process) MUST run the same code over the
// same dependencies, so the only difference between them is how ctx and *in
// arrive, never what the pipeline does with them. Nil semantics are identical
// to Register's (a nil Limiter/ToolReg/Persona/Meter/GW/Guard degrades exactly
// as documented on the corresponding helper), so an embedder that constructs a
// leaner App gets the same defined behavior the HTTP path would.
type ChatDeps struct {
	Client  llm.Client
	WM      *memory.WorkingMemory
	Store   facts.Store
	Limiter *memory.Limiter
	ToolReg *tools.Registry
	Persona PersonaProvider
	Meter   *costmeter.Meter
	GW      *gateway.Gateway
	Guard   *Guard
}

// ChatCore runs the full non-streaming chat pipeline in-process: identity->ctx,
// pre-spend quota gate, input guardrail, gateway model selection, system-prompt
// hydration, transactional-outbox user-turn persist, fail-closed reflection loop,
// output guardrail, cost record. Returned errors are huma.StatusError values
// (they carry an HTTP status but are ordinary errors off the HTTP path). It is
// the EXACT code the HTTP handler runs.
//
// Extracting this from Register's closure keeps the codebase's core value —
// a single pipeline — literally single: the HTTP handler and aisotropy.App.Turn
// share this body verbatim rather than each maintaining their own copy that can
// drift on the constitutional gates (tenant isolation via ctx, quota-before-spend,
// fail-closed reflection). The isolation guarantee is unchanged: tenant_id and
// user_id enter ctx here (via chatIdentity) and never travel any other way.
func ChatCore(ctx context.Context, d ChatDeps, in *ChatInput) (*ChatOutput, error) {
	ctx, key, scope, err := chatIdentity(ctx, in)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
	}

	if err := chatCheckQuota(ctx, d.Limiter, in.TenantID); err != nil {
		return nil, err
	}

	// Input guardrail (redact + detect ALWAYS; block opt-in). forwarded is
	// the REDACTED prompt: it — never the raw prompt — is what hydrates the
	// system prompt, persists to working memory, and reaches the model, so a
	// secret in the prompt never leaks into the model or any memory tier. A
	// suspicious injection verdict is annotated by default and only rejected
	// when BlockInjection is enabled.
	forwarded, blocked := d.Guard.checkInput(ctx, in.TenantID, in.UserID, in.Body.Prompt)
	if blocked {
		return nil, huma.Error400BadRequest("request blocked by input guardrail (possible prompt injection)")
	}

	// Language-detection seam (research R1): detect the prompt's language ONCE
	// here — the same request boundary that injected tenant/user identity — and
	// carry it on ctx via the canonical lang.WithLanguage. Detection runs on the
	// REDACTED prompt (forwarded), never the raw one, so no secret text is
	// inspected beyond what already flows downstream. Every consumer (persona
	// selection today; language-conditional guardrails/tokenizer/embedder later)
	// reads it back via lang.FromContext rather than re-deriving it, so the
	// language is determined exactly once and all layers agree. detected may be
	// Undetermined (short/ambiguous input); FromContext reports that as not-set
	// and consumers keep their English/default behavior.
	detected := lang.Detect(forwarded)
	effective := resolveSubjectLanguage(ctx, d.WM, in.TenantID, in.UserID, detected)
	ctx = lang.WithLanguage(ctx, effective)

	// Model selection through the gateway seam; nil gw resolves to the passed
	// client. The non-streaming route has no quota-pressure degrade override
	// (that is the streaming path's behavior) — the gateway pick is final here.
	active := selectClient(ctx, d.GW, d.Client, d.Meter, in.TenantID, in.UserID, forwarded, in.Body.ModelPreference, len(in.Body.Images) > 0)

	slog.InfoContext(ctx, "chat request",
		"tenant_id", in.TenantID,
		"user_id", in.UserID,
		"session_id", in.Body.SessionID,
		"product_id", in.Body.ProductID,
		"prompt_len", len(in.Body.Prompt),
		"lang", effective.String(),
		"lang_detected", detected.String(),
		"llm", active.Name(),
	)

	system, err := chatBuildSystemPrompt(ctx, d.WM, d.Store, d.ToolReg, d.Persona, key, scope, in, forwarded)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to hydrate working memory", err)
	}

	chatPersistUserTurn(ctx, d.WM, key, forwarded)

	reply, usage, err := chatReflect(ctx, active, system, in, forwarded)
	if err != nil {
		return nil, err
	}

	// Output guardrail, AFTER the model returns and BEFORE persisting or
	// returning. When BlockOutput is enabled and the reply tripped a
	// guardrail (toxicity/leak), the flagged text is replaced with a safe
	// refusal — which is what both persists to memory and returns to the
	// client. Default posture: annotate + log, reply unchanged.
	finalReply := d.Guard.checkOutput(ctx, in.TenantID, in.UserID, forwarded, reply.Reply, system)

	chatRecordTurn(ctx, d.WM, key, in.TenantID, finalReply, usage, d.Meter)

	return chatBuildOutput(finalReply, in.TenantID, active, usage), nil
}

// resolveSubjectLanguage layers per-(tenant,user) persistence on top of the
// request-scoped detection (research R1 follow-on, ADR-0053). It reads the
// language previously remembered for this subject, reconciles it with the fresh
// detection via the single lang.Resolve precedence rule, and writes the detection
// back only when it is confident AND changed. The effect: a confident detection
// updates the subject's remembered language; an Undetermined detection
// (short/code-mixed follow-up) inherits it instead of resetting downstream
// consumers to their default. Both chat paths (ChatCore and StreamCore) call this
// so they remember identically.
//
// It is advisory and cheap by construction: the returned Language conditions only
// downstream prose (persona/guardrails), never authorization, and it costs at
// most one subject-scoped BadgerDB read plus — only on a genuine change — one
// write. Isolation holds by construction: the store is keyed strictly by
// (tenant,user) (see memory.subjectLangKey), so a blank identity is rejected at
// the key layer rather than reading or writing a wildcard. Any read/write error
// is non-fatal — the turn proceeds on the request-scoped detection — because a
// memory hiccup must never fail a chat over an advisory hint.
func resolveSubjectLanguage(ctx context.Context, wm *memory.WorkingMemory, tenant, user string, detected lang.Language) lang.Language {
	if wm == nil {
		return detected
	}

	storedCode, found, err := wm.GetSubjectLanguage(ctx, tenant, user)
	if err != nil {
		slog.WarnContext(ctx, "subject language read failed; using request-scoped detection",
			"tenant_id", tenant, "user_id", user, "error", err)
		return detected
	}

	var stored lang.Language
	if found {
		stored = lang.Language(storedCode)
	}

	effective, persist := lang.Resolve(detected, stored)
	if persist {
		if err := wm.PutSubjectLanguage(ctx, tenant, user, effective.String()); err != nil {
			slog.WarnContext(ctx, "subject language persist failed; hint not remembered",
				"tenant_id", tenant, "user_id", user, "error", err)
		}
	}
	return effective
}
