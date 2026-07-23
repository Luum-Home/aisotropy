// ADR-0066: the OpenAI Chat-Completions-compatible shim — the voice-gateway
// enabler. This file mounts POST /v1/chat/completions, the THIRD wire costume
// over StreamCore (after aisdk.go and agui.go): identity->ctx, the
// quota-before-spend gate, input/output guardrails, tenant-scoped hydration,
// cost accounting, and ctx-cancellation (= barge-in) are the SAME pipeline the
// aisotropy-SSE endpoint runs — this adapter only decodes the OpenAI request
// envelope and RE-ENCODES the output as OpenAI chunks. No pipeline behavior
// lives here, so the constitutional gates hold by construction.
//
// What makes this costume different from the other two: the OpenAI wire has NO
// tenant/user path params — any OpenAI client (Pipecat's OpenAILLMService,
// Open WebUI, LibreChat, the official SDKs) is configured with a base_url and
// an api_key only. Identity therefore comes EXCLUSIVELY from the verified
// credential: the api_key rides as "Authorization: Bearer <token>", a tenant
// JWT is passed AS the api_key with zero client modification, and the route
// opts into BindAuthMiddleware's principal-derived mode via operation metadata
// (authn.go). Fail-closed properties, each by construction:
//
//   - no authenticator configured  -> DenyAllMiddleware 401s (unchanged)
//   - NoAuth-configured server     -> NoAuth copies the (absent) path identity,
//     yielding an empty unverified principal -> 401 from the principal-derived
//     branch. There is no configuration in which this route trusts a bare
//     assertion.
//   - body-supplied identity       -> never read: the request struct has no
//     tenant/user fields and the handler builds ChatInput exclusively from
//     PrincipalFromContext.
//
// Wire contract (grounded in the ADR's source-verified Pipecat/openai-SDK
// contract, and in this repo's own CLIENT of the same protocol,
// internal/llm/openai_compat.go): stream:true yields SSE frames
// `data: {chat.completion.chunk}\n\n` — each chunk carrying the SDK-required
// id/object/created/model/choices — then a finish_reason:"stop" chunk, an
// OPTIONAL usage-only chunk (only when the client asked via
// stream_options.include_usage AND the provider reported usage), and the
// literal `data: [DONE]` terminator. stream:false buffers the SAME pipeline
// and returns one chat.completion JSON object. Pre-stream failures are real
// HTTP statuses with the OpenAI error envelope {"error":{...}}; mid-stream
// failures are a data: {"error":{...}} frame followed by close WITHOUT [DONE]
// (the OpenRouter precedent our own client handles).
//
// Text-only v1 floor (ADR-0066 non-goals): tools/functions/logprobs/n>1 and
// non-text content parts are accepted-and-ignored (lenient decode, same
// posture as the agui adapter), never implemented; incoming system messages
// are ignored (the persona + memory hydration own the system prompt); the
// request's "model" is echoed back but NEVER selects the model — routing
// stays with the gateway + quota-pressure degrade inside StreamCore.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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

const (
	// oaiObjectChunk / oaiObjectCompletion are the OpenAI "object" type
	// discriminators. The openai SDK pydantic-validates them, so they are wire
	// contract, not decoration.
	oaiObjectChunk      = "chat.completion.chunk"
	oaiObjectCompletion = "chat.completion"

	// oaiDefaultModel is echoed as the response "model" when the request omitted
	// one. Advisory only — see the package doc; it never selects the model.
	oaiDefaultModel = "aisotropy"

	// oaiDefaultSession is the working-memory session key used when the request
	// carries no session_id extension field — the aguiDefaultThread pattern. A
	// stock OpenAI client never sends session_id and lands here; Pipecat can
	// send one through settings.extra without forking.
	oaiDefaultSession = "openai"

	// oaiFinishStop is the finish_reason of a cleanly completed text turn.
	oaiFinishStop = "stop"

	// oaiRoleAssistant is the delta role stamped on the first content chunk,
	// mirroring OpenAI's own stream opening so role-aware clients can open the
	// assistant message.
	oaiRoleAssistant = "assistant"

	// OpenAI error envelope "type" values used by this shim.
	oaiErrTypeInvalidRequest = "invalid_request_error"
	oaiErrTypeAuthentication = "authentication_error"
	oaiErrTypeServer         = "server_error"
	oaiErrTypeRateLimit      = "rate_limit_error"
)

