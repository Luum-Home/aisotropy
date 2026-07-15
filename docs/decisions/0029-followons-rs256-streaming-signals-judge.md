---
type: ADR
title: "ADR-0029 — Closing the four named follow-ons: RS256/JWKS auth, streaming hard output-blocking, an explicit model-preference signal, and the eval LLM-judge on the output guardrail"
description: ADR-0024/0025 (authn seam + hardening) and ADR-0028 (capabilities wiring) each shipped with named follow-ons deferred on purpose. This ADR records the four that are now built, each stdlib-only, additive, and default-safe. (1) RS256 verification against a static RSA public key OR a JWKS endpoint, selected by NEW env vars AISOTROPY_JWT_PUBLIC_KEY[_FILE] / AISOTROPY_JWT_JWKS_URL — the algorithm-confusion defence is that the CONFIGURED mode fixes the expected alg, never the token, so an RS256-configured verifier can never be steered onto the HMAC path; JWKS is fetched/TTL-cached/kid-matched with math/big (no external JWK library) and fails CLOSED. (2) Streaming hard output-blocking: when AISOTROPY_GUARDRAILS_BLOCK_OUTPUT is on, chat/stream now BUFFERS the whole response (no live token flush) so a flagged reply can be replaced with a refusal before a single byte is sent — a latency-for-safety trade, opt-in, default off (live flush + annotate-only). (3) ChatInput.model_preference — an ADDITIVE, omitempty request field (existing clients byte-for-byte unaffected, ADR-0002 preserved) that maps to gateway.Request.UISelector so gateway.OverridePolicy honours an explicit target over the cost-budget rule; it is a PREFERENCE not authorization, an unknown name resolves to the default, and the streaming quota-pressure DegradeAt still overrides it. (4) The eval LLMJudge attached to the guardrails OutputCheck seam via the api.EvalOutputCheck adapter (api -> {eval, guardrails}; guardrails never imports eval), opt-in behind AISOTROPY_GUARDRAILS_LLM_JUDGE, driven by the RAW llmClient (not the circuit-broken chat client), with judge-error posture tracking the operator's blocking posture (fail-open when annotating, fail-closed when hard-blocking). All four opt-in / default-off; no new dependency, no contract break.
tags: [decisions, adr, auth, rs256, jwks, guardrails, gateway, eval, llm-judge, streaming, security, follow-ons]
timestamp: 2026-07-15T00:00:00Z
---

# ADR-0029 — Closing the four named follow-ons: RS256/JWKS, streaming hard output-blocking, an explicit model-preference signal, and the eval LLM-judge

## Status

✅ **Accepted — 2026-07-15.** This ADR records four capabilities that earlier ADRs deferred **on
purpose** as named follow-ons, now built. They span two prior decisions:

- From the **authn** line ([ADR-0024](0024-authenticated-identity-seam.md) /
  [ADR-0025](0025-security-hardening-authn-ops.md)), whose one carried-forward *capability* follow-on
  was "RS256 / JWKS — asymmetric verification against a rotating JWKS endpoint":
  1. **RS256 verification** against a **static RSA public key** or a **JWKS endpoint**, selected by the
     new env vars `AISOTROPY_JWT_PUBLIC_KEY` / `AISOTROPY_JWT_PUBLIC_KEY_FILE` /
     `AISOTROPY_JWT_JWKS_URL`.
