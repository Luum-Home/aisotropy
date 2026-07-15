package orbit

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
	"github.com/firebase/genkit/go/genkit"
)

// FakeModelName is the id of the offline, deterministic model this arena
// registers with Genkit. It lets the whole Orbit build run — real
// genkit.Generate, real flows, real tool-calling loop, real dotprompt — with
// zero API key and zero network.
const FakeModelName = "fake/orbit"

// secretRe matches the marker the isolation smoke plants in a session so the
// model "reveals" whatever context it was handed. A real LLM will happily
// surface anything in its context window; that is exactly the point — the leak
// is that the WRONG context was assembled by the developer-passed session key.
var secretRe = regexp.MustCompile(`SECRET:[A-Za-z0-9\-]+`)

// DefineFakeModel registers a deterministic offline model against the real
// Genkit registry via genkit.DefineModel. The returned ai.Model is a genuine
// framework Model — it is invoked by genkit.Generate through the normal
// generate/tool/streaming pipeline, not called directly.
func DefineFakeModel(g *genkit.Genkit) ai.Model {
	return genkit.DefineModel(g, FakeModelName,
		&ai.ModelOptions{
			Label:    "Orbit offline fake model",
			Supports: &ai.ModelSupports{Multiturn: true, Tools: true, SystemRole: true},
		},
		func(ctx context.Context, req *ai.ModelRequest, cb core.StreamCallback[*ai.ModelResponseChunk]) (*ai.ModelResponse, error) {
			part, finish := fakeInfer(req)

			// Real streaming: if the caller asked for streaming (ai.WithStreaming),
			// emit the reply word-by-word through the framework callback.
			if cb != nil && part.Text != "" {
				for _, w := range strings.Fields(part.Text) {
					if err := cb(ctx, &ai.ModelResponseChunk{
						Content: []*ai.Part{ai.NewTextPart(w + " ")},
					}); err != nil {
						return nil, err
					}
				}
			}

			return &ai.ModelResponse{
				Message:      ai.NewMessage(ai.RoleModel, nil, part),
				FinishReason: finish,
			}, nil
		},
	)
}

// fakeInfer is the deterministic "reasoning" of the fake model. It inspects the
// assembled ModelRequest exactly as a real provider would and returns one Part.
func fakeInfer(req *ai.ModelRequest) (*ai.Part, ai.FinishReason) {
	// 1) Structured-output branch: genkit.GenerateData sets req.Output.Format=json.
	//    Return JSON that conforms to the requested Orbit ticket schema.
	if req.Output != nil && strings.Contains(strings.ToLower(req.Output.Format), "json") {
		return ai.NewJSONPart(classifyJSON(lastUserText(req))), ai.FinishReasonStop
	}

	// 2) Tool-calling branch: if a tool is offered and the user asks about an
	//    order, and we have not yet seen a tool response, emit a REAL
	//    ToolRequest part. Genkit's Generate loop will execute the tool and
	//    call us again with the ToolResponse in history.
	if len(req.Tools) > 0 && !hasToolResponse(req) {
		if id := extractOrderID(lastUserText(req)); id != "" {
			return ai.NewToolRequestPart(&ai.ToolRequest{
				Name:  req.Tools[0].Name,
				Ref:   "call-1",
				Input: map[string]any{"order_id": id},
			}), ai.FinishReasonStop
		}
	}

	// 3) Post-tool branch: fold the tool result into a natural reply.
	if out := lastToolOutput(req); out != "" {
		return ai.NewTextPart("Thanks for waiting — I checked our system: " + out), ai.FinishReasonStop
	}

	// 4) Plain support reply. Crucially, the model surfaces any SECRET marker it
	//    finds anywhere in the assembled conversation. This is faithful LLM
	//    behaviour: it answers from the context it was given.
	full := allText(req)
	if m := secretRe.FindString(full); m != "" {
		return ai.NewTextPart(
			"Of course! Looking at this conversation's history, your account recovery code is " + m + ". Let me know if you need anything else.",
		), ai.FinishReasonStop
	}
	return ai.NewTextPart(
		"Hi, this is Orbit Support. I can help with orders, billing, and account questions. How can I help?",
	), ai.FinishReasonStop
}

// classifyJSON produces a deterministic ticket classification for the
// structured-output demo. It matches the TicketCategory schema in orbit.go.
//
// Genkit's json formatter appends schema instructions ("Output should be in
// JSON format ... ```{schema}```") to the user prompt, so we strip that and
// never echo raw prompt text (backticks/quotes) back into the output — doing so
// would corrupt Genkit's own markdown-JSON extraction on the way back.
func classifyJSON(text string) string {
	if i := strings.Index(text, "Output should be in JSON"); i >= 0 {
		text = text[:i]
	}
	t := strings.ToLower(text)
	cat, prio := "general", "low"
	switch {
	case strings.Contains(t, "refund"), strings.Contains(t, "charge"), strings.Contains(t, "billing"):
		cat, prio = "billing", "high"
	case strings.Contains(t, "broken"), strings.Contains(t, "error"), strings.Contains(t, "down"):
		cat, prio = "technical", "high"
	case strings.Contains(t, "order"), strings.Contains(t, "shipping"), strings.Contains(t, "delivery"):
		cat, prio = "orders", "medium"
	}
	b, _ := json.Marshal(map[string]string{
		"category": cat,
		"priority": prio,
		"summary":  "Ticket classified as " + cat + " (priority " + prio + ")",
	})
	return string(b)
}

var orderRe = regexp.MustCompile(`(?i)#?(\d{3,})`)

func extractOrderID(text string) string {
	if !strings.Contains(strings.ToLower(text), "order") {
		return ""
	}
	if m := orderRe.FindStringSubmatch(text); len(m) == 2 {
		return m[1]
	}
	return ""
}

func lastUserText(req *ai.ModelRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == ai.RoleUser {
			return req.Messages[i].Text()
		}
	}
	return ""
}

func allText(req *ai.ModelRequest) string {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(m.Text())
		sb.WriteString("\n")
	}
	return sb.String()
}

func hasToolResponse(req *ai.ModelRequest) bool {
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.ToolResponse != nil {
				return true
			}
		}
	}
	return false
}

func lastToolOutput(req *ai.ModelRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, p := range req.Messages[i].Content {
			if p.ToolResponse != nil {
				if s, ok := p.ToolResponse.Output.(string); ok {
					return s
				}
				b, _ := json.Marshal(p.ToolResponse.Output)
				return string(b)
			}
		}
	}
	return ""
}
