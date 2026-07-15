package orbit

import (
	"context"
	"iter"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// MemoPrefix marks a fact the user asks Orbit to remember. The fake model
// scans conversation history for it so recall is observable end to end.
const MemoPrefix = "MEMO:"

// FakeModel is a deterministic, fully-offline implementation of the REAL
// google.golang.org/adk/v2 model.LLM interface. It never touches the network,
// so the Orbit chatbot (and the isolation smoke test) run with no API key.
//
// It answers from req.Contents — the conversation the ADK runner assembles
// from session state before every model call. That is exactly what makes
// tenant isolation observable through the real framework: a remembered fact
// can only appear in req.Contents if the runner loaded the *correct*
// (appName, userID, sessionID) session. Load the wrong session and the model
// literally cannot see the other tenant's fact.
type FakeModel struct{ name string }

// NewFakeModel returns an offline model.LLM.
func NewFakeModel() *FakeModel { return &FakeModel{name: "orbit-fake-model"} }

// Name implements model.LLM.
func (m *FakeModel) Name() string { return m.name }

// GenerateContent implements model.LLM. It returns a single, non-streamed
// final response derived only from the history the runner passed in.
func (m *FakeModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var lastUser string
		var remembered []string
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p == nil || p.Text == "" {
					continue
				}
				if idx := strings.Index(p.Text, MemoPrefix); idx >= 0 {
					remembered = append(remembered, strings.TrimSpace(p.Text[idx+len(MemoPrefix):]))
				}
				if c.Role == genai.RoleUser {
					lastUser = p.Text
				}
			}
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: m.answer(lastUser, remembered)}},
			},
		}, nil)
	}
}

func (m *FakeModel) answer(lastUser string, remembered []string) string {
	lu := strings.ToLower(lastUser)
	switch {
	case strings.Contains(lu, "remember") || strings.Contains(lu, "recall") || strings.Contains(lu, "what did"):
		if len(remembered) == 0 {
			return "I don't have anything on file for this conversation."
		}
		return "Here is what you told me earlier: " + strings.Join(remembered, "; ")
	case strings.Contains(lastUser, MemoPrefix):
		return "Noted. I'll remember that for this conversation."
	default:
		return "Orbit support here — how can I help? (offline fake model)"
	}
}
