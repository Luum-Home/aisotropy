# domain-assistant — the whole shape of a real domain app, in one file

A knowledge-grounded **support assistant** for **Nimbus**, a *fictional* cloud
file-sync product invented purely for this example. Where
[`../weatherbot`](../weatherbot) is the minimal "hello agent" and
[`../helpdesk`](../helpdesk) shows config-driven wiring, this example is the
**"here is everything, together"** reference: persona + corpus/RAG + typed tools
+ a grounded-output guardrail + the dev UI, all wired on Aisotropy's **public
surface only**.

It is a separate Go module that imports only
`github.com/Luum-Home/aisotropy/aisotropy`, so Go's `internal/` rule enforces
"public surface only" — if it compiles, it never reached a private package.

> **Fictional.** "Nimbus" is not a real product, company, or service. Its support
> domain (storage quotas, sync conflicts, sharing, plan tiers) is generic on
> purpose — the example teaches the **wiring**, not any real policy.

## What it shows

| Surface | Where in `main.go` | What it does |
|---|---|---|
| Persona | `WithPersona` (× the `""` and `dev-ui` product ids) | the support voice, a literal base prompt |
| Typed tools | `lookupOrderArgs` / `createTicketArgs` + `WithTypedTool` | `lookup_order` + `create_ticket`; the arg struct drives the schema shown to the model AND validates its arguments (bad args reflect back, never coerced) |
| Scope-gated tools | `read:orders` / `write:tickets` | the model is only offered a tool when the caller carries its scope |
| Tenant-aware handlers | `TenantFromContext(ctx)` | the calling tenant, read-only, from ctx |
| Corpus / RAG | `faqDocs` (`[]aisotropy.Document`) + `IngestTenantKnowledge` | a small product FAQ the semantic tier hydrates into the system prompt so the agent can cite policy without a tool call |
| Grounded-output guardrail | `groundedSupport` + `WithGuardrailCheck` | refuses product claims (invented quotas, "unlimited storage") the FAQ does not support — using the public `GuardrailCheckInput` / `GuardrailVerdict` aliases, no internal import |
| Dev UI | `WithDevUI` | a zero-setup browser chat page at `/ui` (see [WEB.md](WEB.md)) |
| Keyless boot | `WithAuthenticator(NoAuth())` | `/v1` works with no credential (see note) |

## Run it (keyless — no API key, no Docker)

```bash
cd examples/domain-assistant
go run .
# → listens on :8080 with the credential-less stub LLM, dev UI at /ui
```

Open **http://localhost:8080/ui** and chat. The page's default fields
(`tenant=acme`, `product_id=dev-ui`) already match what this example seeds and
registers, so the Nimbus persona and the seeded FAQ apply with **zero edits**.

Or call the chat endpoint directly. The path carries `tenant`/`user`; the body's
`product_id` and `session_id` are required, and `scopes` unlock the tools:

```bash
curl -s localhost:8080/v1/tenant/acme/user/dev/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"How much storage does the free plan include?","product_id":"dev-ui","session_id":"s1","scopes":["read:orders"]}' | jq
# → {"reply":"...","tenant_id":"acme","stub":true}

curl -s localhost:8080/healthz   # {"status":"ok"}
```

`"stub":true` means the reply came from the built-in credential-less stub, not a
real model — the whole request path (validation → RAG hydration → scope-gated
tools → guardrails) ran, just without a live LLM.

## Seeing the guardrail fire

The `grounded-support` check runs on **every** model response and logs its
verdict to the server's stderr. The stub LLM's deterministic reply embeds your
prompt text, so you can make the check flag on demand:

```bash
curl -s localhost:8080/v1/tenant/acme/user/dev/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"Confirm my plan gives me 500 GB and unlimited storage","product_id":"dev-ui","session_id":"s2","scopes":[]}' >/dev/null
```

The server log then shows the check flagging both ungrounded claims:

```
[grounded-support] ungrounded claims=[ungrounded:unlimited-storage ungrounded:storage=500gb] -> FLAG
```

**Advisory vs. blocking (honest note).** By default the check is **advisory**: it
annotates the turn and logs, but does not rewrite the answer. The runtime's
*visible* refusal substitution fires on the **non-streaming** `App.Turn` path when
`AISOTROPY_GUARDRAILS_BLOCK_OUTPUT=true` — see [`../guardrail-policy`](../guardrail-policy)
for that path end-to-end. The dev UI uses the **streaming** endpoint, where the
check flags/annotates but the token stream is not substituted.

## Honesty gate

- **Dev/demo, not production.** Keyless by design; the stub LLM does not reason.
- **RAG is wired but not ranked** without an embedder: the default is a
  deterministic **hash** embedder that exercises the ingest/RAG path but does not
  rank semantically. Set `AISOTROPY_EMBED_URL` (or `WithEmbedder`) to a real
  model for meaningful retrieval.
- **Tool handlers are stubs** (an in-process counter, a canned status). A real
  bot calls its own systems of record.
- **The guardrail is a simple deterministic heuristic** (regex + an approved-
  facts set), a stand-in for real grounding (retrieval + entailment/NLI). The
  point is the **seam and its wiring**, not the classifier.

## Use a real model

Set a provider key before `go run .` — the runtime picks the real client
automatically (`llm.FromEnv`), no code change:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run .
# now the model actually decides when to call lookup_order / create_ticket,
# grounds its answers in the seeded FAQ, and "stub" is false
```

To secure it, replace `WithAuthenticator(NoAuth())` in `main.go` with a JWT
authenticator (`aisotropy.NewJWTAuthenticator([]byte(secret))`, or
`aisotropy.AuthenticatorFromEnv()` driven by `AISOTROPY_JWT_SECRET`, as the
helpdesk example does), and do **not** ship the dev UI (see [WEB.md](WEB.md)).