- From the **capabilities-wiring** line ([ADR-0028](0028-capabilities-integration.md)), which named
  three follow-ons at the bottom of its Decision:
  2. **Streaming hard output-blocking** (needed response buffering).
  3. **Request-shape signal in `ChatInput`** (the gateway's override rule had no input).
  4. **Attaching `internal/eval`'s `LLMJudge`** to the guardrails `OutputCheck` seam.

Each is **stdlib-only, additive, and default-safe**: no new dependency, no OpenAPI struct-contract
break (ADR-0002 preserved), and every new behavior is **opt-in / default-off** — a runtime that sets
none of the new env vars and sends no new field behaves byte-for-byte as it did before this cut. This
ADR does **not** supersede any prior classification; it *extends* the authn seam (a second
`Authenticator` verification family) and *activates* two of ADR-0028's inert seams (the gateway's
override rule and the guardrails' output-check seam).

## Context

Four seams were left deliberately un-built, each for a concrete reason the deferring ADR named:

1. **RS256/JWKS.** ADR-0024's minimal HS256 verifier and ADR-0025's hardening (32-byte minimum,
   `aud`/`iss`, max-TTL) covered the **symmetric** case. Asymmetric verification against an external
   IdP's rotating key set needs public-key parsing plus key-rotation plumbing (fetch, cache, `kid`
   resolution) that ADR-0025 explicitly parked as "the one named capability follow-on." An operator who
   fronts aisotropyd with an OIDC IdP (Auth0, Keycloak, Cognito, …) mints **RS256** tokens and publishes
   a **JWKS** URL — HS256 does not serve them.
2. **Streaming hard output-blocking.** ADR-0028 wired the output guardrail to hard-block on the
   non-streaming `chat` route (replace a flagged reply with a refusal) but left `chat/stream`
   **annotate-only**: a token-by-token stream has already been sent and cannot be un-sent, so enforcing
   a refusal requires buffering the whole response before the first byte — a latency trade ADR-0028
   declined to smuggle in under wiring pressure.
3. **Request-shape signal in `ChatInput`.** ADR-0028 registered the gateway's `OverridePolicy` as a
   **present-but-inert** reference policy: it routes on `Request.UISelector`, but `ChatInput` carried no
   field to populate it, so an operator/UI could not say "serve this turn on the cheap model." Adding the
   signal is a **struct-contract change** (ADR-0002), which ADR-0028 kept out of a wiring-only cut.
4. **eval `LLMJudge` on the guardrails `OutputCheck` seam.** ADR-0028's output guardrail used built-in
   **heuristics** (toxicity/leak string checks). The heavy, model-backed grade — an LLM-as-a-judge over
   a Safety rubric — was left as an extension seam (`guardrails.OutputCheck`) precisely so the
   evaluation tier (`internal/eval`) and the guardrails tier (`internal/guardrails`) stay decoupled:
   neither may import the other.

The open question this ADR answers is, for each seam, **how it attaches and what its default is** —
holding the invariants the prior ADRs set (stdlib-only dependency policy, fail-closed auth, anti-magic
struct-as-contract, and honest opt-in defaults).

## Goals

- Build all four follow-ons **without a new dependency** (Go stdlib + already-stable internals) and
  **without an OpenAPI contract break** (ADR-0002): the one struct change is a new **omitempty** field.
- Keep every new behavior **opt-in / default-off**, so an unconfigured deployment is unchanged and each
  activation is a **greppable, operator-visible** env var (anti-magic, ADR-0002).
- Preserve the security postures the prior ADRs set: **fail-closed** on the auth path (RS256/JWKS
  reject on any doubt), and **latency-for-safety is a conscious operator choice**, never a silent one.
- Be **honest about the trade-offs**: buffered streaming sacrifices time-to-first-token; the LLM judge
  adds a model call and its own error mode; a model preference is a routing hint, not authorization.

## Non-goals

- **NOT** making RS256 or the LLM judge the default. HS256 (or `NoAuth`) stays the auth default; the
  built-in output heuristics stay the guardrail default. Both new paths are selected by env.
- **NOT** minting tokens or embedding an IdP. aisotropyd **verifies** RS256/JWKS tokens; issuing them is
  the operator's IdP's job.