// newOAICompletionID mints the completion id shared by every chunk of one
// stream (and by the non-streaming object). Package var so the golden wire
// test can pin it; production returns an OpenAI-shaped opaque id. A rand
// failure degrades to a fixed non-empty id so the frames stay well-formed.
var newOAICompletionID = func() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "chatcmpl-aisotropy"
	}
	return "chatcmpl-" + hex.EncodeToString(b[:])
}

// oaiNow returns the "created" unix timestamp stamped on the completion.
// Package var so the golden wire test can pin it to a deterministic value.
var oaiNow = func() int64 { return time.Now().Unix() }

// oaiChatRequest is the lenient subset of the OpenAI Chat Completions request
// this shim consumes. Messages reuses the aguiMessage shape (role + raw
// polymorphic content) so the string-or-parts normalizer (aguiMessageText via
// latestUserMessageText) is shared, not duplicated. Unmodeled params
// (temperature, top_p, tools, logprobs, n, ...) are decoded-and-ignored — the
// text-only v1 floor. Identity is deliberately NOT decodable from this body.
type oaiChatRequest struct {
	// Model is echoed back on the response. Advisory only: it never selects the
	// serving model (gateway + quota-pressure degrade own that decision).
	Model    string        `json:"model"`
	Messages []aguiMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// StreamOptions.IncludeUsage asks for the usage-only chunk right before
	// [DONE] — the OpenAI streaming usage contract our own client also uses.
	StreamOptions *oaiStreamOptions `json:"stream_options"`
	// SessionID is an aisotropy EXTENSION field (stock clients never send it):
	// the working-memory session key. Pipecat can pass it via settings.extra.
	SessionID string `json:"session_id"`
}

// oaiStreamOptions models the stream_options request object.
type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// oaiDelta is one chunk's delta. Role is set only on the first content chunk
// (assistant); Content carries the incremental text. Both omitempty so the
// finish chunk's delta marshals as the empty object {}.
type oaiDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// oaiChunkChoice is one element of a chunk's choices. FinishReason is a
// pointer WITHOUT omitempty so intermediate chunks carry an explicit
// "finish_reason":null (the shape OpenAI emits and the SDK models).
type oaiChunkChoice struct {
	Index        int      `json:"index"`
	Delta        oaiDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

// oaiChunk is one streaming chat.completion.chunk. The openai SDK
// pydantic-validates id/object/created/model/choices on every chunk, so all
// five are always present; the usage-only chunk carries an EMPTY choices array
// (never null), matching the contract our own client's applyStreamChunk reads.
type oaiChunk struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []oaiChunkChoice `json:"choices"`
	Usage   *oaiUsage        `json:"usage,omitempty"`
}

