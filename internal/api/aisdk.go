// Phase 2A of the frontend-UI adapter work (docs/research/frontend-ui-integration-landscape.md):
// a protocol adapter that re-encodes the EXISTING streaming pipeline as the
// Vercel AI SDK "UI Message Stream" (the current v5/v6/v7 wire format consumed
// by `@ai-sdk/react`'s `useChat` AND assistant-ui at once — the single highest
// coverage-per-effort target in the landscape scan).
//
// CRITICAL — this is a costume over the stream, not a fork of it. RegisterAISDK
// runs the SAME StreamCore (stream_core.go) the aisotropy-native SSE endpoint
// (RegisterStream) runs, over the SAME StreamDeps, so the constitutional gates
// hold BY CONSTRUCTION: tenant/user isolation travels via ctx (never the body),
// the quota gate rejects before any token is spent, the input/output guardrails
// run, and provider key-brokering stays inside the pipeline. This file ONLY
// differs from chat_stream.go in how the assistant output is RE-ENCODED on the
// wire — the request is decoded from the AI SDK's `{messages: UIMessage[]}`
// envelope instead of aisotropy's own ChatRequest, and the output is emitted as
// the AI SDK's type-discriminated part stream instead of the named token/done/
// error SSE events. No pipeline behavior lives here.
//
// Wire contract (grounded in the research doc "Vercel AI SDK UI Message Stream"
// section): response `Content-Type: text/event-stream`, header
// `x-vercel-ai-ui-message-stream: v1`, each part framed as
// `data: ${JSON}\n\n`, the stream terminating with the literal `data: [DONE]\n\n`.
// Each part carries a `type` discriminator. For a text-only turn the choreography
// is: `start` -> `text-start`(id) -> `text-delta`(id, delta)* -> `text-end`(id)
// -> `finish` -> `[DONE]`. A pipeline failure emits `error`(errorText); a client
// cancellation (which StreamCore already propagates to stop the upstream LLM)
// emits `abort`.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// AI SDK UI Message Stream constants: the response headers and the `type`
// discriminator values for the parts this text-only adapter emits. Kept as
// named constants (not inline string literals) so the wire contract is stated
// once and the golden byte-format test asserts against the same source of truth.
const (
	// aisdkStreamHeader / aisdkStreamHeaderValue is the protocol-version marker
	// the AI SDK client keys on to select the UI Message Stream decoder.
	aisdkStreamHeader      = "x-vercel-ai-ui-message-stream"
	aisdkStreamHeaderValue = "v1"

	// aisdkContentType is the SSE media type every part rides on.
	aisdkContentType = "text/event-stream"

	// aisdkDoneSentinel is the literal terminator line body the AI SDK reader
	// watches for to know the HTTP stream is complete (written as
	// `data: [DONE]\n\n`).
	aisdkDoneSentinel = "[DONE]"

	// Part type discriminators (the `type` field of each JSON envelope).
	aisdkPartStart     = "start"
	aisdkPartTextStart = "text-start"
	aisdkPartTextDelta = "text-delta"
	aisdkPartTextEnd   = "text-end"
	aisdkPartFinish    = "finish"
	aisdkPartError     = "error"
	aisdkPartAbort     = "abort"
)

// aisdkTextPartID is the id shared by the `text-start`/`text-delta`/`text-end`
// parts that make up one assistant text block. The AI SDK only requires this id
// to correlate a text block's parts WITHIN a single message stream (each stream
// begins a fresh message with its own `start` part), and this adapter emits
// exactly one text block per turn — so a stable constant is sufficient and keeps
// the emitted bytes deterministic (which the golden wire-format test relies on).
const aisdkTextPartID = "0"

// aisdkStartPart is the `start` part opening a message stream.
type aisdkStartPart struct {
	Type string `json:"type"`
}

// aisdkTextStartPart opens a text block; ID correlates its deltas and end.
type aisdkTextStartPart struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// aisdkTextDeltaPart carries one incremental fragment (delta) of assistant text.
type aisdkTextDeltaPart struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Delta string `json:"delta"`
}

// aisdkTextEndPart closes the text block opened by aisdkTextStartPart.
type aisdkTextEndPart struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// aisdkFinishPart marks a clean, complete end of assistant generation. Emitted
// only on terminal success, immediately before the `[DONE]` sentinel.
type aisdkFinishPart struct {
	Type string `json:"type"`
}