- **NOT** turning `model_preference` into an authorization or capability grant. It is a routing
  preference only; scopes remain the authorization surface (ADR-0024's verified principal).
- **NOT** buffering the stream unless the operator opts into hard output-blocking. With
  `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` off, `chat/stream` keeps its live token-by-token flush unchanged.
- **NOT** letting `internal/guardrails` import `internal/eval` (or vice versa). The adapter lives in the
  `api` layer, which already imports both.

## Options considered

### RS256 verification & the algorithm-confusion defence

- **Derive the verification algorithm from the token header `alg` (the classic mistake).** Rejected —
  this is exactly the RS256→HS256 confusion attack: an attacker flips `"alg":"HS256"` and HMAC-signs
  with the *public* key (which is public) as the secret, and a header-driven verifier accepts it.
- **Fix the algorithm by CONFIGURATION, reject a mismatched header BEFORE any signature check
  (CHOSEN).** The construction option sets a `mode` (`modeHS256` / `modeRS256`,
  [`internal/auth/jwt.go`](../../internal/auth/jwt.go)); `mode.expectedAlg()` yields the single accepted
  `alg`, and `Authenticate` rejects any token whose header `alg` differs before verifying anything.
  Verification dispatches purely on `mode` — the token header can never steer an RS256-configured
  verifier onto the HMAC path. This is the whole defence.

### JWKS key handling

- **Add a JWK library dependency.** Rejected — the stack is 100% MIT/Apache **and** deliberately
  dependency-minimal on the auth path (ADR-0024 shipped the HS256 verifier with zero new modules). A JWK
  library is avoidable.
- **Reconstruct the RSA key from the JWK with `math/big` (CHOSEN).** `parseJWKS` base64url-decodes the
  jwk `n`/`e` and builds `*rsa.PublicKey` directly ([`jwt_rs256.go`](../../internal/auth/jwt_rs256.go)),
  keeping only usable RSA **signing** keys (skips `use:"enc"`, skips a non-`RS256` `alg`), TTL-caches the
  set, refetches once (bounded) on an unknown `kid` so a rotated-in key is picked up, bounds the response
  to 1 MiB, and **fails closed** — a fetch failure with no usable cached key rejects the request.

### Streaming output enforcement

- **Refuse mid-stream.** Impossible: bytes already flushed cannot be recalled.
- **Buffer the whole response, inspect, then emit — only when hard-blocking is on (CHOSEN).** When
  `Guard.BlockOutput` is set, `chat/stream` accumulates tokens **without flushing**, runs
  `guard.checkOutput` on the assembled text at `Done`, and emits only the safe text (or a refusal). The
  cost — losing time-to-first-token streaming — is paid **only** by operators who opt into hard blocking;
  with the toggle off, the live flush + `annotateOutput` path is untouched. This makes the
  streaming/non-streaming enforcement symmetric under the *same* toggle, at a documented latency cost.

### Model-preference signal placement

- **A header or query param.** Rejected — identity/authorization-adjacent routing signals belong in the
  typed contract, not out-of-band, and the struct-as-contract thesis (ADR-0002) says the Go struct is
  the single source of truth for both validation and the OpenAPI schema.
- **An additive `omitempty` body field (CHOSEN).** `ChatRequest.ModelPreference`
  (`json:"model_preference,omitempty"`) maps to `gateway.Request.UISelector`. Additive + omitempty means
  the generated OpenAPI schema gains an optional field and **existing clients that never send it are
  byte-for-byte unaffected** — the ADR-0002 versioning gate is satisfied without a breaking change.

### LLM-judge decoupling

- **Have `internal/guardrails` call `internal/eval` directly.** Rejected — it couples the safety tier to
  the evaluation tier, and the `OutputCheck` interface exists precisely to avoid that.
- **An `api`-layer adapter over the `OutputCheck` seam (CHOSEN).** `api.EvalOutputCheck`
  ([`internal/api/eval_output_check.go`](../../internal/api/eval_output_check.go)) implements
  `guardrails.OutputCheck` and wraps an `eval.Judge`. The import direction is `api → {eval, guardrails}`;
  neither of those two depends on the other.

## Decision

### 1. RS256 + JWKS verification (a second `Authenticator` family, selected by env)

`internal/auth` gains an asymmetric verification path alongside the existing HS256 one, all stdlib:

- **The mode fixes the algorithm, never the token** ([`jwt.go`](../../internal/auth/jwt.go)). `jwtMode`
  (`modeHS256` default, `modeRS256`) drives both the header-`alg` pin (`Authenticate` rejects a
  mismatched `alg` before any signature check) and the verification path (`verifySignature` dispatches on
  `mode`). Configuring an RSA key switches the verifier to `modeRS256` and **removes the HMAC path
  entirely** — the RS256→HS256 confusion attack is structurally impossible.
- **Static RSA key.** `WithRSAPublicKey(pub)` verifies `rsa.VerifyPKCS1v15` over a SHA-256 digest.
  `ParseRSAPublicKeyPEM` accepts PKIX (`PUBLIC KEY`) and PKCS#1 (`RSA PUBLIC KEY`) blocks and returns
  errors that **never contain key bytes**.
- **JWKS.** `WithJWKS(url, …)` fetches the key set with `net/http`, parses it with the stdlib only
  (`math/big` over the jwk `n`/`e`), TTL-caches it (default 5 min), matches the token `kid`, refetches
  once (bounded, default 1 min) on an unknown `kid`, bounds the response to 1 MiB, and **fails closed**
  (a fetch failure with no usable cache rejects the request) ([`jwt_rs256.go`](../../internal/auth/jwt_rs256.go)).
- **Securable by config** ([`aisotropy/authenv.go`](../../aisotropy/authenv.go)). `AuthenticatorFromEnv`
  counts the configured mode families and requires **exactly one** — setting more than one is an
  ambiguous configuration that refuses to boot:
  - `AISOTROPY_JWT_SECRET` → HS256 (mode label `jwt-hs256`; the 32-byte minimum from ADR-0025 still
    applies, fail-fast at boot).
  - `AISOTROPY_JWT_PUBLIC_KEY` (PEM literal) **or** `AISOTROPY_JWT_PUBLIC_KEY_FILE` (path) → RS256 static
    key (mode label `jwt-rs256`; the two are mutually exclusive; a parse/read failure returns an error
    with no key bytes).
  - `AISOTROPY_JWT_JWKS_URL` → RS256 JWKS (mode label `jwt-rs256-jwks`).
  - none → `NoAuth()` (mode label `noauth`, the explicit insecure opt-out, logged as a warning).
  The optional `AISOTROPY_JWT_AUDIENCE` / `AISOTROPY_JWT_ISSUER` / `AISOTROPY_JWT_MAX_TTL` hardening from
  ADR-0025 applies to **whichever** JWT mode is selected. The mode label is for logging only — no key
  material (secret, RSA key, JWKS URL) is ever returned or logged.
- **Public facade** ([`aisotropy/auth.go`](../../aisotropy/auth.go)). `WithJWTRSAPublicKey`,
  `WithJWTJWKS`, `WithJWTJWKSCacheTTL`, and `ParseRSAPublicKeyPEM` are thin re-exports so a library
  embedder gets the same asymmetric options as the binary.

### 2. Streaming hard output-blocking via response buffering (opt-in, default off)

`Guard.BlockOutput` now governs **both** chat routes ([`internal/api/guard.go`](../../internal/api/guard.go),
[`chat_stream.go`](../../internal/api/chat_stream.go)):

- **When `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` is ON.** `guard.outputBlockingEnabled()` is true, and
  `chat/stream` accumulates tokens into a buffer **without flushing any to the client**. At a clean
  `Done`, `guard.checkOutput` inspects the **assembled** text and returns it unchanged (safe) or the safe
  refusal (flagged) — and **only that safe text** is emitted and persisted. The toxic/leaky text never
  reaches the wire nor memory, mirroring the non-streaming route.
- **When OFF (default).** The stream flushes live token-by-token as before, and the assembled text is
  inspected **annotate-only** afterward (`guard.annotateOutput`) — a streamed response cannot be
  un-sent, so it logs a tripped verdict but never blocks. **No latency change.**
- **The trade-off, stated plainly.** Buffering sacrifices time-to-first-token (the client waits for the
  full response) in exchange for the guarantee that unsafe output is never sent. It is the operator's
  conscious opt-in, not a silent default. `chat/agent` is unaffected (input-only guardrail there).

### 3. `ChatInput.model_preference` — an additive request-shape signal for the gateway override

`ChatRequest` gains `ModelPreference` (`json:"model_preference,omitempty"`,
[`internal/api/chat.go`](../../internal/api/chat.go)) — the registry **name** of a gateway target (e.g.
`"cheap"`) the caller wants to serve this turn:

- **It activates the gateway's override rule.** `selectClient` maps it onto `gateway.Request.UISelector`,
  and `buildModelGateway` now registers `gateway.OverridePolicy` **first** in the chain so an explicit
  preference **wins over** the cost-budget rule ([`aisotropy/app.go`](../../aisotropy/app.go),
  [`internal/gateway/policies.go`](../../internal/gateway/policies.go)). It is inert-safe: `OverridePolicy`
  abstains when `UISelector` is empty, so requests without a preference route exactly as before.
- **It is a preference, NOT authorization.** An unknown/unregistered name resolves to the default at
  `Route` time (a client value can never drop a request), and the streaming path's quota-pressure
  `llm.DegradeAt` still **overrides** even an honored preference (the safety valve), because it is applied
  *after* the gateway pick. Tool authorization remains the verified principal's scopes (ADR-0024).
- **The contract is preserved (ADR-0002).** The field is additive and `omitempty`: the generated OpenAPI
  3.1 schema gains an optional property, and existing clients that never send it are byte-for-byte
  unaffected — not a breaking change.

### 4. eval `LLMJudge` on the guardrails `OutputCheck` seam (opt-in, default off)

An LLM-as-a-judge attaches to the output guardrail through an `api`-layer adapter, without either tier
importing the other ([`internal/api/eval_output_check.go`](../../internal/api/eval_output_check.go),
wired in [`aisotropy/app.go`](../../aisotropy/app.go)):

- **Attachment.** When `AISOTROPY_GUARDRAILS_LLM_JUDGE` is truthy **and** an LLM client exists, `App.Run`
  builds `eval.NewLLMJudge(llmClient, eval.Safety, eval.SafetyRubric, 0)`, wraps it in
  `api.NewEvalOutputCheck(judge, 0.5, blockOutput)`, and installs it as
  `guardrails.NewOutputInspector(nil).WithChecks(check)` on a `guardrails.New(Config{Output: inspector})`
  aggregator. Otherwise the aggregator is the default built-in output heuristics.
- **Scoring.** `eval.Safety` scores `1.0 = safe … 0.0 = unsafe`; the adapter flags **Suspicious** when the
  normalized score falls **strictly below** the `0.5` threshold (`guardrailsJudgeSafetyThreshold`), and it
  reports `Verdict.Score` as the inverse (problem-confidence). A Suspicious output verdict is then subject
  to the same `BlockOutput` posture as the heuristics.
- **The judge is driven by the RAW `llmClient`, not the circuit-broken chat client** — deliberately, so a
  flaky judge cannot trip the chat breaker and take down the primary path.
- **Judge-error posture tracks the operator's blocking posture.** A judge **operational** failure (model
  unreachable, schema never satisfied — never a merely-low-quality sample, which is just a low score) is
  resolved per `failClosed`, which is set to `blockOutput`: **fail-open** (treat as not-suspicious) when
  annotating, so a flaky judge does not silently start refusing every response; **fail-closed** (treat as
  suspicious) when hard-blocking, so an un-vetted response does not slip through with enforcement on.
