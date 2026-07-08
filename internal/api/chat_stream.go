// Phase 1 of the roadmap: the same Phase 0 contract (ChatInput/ChatRequest)
// is now also served in SSE streaming mode, token by token, backed by a real
// LLM (or the canned stub if ANTHROPIC_API_KEY is not set — see internal/llm).
// The Phase 0 stub endpoint (POST /v1/tenant/{tenant_id}/chat) remains intact.
package api

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// StreamTokenEvent is the payload of each "token" SSE event: an incremental
// fragment (delta) of the LLM response.
type StreamTokenEvent struct {
	Text string `json:"text" doc:"Incremental fragment of the response"`
}

// StreamDoneEvent is the payload of the final "done" SSE event: the stream
// finished normally (not due to client cancellation or an error).
type StreamDoneEvent struct {
	TenantID string `json:"tenant_id" doc:"Tenant that served the request"`
}

// StreamErrorEvent is the payload of the "error" SSE event: the LLM returned
// an error before completing the stream.
type StreamErrorEvent struct {
	Message string `json:"message" doc:"Error description"`
}

// RegisterStream mounts the chat endpoint in SSE streaming mode. It receives
// the already-built llm.Client (llm.FromEnv() in main.go) instead of
// creating it here — no global state, consistent with isolation via ctx
// (see docs/vision/principles.md).
//
// Deliberately reuses ChatInput/ChatRequest from chat.go: the same struct
// governs the contract both in the stub endpoint (Phase 0) and in real
// streaming (Phase 1) — see "the struct as the single source of truth"
// (docs/vision/thesis.md).
func RegisterStream(api huma.API, client llm.Client) {
	sse.Register(api, huma.Operation{
		OperationID: "chat-stream",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/chat/stream",
		Summary:     "Chat with the agent via SSE streaming (Phase 1)",
		Description: "Streams the LLM response token by token. Uses " +
			"ANTHROPIC_API_KEY if set; otherwise falls back to the canned " +
			"stub (internal/llm) so the SSE path can be verified without " +
			"credentials. Honors ctx cancellation: if the client cuts the " +
			"connection, the stream stops immediately.",
	}, map[string]any{
		"token": StreamTokenEvent{},
		"done":  StreamDoneEvent{},
		"error": StreamErrorEvent{},
	}, func(ctx context.Context, in *ChatInput, send sse.Sender) {
		ctx = context.WithValue(ctx, tenantCtxKey{}, in.TenantID)
		slog.InfoContext(ctx, "chat stream request",
			"tenant_id", in.TenantID,
			"product_id", in.Body.ProductID,
			"prompt_len", len(in.Body.Prompt),
			"llm", client.Name(),
		)

		system := systemPromptFor(in.Body.ProductID)
		tokens, err := client.StreamChat(ctx, system, in.Body.Prompt)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("failed to start stream: %v", err)})
			return
		}

		for {
			select {
			case <-ctx.Done():
				// The client closed the connection (or the request expired).
				// There is nothing else to do here: the `tokens` channel is
				// closed by its own producer upon observing the same
				// ctx.Done() (see internal/llm/stub.go and anthropic.go) —
				// token spend is cut there, not here.
				slog.InfoContext(ctx, "chat stream cancelled", "tenant_id", in.TenantID)
				return
			case tok, ok := <-tokens:
				if !ok {
					return
				}
				if tok.Done {
					_ = send.Data(StreamDoneEvent{TenantID: in.TenantID})
					return
				}
				if err := send.Data(StreamTokenEvent{Text: tok.Text}); err != nil {
					// Client disconnected mid-write: there is no way to keep
					// writing, so we stop.
					return
				}
			}
		}
	})
}

// systemPromptFor builds the minimal system prompt for the given product_id.
// Phase 1: no hydrated memory yet (that arrives in future phases, see
// docs/architecture/memory-tiers.md) — the context is deliberately small.
func systemPromptFor(productID string) string {
	if productID == "" {
		return "You are the Aisotropy agent. Respond briefly and directly."
	}
	return fmt.Sprintf("You are the Aisotropy agent for product %q. Respond briefly and directly.", productID)
}
