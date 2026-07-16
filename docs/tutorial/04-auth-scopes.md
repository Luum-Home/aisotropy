---
type: Tutorial
title: "04 — Auth & scopes"
description: Verify who the caller is over HTTP with a JWT authenticator (must-agree bind to the path), and gate tools on the verified scopes.
tags: [tutorial, auth, jwt, scopes, authenticator]
timestamp: 2026-07-16T00:00:00Z
---

# 04 — Auth & scopes

**Goal:** stop trusting the caller's word. Verify a credential, bind it to the path identity, and let
verified scopes decide which tools the model may use.

In-process (`Turn`), *your own trusted code* supplies the identity — you're inside the trust boundary,
so `ChatInput.Scopes` gate tools directly. Over HTTP the identity arrives from an **untrusted client**,
so a credential must be verified. The runtime is **default-deny**: until you register an
`Authenticator`, every `/v1` request is rejected `401`.

```go
secret := []byte(os.Getenv("MYBOT_JWT_SECRET"))

app := aisotropy.New(
	// The tool from lesson 01, gated on "read:weather".
	aisotropy.WithTypedTool("get_weather", "get the weather for a city", "read:weather", getWeather),

	// Verify an "Authorization: Bearer <jwt>" credential (HS256 by default, stdlib only).
	// The verified tenant/user MUST equal the URL path tenant/user (must-agree bind):
	//   mismatch -> 403, missing/invalid/expired token -> 401.
	aisotropy.WithAuthenticator(aisotropy.NewJWTAuthenticator(secret)),
)
if err := app.Run(context.Background()); err != nil { // serves :8080
	log.Fatal(err)
}
```

The authenticator does two jobs. It **verifies** the token (signature, expiry), and the runtime
**binds** the verified `Principal` to the URL path with *must-agree* semantics — a valid token for
`globex` cannot drive a request to `/v1/tenant/acme/...`. The token's `scope` claim becomes the
request's granted scopes, so `get_weather` (gated on `read:weather`) is offered to the model **only**
when the caller's token actually carries that scope. Auth and tool-gating are the same mechanism.

Harden it as your IdP requires — all stdlib, no new dependency:

```go
aisotropy.NewJWTAuthenticator(secret,
	aisotropy.WithJWTIssuer("https://idp.example.com"),
	aisotropy.WithJWTAudience("mybot"),
	aisotropy.WithJWTMaxTTL(1*time.Hour),
)
// External IdP with RS256? Pass aisotropy.WithJWTJWKS("https://idp.example.com/.well-known/jwks.json")
// or aisotropy.WithJWTRSAPublicKey(pub) — this also pins the verifier to RS256, blocking
// the RS256->HS256 algorithm-confusion attack.
```

For local/dev only, `aisotropy.WithAuthenticator(aisotropy.NoAuth())` trusts the path identity
verbatim — the explicit opt-out from the default-deny gate, never the silent default.

**Runnable example:** [`auth-scopes`](../../examples/auth-scopes) — a JWT-gated bot with a
scope-gated tool, and `curl` commands minting tokens with and without the scope.

**Depth:** [ADR-0024 — authenticated-identity seam](../decisions/0024-authenticated-identity-seam.md).

Next: [05 — Knowledge & RAG](05-knowledge-rag.md).
