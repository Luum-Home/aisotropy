// Phase 2B of the adoption roadmap: the AG-UI protocol adapter.
//
// AG-UI (github.com/ag-ui-protocol/ag-ui) is the open, event-based agent<->frontend
// protocol that CopilotKit speaks natively (docs/research/frontend-ui-integration-landscape.md).
// This file mounts POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agui, which
// accepts an AG-UI RunAgentInput and re-encodes aisotropy's existing streaming
// pipeline as AG-UI SSE events. It is a PROTOCOL COSTUME over StreamCore
// (stream_core.go): identity->ctx, quota gate, input/output guardrails,
// tenant-scoped hydration, the streaming LLM call, and cost accounting are the
// SAME code the aisotropy-SSE endpoint (RegisterStream) and the AI-SDK adapter
// run — this adapter only RE-ENCODES the output. There is no forked pipeline,
// so multi-tenant isolation and key-brokering hold by construction; the wire
// format is the only thing that differs.
//
// The wire format is implemented in PURE GO (the event structs + the SSE
// encoder below) — the in-repo AG-UI community Go SDK is deliberately NOT
// imported (its stability was flagged, and the repo's constitutional "no new
// dependency" gate stands). AG-UI's event set is ~16 types; this adapter emits
// the documented FIVE-EVENT FLOOR that powers a text copilot:
//
//	RUN_STARTED
//	  -> TEXT_MESSAGE_START (role "assistant")
//	    -> TEXT_MESSAGE_CONTENT (one per delivered token delta)
//	  -> TEXT_MESSAGE_END
//	-> RUN_FINISHED
//
// On a pipeline failure the terminal frame is RUN_ERROR instead of RUN_FINISHED
// (and, when the failure happens before any token, no TEXT_MESSAGE_* frames are
// emitted at all). Everything else in AG-UI (tool lifecycle, shared state,
// human-in-the-loop, generative UI) is additive on top of this floor and out of
// scope here.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// AG-UI event "type" discriminators. Each SSE frame is a single JSON object
// whose "type" field selects the event; these are the five floor events plus
// the terminal error. The string values are AG-UI's canonical SCREAMING_SNAKE
// spelling (docs/research/frontend-ui-integration-landscape.md) — a client
// matches on them verbatim, so they are part of the wire contract and must not
// be renamed.
const (
	aguiEventRunStarted         = "RUN_STARTED"
	aguiEventTextMessageStart   = "TEXT_MESSAGE_START"
	aguiEventTextMessageContent = "TEXT_MESSAGE_CONTENT"
	aguiEventTextMessageEnd     = "TEXT_MESSAGE_END"
	aguiEventRunFinished        = "RUN_FINISHED"
	aguiEventRunError           = "RUN_ERROR"
)

// aguiDefaultThread is the session key used when a RunAgentInput arrives without
// a threadId. AG-UI's threadId is the conversation identifier, so it maps
// naturally onto aisotropy's working-memory session key (memory.Key.Session);
// a client that omits it still needs a non-empty, reserved-char-free session for
// StreamCore's identity validation, so this stable fallback is substituted. Real
// CopilotKit clients always send a threadId, so this is a degenerate-input guard,
// not the common path.
const aguiDefaultThread = "agui"

// aguiRunStarted is the RUN_STARTED frame: the run has begun. It echoes the
// request's threadId/runId so the client can correlate the stream with the run
// it kicked off.
type aguiRunStarted struct {
	Type     string `json:"type"`
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
}

// aguiTextMessageStart is the TEXT_MESSAGE_START frame: the assistant is about
// to stream a text message. messageId is minted once per run (newAGUIMessageID)
// and reused by every following CONTENT frame and the END frame so the client
// can assemble the deltas into one message.
type aguiTextMessageStart struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
}

// aguiTextMessageContent is a TEXT_MESSAGE_CONTENT frame: one incremental delta
// of the assistant message, carrying the same messageId as its START. The
// concatenation of every delta, in order, is the full assistant reply.
type aguiTextMessageContent struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
}

// aguiTextMessageEnd is the TEXT_MESSAGE_END frame: the assistant message is
// complete. It carries the messageId it closes.
type aguiTextMessageEnd struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
}