// oaiUsage is the OpenAI usage accounting object (prompt/completion/total).
type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// oaiMessageOut is the assistant message of a non-streaming completion choice.
type oaiMessageOut struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// oaiCompletionChoice is one choice of a non-streaming chat.completion.
type oaiCompletionChoice struct {
	Index        int           `json:"index"`
	Message      oaiMessageOut `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// oaiCompletion is the non-streaming chat.completion response object.
type oaiCompletion struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []oaiCompletionChoice `json:"choices"`
	Usage   *oaiUsage             `json:"usage,omitempty"`
}

// oaiErrorBody is the inner object of the OpenAI error envelope.
type oaiErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// oaiErrorFrame is the mid-stream error frame: data: {"error":{...}} followed
// by close WITHOUT [DONE] — the OpenRouter precedent this repo's own client
// ecosystem already handles.
type oaiErrorFrame struct {
	Error oaiErrorBody `json:"error"`
}

// oaiError is a pre-stream failure: a huma.StatusError whose body marshals as
// the OpenAI error envelope {"error":{message,type,code}} with a real HTTP
// status — so an OpenAI SDK client sees the error shape it expects instead of
// an RFC-7807 problem document. status is unexported (not marshaled).
type oaiError struct {
	status  int
	Payload oaiErrorBody `json:"error"`
}

func (e *oaiError) Error() string { return e.Payload.Message }

// GetStatus implements huma.StatusError, so a handler-returned *oaiError is
// written with this status and the struct's own JSON shape (huma marshals the
// error value itself when it implements StatusError).
func (e *oaiError) GetStatus() int { return e.status }

func newOAIError(status int, errType, msg string) *oaiError {
	return &oaiError{status: status, Payload: oaiErrorBody{Message: msg, Type: errType}}
}

// oaiSession maps the optional session_id extension onto the working-memory
// session key, substituting the stable default when absent. A session that
// contains a reserved character is passed through and rejected downstream by
// StreamCore's identity validation — lenient-but-safe, same as aguiSession.
func oaiSession(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return oaiDefaultSession
	}
	return sessionID
}

// oaiEchoModel resolves the ADVISORY response model string: the request's
// model echoed back, else the stable default. It never selects the model.
func oaiEchoModel(requested string) string {
	if strings.TrimSpace(requested) == "" {
		return oaiDefaultModel
	}
	return requested
}

// openAICompatInput is the Huma input contract: ONLY the raw body. There are
// deliberately no tenant/user path params (identity is principal-derived, see
// the package doc) and no decoded body fields (OpenAI messages[].content is
// polymorphic, so it is decoded leniently in-handler — the agui RawBody
// pattern; SkipValidateBody on the operation keeps Huma from 422-ing the JSON
// object against the []byte field's own schema).
type openAICompatInput struct {
	RawBody []byte `contentType:"application/json"`
}

// RegisterOpenAICompat mounts POST /v1/chat/completions, the OpenAI
// Chat-Completions-compatible shim (ADR-0066). Its parameter list mirrors
// RegisterStream/RegisterAISDK/RegisterAGUI one-for-one so aisotropy/app.go
// wires it with the identical dependencies: same StreamCore pipeline, same
// StreamDeps, different wire costume. The operation opts into the
// principal-derived identity mode via Metadata (authn.go): without a verified
// principal carrying tenant/user, the middleware 401s before this handler runs.
func RegisterOpenAICompat(api huma.API, client llm.Client, wm *memory.WorkingMemory, store facts.Store, limiter *memory.Limiter, toolReg *tools.Registry, meter *costmeter.Meter, cheapClient llm.Client, semStore semantic.Store, embedder semantic.Embedder, reranker semantic.Reranker, persona PersonaProvider, inPricePer1M, outPricePer1M float64, bptSampler bitrate.SampleRecorder, gw *gateway.Gateway, guard *Guard) {
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
		OperationID: "openai-chat-completions",
		Method:      httpMethodPost,
		Path:        "/v1/chat/completions",
		// The OpenAI request is decoded leniently in-handler (polymorphic
		// messages[].content; unknown params ignored). SkipValidateBody stops
		// Huma from validating the JSON object against RawBody's own schema.
		SkipValidateBody: true,
		// Principal-derived identity (ADR-0066): no tenant/user path params on
		// this route — BindAuthMiddleware requires a VERIFIED principal carrying
		// tenant/user claims and injects it into ctx; NoAuth/no-auth setups 401.
		Metadata: map[string]any{identityMetadataKey: identityModePrincipal},
		Summary:  "OpenAI Chat Completions compatible endpoint (ADR-0066 voice-gateway shim)",
		Description: "Serves the SAME streaming pipeline as /chat/stream re-encoded as the " +
			"OpenAI Chat Completions wire protocol, so any OpenAI client (Pipecat, Open " +
			"WebUI, LibreChat, the official SDKs) can point base_url at aisotropy with " +
			"zero modification. Identity comes EXCLUSIVELY from the verified bearer " +
			"credential (pass a tenant JWT as the api_key); the body can never supply " +
			"tenant/user, and the route refuses to serve without a real authenticator — " +
			"including on a NoAuth-configured server. stream:true emits chat.completion.chunk " +
			"SSE frames terminated by data: [DONE] (with an optional usage chunk when " +
			"stream_options.include_usage is set); stream:false returns one chat.completion " +
			"object. Text-only v1 floor: tools/functions/logprobs/n>1 and non-text content " +
			"parts are ignored; the request's model is echoed back but never selects the " +
			"serving model. Quota-before-spend, guardrails, tenant-scoped hydration, cost " +
			"accounting and ctx-cancellation (barge-in) are unchanged — only the wire " +
			"format differs.",
		Responses: map[string]*huma.Response{
			"200": {
				Description: "chat.completion object (stream:false) or chat.completion.chunk SSE stream (stream:true)",
				Content: map[string]*huma.MediaType{
					"application/json": {},
					aisdkContentType:   {},
				},
			},
		},
	}, func(ctx context.Context, in *openAICompatInput) (*huma.StreamResponse, error) {
		// Defense-in-depth identity check: the principal-derived middleware mode
		// already 401'd anything without a verified principal, but a wiring that
		// registers this route without the middleware (as unit tests can) must
		// fail closed too — never fall back to any other identity source.
		p, ok := PrincipalFromContext(ctx)
		if !ok || p.TenantID == "" || p.UserID == "" || !p.ScopesVerified {
			return nil, newOAIError(http.StatusUnauthorized, oaiErrTypeAuthentication,
				"this endpoint derives identity from the credential: a verified bearer token (JWT) carrying tenant/user claims is required")
		}

		// Decode + validate BEFORE any streaming, so a bad request is a real
		// HTTP status with the OpenAI error envelope (the decode-before-stream
		// discipline the aisdk adapter established).
		var req oaiChatRequest
		if err := json.Unmarshal(in.RawBody, &req); err != nil {
			return nil, newOAIError(http.StatusBadRequest, oaiErrTypeInvalidRequest,
				"invalid chat completions request JSON: "+err.Error())
		}
		prompt := latestUserMessageText(req.Messages)
		if strings.TrimSpace(prompt) == "" {
			return nil, newOAIError(http.StatusBadRequest, oaiErrTypeInvalidRequest,
				"messages must include a user message with text content")
		}

		// Identity comes EXCLUSIVELY from the verified principal — never the
		// body, never a path param. Scopes: the OpenAI body has no scopes field
		// and the route requires a verified principal, so effectiveScopes always
		// resolves to the verified claim scopes (the body-fallback branch is
		// unreachable here by construction).
		chatIn := &ChatInput{
			TenantID: p.TenantID,
			UserID:   p.UserID,
			Body: ChatRequest{
				Prompt:    prompt,
				SessionID: oaiSession(req.SessionID),
			},
		}
		model := oaiEchoModel(req.Model)

		if !req.Stream {
			return oaiNonStreaming(ctx, deps, chatIn, model)
		}
		includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		return &huma.StreamResponse{
			Body: func(hctx huma.Context) {
				streamOAI(hctx, deps, chatIn, model, includeUsage)
			},
		}, nil
	})
}

// oaiNonStreaming drives the SAME StreamCore pipeline with a buffering sink
// (one pipeline, one costume — never ChatCore) and returns a single
// chat.completion object. Because the pipeline runs BEFORE the response
// begins, pipeline failures here surface as real HTTP statuses with the
// OpenAI error envelope, unlike the streaming path where the 200 is already
// on the wire.
func oaiNonStreaming(ctx context.Context, deps StreamDeps, chatIn *ChatInput, model string) (*huma.StreamResponse, error) {
	var final *StreamFinal
	sink := StreamSink{
		// Tokens are accumulated by StreamCore itself (StreamFinal.Text is the
		// delivered reply); this sink only needs the terminal payload.
		OnToken: func(string) error { return nil },
		OnDone:  func(f StreamFinal) { final = &f },
	}
	if err := StreamCore(ctx, deps, chatIn, sink); err != nil {
		// The quota gate rejects BEFORE any LLM spend (constitutional). OpenAI
		// clients key retry/backoff on 429 + rate_limit_error — anything else
		// (a 500) makes stock SDKs fail instead of backing off.
		if errors.Is(err, ErrRateLimited) {
			return nil, newOAIError(http.StatusTooManyRequests, oaiErrTypeRateLimit, err.Error())
		}
		return nil, newOAIError(http.StatusInternalServerError, oaiErrTypeServer, err.Error())
	}
	if final == nil {
		// Client cancellation: StreamCore returned nil without OnDone. The
		// client is gone; there is no response to build and nothing to write.
		return nil, nil //nolint:nilnil // huma treats nil output as status-only; the peer already disconnected
	}

	completion := oaiCompletion{
		ID:      newOAICompletionID(),
		Object:  oaiObjectCompletion,
		Created: oaiNow(),
		Model:   model,
		Choices: []oaiCompletionChoice{{
			Index:        0,
			Message:      oaiMessageOut{Role: oaiRoleAssistant, Content: final.Text},
			FinishReason: oaiFinishStop,
		}},
	}
	if final.Usage != nil {
		completion.Usage = &oaiUsage{
			PromptTokens:     final.Usage.InputTokens,
			CompletionTokens: final.Usage.OutputTokens,
			TotalTokens:      final.Usage.InputTokens + final.Usage.OutputTokens,
		}
	}
	payload, err := json.Marshal(completion)
	if err != nil {
		return nil, newOAIError(http.StatusInternalServerError, oaiErrTypeServer, "encode completion: "+err.Error())
	}
	return &huma.StreamResponse{
		Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "application/json")
			_, _ = hctx.BodyWriter().Write(payload)
		},
	}, nil
}

// streamOAI drives StreamCore and re-encodes its sink callbacks as OpenAI
// chat.completion.chunk SSE frames. It runs on the request goroutine; the
// framing writer is the shared aisdkStream (`data: ${JSON}\n\n` + the literal
// `data: [DONE]\n\n` terminator — byte-identical framing between the two
// protocols, so the encoder is reused rather than re-invented).
//
// Frame order: one chunk per delivered token (the FIRST also carries
// delta.role "assistant", mirroring OpenAI's stream opening); on terminal
// success a finish_reason:"stop" chunk, then — only when the client asked via
// stream_options.include_usage AND the provider reported usage — a usage-only
// chunk with an empty choices array, then [DONE]. A pipeline failure emits a
// data: {"error":{...}} frame and closes WITHOUT [DONE]. A client
// cancellation writes nothing further (the connection is already gone), and
// StreamCore has already cut the upstream LLM spend — this IS barge-in.
func streamOAI(hctx huma.Context, d StreamDeps, in *ChatInput, model string, includeUsage bool) {
	hctx.SetHeader("Content-Type", aisdkContentType)
	hctx.SetHeader("Cache-Control", "no-cache")
	hctx.SetHeader("Connection", "keep-alive")
	hctx.SetHeader("X-Accel-Buffering", "no")

	stream := newAISDKStream(hctx.BodyWriter())
	id := newOAICompletionID()
	created := oaiNow()

	// chunk stamps the SDK-required id/object/created/model envelope on every
	// frame of this stream.
	chunk := func(choices []oaiChunkChoice, usage *oaiUsage) oaiChunk {
		return oaiChunk{ID: id, Object: oaiObjectChunk, Created: created, Model: model, Choices: choices, Usage: usage}
	}

	var started, finished bool
	sink := StreamSink{
		OnToken: func(text string) error {
			delta := oaiDelta{Content: text}
			if !started {
				// First delta also opens the assistant role, as OpenAI does.
				delta.Role = oaiRoleAssistant
				started = true
			}
			return stream.part(chunk([]oaiChunkChoice{{Index: 0, Delta: delta, FinishReason: nil}}, nil))
		},
		OnDone: func(final StreamFinal) {
			stop := oaiFinishStop
			_ = stream.part(chunk([]oaiChunkChoice{{Index: 0, Delta: oaiDelta{}, FinishReason: &stop}}, nil))
			if includeUsage && final.Usage != nil {
				// Usage-only penultimate chunk: EMPTY choices array (never null),
				// the exact shape our own client's applyStreamChunk reads. Nil
				// provider usage -> chunk omitted (Pipecat tolerates: source-
				// verified `if chunk.usage:` in the ADR).
				_ = stream.part(chunk([]oaiChunkChoice{}, &oaiUsage{
					PromptTokens:     final.Usage.InputTokens,
					CompletionTokens: final.Usage.OutputTokens,
					TotalTokens:      final.Usage.InputTokens + final.Usage.OutputTokens,
				}))
			}
			finished = true
		},
	}

	// hctx.Context() is the request context: a client disconnect cancels the
	// upstream LLM call through StreamCore (the streaming constitutional gate).
	err := StreamCore(hctx.Context(), d, in, sink)
	switch {
	case err != nil:
		// Pipeline failure (quota, blocked input, hydration, stream start, or a
		// mid-stream LLM error): error frame, close WITHOUT [DONE]. The 200 is
		// already on the wire, so the status cannot change — but the frame's
		// error TYPE still tells an OpenAI client that a quota rejection is
		// retryable (rate_limit_error) rather than a server fault.
		errType := oaiErrTypeServer
		if errors.Is(err, ErrRateLimited) {
			errType = oaiErrTypeRateLimit
		}
		_ = stream.part(oaiErrorFrame{Error: oaiErrorBody{Message: err.Error(), Type: errType}})
	case !finished:
		// Client cancellation: nothing further to write; the peer is gone and
		// the turn was interrupted, not completed — no [DONE].
	default:
		stream.done()
	}
}
