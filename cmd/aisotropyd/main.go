// Command aisotropyd is the Aisotropy agent runtime binary.
//
// As of Phase 8 (adoption surface) this is a THIN consumer of the public
// builder in package aisotropy: it registers the demo tools, the demo executor,
// and the default persona, then calls App.Run. All runtime wiring — working
// memory, tier routing, facts + semantic tiers, the outbox worker, OTel, the
// circuit breaker, cheap-model degradation, the sleep cycle, rate limiting, the
// cost meter, the /healthz + /health + /costs endpoints, the three /v1 chat
// routes, and SIGINT/SIGTERM graceful shutdown — lives in aisotropy.App.Run, not
// here. The env-var contract (AISOTROPY_ADDR, AISOTROPY_DATA_DIR,
// AISOTROPY_DEDICATED_TENANTS, RQLITE_URL, QDRANT_*, AISOTROPY_CHEAP_MODEL,
// AISOTROPY_SLEEPCYCLE_BIN, AISOTROPY_RATE_PER_SEC, AISOTROPY_BURST,
// ANTHROPIC_API_KEY) is unchanged.
//
// The demo tools (search_docs, read_financials, write_tasks) + the echo demo
// executor are placeholders, not a product: they exist so the binary boots and
// exercises the scope-gated agent loop without a real integration. A real bot is
// built the same way this file is — against package aisotropy only — replacing
// the demo tools/executor/persona with its own (see examples/weatherbot and
// docs/guides/build-your-own-agent).
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"

	"github.com/Luum-Home/aisotropy/aisotropy"
	"github.com/Luum-Home/aisotropy/internal/api"
)

// defaultAddr mirrors aisotropy.App.Run's fallback when AISOTROPY_ADDR is unset,
// so the boot-time NoAuth non-loopback guard evaluates the SAME address the server
// will actually bind. (App.Run: default AISOTROPY_ADDR, or ":8080".)
const defaultAddr = ":8080"

// authBootDecision is the boot-time outcome of resolving authentication from the
// environment: either PROCEED (a non-nil Authenticator + a mode label for logging)
// or REFUSE to boot with an operator-facing reason. refuse is empty on success and,
// when set, never contains secret material (the underlying config errors report
// lengths/paths/formats only, never key bytes).
type authBootDecision struct {
	authr  aisotropy.Authenticator
	mode   string
	refuse string
}

// resolveAuthBoot turns AuthenticatorFromEnv plus the NoAuth non-loopback guard into
// a single, testable boot decision. It is pure w.r.t. process state (reads only the
// environment via the aisotropy helpers) and performs NO logging or os.Exit, so the
// exact refuse/allow policy can be unit-tested without driving main(). addr is the
// resolved listen address the server will bind.
func resolveAuthBoot(addr string) authBootDecision {
	authr, mode, err := aisotropy.AuthenticatorFromEnv()
	switch {
	case errors.Is(err, aisotropy.ErrNoAuthenticator):
		// No JWT configured and NoAuth was not explicitly opted into: refuse to
		// come up wide open by omission.
		return authBootDecision{refuse: "no authentication is configured. Set AISOTROPY_JWT_SECRET " +
			"(HS256), AISOTROPY_JWT_PUBLIC_KEY[_FILE] (RS256), or AISOTROPY_JWT_JWKS_URL (RS256 JWKS) to " +
			"enable JWT authentication, OR explicitly set AISOTROPY_ALLOW_NOAUTH=1 to run WITHOUT " +
			"authentication (local dev only — path tenant_id/user_id are then trusted with no credential)."}
	case err != nil:
		// Malformed/weak config (short HS256 secret, bad max-ttl, ambiguous modes,
		// unparseable key). err reports lengths/formats/paths only, never the secret.
		return authBootDecision{refuse: "invalid authentication configuration " +
			"(check AISOTROPY_JWT_SECRET length and AISOTROPY_JWT_MAX_TTL format): " + err.Error()}
	}
	if mode == "noauth" {
		// NoAuth was explicitly opted in; still refuse on a non-loopback bind unless
		// the operator ALSO set AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1.
		if gErr := aisotropy.NoAuthNonLocalGuard(addr); gErr != nil {
			return authBootDecision{refuse: "NoAuth (no credential verification) requested on the " +
				"non-loopback bind address " + addr + ". Bind AISOTROPY_ADDR to loopback " +
				"(127.0.0.1/localhost/::1) for local dev, configure a JWT authenticator, or set " +
				"AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1 to override (STRONGLY discouraged)."}
		}
	}
	return authBootDecision{authr: authr, mode: mode}
}

