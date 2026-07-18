package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// mode=middleware exercises Genkit-Go's NATIVE model-middleware mechanism —
// github.com/firebase/genkit/go/ai's ai.ModelMiddleware, defined in
// generate.go as:
//
//	type ModelMiddleware = core.Middleware[*ModelRequest, *ModelResponse, *ModelResponseChunk]
//
// and core.Middleware itself (core/middleware.go) as:
//
//	type Middleware[In, Out, Stream any] = func(StreamingFunc[In, Out, Stream]) StreamingFunc[In, Out, Stream]
//
// i.e. EXACTLY `func(ai.ModelFunc) ai.ModelFunc` — a middleware wraps the
// model's call function and returns a new one. It is attached to a single
// genkit.Generate call via ai.WithMiddleware(...) (ai/option.go), which
// v1.10.0 still ships and applies (marked "Deprecated: use WithUse instead,
// which supports Generate/Model/Tool hooks" — WithUse's ai.Hooks.WrapModel
// is the newer, non-deprecated way to do the same thing; this mode uses the
// literal `func(ModelFunc) ModelFunc` shape because that IS the artifact
// under test here). See README.md "middleware" for the WithMiddleware-vs-
// WithUse tradeoff.
//
// Like mode=observability (observability.go), mode=middleware does NOT
// reuse the shared arenaOutput envelope — its stdout contract is
// {text, middleware:{mechanism,inbound_fired,outbound_fired,invocations,
// supported}}. See main.go's main(): when out.Mode=="middleware", the
// arenaOutput.Structured field (stashed by runMiddleware below) is encoded
// directly instead of the arenaOutput itself.
type middlewareInfo struct {
	Mechanism     string `json:"mechanism"`
	InboundFired  bool   `json:"inbound_fired"`
	OutboundFired bool   `json:"outbound_fired"`
	Invocations   int    `json:"invocations"`
	Supported     bool   `json:"supported"`
}

type middlewareOutput struct {
	Text       string          `json:"text"`
	Middleware *middlewareInfo `json:"middleware"`
	Error      string          `json:"error,omitempty"`
}

// arenaMiddlewareSentinel is appended to the request's system message by the
// INBOUND half of newArenaMiddleware below, before the model ever sees the
// request.
const arenaMiddlewareSentinel = "\n[ARENA_MW]"

// middlewareState is the shared, mutex-guarded record newArenaMiddleware's
// closure mutates on every call — a plain ai.ModelMiddleware is just a Go
// closure, so per-call state is whatever the caller closes over, same as any
// HTTP middleware pattern.
type middlewareState struct {
	mu            sync.Mutex
	invocations   int
	inboundFired  bool
	outboundFired bool
}

// newArenaMiddleware builds one ai.ModelMiddleware instance with two
// OBSERVABLE transforms:
//
//   - INBOUND: before calling `next`, appends arenaMiddlewareSentinel to the
//     text of the request's system message (ai.RoleSystem), or — if the
//     request carries no system message at all — injects a new leading one
//     containing only the sentinel. Either way this mutates `req` BEFORE the
//     model function underneath sees it, proving the middleware ran on the
//     request path.
//   - OUTBOUND: after `next` returns, marks outboundFired — proving this
//     middleware's own code ran AGAIN on the response path, after the model
//     call completed, not merely once "around" it.
func newArenaMiddleware(st *middlewareState) ai.ModelMiddleware {
	return func(next ai.ModelFunc) ai.ModelFunc {
		return func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			st.mu.Lock()
			st.invocations++
			st.mu.Unlock()

			injected := false
			for _, m := range req.Messages {
				if m.Role != ai.RoleSystem {
					continue
				}
				for _, p := range m.Content {
					if p.IsText() {
						p.Text += arenaMiddlewareSentinel
						injected = true
					}
				}
			}
			if !injected {
				sysMsg := &ai.Message{Role: ai.RoleSystem, Content: []*ai.Part{ai.NewTextPart(arenaMiddlewareSentinel)}}
				req.Messages = append([]*ai.Message{sysMsg}, req.Messages...)
				injected = true
			}
			st.mu.Lock()
			st.inboundFired = injected
			st.mu.Unlock()

			// INBOUND has already run (req is mutated above); `next` is the
			// rest of the chain — every other middleware plus the model
			// itself. Whatever it returns is this middleware's OUTBOUND
			// observation point.
			resp, err := next(ctx, req, cb)

			st.mu.Lock()
			st.outboundFired = true
			st.mu.Unlock()

			return resp, err
		}
	}
}

// runMiddleware drives ONE qa-shaped turn through genkit.Generate with
// newArenaMiddleware attached via ai.WithMiddleware, then reports whether
// both the inbound (pre-model) and outbound (post-model) hooks actually
// fired — the executable proof, not a doc claim, that ai.ModelMiddleware is
// real and wired correctly in this Genkit-Go version.
func runMiddleware(rc *runCtx, out *arenaOutput) error {
	res := &middlewareOutput{Middleware: &middlewareInfo{Mechanism: "ai.ModelMiddleware", Supported: true}}
	out.Structured = res

	st := &middlewareState{}
	mw := newArenaMiddleware(st)

	opts := buildBaseOpts(rc)
	opts = append(opts, ai.WithMiddleware(mw))

	fmt.Fprintln(os.Stderr, "PROGRESS: middleware: running one qa turn through ai.ModelMiddleware (ai.WithMiddleware)")

	start := time.Now()
	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()

	st.mu.Lock()
	res.Middleware.InboundFired = st.inboundFired
	res.Middleware.OutboundFired = st.outboundFired
	res.Middleware.Invocations = st.invocations
	st.mu.Unlock()

	if err != nil {
		res.Error = err.Error()
		return fmt.Errorf("middleware: genkit.Generate: %w", err)
	}

	applyUsage(out, resp)
	res.Text = resp.Text()
	out.Reply = res.Text

	fmt.Fprintf(os.Stderr, "PROGRESS: middleware: inbound_fired=%v outbound_fired=%v invocations=%d\n",
		res.Middleware.InboundFired, res.Middleware.OutboundFired, res.Middleware.Invocations)
	return nil
}
