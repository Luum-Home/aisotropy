# Dependency-Reuse-First (gollem-first) — a standing PREMISE

> **Origin (the mistake this prevents):** we began building `WithModelMiddleware` (ADR-0062) as a
> hand-rolled `llm.Client` decorator — **before** reading that gollem v0.5.1 already ships
> `core/agent_middleware.go` (`AgentMiddleware`, `RequestMiddlewareFunc`, `AgentStreamMiddleware`,
> `WithAgentMiddleware[T]`, built-in `LoggingMiddleware`/`MaxTokensMiddleware`/`TimingMiddleware`) and
> `core/interceptor.go` (`WithMessageInterceptor`/`WithResponseInterceptor` + ready-made **`RedactPII`**
> and **`AuditLog`** — exactly the "what it enables" list of that ADR). We were about to reinvent a
> primitive the cognitive engine already provides. One `grep` of gollem's own source caught it. This is
> the same failure class as the prune-evidence miss: **asserting a fact about capability from assumption
> instead of from the source.**

## The rule (always active before building on top of any dependency)

**Before you build a capability on top of a dependency, you MUST first inventory what the dependency
already provides — from its SOURCE, not its docs or your memory — and either reuse it or document the
precise gap that justifies building above it.** "We'll just add our own X" is rejected until the
dependency has been shown (code-truth) to lack a usable X.

This applies to **every** dependency (Huma, chi, BadgerDB, the Qdrant/rqlite clients, …) and **most of
all to gollem** — it is aisotropy's cognitive engine (the "kernel"), so anything resembling an agent
loop, middleware, interceptor, tool dispatch, orchestration, reflection, repair, structured output,
quota, cost/usage, memory, checkpoint/snapshot, hooks/events, tracing, or a test model is
**presumed to exist in gollem until proven otherwise** — check `core/` first.

## The four-step discipline (code-truth)

1. **Inventory-first.** Locate the dependency's real source
   (`go list -m -f '{{.Dir}}' github.com/fugue-labs/gollem`) and enumerate its public capability
   surface for the area you're about to touch. gollem's is almost all in `core/` (+ `core/orchestration`,
   `core/memory`, `ext/mcp`, `ext/otel`, `contrib/chi`).
2. **Map-before-build.** For each thing you intend to build, name the dependency symbol that already
   does it (or prove none exists). A one-line "gollem has: `core.WithAgentMiddleware`" beats a new file.
3. **Reuse or justify.** Adopt the dependency's primitive. If you still build above it, the ADR/commit
   MUST carry a **"Reuse of `<dep>` primitives"** section stating what the dep provides and the concrete
   reason its version does not fit — a **layer** difference (e.g. gollem middleware is a `core.Agent`
   option; aisotropy's direct-streaming path builds no `core.Agent`), a **semantics** difference (the
   constitutional pre-LLM multi-tenant quota gate is not gollem's per-run `WithUsageQuota`), or a genuine
   **missing feature**. "It was easier to write my own" is not a justification.
4. **Evidence, not assumption.** Every claim of "gollem has X" / "gollem lacks Y" cites a `file:line` in
   the gollem source. Every "we reinvented Z" cites both the aisotropy `file:line` and the gollem
   `file:line` it duplicates. This is the prune-evidence-gate's code-truth discipline extended to
   **capability claims about dependencies.**

## When building above IS correct (the additive test)

Building on top of gollem is right when aisotropy adds something gollem structurally does not: **hard
multi-tenant isolation by construction** (ctx + key-prefix + Qdrant in-scan filter), the **tiered
memory hierarchy by access temperature**, the **transactional-outbox → sleep-cycle consolidation**, the
**Huma struct-as-contract syscall layer**, the **constitutional pre-spend gates**. Those are the thesis.
The discipline is not "never build above gollem" — it is "**never build above gollem what gollem already
gives you, and always know which is which.**"

## Enforcement / practice

- New work touching the LLM/agent path opens with a gollem-surface check (step 1–2) recorded in the
  ADR or the agent's report.
- The **gollem-exploitation audit** (`docs/research/gollem-exploitation-audit-*-2026-07-17.md`) is the
  first full application: a per-subsystem ledger classifying each aisotropy construct as
  **REUSE-CORRECT / REINVENTION-ADOPT-GOLLEM / ADDITIVE-JUSTIFIED / EXPLOIT-MORE**.
- Projected into `.claude/rules/dependency-reuse-first.md` so in-repo agents auto-load it.

## Contextual Trigger

- Always active before adding a capability on top of any dependency, and specifically before any new
  agent-loop / middleware / orchestration / reflection / memory / quota / tooling construct in
  `internal/llm`, `internal/graph`, `internal/api`, or `aisotropy/`.