- **Construction failure is non-fatal.** If the judge or adapter fails to construct, it is logged and the
  runtime falls back to the built-in output heuristics only. Default off: keyless/stub runtimes are
  byte-for-byte unchanged.

## Consequences

- **aisotropyd verifies OIDC/RS256 tokens.** An operator can front the binary with a standard IdP (JWKS
  URL) or a pinned RSA public key, with the same must-agree bind and `aud`/`iss`/max-TTL hardening as
  HS256 — and the algorithm-confusion attack is closed by construction (config fixes the alg). Exactly one
  mode may be configured; more than one refuses to boot.
- **Streaming can now hard-block output — at a latency cost the operator opts into.** With
  `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` on, `chat/stream` buffers and can refuse; the enforcement is now
  symmetric with `chat`. With it off (default), streaming latency is unchanged and output is annotate-only.
- **An explicit model preference can route a turn — but never escalate privileges.** `model_preference`
  gives a UI/operator a per-turn model choice that wins over cost shaping, bounded by the quota-pressure
  safety valve and by resolving unknown names to the default. The contract stays backward-compatible.
- **The output guardrail can now grade with a model, decoupled from the evaluation tier.** The
  `OutputCheck` seam carries a real Safety judge without `internal/guardrails` importing `internal/eval`;
  the judge runs on the raw client and its error mode follows the blocking posture. Default off.