// aguiRunFinished is the RUN_FINISHED frame: the run completed successfully. It
// echoes threadId/runId, mirroring RUN_STARTED.
type aguiRunFinished struct {
	Type     string `json:"type"`
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
}

// aguiRunError is the RUN_ERROR frame: the run failed. It is emitted INSTEAD of
// RUN_FINISHED and is terminal. Message is the failure description (the plain
// error string StreamCore returned); Code is an optional machine-readable code
// (omitted here — aisotropy's pipeline errors are not yet classified into AG-UI
// codes, and omitempty keeps the field absent rather than sending an empty one).
type aguiRunError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// aguiRunAgentInput is the AG-UI request body (a lenient subset of the full
// RunAgentInput). Only the fields this floor adapter needs are modeled;
// unmodeled fields (tools, context, state, forwardedProps, ...) are ignored by
// the JSON decoder, which is the intended leniency — an AG-UI client may send a
// far richer body and this adapter simply runs the latest user turn.
//
// Identity is deliberately NOT read from this body: tenant_id and user_id come
// from the PATH (and, from there, into ctx via StreamCore), per the multi-tenancy
// constitutional gate. threadId becomes the working-memory session key.
type aguiRunAgentInput struct {
	ThreadID string        `json:"threadId"`
	RunID    string        `json:"runId"`
	Messages []aguiMessage `json:"messages"`
}

// aguiMessage is one OpenAI-shaped chat message. Content is kept as RawMessage
// because OpenAI content is polymorphic — either a plain string or an array of
// typed parts ({type:"text", text:"..."}); aguiMessageText normalizes both to a
// string. Fields beyond role/content (id, name, tool_calls, ...) are ignored.
type aguiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// aguiContentPart is one element of an OpenAI array-form message content. Only
// text parts contribute to the extracted prompt (image/other parts are ignored,
// consistent with the text-only pipeline).
type aguiContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// AGUIInput is the Huma input contract for the AG-UI endpoint: the tenant/user
// path params (validated to the same safe identifier charset as ChatInput, so
// an oversize/injection identity is rejected with 422 before the handler runs)
// plus the raw request body. RawBody (a Huma-recognized field) hands the handler
// the undecoded bytes so the AG-UI RunAgentInput is decoded leniently in-handler
// — Huma does not schema-validate it, letting a rich AG-UI body through while
// the handler still returns a clean 4xx for a genuinely empty/malformed one.
type AGUIInput struct {
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string `path:"user_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique user ID within the tenant" example:"alice"`
	RawBody  []byte `contentType:"application/json"`
}

// newAGUIMessageID mints the assistant messageId shared by a run's
// TEXT_MESSAGE_START/CONTENT/END frames. It is a package var so a golden wire
// test can pin it to an exact value; production returns a random 128-bit hex id
// (unique per run, opaque to the client). A rand failure degrades to a fixed
// non-empty id rather than an empty one, so the frames stay well-formed.
var newAGUIMessageID = func() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "assistant-message"
	}
	return hex.EncodeToString(b[:])
}

