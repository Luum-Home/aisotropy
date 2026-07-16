# auth-scopes — authentication & scope-gated tools (Lesson 04)

A keyless, runnable server showing the two things every real deployment needs at
the HTTP boundary: **who is calling** (authentication) and **what they may do**
(scopes). It registers one scope-gated tool, boots behind a JWT authenticator,
and prints ready-to-run curl commands for a token that unlocks the tool and one
that does not.

It is a separate Go module that imports only
`github.com/Luum-Home/aisotropy/aisotropy`, so Go's `internal/` rule enforces
"public surface only" — if it compiles, it never reached a private package.

## The two invariants

| Invariant | Why it matters |
|---|---|
| **Auth is a network-boundary concern** | In-process `App.Turn` trusts the identity your own trusted code supplies. The credential check exists only on the HTTP surface `App.Run` serves, where identity arrives from an untrusted client — so this lesson drives the HTTP path. |
| **Verified scopes are authoritative** | When a real authenticator vouched for the caller's scopes, those *verified* scopes gate tools. A client that smuggles extra `scopes` into the request body **cannot** escalate — the body scopes are ignored. A tool whose `RequiredScope` the caller lacks is never offered to the model and never runs. |

## What it shows

| Concept | Where |
|---|---|
| JWT bearer gate | `WithAuthenticator(NewJWTAuthenticator(secret))` — verifies HS256 and binds must-agree to the URL path |
| Scope-gated tool | `WithTypedTool("reveal_secret", …, "read:secrets", …)` — offered only when verified scopes cover `read:secrets` |
| Must-agree bind | a valid token for `acme/alice` on another tenant's path → `403` |
| Default-deny | no token → `401` (running without a check is an explicit `NoAuth()` opt-in, never silent) |
| stdlib token minting | `mintHS256` — a minimal HS256 JWT with only `crypto/hmac` + `encoding/json`, mirroring the stdlib-only verifier |

## Run it (keyless — no API key, no Docker)

```bash
cd examples/auth-scopes
go run .
# → listens on :8080 with the credential-less stub LLM, and prints two curl
#   commands (token WITH the scope, token WITHOUT it)
```

Copy the two printed commands into another shell. Both target
`/v1/tenant/acme/user/alice/chat`; only the first carries a token whose
`scope` claim includes `read:secrets`, so only the first makes the model eligible
to call `reveal_secret`.

```bash
# (A) token WITH read:secrets  → reveal_secret is OFFERED to the model
# (B) token WITHOUT the scope   → reveal_secret is WITHHELD (identical request)
# both return 200 with a stub reply; the difference is whether the tool is in play
```

The remaining two cases prove the auth gate itself:

```bash
# (C) no Authorization header → 401 (default-deny)
curl -s -o /dev/null -w '%{http_code}\n' \
  localhost:8080/v1/tenant/acme/user/alice/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"hi","product_id":"vault-bot","session_id":"s1"}'

# (D) token (A) against a DIFFERENT tenant path → 403 (must-agree bind)
#     reuse the printed token (A) but hit /v1/tenant/other/user/alice/chat
```

`"stub":true` in the reply body means the answer came from the built-in
credential-less stub, not a real model — the whole request path (auth →
must-agree bind → scope-gated tools → memory → reflection) ran, just without a
live LLM.

## Why body scopes can't escalate

Send request (B)'s scope-less token but add `"scopes":["read:secrets"]` to the
JSON body. The tool stays withheld: because a real authenticator verified the
principal, the runtime uses the token's scopes as the authority and ignores the
body entirely. Body scopes are honored *only* under `NoAuth()` (or when no auth
middleware runs, as in unit tests) — see the weatherbot example, which passes
scopes in the body precisely because it runs keyless with `NoAuth()`.

## Use a real model

Set a provider key before `go run .` — the runtime picks the real client
automatically (`llm.FromEnv`), no code change:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run .
# now the model actually decides when to call reveal_secret (when the scope allows it)
```

## Harden the JWT gate (production)

`NewJWTAuthenticator` takes options to tighten verification, and can verify
RS256 tokens from an external IdP instead of HS256:

```go
aisotropy.NewJWTAuthenticator(secret,
    aisotropy.WithJWTIssuer("https://issuer.example.com"),
    aisotropy.WithJWTAudience("aisotropy"),
    aisotropy.WithJWTMaxTTL(time.Hour),          // reject effectively immortal tokens
)
// or verify an external IdP's RS256 tokens (HS256 secret unused → nil):
aisotropy.NewJWTAuthenticator(nil,
    aisotropy.WithJWTJWKS("https://issuer.example.com/.well-known/jwks.json"),
)
```

The shipped `aisotropyd` binary and the `helpdesk` example read the secret from
`AISOTROPY_JWT_SECRET` via `AuthenticatorFromEnv()` rather than hard-coding it —
do the same for anything beyond a local demo. The secret in this example's
`main.go` is a printed demo constant, deliberately not the pattern to copy.
