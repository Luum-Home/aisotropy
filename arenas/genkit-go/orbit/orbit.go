// Package orbit builds the "Orbit" multi-tenant support chatbot on the REAL
// Firebase Genkit Go framework (github.com/firebase/genkit/go, Apache-2.0).
//
// Everything here runs against the genuine framework surface:
//   - genkit.Init                 real registry + plugin init
//   - genkit.DefineModel          real offline fake model (no API key)
//   - genkit.DefineTool           real tool the model calls via the generate loop
//   - genkit.DefineFlow           real observable, traced flow
//   - genkit.Generate             real generate pipeline (messages, tools, streaming)
//   - genkit.GenerateData[T]      real structured output
//   - genkit.DefinePrompt / .prompt (dotprompt) via WithPromptFS
//
// The one thing the framework does NOT provide is tenant isolation. Stable
// Genkit Go has no Session/Memory type; multi-tenancy is developer-passed
// (see store.go). This build makes that boundary explicit so the isolation
// smoke can drive a real cross-tenant leak through the real generate pipeline.
package orbit

import (
	"context"
	"embed"
	"fmt"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
	"github.com/firebase/genkit/go/genkit"
)

//go:embed prompts/*.prompt
var promptFS embed.FS

const systemInstruction = "You are Orbit Support. Help with orders, billing, and account questions. " +
	"Answer only from the information in this conversation."

// ChatInput is the flow input. Note SessionKey is a bare developer-passed
// string: NOTHING here forces it to encode TenantID/UserID. That freedom is the
// convention leak the smoke exploits.
type ChatInput struct {
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	SessionKey string `json:"session_key"`
	Prompt     string `json:"prompt"`
}

type ChatOutput struct {
	Reply      string `json:"reply"`
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	SessionKey string `json:"session_key"`
}

// TicketCategory is the structured-output schema for GenerateData.
type TicketCategory struct {
	Category string `json:"category"`
	Priority string `json:"priority"`
	Summary  string `json:"summary"`
}

// Orbit holds the real Genkit instance and the app's flows/tools.
type Orbit struct {
	G     *genkit.Genkit
	Model ai.Model
	Store *SessionStore

	chatFlow     *core.Flow[ChatInput, ChatOutput, struct{}]
	classifyFlow *core.Flow[string, *TicketCategory, struct{}]
	orderTool    ai.Tool
}

// New initialises the real framework and defines the model, tool, flows and
// dotprompt. It is fully offline — no API key, no network.
func New(ctx context.Context) (*Orbit, error) {
	// Real framework init. WithPromptFS registers the embedded .prompt files
	// (dotprompt) — Genkit looks for a "prompts/" dir at the FS root, which is
	// exactly what promptFS embeds. WithDefaultModel points the registry at our
	// offline model.
	g := genkit.Init(ctx,
		genkit.WithDefaultModel(FakeModelName),
		genkit.WithPromptFS(promptFS),
	)

	o := &Orbit{G: g, Store: NewSessionStore()}
	o.Model = DefineFakeModel(g)

	// Feature #3 — a REAL tool. The fake model emits a genuine ToolRequest and
	// Genkit's Generate loop executes this function, then re-invokes the model.
	o.orderTool = genkit.DefineTool(g, "lookupOrder",
		"Look up the status of a customer order by its numeric order_id.",
		func(_ *ai.ToolContext, in struct {
			OrderID string `json:"order_id"`
		}) (string, error) {
			return fmt.Sprintf("order %s is shipped and arrives in 2 days", in.OrderID), nil
		},
	)

	// Feature #2 — structured output through the real GenerateData path.
	o.classifyFlow = genkit.DefineFlow(g, "orbitClassify",
		func(ctx context.Context, text string) (*TicketCategory, error) {
			cat, _, err := genkit.GenerateData[TicketCategory](ctx, g,
				ai.WithModel(o.Model),
				ai.WithPrompt(text),
			)
			return cat, err
		},
	)

	// The main chat flow — real, traced genkit.DefineFlow. It hydrates history
	// from the developer-passed store key and runs it through real Generate.
	o.chatFlow = genkit.DefineFlow(g, "orbitChat",
		func(ctx context.Context, in ChatInput) (ChatOutput, error) {
			history := o.Store.History(in.SessionKey) // <- developer-passed key

			resp, err := genkit.Generate(ctx, g,
				ai.WithModel(o.Model),
				ai.WithSystem(systemInstruction),
				ai.WithMessages(history...),
				ai.WithPrompt(in.Prompt),
				ai.WithTools(o.orderTool),
			)
			if err != nil {
				return ChatOutput{}, err
			}
			reply := resp.Text()

			// Persist this turn under the SAME developer-passed key.
			o.Store.Append(in.SessionKey,
				ai.NewUserTextMessage(in.Prompt),
				ai.NewTextMessage(ai.RoleModel, reply),
			)
			return ChatOutput{
				Reply:      reply,
				TenantID:   in.TenantID,
				UserID:     in.UserID,
				SessionKey: in.SessionKey,
			}, nil
		},
	)

	return o, nil
}

// Chat runs the real chat flow.
func (o *Orbit) Chat(ctx context.Context, in ChatInput) (ChatOutput, error) {
	return o.chatFlow.Run(ctx, in)
}

// ChatStream runs the chat flow's generate with real streaming, invoking onChunk
// for each streamed text fragment. Used by the SSE endpoint.
func (o *Orbit) ChatStream(ctx context.Context, in ChatInput, onChunk func(string)) (ChatOutput, error) {
	history := o.Store.History(in.SessionKey)
	resp, err := genkit.Generate(ctx, o.G,
		ai.WithModel(o.Model),
		ai.WithSystem(systemInstruction),
		ai.WithMessages(history...),
		ai.WithPrompt(in.Prompt),
		ai.WithStreaming(func(_ context.Context, chunk *ai.ModelResponseChunk) error {
			for _, p := range chunk.Content {
				if p.Text != "" {
					onChunk(p.Text)
				}
			}
			return nil
		}),
	)
	if err != nil {
		return ChatOutput{}, err
	}
	reply := resp.Text()
	o.Store.Append(in.SessionKey,
		ai.NewUserTextMessage(in.Prompt),
		ai.NewTextMessage(ai.RoleModel, reply),
	)
	return ChatOutput{Reply: reply, TenantID: in.TenantID, UserID: in.UserID, SessionKey: in.SessionKey}, nil
}

// Classify runs the structured-output flow.
func (o *Orbit) Classify(ctx context.Context, text string) (*TicketCategory, error) {
	return o.classifyFlow.Run(ctx, text)
}

// RunDotprompt executes the embedded .prompt (dotprompt) file end-to-end
// through the real prompt + generate pipeline. Proves feature parity for
// dotprompt offline.
func (o *Orbit) RunDotprompt(ctx context.Context, product, question string) (string, error) {
	p := genkit.LookupPrompt(o.G, "orbit_support")
	if p == nil {
		return "", fmt.Errorf("dotprompt 'orbit_support' not registered")
	}
	resp, err := p.Execute(ctx,
		ai.WithModel(o.Model),
		ai.WithInput(map[string]any{"product": product, "question": question}),
	)
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}
