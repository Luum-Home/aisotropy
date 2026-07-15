// Package orbit builds the "Orbit" multi-tenant support chatbot on top of the
// REAL CloudWeGo Eino framework (github.com/cloudwego/eino, Apache-2.0).
//
// Everything here runs through Eino's real runtime:
//   - compose.Chain[[]*schema.Message, *schema.Message] for the plain chat turn
//   - the same compiled runnable's Stream() for SSE-style streaming
//   - flow/agent/react.Agent + a real components/tool for tool calling
//
// The only fake is the *model* (StubChatModel), so the smoke test needs no API
// key. Swap NewStubChatModel() for eino-ext's openai/claude ChatModel and the
// exact same graph talks to a live LLM.
package orbit

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

// Orbit wires the Eino graphs once and serves chat turns.
//
// Note the honesty point baked into the type: there is NO tenant field on the
// runtime. Isolation is entirely the caller's job — see BuildMessages / the
// working-memory map, which are developer-passed, not framework-enforced.
type Orbit struct {
	chat  compose.Runnable[[]*schema.Message, *schema.Message] // real compiled Eino chain
	agent *react.Agent                                         // real Eino ReAct agent

	// workingMemory is Orbit's DIY working memory. Eino has no built-in
	// tenant-scoped store, so the developer owns this map AND its keying.
	// A wrong key here is a silent cross-tenant read — the crux of FEATURES.md.
	workingMemory map[string][]string
}

// New compiles the real Eino chat chain and ReAct agent with the offline stub
// model. Pass a real model.ToolCallingChatModel for live mode.
func New(ctx context.Context) (*Orbit, error) {
	stub := NewStubChatModel()

	// --- Real Eino chain: []messages -> assistant message ---
	chain, err := compose.NewChain[[]*schema.Message, *schema.Message]().
		AppendChatModel(stub).
		Compile(ctx)
	if err != nil {
		return nil, err
	}

	// --- Real Eino tool + ReAct agent ---
	orderTool, err := newOrderTool()
	if err != nil {
		return nil, err
	}
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: stub,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: []tool.BaseTool{orderTool}},
		MaxStep:          6,
	})
	if err != nil {
		return nil, err
	}

	return &Orbit{
		chat:          chain,
		agent:         agent,
		workingMemory: make(map[string][]string),
	}, nil
}

// sessionKey is Orbit's DIY scoping key. The framework never sees tenant/user —
// the developer must remember to build the full key. Forgetting the tenant part
// is exactly the leak the isolation harness proves.
func sessionKey(tenantID, userID, sessionID string) string {
	return tenantID + ":" + userID + ":" + sessionID
}

// Remember appends a note to a session's working memory (developer-managed).
func (o *Orbit) Remember(tenantID, userID, sessionID, note string) {
	k := sessionKey(tenantID, userID, sessionID)
	o.workingMemory[k] = append(o.workingMemory[k], note)
}

// BuildMessages assembles the model input: a system prompt (with the session's
// remembered notes folded in) plus the user's prompt. The `memKey` argument is
// deliberately explicit — pass the RIGHT key and it is correct; pass a wrong
// key and you read another session's notes. Nothing in Eino stops you.
func (o *Orbit) BuildMessages(memKey, prompt string) []*schema.Message {
	notes := ""
	for i, n := range o.workingMemory[memKey] {
		if i > 0 {
			notes += "; "
		}
		notes += n
	}
	sys := "You are Orbit, a support assistant. notes:" + notes
	return []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(prompt),
	}
}

// Chat runs one turn through the real compiled Eino chain.
func (o *Orbit) Chat(ctx context.Context, tenantID, userID, sessionID, prompt string) (string, error) {
	msgs := o.BuildMessages(sessionKey(tenantID, userID, sessionID), prompt)
	out, err := o.chat.Invoke(ctx, msgs)
	if err != nil {
		return "", err
	}
	return out.Content, nil
}

// ChatWithMemKey lets a caller pass an explicit working-memory key — used by
// the isolation harness to demonstrate the wrong-key cross-read at the app tier.
func (o *Orbit) ChatWithMemKey(ctx context.Context, memKey, prompt string) (string, error) {
	out, err := o.chat.Invoke(ctx, o.BuildMessages(memKey, prompt))
	if err != nil {
		return "", err
	}
	return out.Content, nil
}

// Stream runs one turn through the real compiled Eino chain's Stream() and
// returns the assembled chunks plus their count (proves streaming is real).
func (o *Orbit) Stream(ctx context.Context, tenantID, userID, sessionID, prompt string) (string, int, error) {
	msgs := o.BuildMessages(sessionKey(tenantID, userID, sessionID), prompt)
	sr, err := o.chat.Stream(ctx, msgs)
	if err != nil {
		return "", 0, err
	}
	defer sr.Close()

	var full string
	var n int
	for {
		chunk, err := sr.Recv()
		if err != nil {
			break // io.EOF terminates the stream
		}
		full += chunk.Content
		n++
	}
	return full, n, nil
}

// Agent runs one turn through the real Eino ReAct agent (may invoke the order
// tool). Returns the final assistant content.
func (o *Orbit) Agent(ctx context.Context, prompt string) (string, error) {
	out, err := o.agent.Generate(ctx, []*schema.Message{
		schema.SystemMessage("You are Orbit. Use tools when asked about orders."),
		schema.UserMessage(prompt),
	})
	if err != nil {
		return "", err
	}
	return out.Content, nil
}

// --- Real Eino tool ---

type orderReq struct {
	OrderID string `json:"order_id" jsonschema:"description=the order id to look up"`
}

type orderResp struct {
	Status string `json:"status"`
}

// newOrderTool builds a real components/tool.InvokableTool via eino's InferTool,
// which reflects the request struct into a schema.ToolInfo for the model.
func newOrderTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"lookup_order",
		"Look up the shipping status of a customer order by its order_id.",
		func(ctx context.Context, req orderReq) (orderResp, error) {
			return orderResp{Status: "shipped, arriving Thursday (order " + req.OrderID + ")"}, nil
		},
	)
}