func main() {
	// Authentication is SECURABLE BY CONFIG and SECURE BY DEFAULT: AuthenticatorFromEnv
	// returns a real JWT authenticator when a secret/key is set, and NoAuth ONLY when
	// AISOTROPY_ALLOW_NOAUTH=1 is explicitly set. Absent both, resolveAuthBoot reports a
	// refusal and the binary exits non-zero rather than coming up wide open by omission.
	// A weak signing secret, a malformed AISOTROPY_JWT_MAX_TTL, or NoAuth on a
	// non-loopback interface are likewise fatal. No refusal message contains the secret.
	addr := os.Getenv("AISOTROPY_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	dec := resolveAuthBoot(addr)
	if dec.refuse != "" {
		slog.Error("aisotropyd refusing to start: " + dec.refuse)
		os.Exit(1)
	}
	authr, authMode := dec.authr, dec.mode

	app := aisotropy.New(
		// Scope-gated demo tools (Phase 7 behavior axis): read_financials and
		// write_tasks require a scope; search_docs is always available.
		aisotropy.WithTool(aisotropy.Tool{Name: "search_docs", Description: "search the tenant knowledge base"}),
		aisotropy.WithTool(aisotropy.Tool{Name: "read_financials", Description: "read financial records", RequiredScope: "read:financial-data"}),
		aisotropy.WithTool(aisotropy.Tool{Name: "write_tasks", Description: "create and update tasks", RequiredScope: "write:tasks"}),
		// The demo executor echoes calls and never fails; a real deployment
		// replaces it with WithToolExecutor(itsOwnExecutor).
		aisotropy.WithToolExecutor(api.DemoExecutor{}),
		// Register the built-in default persona explicitly for the empty product
		// id, so the binary's persona is self-documenting rather than implicit.
		aisotropy.WithPersona("", aisotropy.Persona{BasePrompt: "You are the Aisotropy agent. Respond briefly and directly."}),
		// Authentication is selected from the environment (see
		// AuthenticatorFromEnv): with AISOTROPY_JWT_SECRET set the binary
		// verifies HS256 bearer JWTs; unset it opts OUT via NoAuth (path
		// tenant/user trusted verbatim) so the binary still boots zero-config.
		// Without any WithAuthenticator the runtime would default-deny every /v1
		// request.
		aisotropy.WithAuthenticator(authr),
	)

	switch authMode {
	case "jwt-hs256":
		slog.Info("aisotropyd authentication enabled: verifying HS256 bearer JWTs (AISOTROPY_JWT_SECRET is set).")
	case "jwt-rs256":
		slog.Info("aisotropyd authentication enabled: verifying RS256 bearer JWTs against a static RSA public key (AISOTROPY_JWT_PUBLIC_KEY[_FILE] is set).")
	case "jwt-rs256-jwks":
		slog.Info("aisotropyd authentication enabled: verifying RS256 bearer JWTs against a JWKS endpoint (AISOTROPY_JWT_JWKS_URL is set).")
	default: // "noauth" — reached only via an explicit AISOTROPY_ALLOW_NOAUTH=1 opt-in
		slog.Warn("aisotropyd is running in NoAuth (insecure) mode: path tenant_id/user_id are trusted with no credential verification (AISOTROPY_ALLOW_NOAUTH is set). " +
			"Set AISOTROPY_JWT_SECRET (HS256), AISOTROPY_JWT_PUBLIC_KEY[_FILE] (RS256), or AISOTROPY_JWT_JWKS_URL (RS256 JWKS) to enable JWT authentication for any non-local deployment.")
	}

	// Operator-endpoint + transport posture (values never printed, only presence):
	// /costs and /bitrate are default-closed unless AISOTROPY_OPS_TOKEN is set,
	// and plaintext requests are rejected only when AISOTROPY_REQUIRE_TLS is on.
	if api.OpsTokenFromEnv() != "" {
		slog.Info("aisotropyd ops endpoints enabled: /costs and /bitrate require the AISOTROPY_OPS_TOKEN bearer token.")
	} else {
		slog.Info("aisotropyd ops endpoints disabled: /costs and /bitrate return 503 until AISOTROPY_OPS_TOKEN is set.")
	}
	if requireTLS, _ := strconv.ParseBool(os.Getenv("AISOTROPY_REQUIRE_TLS")); requireTLS {
		slog.Info("aisotropyd TLS enforcement ON (AISOTROPY_REQUIRE_TLS): non-TLS requests are rejected 400.")
	}

	if err := app.Run(context.Background()); err != nil {
		slog.Error("aisotropyd exited with error", "err", err)
		os.Exit(1)
	}
}