// aisdkErrorPart surfaces a pipeline failure in-band (the AI SDK reports errors
// as stream parts, never as an HTTP status — the response is already 200 with
// the streaming body open by the time the pipeline can fail).
type aisdkErrorPart struct {
	Type      string `json:"type"`
	ErrorText string `json:"errorText"`
}

// aisdkAbortPart signals the turn was aborted (client cancellation). StreamCore
// already stops the upstream LLM on ctx cancellation; this part tells any still-
// connected consumer the generation was interrupted rather than completed.
type aisdkAbortPart struct {
	Type string `json:"type"`
}

// aisdkUIPart is one part of an incoming UIMessage. Only the text discriminator
// is consumed by this text-only adapter; any other part type (tool-input-*,
// data-*, file, …) is decoded but ignored when reconstructing the user's text,
// so a richer client payload degrades cleanly to its text content rather than
// failing to parse.
type aisdkUIPart struct {
	Type string `json:"type" doc:"Part discriminator; only \"text\" contributes to the decoded prompt"`
	Text string `json:"text,omitempty" doc:"Text content when Type == \"text\""`
}

// aisdkUIMessage is one message in the AI SDK request envelope: a role plus an
// ordered list of parts (NOT a flat content string — that is the pre-v5 shape).
type aisdkUIMessage struct {
	ID    string        `json:"id,omitempty" doc:"Client message id"`
	Role  string        `json:"role" doc:"Message role (user/assistant/system)"`
	Parts []aisdkUIPart `json:"parts" doc:"Ordered content parts of the message"`
}

// aisdkRequest is the AI SDK `useChat` request body. Messages is the required
// conversation transcript; the adapter decodes the LATEST user message's text
// into the aisotropy prompt. ID is the AI SDK chat/thread identifier, used as
// the working-memory session key when no explicit session_id is supplied.
// SessionID/ProductID are optional aisotropy extensions honored when present so
// a caller can pin the session key or persona without diverging from the AI SDK
// envelope. Tenant and user are NOT read from the body — they arrive on the path
// (and, in production, via the authn middleware), per the tenant-isolation gate.
type aisdkRequest struct {
	ID       string           `json:"id,omitempty" doc:"AI SDK chat/thread id; used as the working-memory session key when session_id is absent"`
	Messages []aisdkUIMessage `json:"messages" doc:"Conversation transcript; the latest user message's text becomes the prompt"`

	// SessionID / ProductID are optional aisotropy extensions (omitempty): a
	// stock AI SDK client never sends them and is unaffected; a caller that does
	// gets explicit control over the session key and the persona.
	SessionID string `json:"session_id,omitempty" doc:"Optional explicit working-memory session key (overrides id)"`
	ProductID string `json:"product_id,omitempty" doc:"Optional persona/product selector"`
}