// RegisterAGUI mounts the AG-UI protocol endpoint. Its parameter list mirrors
// RegisterStream one-for-one so app.go wires it identically and it shares the
// exact same dependencies — the whole point is that the AG-UI route and the
// aisotropy-SSE route run the SAME StreamCore pipeline over the SAME wiring and
// differ only in output encoding (see this file's package doc, and StreamDeps in
// stream_core.go). Because it registers through huma.Register, it inherits the
// identity path params and the API's authn middleware chain exactly like every
// other /v1 operation.
//
// The response is a raw SSE stream (huma.StreamResponse): the handler decodes
// the RunAgentInput and, on a valid one, returns a streaming body that emits the
// AG-UI floor events by driving StreamCore. A missing/malformed/user-message-less
// body is rejected with a 4xx BEFORE any streaming begins (a clean HTTP error,
// not an SSE error frame), because at that point there is no run to start.
func RegisterAGUI(api huma.API, client llm.Client, wm *memory.WorkingMemory, store facts.Store, limiter *memory.Limiter, toolReg *tools.Registry, meter *costmeter.Meter, cheapClient llm.Client, semStore semantic.Store, embedder semantic.Embedder, reranker semantic.Reranker, persona PersonaProvider, inPricePer1M, outPricePer1M float64, bptSampler bitrate.SampleRecorder, gw *gateway.Gateway, guard *Guard) {
	huma.Register(api, huma.Operation{
		OperationID: "chat-agui",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat/agui",
		// The AG-UI RunAgentInput is decoded and validated in-handler (leniently,
		// so a rich body is accepted and a genuinely empty/malformed one returns a
		// clean 4xx). SkipValidateBody stops Huma from validating the JSON object
		// against the RawBody []byte field's own "string/binary" schema — without
		// it, Huma rejects every JSON body with a 422 "expected string" before the
		// handler ever runs. RawBody still receives the raw bytes.
		SkipValidateBody: true,
		Summary:          "Chat with the agent via the AG-UI protocol (CopilotKit)",
		Description: "Accepts an AG-UI RunAgentInput (threadId, runId, OpenAI-shaped " +
			"messages, plus optional tools/context/state which are ignored by this " +
			"floor adapter) and streams the reply as AG-UI SSE events: RUN_STARTED, " +
			"TEXT_MESSAGE_START, TEXT_MESSAGE_CONTENT per token, TEXT_MESSAGE_END, " +
			"RUN_FINISHED (or RUN_ERROR on failure). tenant_id/user_id come from the " +
			"path, never the body. It re-encodes the SAME streaming pipeline the " +
			"/chat/stream endpoint runs (StreamCore): identity->ctx isolation, quota " +
			"gate, input/output guardrails, tenant-scoped hydration and cost accounting " +
			"all apply unchanged — only the wire format differs.",
	}, func(_ context.Context, in *AGUIInput) (*huma.StreamResponse, error) {
		// Decode + validate the AG-UI body BEFORE streaming, so a bad request is a
		// clean 4xx (a run that never started) rather than an SSE error frame.
		var input aguiRunAgentInput
		if err := json.Unmarshal(in.RawBody, &input); err != nil {
			return nil, huma.Error400BadRequest("invalid AG-UI RunAgentInput JSON", err)
		}
		prompt := latestUserMessageText(input.Messages)
		if strings.TrimSpace(prompt) == "" {
			return nil, huma.Error400BadRequest("AG-UI RunAgentInput carries no user message to run")
		}

		// Map the AG-UI request onto the existing ChatInput contract: the latest
		// user message is the prompt; threadId is the working-memory session key;
		// tenant/user come from the PATH (never the body).
		chatIn := &ChatInput{
			TenantID: in.TenantID,
			UserID:   in.UserID,
			Body: ChatRequest{
				Prompt:    prompt,
				SessionID: aguiSession(input.ThreadID),
			},
		}
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
		return &huma.StreamResponse{
			Body: func(hctx huma.Context) {
				streamAGUI(hctx, deps, chatIn, input.ThreadID, input.RunID)
			},
		}, nil
	})
}

