---
type: ADR
title: "ADR-0042 — NoAuth is an explicit affirmative opt-in, never a zero-config default"
description: A security review found the #1 critical residual in the authn posture — NoAuth was still a SHIPPABLE ZERO-CONFIG DEFAULT. With no JWT env set (AISOTROPY_JWT_SECRET / AISOTROPY_JWT_PUBLIC_KEY[_FILE] / AISOTROPY_JWT_JWKS_URL), AuthenticatorFromEnv silently returned NoAuth() and the binary booted trusting path tenant_id/user_id with no credential (a client sets tenant_id=B in the URL and reads tenant B's data), emitting only a soft slog.Warn. Decision — make NoAuth reachable ONLY by an explicit AISOTROPY_ALLOW_NOAUTH=1 opt-in. Absent both a JWT authenticator and that opt-in, AuthenticatorFromEnv returns the sentinel ErrNoAuthenticator and the boot path REFUSES TO START (non-zero exit). A second gate — NoAuthNonLocalGuard — refuses NoAuth on any non-loopback bind unless AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1 is also set. Local-dev keyless boot is preserved (AISOTROPY_ALLOW_NOAUTH=1 on loopback). No new dependency (stdlib only). No secret is ever logged.
tags: [decisions, adr, security, hardening, authentication, noauth, fail-closed, multi-tenancy, boot]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0042 — NoAuth is an explicit affirmative opt-in, never a zero-config default

## Status

✅ **Accepted — 2026-07-17.** Follow-up to [ADR-0024](0024-authenticated-identity-seam.md)
(the authenticated-identity seam) and [ADR-0025](0025-security-hardening-authn-ops.md)
(security-review hardening). ADR-0024 introduced the `Authenticator` seam and made NoAuth a
*named, opt-in* mode rather than an anonymous fallback; ADR-0025 hardened the HS256 verifier and
the ops endpoints. This ADR closes the residual that both left standing: NoAuth, though *named*,
was still reachable by mere **omission** — the default when an operator wired nothing. It does not
supersede ADR-0024/0025; it hardens the same boot boundary.

## Context

`aisotropy.AuthenticatorFromEnv()` (`aisotropy/authenv.go`) is the FromEnv selector the shipped
`aisotropyd` binary uses to decide how callers are authenticated. Before this ADR its `modes == 0`
branch — no `AISOTROPY_JWT_SECRET`, no `AISOTROPY_JWT_PUBLIC_KEY[_FILE]`, no
`AISOTROPY_JWT_JWKS_URL` — returned:

```go
return NoAuth(), "noauth", nil
```

`NoAuth` trusts the URL path identity verbatim: the verified `Principal` is exactly the path
`tenant_id`/`user_id`, so the must-agree bind is a tautology that never rejects. The consequence:
**a deployment that simply forgot to configure authentication booted wide open.** Any caller who
could reach the port could set `tenant_id=B` in the URL and read tenant B's data — a cross-tenant
disclosure that directly violates the project's hard multi-tenant isolation gate. The only signal
was a soft `slog.Warn` in `cmd/aisotropyd/main.go`, which a running service does not enforce.

This is the classic "insecure state reachable by accident" anti-pattern that ADR-0025 itself named
as its guiding rule ("a mechanism whose insecure state is reachable by accident is not a fix").
NoAuth being a *named* mode was necessary but not sufficient; it also had to be **affirmatively
chosen**, never defaulted into.

The tension: `examples/`, the local dev flows, and the keyless "boots zero-config" story genuinely
rely on being able to run without minting a JWT. The fix must keep that escape hatch — it must just
make the hatch **explicit** and, for anything but a loopback bind, **doubly explicit**.

## Decision

Three stdlib-only changes, each defaulting toward refusal.

### 1. NoAuth requires an explicit `AISOTROPY_ALLOW_NOAUTH=1` opt-in

In `AuthenticatorFromEnv()`, the `modes == 0` branch no longer returns `NoAuth()` unconditionally:

- If `AISOTROPY_ALLOW_NOAUTH` is truthy (`1`/`t`/`T`/`TRUE`/`true`/`True`, per `strconv.ParseBool`;
  a non-boolean like `yes` does NOT count) → return `NoAuth()`, mode label `"noauth"`, nil error.
- Otherwise → return `(nil, "noauth", ErrNoAuthenticator)`.

`ErrNoAuthenticator` is a new exported sentinel. It is a **server-side configuration outcome**, not
a per-request authentication result. The existing `< 32`-byte HS256 fail-fast, the ambiguous-modes
error, the malformed-`MAX_TTL` error, and the RS256 parse/file errors are unchanged. The return
signature `(Authenticator, string, error)` is unchanged (backward compatible for any embedder).

### 2. The boot path refuses to start on `ErrNoAuthenticator`

