package aisotropy

import (
	_ "embed"
	"net/http"
	"strings"
)

// devUIHTML is the single, self-contained development chat page served by
// WithDevUI. It is embedded at build time (//go:embed) so the binary carries no
// external asset dependency and the page issues ZERO external requests — inline
// CSS, vanilla JS, no CDN, no framework, no fonts. The page talks only to the
// app's own streaming endpoint on the same origin.
//
//go:embed devui.html
var devUIHTML []byte

// defaultDevUIPath is where WithDevUI mounts the page: a NON-/v1 route, so it
// sits OUTSIDE the /v1 authn middleware exactly like /healthz. It is a
// convenience surface, deliberately unauthenticated (see the WithDevUI doc).
const defaultDevUIPath = "/ui"

// WithDevUI mounts a built-in, minimal, single-page development chat UI at
// GET /ui. It is a DEVELOPMENT CONVENIENCE, NOT A PRODUCTION UI:
//
//   - It is UNAUTHENTICATED. The page is served on a non-/v1 route (like
//     /healthz), outside the authn middleware, so anyone who can reach the
//     process can open it. Never expose it on a public listener.
//   - It is a single self-contained HTML page (embedded via //go:embed, inline
//     CSS, vanilla JS, zero external requests) whose only job is to let a
//     developer type a message and watch the streamed response, so the
//     streaming pipeline can be exercised end-to-end without hand-rolling a
//     client.
//
// The page POSTs to the app's OWN streaming endpoint
// (/v1/tenant/{t}/user/{u}/chat/stream) and parses the existing named-event SSE
// wire (token/done/error) with the browser's fetch + ReadableStream — there is
// no reverse proxy in front of the stream, so the classic proxy SSE buffering
// gotcha (a proxy that withholds bytes until its own flush interval elapses)
// structurally cannot occur here: the browser reads the Huma SSE handler's
// per-event flush directly. Because the endpoint lives behind the /v1 authn
// middleware, the page carries an optional bearer-token field; with the app run
// under WithAuthenticator(NoAuth()) no token is needed, and with a real
// authenticator the developer pastes a valid token.
//
// For PRODUCTION, do not ship this page. Build your own frontend against the
// AG-UI (/chat/agui) or Vercel-AI-SDK (/chat/aisdk) protocol adapters, or the
// native SSE endpoint, behind your own authenticated surface.
//
// WithDevUI is inert unless called: without it, GET /ui is not registered and
// returns 404, so there is no drift in a default build.
func WithDevUI() Option {
	return WithDevUIAt(defaultDevUIPath)
}

// WithDevUIAt is WithDevUI mounted at a caller-chosen path instead of /ui. The
// same "development convenience, unauthenticated, not for production" caveats in
// the WithDevUI doc apply verbatim.
//
// The path is normalized to a single leading slash. An empty (or slash-only)
// path falls back to the default /ui. Mount it on a NON-/v1 route: a /v1 path
// would land the page BEHIND the authn middleware and be rejected before it
// could load, defeating the point of a zero-setup dev page.
func WithDevUIAt(path string) Option {
	return func(a *App) { a.devUIPath = normalizeDevUIPath(path) }
}

// normalizeDevUIPath coerces a caller path to a single-leading-slash route,
// falling back to defaultDevUIPath for an empty or slash-only value.
func normalizeDevUIPath(path string) string {
	path = strings.TrimSpace(path)
	path = "/" + strings.TrimLeft(path, "/")
	if path == "/" {
		return defaultDevUIPath
	}
	return path
}

// devUIHandler serves the embedded development chat page as text/html. It is a
// static, read-only handler: it renders the same bytes on every request and
// performs no per-request work, so it is safe to mount outside the authn chain.
func devUIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(devUIHTML)
	}
}
