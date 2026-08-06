---
type: Architecture
title: "Model provider — the single source of truth for which model runs, and how to change it"
description: Everything about the chat-model provider in one place — the variables that select it, what is provider-agnostic (all product code) versus what is not (the arena adapter's defaults and the identity-concealment token list), the currently configured provider, and the exact procedure to switch. Also states the rule that keeps published benchmark numbers honest across a provider change - historical model names in boards and analyses are PROVENANCE and are never rewritten.
tags: [architecture, llm, provider, configuration, arena, openai-compatible]
timestamp: 2026-07-27T00:00:00Z
---

# Model provider

## The contract

aisotropy talks to **any OpenAI-compatible chat endpoint**. Three environment
variables select it; nothing else in the product needs to change.

| Variable | Meaning |
|---|---|
| `OPENAI_BASE_URL` | Base URL of the OpenAI-compatible endpoint, including `/v1` |
| `OPENAI_API_KEY` | Bearer credential for that endpoint |
| `OPENAI_MODEL` | Model identifier the endpoint expects |

They are read from the repo-root `.env` (gitignored) or the process
environment. `internal/llm/openai_compat.go` is the client; `llm.FromEnv()`
selects it, and falls back to the keyless canned stub when no key is present.

## What is provider-agnostic, and what is not

**Provider-agnostic (verified — no change needed to switch):** every product
package. `git grep agnes -- internal/ aisotropy/ cmd/` returns only explanatory
comments and one test's token list; there is no provider branch, no
vendor-specific request shaping, no hardcoded endpoint.

**NOT provider-agnostic — the three places a switch must touch:**

1. **The arena adapter's fallback model** — `arenas/live/aisotropy/agnes_client.go`
   and `main.go`'s `modelName()` default to a specific model id when
   `OPENAI_MODEL` is unset. A default here is convenient locally and dangerous in
   a public repo: it silently sends prompts to one vendor for anyone who runs the
   arena without configuring it.
2. **The identity-concealment test** — `internal/api/owasp_llm07_persona_test.go`
   holds `deniedIdentityTokens`, the list of vendor/model names the assistant must
   never disclose (the `sg-identity-model` safeguard probe, ADR-0070). **This list
   is provider-specific by nature.** Switching providers without extending it
   leaves the new vendor's names undefended — the test keeps passing while
   protecting nothing.
3. **Adapter env plumbing** — each `arenas/live/<fw>/run.sh` and `run-docker.sh`
   forwards `OPENAI_BASE_URL`/`OPENAI_API_KEY` to its framework. These must
   forward, never default.

## Supported providers

Both are OpenAI-compatible and both offer a free developer tier. Neither is
privileged in the code: they are two valid values of the same three variables,
and the repo is deliberately configured to work with either (or with any other
OpenAI-compatible endpoint).

| | **poolside** | **Agnes AI** |
|---|---|---|
| Home | <https://poolside.ai> | <https://agnes-ai.com> |
| Base URL | `https://inference.poolside.ai/v1` | `https://apihub.agnes-ai.com/v1` |
| Model used here | `poolside/laguna-s-2.1` | `agnes-2.0-flash` |
| API docs | <https://docs.poolside.ai/api/overview> | <https://wiki.agnes-ai.com/en/docs/agnes-20-flash> |
| Compatibility | OpenAI-compatible (Bearer auth, chat-completions shape) | OpenAI-compatible |
| Free tier | free developer API key | free tier available |
| Native credential var | `POOLSIDE_API_KEY` | — |

Our stack reads `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL` for both,
because both speak the OpenAI-compatible protocol; a provider's own variable name
is only needed if you use that vendor's SDK directly.

**Which one produced which numbers:** every benchmark published in this repo
before 2026-07-27 was measured on **`agnes-2.0-flash`**, and the boards say so.
See the provenance rule below — those labels are never rewritten.

### Currently configured, and how to find out rather than assume

**Run this before repeating anything below as current state.** Three commands, one
of which costs 291 tokens; the rest are free. The table under them is a *reading*,
not the state:

```bash
# which provider is actually selected right now (prints no secret)
set -a; source .env 2>/dev/null; set +a
echo "${OPENAI_BASE_URL:-https://api.openai.com/v1} ${OPENAI_MODEL:-unset}"

# is the quota available right now — the runbook's §Step 0, ~291 tokens
curl -s -o /tmp/probe.json -w "HTTP %{http_code}\n" --max-time 40 \
  -X POST "${OPENAI_BASE_URL}/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" \
  -d "{\"model\":\"$OPENAI_MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"Reply OK\"}]}"
# 200 -> available.  429 -> exhausted or rate-limited; do NOT launch a sweep.

# the real lane ALSO needs these, or RAG cells fail and each retry still spends a call
curl -s -o /dev/null -w "qdrant %{http_code}\n" --max-time 5 http://localhost:6333/healthz
curl -s -o /dev/null -w "rqlite %{http_code}\n" --max-time 5 http://localhost:4001/status
curl -s -o /dev/null -w "e5     %{http_code}\n" --max-time 5 http://127.0.0.1:8899/health
```

The fuller procedure — cost tiers, flags, anti-patterns — is
[the agnes quota runbook](arena-agnes-quota-runbook.md). Treat it as *how to run a
sweep*, not as a description of today.

What the commands returned on **2026-08-05**, recorded as a dated reading and not as
a property:

| | observed |
|---|---|
| `OPENAI_BASE_URL` | `https://apihub.agnes-ai.com/v1` |
| `OPENAI_MODEL` | `agnes-2.0-flash` |
| Quota that day | **available** (HTTP 200) |
| Shape | a **reasoning** model: the response carries `reasoning_content` and `usage.completion_tokens_details` |
| Token accounting | `prompt 286 + completion 5 = total 291` — reasoning is produced *inside* the completion count, not added to it |

That last row is not trivia. It is first-hand evidence for the invariant
[ADR-0061](../decisions/0061-reasoning-model-support.md) asserts and that
`internal/api/turn_cost.go` prices on: reasoning tokens are a **subset** of completion tokens. Pricing
them as a separate line double-charged every reasoning turn until 2026-08-04. One probe is a sample
and not a vendor guarantee — but it is more than the comment that stood there before.

> **Read this before telling anyone the quota is exhausted.** The free-tier quota is a *state you
> check*, not a property you inherit. Several documents in this repository mention quota exhaustion
> in the past tense — the runbook opens "written after a session that exhausted the free-tier
> quota", `FULL-MATRIX.md`'s `candidate` exception names "a real-lane cell needing the provider while
> the free-tier quota is exhausted", and `RESULTS.md` headers count billable round-trips. Every one
> of those is a true statement about a past event, and every one of them reads to a fresh reader as
> a description of now.
>
> It happened on 2026-08-05: a session declared the arena re-run "blocked on credits" three times,
> having absorbed the constraint from these documents and never run the probe. The probe took one
> call and returned 200. The block was invented. **The cost of checking is 291 tokens; the cost of
> assuming was an item sitting closed-as-blocked in a status report.** Same failure shape as a stale
> rule file: documentation of a past state, unmarked, promoted to present by every reader after.

## Acknowledgements

This project's benchmarks consume a real, paid-for-by-someone inference service.
Both vendors below provide a **free developer tier**, and the live arena would
not exist without one of them:

- **[poolside](https://poolside.ai)** — Laguna model family, OpenAI-compatible
  inference API. Free developer key.
- **[Agnes AI](https://agnes-ai.com)** — `agnes-2.0-flash`, OpenAI-compatible
  API hub. Free tier. Every board published in this repo up to 2026-07-27 ran on
  it.

Naming them is transparency about what the published numbers were measured on,
and plain courtesy toward services offered for free. It is **not** a claim of
endorsement, partnership, or sponsorship in either direction, and neither vendor
reviewed or approved anything in this repository. Product and company names are
the trademarks of their respective owners and are used here nominatively — to
identify the service actually used.

> **If you fork or run this arena:** bring your own key and read the provider's
> terms. Free tiers carry rate limits and acceptable-use conditions that this
> repository does not restate and cannot grant on their behalf.

## How to switch providers

```bash
# 1. Point the stack at the new endpoint (repo-root .env, gitignored)
OPENAI_BASE_URL=https://inference.poolside.ai/v1
OPENAI_API_KEY=<key>
OPENAI_MODEL=poolside/laguna-s-2.1

# 2. Verify the endpoint answers in the shape we expect, BEFORE any board spend
curl -s -X POST "$OPENAI_BASE_URL/chat/completions" \
  -H "authorization: Bearer $OPENAI_API_KEY" -H 'content-type: application/json' \
  -d '{"model":"'"$OPENAI_MODEL"'","messages":[{"role":"user","content":"reply with OK"}]}' \
  | head -c 400

# 3. Extend the identity-concealment token list for the new vendor
#    internal/api/owasp_llm07_persona_test.go -> deniedIdentityTokens
go test ./internal/api/ -run OWASP_LLM07 -count=1

# 4. Full offline verification before spending anything
go test ./... -count=1 && bash scripts/owasp-llm-scan.sh
```

Only after those pass is a real board run worth its cost.

## The provenance rule (do not "update" historical model names)

A board is a measurement of **(framework × model × workload)**. The model is part
of the result, not a config detail attached to it.

> **Model names already recorded in `arenas/live/RESULTS.md`, in
> `docs/architecture/arena-*.md`, and in ADRs are PROVENANCE. They are never
> rewritten when the provider changes.** A number measured with
> `agnes-2.0-flash` stays labelled `agnes-2.0-flash` forever.

Rewriting them would silently re-attribute results to a model that never produced
them — the same class of contamination as mixing two boards' numbers. When a new
board runs under a new model, it renders that model's name, and the two sets of
numbers are compared **as different measurements**, never merged.

This is also why a provider switch does not obsolete the existing analyses: they
remain true statements about what was measured, under a model they name.

## Why the arena needs the model held CONSTANT

Every framework in `arenas/live/` is driven by the **same** endpoint and model on
a given board. That is the whole design: with the model held constant, a
difference between frameworks is attributable to the framework. It is also why
`qa` is a smoke test rather than a quality signal there — the model cannot vary,
so `qa` can only detect gross adapter breakage. See
`docs/architecture/arena-benchmark-methodology.md`.

A consequence worth stating plainly: **switching providers invalidates
cross-board comparison, not the boards themselves.** Numbers from an
`agnes-2.0-flash` board and a `laguna-s-2.1` board are not comparable to each
other; each is internally comparable across frameworks.
