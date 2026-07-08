// Phase 1 of the roadmap: the same Phase 0 contract (ChatInput/ChatRequest)
// is now also served in SSE streaming mode, token by token, backed by a real
// LLM (or the canned stub if ANTHROPIC_API_KEY is not set — see internal/llm).
// The Phase 0 stub endpoint (POST /v1/tenant/{tenant_id}/user/{user_id}/chat)
// remains intact.
//
// Phase 2 follow-up "C": this is the streaming path that actually reads and
// writes the BadgerDB working-memory tier (internal/memory). Before calling
// the LLM, the session's condensed Summary plus its Recent messages are
// folded into the system prompt so the model sees prior turns; the incoming
// user message is appended immediately, and the assistant's full reply is
// appended only after the stream completes successfully (Done, not Err or
// ctx cancellation) — a partial or failed turn is never persisted (see
// docs/architecture/memory-tiers.md).
package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// recentMessagesWindow bounds how many recent active-buffer messages are
// folded into the system prompt alongside the condensed summary.
const recentMessagesWindow = 12

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
// the already-built llm.Client (llm.FromEnv() in main.go) and the shared
// *memory.WorkingMemory instead of creating them here — no global state,
// consistent with isolation via ctx (see docs/vision/principles.md).
//
// Deliberately reuses ChatInput/ChatRequest from chat.go: the same struct
// governs the contract both in the stub endpoint (Phase 0) and in real
// streaming (Phase 1+) — see "the struct as the single source of truth"
// (docs/vision/thesis.md).
func RegisterStream(api huma.API, client llm.Client, wm *memory.WorkingMemory) {
	sse.Register(api, huma.Operation{
		OperationID: "chat-stream",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat/stream",
		Summary:     "Chat with the agent via SSE streaming (Phase 1+)",
		Description: "Streams the LLM response token by token, hydrating the " +
			"system prompt from the session's working memory (summary + " +
			"recent messages) and persisting both the user and assistant " +
			"turns once the stream completes successfully. Uses " +
			"ANTHROPIC_API_KEY if set; otherwise falls back to the canned " +
			"stub (internal/llm) so the SSE path can be verified without " +
			"credentials. Honors ctx cancellation: if the client cuts the " +
			"connection, the stream stops immediately and no partial " +
			"assistant turn is persisted.",
	}, map[string]any{
		"token": StreamTokenEvent{},
		"done":  StreamDoneEvent{},
		"error": StreamErrorEvent{},
	}, func(ctx context.Context, in *ChatInput, send sse.Sender) {
		ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("invalid tenant/user/session identity: %v", err)})
			return
		}
		// In addition to the api-layer ctx injection above (withIdentity,
		// consumed via TenantFromContext/UserFromContext in this package),
		// set the same identity through internal/llm's own ctx carrier so
		// the WithOTel decorator (internal/llm/otel.go) can attribute its
		// gen_ai span with tenant_id/user_id without internal/llm importing
		// internal/api (see ADR-0010's "Observability decision" and
		// internal/llm/context.go).
		ctx = llm.WithTenant(ctx, in.TenantID)
		ctx = llm.WithUser(ctx, in.UserID)
		key := memory.Key{Tenant: in.TenantID, User: in.UserID, Session: in.Body.SessionID}

		slog.InfoContext(ctx, "chat stream request",
			"tenant_id", in.TenantID,
			"user_id", in.UserID,
			"session_id", in.Body.SessionID,
			"product_id", in.Body.ProductID,
			"prompt_len", len(in.Body.Prompt),
			"llm", client.Name(),
		)

		system, err := hydrateSystemPrompt(ctx, wm, key, in.Body.ProductID)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("failed to hydrate working memory: %v", err)})
			return
		}

		// Persist the user's turn before calling the LLM: even if the
		// stream itself later fails, the user's message is real input that
		// already happened and should not be lost.
		if err := wm.Append(ctx, key, memory.Message{Role: "user", Content: in.Body.Prompt}); err != nil {
			slog.ErrorContext(ctx, "failed to append user message to working memory", "err", err)
		}

		tokens, err := client.StreamChat(ctx, system, in.Body.Prompt)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("failed to start stream: %v", err)})
			return
		}

		var assistantReply strings.Builder
		for {
			select {
			case <-ctx.Done():
				// The client closed the connection (or the request expired).
				// There is nothing else to do here: the `tokens` channel is
				// closed by its own producer upon observing the same
				// ctx.Done() (see internal/llm/stub.go and anthropic.go) —
				// token spend is cut there, not here. The partial
				// assistant reply accumulated so far is deliberately NOT
				// persisted: a cancelled/failed turn is not a completed one.
				slog.InfoContext(ctx, "chat stream cancelled", "tenant_id", in.TenantID, "user_id", in.UserID)
				return
			case tok, ok := <-tokens:
				if !ok {
					return
				}
				if tok.Err != nil {
					// Mid-stream failure: surface it as an SSE error event and stop,
					// so the client never mistakes a truncated stream for success.
					// The partial reply is not persisted (see above).
					_ = send.Data(StreamErrorEvent{Message: tok.Err.Error()})
					return
				}
				if tok.Done {
					// Only a clean, complete stream gets its assistant turn
					// persisted to working memory.
					if assistantReply.Len() > 0 {
						if err := wm.Append(ctx, key, memory.Message{Role: "assistant", Content: assistantReply.String()}); err != nil {
							slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
						}
					}
					_ = send.Data(StreamDoneEvent{TenantID: in.TenantID})
					return
				}
				assistantReply.WriteString(tok.Text)
				if err := send.Data(StreamTokenEvent{Text: tok.Text}); err != nil {
					// Client disconnected mid-write: there is no way to keep
					// writing, so we stop. The partial reply is not persisted.
					return
				}
			}
		}
	})
}

// hydrateSystemPrompt builds the system prompt for the given product_id and
// folds in the session's working-memory context (the condensed summary plus
// its most recent active-buffer messages), so the model sees prior turns of
// the conversation instead of starting fresh on every request (see
// docs/architecture/memory-tiers.md).
func hydrateSystemPrompt(ctx context.Context, wm *memory.WorkingMemory, key memory.Key, productID string) (string, error) {
	base := systemPromptFor(productID)

	summary, err := wm.Summary(ctx, key)
	if err != nil {
		return "", fmt.Errorf("read working-memory summary: %w", err)
	}
	recent, err := wm.Recent(ctx, key, recentMessagesWindow)
	if err != nil {
		return "", fmt.Errorf("read working-memory recent messages: %w", err)
	}

	if summary == "" && len(recent) == 0 {
		return base, nil
	}

	var sb strings.Builder
	sb.WriteString(base)
	if summary != "" {
		sb.WriteString("\n\nSummary of the conversation so far: ")
		sb.WriteString(summary)
	}
	if len(recent) > 0 {
		sb.WriteString("\n\nMost recent messages in this session:\n")
		for _, m := range recent {
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
		}
	}
	return sb.String(), nil
}

// systemPromptFor builds the minimal system prompt for the given product_id.
func systemPromptFor(productID string) string {
	if productID == "" {
		return "You are the Aisotropy agent. Respond briefly and directly."
	}
	return fmt.Sprintf("You are the Aisotropy agent for product %q. Respond briefly and directly.", productID)
}
