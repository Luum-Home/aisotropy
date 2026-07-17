package aisotropy

import (
	"net/http"
	"strings"
)

// sseStreamingSuffixes are the request-path suffixes of aisotropy's token-by-token
// SSE streaming routes. These responses MUST NOT be buffered by an intermediary
// reverse proxy: nginx buffers proxied responses by default (proxy_buffering on),
// which would collect the whole event stream and deliver it to the client in one
// write at the very end — turning a live token stream into a single delayed dump.
//
// The three routes are the native aisotropy SSE endpoint (/chat/stream, served by
// Huma's sse.Register), and the two protocol costumes over the SAME StreamCore
// pipeline: the Vercel AI SDK adapter (/chat/aisdk) and the AG-UI adapter
// (/chat/agui). See internal/api/{chat_stream,aisdk,agui}.go.
var sseStreamingSuffixes = []string{"/chat/stream", "/chat/aisdk", "/chat/agui"}

// sseAntiBuffering sets nginx's `X-Accel-Buffering: no` opt-out on the SSE
// streaming routes so a token stream survives a reverse proxy that buffers by
// default (nginx does; Caddy does not). It is DEFENSE IN DEPTH, not the operator's
// only line: the reference deploy/proxy/nginx.conf ALSO disables buffering on the
// stream location, but this header means the stream still works even if the
// operator forgets that directive.
//
// Why a middleware and not the handlers: the raw AG-UI adapter already sets this
// header by hand (internal/api/agui.go), but the native /chat/stream path is
// served by Huma's sse.Register, whose response Body func aisotropy does not
// control — it emits only `Content-Type: text/event-stream` and no anti-buffering
// header. The Vercel AI SDK adapter likewise omitted it. Setting the header in a
// chi middleware — which runs before any bytes are written, so it lands in the
// header block flushed on the first event — covers every SSE route uniformly,
// including the one whose handler is out of reach. Non-streaming JSON routes are
// left untouched: the header is only relevant to (and only set on) streamed
// responses.
func sseAntiBuffering(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, suffix := range sseStreamingSuffixes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				// nginx honors this per-response even under the default
				// proxy_buffering on, disabling buffering for THIS stream.
				w.Header().Set("X-Accel-Buffering", "no")
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}