`cmd/aisotropyd/main.go` resolves the boot decision through a new pure, testable helper
`resolveAuthBoot(addr)`. On `errors.Is(err, ErrNoAuthenticator)` it emits an `slog.Error` that names
both remedies (configure a JWT authenticator, or set `AISOTROPY_ALLOW_NOAUTH=1` for local dev) and
exits non-zero. Refusing by omission is now the default; there is no code path where the absence of
configuration yields a running, unauthenticated server.

### 3. NoAuth on a non-loopback bind requires a SECOND, separate opt-in

Even with `AISOTROPY_ALLOW_NOAUTH=1`, exposing an unauthenticated runtime on a routable interface is
a cross-tenant disclosure by construction. `NoAuthNonLocalGuard(addr)` (in `authenv.go`) is called by
the boot path only when NoAuth is the selected mode. It returns:

- nil when the bind address is loopback (`127.0.0.1` / `localhost` / `::1`), OR
- nil when `AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1` is explicitly set, OR
- `ErrNoAuthNonLocal` otherwise → the boot path refuses to start.

A **wildcard bind** — an empty host (`:8080`, the default `AISOTROPY_ADDR`) or `0.0.0.0` / `::` — is
treated as **non-loopback**: it exposes the port on every interface, so it must clear the second
gate. This makes accidental non-local NoAuth (the most dangerous case) the hardest state to reach,
while a deliberate `AISOTROPY_ADDR=127.0.0.1:8080` local-dev run needs only the single opt-in.

## Environment contract (as implemented)

| Variable | Effect |
|---|---|
| `AISOTROPY_JWT_SECRET` / `AISOTROPY_JWT_PUBLIC_KEY[_FILE]` / `AISOTROPY_JWT_JWKS_URL` | Selects a real JWT authenticator (HS256 / RS256 static / RS256 JWKS). When any is set, `AISOTROPY_ALLOW_NOAUTH` is **ignored**. |
| _(none of the above)_ + `AISOTROPY_ALLOW_NOAUTH` unset/empty/non-boolean | **Boot refused** — `ErrNoAuthenticator`, non-zero exit. |
| _(no JWT)_ + `AISOTROPY_ALLOW_NOAUTH=1` on a loopback `AISOTROPY_ADDR` | **NoAuth boots** (local-dev keyless ergonomics preserved). Loud `slog.Warn`. |
| _(no JWT)_ + `AISOTROPY_ALLOW_NOAUTH=1` on a non-loopback / wildcard `AISOTROPY_ADDR` | **Boot refused** — `ErrNoAuthNonLocal` — unless `AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1` is ALSO set. |

Truthy = `strconv.ParseBool` (`1`, `t`, `T`, `TRUE`, `true`, `True`). Anything else is false, so an
insecure opt-in is never triggered by a typo.

## Consequences

- **Positive.** The zero-config-insecure hole is closed by construction: no omission yields a running
  unauthenticated server, and non-local NoAuth takes two deliberate env vars. The change is stdlib-only
  (`errors`, `net`, `strconv`, `strings`) — the repo's 100% MIT/Apache-2.0 posture is unchanged. The
  boot decision is a pure, unit-tested function (`resolveAuthBoot`), so the policy is verifiable without
  driving `main()`. No refusal message contains secret material (the config errors report lengths,
  formats, and paths only — verified by a dedicated no-leak test).
- **Negative / breaking.** Any existing deployment (or CI/dev script) that relied on keyless boot by
  omission now fails to start until it sets `AISOTROPY_ALLOW_NOAUTH=1` (and, off loopback,
  `AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1`). This is the intended blast radius: the break is loud, at boot,
  with an actionable message — strictly safer than the previous silent-warn-and-serve behavior. This is
  acceptable in the current reconstruction phase (safety over backward compatibility).

## Alternatives considered

- **Keep NoAuth as the default, escalate the warning.** Rejected — a warning does not enforce; the
  insecure state remained reachable by accident.
- **Only warn (not refuse) on non-local NoAuth.** Rejected in favor of refuse-by-default with an
  explicit override, per the task's "much harder to reach than today" bar; the loud non-local warning
  alone would repeat the same accident-reachable pattern this ADR exists to remove.
- **Default-deny (reject every request) instead of refusing to boot.** The runtime already default-denies
  when no authenticator is wired; but a binary that boots and 403s every request is a worse operator
  experience than a fatal, self-explaining boot refusal that says exactly which env var to set.

## Verification

- `go build ./...` exits 0; `gofmt`/`go vet` clean on the touched files.
- `go test ./aisotropy/... ./cmd/... ./internal/auth/...` passes, including the added cases:
  (a) no JWT + no `ALLOW_NOAUTH` → `ErrNoAuthenticator` / boot refused;
  (b) `ALLOW_NOAUTH=1` on loopback → NoAuth returned and boots;
  (c) JWT secret set → real authenticator, `ALLOW_NOAUTH` ignored;
  plus the non-local guard table (loopback passes; wildcard/`0.0.0.0`/routable refuse without the
  second override, pass with it) and a weak-secret no-leak assertion.
