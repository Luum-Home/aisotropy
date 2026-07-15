package api

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// ops_gate.go guards the OPERATOR endpoints — GET /costs (costMeter.Snapshot,
// all-tenant cost data) and GET /bitrate (bitrateStore.AllSummaries,
// cross-tenant memory-lift data). These are plain chi routes registered
// OUTSIDE the Huma auth middleware chain that covers /v1 (see
// internal/api/authn.go), so without a gate any unauthenticated caller could
// read aggregated per-tenant data.
//
// These endpoints are fundamentally DIFFERENT from the per-tenant /v1 routes:
// /v1 is authenticated per (tenant, user) and returns one tenant's data, gated
// by the must-agree bind in BindAuthMiddleware. The ops endpoints return
// ALL-tenant aggregates and are gated instead by a single shared operator
// token (AISOTROPY_OPS_TOKEN) — they are meant for the operator running the
// deployment, not for tenants.
//
// OpsEnvVar is the environment variable the wiring reads to obtain the operator
// token; OpsTokenFromEnv is the single source of truth so app.go and this
// gate never diverge on the name.
const OpsEnvVar = "AISOTROPY_OPS_TOKEN"

// OpsTokenFromEnv returns the configured operator token (AISOTROPY_OPS_TOKEN),
// or "" when unset. An empty result makes OpsAuthGate close the endpoint
// (default-deny — see below), so the wiring can pass this straight through
// without any special-casing.
func OpsTokenFromEnv() string {
	return os.Getenv(OpsEnvVar)
}

// OpsAuthGate returns a chi-style middleware that guards the operator endpoints
// with a shared bearer token, default-deny and consistent with the Option-C
// posture of the /v1 gate (no silent open):
//
//   - opsToken == "": the endpoint is CLOSED. Every request gets 503 with a
//     JSON body telling the operator to set AISOTROPY_OPS_TOKEN. This is the
//     safe default — an unconfigured deployment does not leak all-tenant data.
//   - opsToken != "": the request MUST carry "Authorization: Bearer <token>"
//     whose token matches opsToken via crypto/subtle.ConstantTimeCompare
//     (constant-time and length-safe). A missing header, malformed header, or
//     mismatched token is a generic 401 "unauthorized"; a match calls next.
//
// The token is NEVER logged. Errors are deliberately generic (no distinction
// between "no header" and "wrong token") so the gate reveals nothing about why
// a request was rejected.
func OpsAuthGate(opsToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opsToken == "" {
				writeOpsError(w, http.StatusServiceUnavailable,
					"ops endpoints are disabled: set AISOTROPY_OPS_TOKEN to enable /costs and /bitrate")
				return
			}

			presented, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(opsToken)) != 1 {
				writeOpsError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the credential from an "Authorization: Bearer <token>"
// header. The scheme match is case-insensitive per RFC 7235; the returned bool
// is false when the header is missing or not a Bearer credential. The token
// itself is returned verbatim (no trimming) so ConstantTimeCompare — which is
// length-safe — sees the exact presented bytes.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return header[len(prefix):], true
}

// writeOpsError writes a JSON error body with the given status. It mirrors the
// {"error":"..."} shape the existing ops handlers use (see app.go's /bitrate
// 500 path) and never includes the token.
func writeOpsError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// msg is a fixed internal string, never caller-controlled, so a plain
	// concatenation cannot inject into the JSON.
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
