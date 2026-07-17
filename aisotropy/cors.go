package aisotropy

import (
	"net/http"
	"os"
)

// corsAllowedMethods / corsAllowedHeaders / corsMaxAgeSeconds are the fixed CORS
// response values aisotropyd advertises for an allowed origin. The API only
// serves GET/POST (plus the preflight OPTIONS), and a browser client needs to
// send Content-Type (JSON bodies), Authorization (the /v1 bearer JWT), and
// Last-Event-ID (SSE stream resume via EventSource / a manual reconnect).
const (
	corsAllowedMethods = "GET, POST, OPTIONS"
	corsAllowedHeaders = "Content-Type, Authorization, Last-Event-ID"
	// corsMaxAgeSeconds lets a browser cache a successful preflight so it is not
	// re-sent before every cross-origin call. 600s (10 min) is conservative.
	corsMaxAgeSeconds = "600"
)

// corsAllowlistFromEnv parses AISOTROPY_CORS_ALLOWED_ORIGINS — a comma-separated
// list of EXACT origins (scheme + host + optional port, e.g.
// "https://app.example.com,http://localhost:5173") — into a lookup set. An unset
// or blank value yields an empty set, which newCORSMiddleware treats as
// "same-origin only: emit no CORS headers at all" (secure default). Values are
// trimmed; blanks are dropped. Origins are matched case-sensitively except for
// the scheme+host, which browsers always send lowercase, so an exact string
// match is correct.
func corsAllowlistFromEnv() map[string]struct{} {
	return corsAllowlist(splitCSV(os.Getenv("AISOTROPY_CORS_ALLOWED_ORIGINS")))
}

// corsAllowlist builds the origin lookup set from an already-parsed slice.
func corsAllowlist(origins []string) map[string]struct{} {
	set := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		if o != "" {
			set[o] = struct{}{}
		}
	}
	return set
}

// newCORSMiddleware returns a chi middleware implementing an opt-in,
// exact-origin-allowlist CORS policy — hand-rolled on net/http, no dependency.
//
// SECURE DEFAULT: an empty allowlist (AISOTROPY_CORS_ALLOWED_ORIGINS unset) makes
// this a pass-through that emits NO CORS headers, so the API stays same-origin
// only. It NEVER reflects "*" — this is a multi-tenant, bearer-authenticated API,
// and a wildcard (especially with credentials) would let any web page drive it
// with a victim's token. When the allowlist is non-empty, the middleware echoes
// the request Origin back in Access-Control-Allow-Origin ONLY when that exact
// origin is on the list; an origin not on the list receives no CORS headers and
// the browser blocks the cross-origin read. Access-Control-Allow-Credentials is
// deliberately NOT sent: the API authenticates via the Authorization header
// (a bearer token the client sets explicitly), which does not require credentials
// mode, so cookies are never in scope and the safest posture is to leave it off.
//
// Preflight (OPTIONS carrying Access-Control-Request-Method) is answered here
// with 204 and the method/header allowances, short-circuiting before the request
// reaches the router's routes or the /v1 auth gate. Because it is installed as an
// early chi middleware wrapping every route, the Access-Control-Allow-Origin
// header is set in the response header block BEFORE any handler writes — so it is
// present on the streaming (text/event-stream) responses too, not just JSON.
func newCORSMiddleware(allowed map[string]struct{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No allowlist configured: same-origin only, emit nothing.
			if len(allowed) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			isPreflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""

			// Any Origin-conditional response varies by Origin; advertise that to
			// caches whenever a cross-origin request could be shaped by the header,
			// so a cached allowed response is never served for a different origin.
			if origin != "" {
				w.Header().Add("Vary", "Origin")
			}

			_, ok := allowed[origin]
			if origin != "" && ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if isPreflight {
					w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
					w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
					w.Header().Set("Access-Control-Max-Age", corsMaxAgeSeconds)
				}
			}

			// A preflight is a browser handshake, never routed to a handler: answer
			// it here with 204. When the origin was allowed the headers above make
			// the browser proceed; when it was not, the absence of
			// Access-Control-Allow-Origin makes the browser block the real request.
			if isPreflight {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