- **No new dependency, no new license surface, no contract break.** All four are Go stdlib +
  already-stable internals; the sole struct change is an additive `omitempty` field (ADR-0002 preserved).
- **Honest defaults.** Every new behavior is off until an env var (or a request field) turns it on. A
  reader must not infer that a fresh deployment does RS256, buffers its stream, honors a preference, or
  runs an LLM judge — it does none of these until configured.

## References

- **Prior ADRs whose follow-ons this closes:**
  [ADR-0024](0024-authenticated-identity-seam.md) / [ADR-0025](0025-security-hardening-authn-ops.md) (the
  authn seam + hardening; RS256/JWKS was their one named capability follow-on) ·
  [ADR-0028](0028-capabilities-integration.md) (guardrails/gateway wiring; named the streaming-block,
  request-shape-signal, and LLM-judge follow-ons)
- **Invariants held:** [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract
  kept backward-compatible; no silent behavior) · [ADR-0021](0021-compression-signal-small-lm-surprisal-ceiling.md)
  (Bet-4 refuted — why the gateway still has no length/difficulty rule) ·
  [ADR-0026](0026-product-evaluation-harness.md) (the `internal/eval` harness the judge comes from)
- **RS256/JWKS:** [`internal/auth/jwt.go`](../../internal/auth/jwt.go) (`jwtMode`, `expectedAlg`,
  `verifySignature`, the header-`alg` pin), [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go)
  (`WithRSAPublicKey`, `WithJWKS`, `ParseRSAPublicKeyPEM`, `jwksSource`, `parseJWKS`,
  `jwkToRSAPublicKey`); wiring in [`aisotropy/authenv.go`](../../aisotropy/authenv.go)
  (`AuthenticatorFromEnv`); facade in [`aisotropy/auth.go`](../../aisotropy/auth.go)
  (`WithJWTRSAPublicKey`, `WithJWTJWKS`, `WithJWTJWKSCacheTTL`, `ParseRSAPublicKeyPEM`)