// aisdkInput binds the tenant/user path params (validated by the same
// identifier pattern ChatInput uses — defense-in-depth for the isolation gate)
// to the AI SDK request body.
type aisdkInput struct {
	TenantID string       `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string       `path:"user_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique user ID within the tenant" example:"alice"`
	Body     aisdkRequest `contentType:"application/json"`
}

// latestUserPrompt returns the concatenated text of the LAST message whose role
// is "user", scanning from the end so a multi-turn transcript resolves to the
// current turn (the AI SDK sends the full history; only the newest user message
// is this request's input). Non-text parts are skipped. Returns "" when there is
// no user message or it carries no text — the caller treats that as an empty/
// malformed request and rejects it before any stream opens.
func latestUserPrompt(msgs []aisdkUIMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != roleUser {
			continue
		}
		var sb strings.Builder
		for _, p := range msgs[i].Parts {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// aisdkStream is the UI Message Stream encoder: it frames each part as
// `data: ${JSON}\n\n` and flushes so the client renders live. JSON is encoded
// with HTML escaping DISABLED so assistant text containing <, >, or & streams
// verbatim (still valid JSON — the AI SDK decodes it back exactly) rather than
// as </>/&, which matters for a text/code channel.
type aisdkStream struct {
	w       io.Writer
	flusher http.Flusher
}

// newAISDKStream discovers the http.Flusher behind the (possibly wrapped)
// response writer using the same unwrap walk huma's own SSE helper uses, so live
// per-part flushing works whether the writer is the raw net/http one or an
// adapter wrapper (humachi) or a test ResponseRecorder.
func newAISDKStream(w io.Writer) *aisdkStream {
	s := &aisdkStream{w: w}
	probe := w
	for {
		if f, ok := probe.(http.Flusher); ok {
			s.flusher = f
			break
		}
		u, ok := probe.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		probe = u.Unwrap()
	}
	return s
}

// part JSON-encodes v and writes it as one `data: ${JSON}\n\n` frame, then
// flushes. It returns any write error so the caller (StreamSink.OnToken) can
// abort the turn on a client disconnect — mirroring the aisotropy SSE encoder's
// failed-write early return, so StreamCore stops WITHOUT recording cost or
// persisting a partial turn.
func (s *aisdkStream) part(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	// json.Encoder.Encode appends a trailing newline; the SSE framing supplies
	// its own "\n\n", so strip the encoder's.
	payload := bytes.TrimRight(buf.Bytes(), "\n")
	if _, err := s.w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := s.w.Write(payload); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	s.flush()
	return nil
}

// done writes the literal `data: [DONE]\n\n` stream terminator and flushes.
// Best-effort: a write failure here (client already gone) has nowhere to go.
func (s *aisdkStream) done() {
	_, _ = s.w.Write([]byte("data: " + aisdkDoneSentinel + "\n\n"))
	s.flush()
}

func (s *aisdkStream) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// RegisterAISDK mounts POST /v1/tenant/{tenant_id}/user/{user_id}/chat/aisdk,
// the Vercel AI SDK "UI Message Stream" adapter. Its parameter list mirrors
// RegisterStream's one-for-one so cmd/aisotropyd (and aisotropy.App) wire it
// with the identical dependencies and it inherits the same route middleware /
// path identity — the two endpoints are the SAME pipeline behind two wire
// costumes.
//
// See RegisterStream's doc for the meaning of each dependency (client, wm,
// store, limiter, toolReg, meter, cheapClient, semStore, embedder, reranker,
// persona, inPricePer1M/outPricePer1M, bptSampler, gw, guard); they are bundled
// verbatim into the same StreamDeps StreamCore consumes.
func RegisterAISDK(api huma.API, client llm.Client, wm *memory.WorkingMemory, store facts.Store, limiter *memory.Limiter, toolReg *tools.Registry, meter *costmeter.Meter, cheapClient llm.Client, semStore semantic.Store, embedder semantic.Embedder, reranker semantic.Reranker, persona PersonaProvider, inPricePer1M, outPricePer1M float64, bptSampler bitrate.SampleRecorder, gw *gateway.Gateway, guard *Guard) {
	deps := StreamDeps{
		Client:        client,
		WM:            wm,
		Store:         store,
		Limiter:       limiter,
		ToolReg:       toolReg,
		Meter:         meter,
		CheapClient:   cheapClient,
		SemStore:      semStore,
		Embedder:      embedder,
		Reranker:      reranker,
		Persona:       persona,
		InPricePer1M:  inPricePer1M,
		OutPricePer1M: outPricePer1M,
		BPTSampler:    bptSampler,
		GW:            gw,
		Guard:         guard,
	}

	huma.Register(api, huma.Operation{
		OperationID: "chat-aisdk",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat/aisdk",
		Summary:     "Chat via the Vercel AI SDK UI Message Stream (Phase 2A adapter)",
		Description: "Serves the SAME streaming pipeline as /chat/stream re-encoded as the " +
			"Vercel AI SDK \"UI Message Stream\" (header x-vercel-ai-ui-message-stream: v1, " +
			"SSE parts framed as data: ${JSON}, terminated by data: [DONE]). Consumed by " +
			"@ai-sdk/react useChat and assistant-ui. The request body is the AI SDK " +
			"{messages: UIMessage[]} envelope; the latest user message's text is decoded " +
			"into the prompt. Tenant/user isolation, the quota gate, guardrails, memory " +
			"hydration, and cost accounting are unchanged — this endpoint only re-encodes " +
			"the output.",
		Responses: map[string]*huma.Response{
			"200": {
				Description: "AI SDK UI Message Stream (SSE)",
				Content: map[string]*huma.MediaType{
					aisdkContentType: {},
				},
			},
		},
	}, func(ctx context.Context, in *aisdkInput) (*huma.StreamResponse, error) {
		// Decode BEFORE opening the stream so a malformed/empty request is a
		// clean HTTP 4xx (no 200 + streaming body). Once the StreamResponse is
		// returned the status is already 200 and every failure — including a
		// pipeline error — must be surfaced in-band as an `error` part.
		prompt := latestUserPrompt(in.Body.Messages)
		if strings.TrimSpace(prompt) == "" {
			return nil, huma.Error400BadRequest("request must contain a user message with text content")
		}
		session := firstNonEmpty(in.Body.SessionID, in.Body.ID)
		if session == "" {
			return nil, huma.Error400BadRequest("request must carry a conversation id (top-level \"id\" or \"session_id\")")
		}

		// Reuse the shared ChatInput as StreamCore's input: identity comes from
		// the path (never the body), the decoded latest-user text is the prompt,
		// and the AI SDK chat id (or explicit session_id) is the session key.
		chatIn := &ChatInput{
			TenantID: in.TenantID,
			UserID:   in.UserID,
			Body: ChatRequest{
				Prompt:    prompt,
				SessionID: session,
				ProductID: in.Body.ProductID,
			},
		}

		return &huma.StreamResponse{
			Body: func(hc huma.Context) {
				hc.SetHeader("Content-Type", aisdkContentType)
				hc.SetHeader(aisdkStreamHeader, aisdkStreamHeaderValue)
				hc.SetHeader("Cache-Control", "no-cache")
				hc.SetHeader("Connection", "keep-alive")

				stream := newAISDKStream(hc.BodyWriter())

				// Open the message. Emitted up front (before StreamCore) so the
				// client can render a pending assistant message immediately; a
				// pipeline error that follows is then reported as an in-band
				// `error` part on the already-open stream, which is exactly how
				// the AI SDK expects backend failures.
				_ = stream.part(aisdkStartPart{Type: aisdkPartStart})

				// textOpen tracks whether a `text-start` has been emitted, so the
				// text block is opened lazily on the first delivered delta (a turn
				// that produces no text emits no text block at all). finished is
				// set in OnDone; StreamCore returns nil WITHOUT calling OnDone on a
				// client cancellation, so this flag is how the terminal switch
				// tells a clean finish from an abort.
				var textOpen, finished bool
				sink := StreamSink{
					OnToken: func(text string) error {
						if !textOpen {
							if err := stream.part(aisdkTextStartPart{Type: aisdkPartTextStart, ID: aisdkTextPartID}); err != nil {
								return err
							}
							textOpen = true
						}
						return stream.part(aisdkTextDeltaPart{Type: aisdkPartTextDelta, ID: aisdkTextPartID, Delta: text})
					},
					OnDone: func(_ StreamFinal) {
						if textOpen {
							_ = stream.part(aisdkTextEndPart{Type: aisdkPartTextEnd, ID: aisdkTextPartID})
						}
						_ = stream.part(aisdkFinishPart{Type: aisdkPartFinish})
						finished = true
					},
				}

				err := StreamCore(ctx, deps, chatIn, sink)
				switch {
				case err != nil:
					// Pipeline failure (quota, blocked input, hydration, stream
					// start, or a mid-stream LLM error): report it in-band and
					// still terminate the SSE stream with [DONE] so the client's
					// reader loop ends cleanly.
					_ = stream.part(aisdkErrorPart{Type: aisdkPartError, ErrorText: err.Error()})
					stream.done()
				case !finished:
					// Client cancellation / abnormal upstream close: StreamCore
					// already stopped the upstream LLM. Emit `abort` best-effort
					// (the client is likely already gone) and do NOT append [DONE]:
					// the turn was interrupted, not completed.
					_ = stream.part(aisdkAbortPart{Type: aisdkPartAbort})
				default:
					// Clean completion: OnDone already emitted text-end (if any)
					// and finish; close the stream with the [DONE] sentinel.
					stream.done()
				}
			},
		}, nil
	})
}