// streamAGUI drives StreamCore and re-encodes its sink callbacks as AG-UI SSE
// frames. It runs on the request goroutine (Huma calls it once the response
// headers are ready), so every frame is written from a single goroutine — no
// concurrent writes to the response.
//
// Frame order: RUN_STARTED is emitted first; then TEXT_MESSAGE_START is emitted
// LAZILY on the first delivered token (so a failure before any token yields
// RUN_STARTED -> RUN_ERROR with no dangling open message); each token is a
// TEXT_MESSAGE_CONTENT; on terminal success TEXT_MESSAGE_END (only if a message
// was started) then RUN_FINISHED close the run. A StreamCore error becomes a
// terminal RUN_ERROR instead of RUN_FINISHED. A client cancellation makes
// StreamCore return nil WITHOUT calling OnDone, so nothing further is written —
// correct, since the connection is already gone.
func streamAGUI(hctx huma.Context, d StreamDeps, in *ChatInput, threadID, runID string) {
	// SSE response headers. StreamResponse hands us the raw body writer after the
	// status line; we set the content type (and disable proxy buffering) here so
	// the client sees an event stream, not a JSON body.
	hctx.SetHeader("Content-Type", "text/event-stream")
	hctx.SetHeader("Cache-Control", "no-cache")
	hctx.SetHeader("Connection", "keep-alive")
	hctx.SetHeader("X-Accel-Buffering", "no")

	w := hctx.BodyWriter()
	flusher, _ := w.(http.Flusher)
	emit := func(ev any) error { return writeAGUIFrame(w, flusher, ev) }

	// RUN_STARTED. If this first write fails the client is already gone; there is
	// nothing to stream, so return without invoking the pipeline.
	if err := emit(aguiRunStarted{Type: aguiEventRunStarted, ThreadID: threadID, RunID: runID}); err != nil {
		return
	}

	messageID := newAGUIMessageID()
	started := false

	sink := StreamSink{
		OnToken: func(text string) error {
			if !started {
				// Lazy TEXT_MESSAGE_START: emitted only once, on the first delivered
				// delta, so an error before any token never leaves an open message.
				if err := emit(aguiTextMessageStart{Type: aguiEventTextMessageStart, MessageID: messageID, Role: roleAssistant}); err != nil {
					return err
				}
				started = true
			}
			// A failed write signals a client disconnect: returning the error makes
			// StreamCore abort WITHOUT calling OnDone or recording cost, exactly as
			// the aisotropy-SSE encoder does.
			return emit(aguiTextMessageContent{Type: aguiEventTextMessageContent, MessageID: messageID, Delta: text})
		},
		OnDone: func(_ StreamFinal) {
			// Terminal success. Close the assistant message (only if one was opened —
			// an empty reply produced no TEXT_MESSAGE_START) then finish the run.
			// Best-effort, matching the sink contract (OnDone's result is ignored).
			if started {
				_ = emit(aguiTextMessageEnd{Type: aguiEventTextMessageEnd, MessageID: messageID})
			}
			_ = emit(aguiRunFinished{Type: aguiEventRunFinished, ThreadID: threadID, RunID: runID})
		},
	}

	// hctx.Context() is the request context, so a client disconnect cancels the
	// upstream LLM call through StreamCore (the streaming constitutional gate).
	if err := StreamCore(hctx.Context(), d, in, sink); err != nil {
		// Pipeline failure -> terminal RUN_ERROR (never RUN_FINISHED). The error
		// string is the message, mirroring how the aisotropy-SSE encoder puts
		// err.Error() into its error event.
		_ = emit(aguiRunError{Type: aguiEventRunError, Message: err.Error()})
	}
}

// writeAGUIFrame encodes one AG-UI event as a single SSE frame: `data: ` + the
// event JSON + a blank line (`\n\n`) terminator, then flushes so the client
// receives it immediately. AG-UI puts the discriminator inside the JSON ("type")
// rather than in an SSE `event:` line, so — unlike the aisotropy-SSE encoder —
// there is no `event:` line here; every frame is a lone `data:` line. A write
// error is returned so the caller can treat it as a client disconnect.
func writeAGUIFrame(w io.Writer, flusher http.Flusher, ev any) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		// Encoding a fixed-shape event struct cannot realistically fail; surface it
		// rather than silently dropping a frame if it ever does.
		return fmt.Errorf("marshal AG-UI event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// latestUserMessageText returns the text of the LAST user-role message in the
// AG-UI message list — the turn the run is meant to answer. Assistant/system/
// tool messages are prior context that hydration already reconstructs from
// working memory, so only the newest user message drives this turn. Returns ""
// when there is no user message (the handler rejects that as a 4xx).
func latestUserMessageText(msgs []aguiMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.EqualFold(msgs[i].Role, roleUser) {
			return aguiMessageText(msgs[i].Content)
		}
	}
	return ""
}

// aguiMessageText normalizes an OpenAI-shaped message content into plain text,
// leniently handling both forms: a bare JSON string, or an array of typed parts
// (only "text" parts — or untyped parts carrying text — contribute; image/other
// parts are ignored, matching the text-only pipeline). Anything it cannot parse
// yields "" rather than an error, keeping the adapter lenient.
func aguiMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// String form: {"content": "hello"}.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array-of-parts form: {"content": [{"type":"text","text":"hello"}, ...]}.
	var parts []aguiContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "" || p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// aguiSession maps an AG-UI threadId onto a working-memory session key,
// substituting the stable fallback when the client omitted it. A threadId that
// contains a reserved character is passed through unchanged and rejected
// downstream by StreamCore's identity validation (surfacing as a RUN_ERROR),
// which is the intended lenient-but-safe behavior.
func aguiSession(threadID string) string {
	if strings.TrimSpace(threadID) == "" {
		return aguiDefaultThread
	}
	return threadID
}