- **Streaming hard output-blocking:** [`internal/api/guard.go`](../../internal/api/guard.go)
  (`Guard.BlockOutput`, `outputBlockingEnabled`, `checkOutput`, `annotateOutput`),
  [`internal/api/chat_stream.go`](../../internal/api/chat_stream.go) (the buffered-vs-live drain loop)
- **`model_preference` signal:** [`internal/api/chat.go`](../../internal/api/chat.go)
  (`ChatRequest.ModelPreference`), [`internal/api/guard.go`](../../internal/api/guard.go) (`selectClient`
  → `UISelector`), [`aisotropy/app.go`](../../aisotropy/app.go) (`buildModelGateway`, `OverridePolicy`
  first), [`internal/gateway/policies.go`](../../internal/gateway/policies.go) (`OverridePolicy`)
- **eval LLM-judge:** [`internal/api/eval_output_check.go`](../../internal/api/eval_output_check.go)
  (`EvalOutputCheck`, `NewEvalOutputCheck`), [`aisotropy/app.go`](../../aisotropy/app.go) (the
  `AISOTROPY_GUARDRAILS_LLM_JUDGE` branch, `guardrailsJudgeSafetyThreshold`),
  [`internal/eval`](../../internal/eval) (`LLMJudge`, `Safety`, `SafetyRubric`),
  [`internal/guardrails/output.go`](../../internal/guardrails/output.go) (the `OutputCheck` seam)
- [runtime as built](../architecture/runtime-as-built.md) (the request-lifecycle ledger updated
  alongside this ADR) · [operations runbook](../deployment/runbook.md) (the operator env-var tables
  updated alongside this ADR)
