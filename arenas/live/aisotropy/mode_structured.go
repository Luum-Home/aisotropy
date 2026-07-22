package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// structuredAnswer is the typed target the model's tool arguments are captured
// into and validated against.
type structuredAnswer struct {
	Answer     string `json:"answer"`
	Confidence string `json:"confidence"`
}

// emitParamsSchema is the JSON Schema aisotropy advertises to the model for the
// structured-emit tool (ADR-0016 lever #1: the tool's argument shape is the
// contract the model is shown). The agent route offers it structurally.
const emitParamsSchema = `{
  "type": "object",
  "properties": {
    "answer": {"type": "string", "description": "A direct, concise answer to the user's question."},
    "confidence": {"type": "string", "enum": ["high", "medium", "low"], "description": "Confidence in the answer."}
  },
  "required": ["answer", "confidence"]
}`

const structuredPersona = `You answer questions by calling the
emit_structured_answer tool exactly once with your answer and your confidence
(one of: high, medium, low).`

// structuredExecutor is the tool body: it validates the model's arguments against
// the schema (a valid confidence enum + a non-empty answer) and echoes the
// normalized structured object back as JSON. A validation failure is returned as
// an error so it appears in the tool result the model sees — the fail-closed
// posture, never a coerced/invented value.
type structuredExecutor struct{}

func (structuredExecutor) Execute(_ context.Context, name string, args map[string]string) (string, error) {
	if name != "emit_structured_answer" {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	answer := args["answer"]
	conf := args["confidence"]
	if answer == "" {
		return "", fmt.Errorf("answer is required")
	}
	switch conf {
	case "high", "medium", "low":
	default:
		return "", fmt.Errorf("confidence %q is not one of high|medium|low", conf)
	}
	b, _ := json.Marshal(structuredAnswer{Answer: answer, Confidence: conf})
	return string(b), nil
}

// runStructured exercises aisotropy's REAL structured-output path. aisotropy's
// chat surface returns free text (its reflection target is a string), so the
// genuine schema-shaped path is the tool arm: a `emit_structured_answer` tool with
// an advertised JSON Schema, driven over the real agent route (chat_agent.go). The
// model must produce arguments matching the schema; the executor validates them
// fail-closed (rejecting a bad confidence enum rather than coercing) and echoes
// the normalized object, which is surfaced on `structured`. SHOULD-level: if the
// model never emits valid arguments within the step budget, structured is null.
func runStructured(req *request, out *result) error {
	question, err := lastUserMessage(req.Messages)
	if err != nil {
		return err
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}

	emitTool := aisotropy.Tool{
		Name:          "emit_structured_answer",
		Description:   "Emit the final answer as a structured object: an answer string and a confidence level (high|medium|low).",
		RequiredScope: "structured",
		Parameters:    json.RawMessage(emitParamsSchema),
	}
	opts := []aisotropy.Option{
		aisotropy.WithTool(emitTool),
		aisotropy.WithToolExecutor(structuredExecutor{}),
	}
	if agnes == nil {
		// ARENA_STUB=1: inject the adapter-local tool-capable stub (tool_stub.go)
		// instead of falling through to aisotropy's internal keyless StubClient,
		// which does not implement llm.ToolCaller and cannot drive the
		// /chat/agent route offline (see tool_stub.go's doc comment).
		opts = append(opts, aisotropy.WithLLM(toolStub{}))
	}
	srv, err := bootServer(agnes, structuredPersona, opts...)
	if err != nil {
		return err
	}
	defer srv.stop()

	agentBody := map[string]any{
		"prompt":     question + "\n\nAnswer by calling emit_structured_answer with your answer and confidence.",
		"product_id": productID,
		"session_id": sessionID(req),
		"scopes":     []string{"structured"},
	}
	body, _ := json.Marshal(agentBody)
	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/agent", srv.baseURL, arenaTenant, arenaUser)

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 115 * time.Second}).Do(httpReq)
	if err != nil {
		return fmt.Errorf("POST /chat/agent (structured): %w", err)
	}
	defer resp.Body.Close()
	out.LatencyMs = time.Since(start).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/chat/agent (structured) status %d", resp.StatusCode)
	}

	var agentResp struct {
		Steps []struct {
			Tool   string            `json:"tool"`
			Args   map[string]string `json:"args"`
			Result string            `json:"result"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentResp); err != nil {
		return fmt.Errorf("decode /chat/agent (structured): %w", err)
	}
	setUsage(out, srv.app)

	for _, s := range agentResp.Steps {
		if s.Tool != "emit_structured_answer" || s.Result == "" {
			continue
		}
		var typed structuredAnswer
		if err := json.Unmarshal([]byte(s.Result), &typed); err == nil && typed.Answer != "" {
			out.Structured = typed
			out.Reply = typed.Answer
			logf("structured: validated typed object via agent route (confidence=%s)", typed.Confidence)
			return nil
		}
	}
	logf("structured: model did not emit valid structured args within the step budget; structured=null")
	if out.Reply == "" {
		out.Reply = "(no structured answer produced)"
	}
	return nil
}
