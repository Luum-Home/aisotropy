// Command eino-live-arena is an EXHAUSTIVE multi-mode LIVE arena adapter for
// the CloudWeGo Eino agent framework. It reads one JSON request from stdin
// (a "mode" field selects the behavior), drives Eino's native components
// (chat model, embedding, retriever, ReAct agent, chat template, streaming)
// against REAL infrastructure — the agnes-2.0-flash chat endpoint, a real
// e5 embeddings server, a real Qdrant collection, and a real on-disk SQLite
// store for cross-process memory — and writes exactly one JSON response to
// stdout.
//
// See README.md for the full per-mode contract and the Eino wiring details.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ---- Request contract (stdin) ----

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type inputTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type adapterInput struct {
	Mode       string          `json:"mode"`
	System     string          `json:"system"`
	Messages   []inputMessage  `json:"messages"`
	Tools      []inputTool     `json:"tools"`
	SessionID  string          `json:"session_id"`  // memory mode: correlates turns across process invocations; memory_scoped/isolation_leak: optional extra scope layer
	TopK       int             `json:"top_k"`       // rag mode: retrieval depth (default 4)
	Schema     json.RawMessage `json:"schema"`      // structured mode: JSON-Schema override
	SchemaName string          `json:"schema_name"` // structured mode: schema name (default "structured_answer")

	// memory_scoped / isolation_leak: hand-rolled compound multi-tenant scope.
	// Eino has no native concept of these — see mode_scoped_memory.go.
	TenantID  string `json:"tenant_id"`  // required
	UserID    string `json:"user_id"`    // required
	ProductID string `json:"product_id"` // required
	Fact      string `json:"fact"`       // write/seed payload: store this fact under the scope above
	Query     string `json:"query"`      // recall/probe payload: ask this question against the scope above

	// crash_recovery mode — see mode_crash.go. Exactly one of CrashTurns
	// (producer/process A) or Messages (resumer/process B) is set per call.
	CrashTurns      []string `json:"crash_turns"`      // producer: ordered user turns to run+persist one at a time
	ExpectSubstring string   `json:"expect_substring"` // resumer: substring the reply must contain to count as recall of the pre-crash fact
}

// ---- Response contract (stdout) ----

type outputToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Result    string         `json:"result,omitempty"` // populated only when the tool was actually executed (tools mode)
}

type outputRetrievedDoc struct {
	DocID string  `json:"doc_id"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
	Text  string  `json:"text"`
}

type adapterOutput struct {
	Framework    string               `json:"framework"`
	Mode         string               `json:"mode"`
	Model        string               `json:"model"`
	Reply        string               `json:"reply"`
	InputTokens  int                  `json:"input_tokens"`
	OutputTokens int                  `json:"output_tokens"`
	LatencyMS    int64                `json:"latency_ms"`
	ToolCalls    []outputToolCall     `json:"tool_calls"`
	Retrieved    []outputRetrievedDoc `json:"retrieved"`
	Structured   any                  `json:"structured"`
	TTFTMS       int64                `json:"ttft_ms"`
	Error        string               `json:"error"`

	// memory_scoped mode only (null otherwise) — see mode_scoped_memory.go.
	ScopedRecall *scopedRecallResult `json:"scoped_recall"`
	// isolation_leak mode only (null otherwise); false = no leak (good).
	LeakDetected *bool `json:"leak_detected"`

	// crash_recovery mode only (null otherwise) — see mode_crash.go. Only
	// populated by the resumer (process B) call; the producer (process A)
	// call leaves these nil since recovery isn't evaluated from that side.
	Resumed              *bool `json:"resumed"`
	PrecrashFactRecalled *bool `json:"precrash_fact_recalled"`
	StateLost            *bool `json:"state_lost"`
}

const (
	frameworkName = "eino"

	modeQA            = "qa"
	modeRAG           = "rag"
	modeTools         = "tools"
	modeMemory        = "memory"
	modeStructured    = "structured"
	modeStream        = "stream"
	modeMemoryScoped  = "memory_scoped"
	modeIsolationLeak = "isolation_leak"
	modeCrashRecovery = "crash_recovery"
	modeObservability = "observability"
)

func main() {
	out := &adapterOutput{
		Framework: frameworkName,
		ToolCalls: []outputToolCall{},
		Retrieved: []outputRetrievedDoc{},
	}

	if err := dispatch(out); err != nil {
		out.Error = err.Error()
		fmt.Fprintln(os.Stderr, "eino-live-arena: "+err.Error())
		writeOutput(out)
		os.Exit(1)
	}

	writeOutput(out)
}

// dispatch reads stdin, resolves the shared env/client config, selects the
// mode handler, and populates out in place.
func dispatch(out *adapterOutput) error {
	env, err := loadEnv()
	if err != nil {
		return err
	}
	out.Model = env.model

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	var in adapterInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("parsing stdin JSON: %w", err)
	}
	if in.Mode == "" {
		in.Mode = modeQA
	}
	out.Mode = in.Mode

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	switch in.Mode {
	case modeQA:
		return runQA(ctx, env, in, out)
	case modeRAG:
		return runRAG(ctx, env, in, out)
	case modeTools:
		return runTools(ctx, env, in, out)
	case modeMemory:
		return runMemory(ctx, env, in, out)
	case modeStructured:
		return runStructured(ctx, env, in, out)
	case modeStream:
		return runStream(ctx, env, in, out)
	case modeMemoryScoped:
		return runMemoryScoped(ctx, env, in, out)
	case modeIsolationLeak:
		return runIsolationLeak(ctx, env, in, out)
	case modeCrashRecovery:
		return runCrashRecovery(ctx, env, in, out)
	case modeObservability:
		// observabilityOutput is a deliberately different wire contract from
		// adapterOutput (see mode_observability.go) — runObservability owns
		// the entire stdout write and process exit code itself and never
		// returns, so the generic writeOutput(out) call in main() below is
		// simply never reached for this mode.
		runObservability(ctx, env, in)
		return nil
	default:
		return fmt.Errorf("unknown mode %q (want one of qa|rag|tools|memory|structured|stream|memory_scoped|isolation_leak|crash_recovery|observability)", in.Mode)
	}
}

// buildMessages converts the wire-format history into Eino's native
// []*schema.Message, prefixed by the system prompt when present. Shared by
// every mode that just replays the given history as-is (qa, tools, structured,
// stream). rag and memory build their own message lists (grounding context /
// persisted history respectively).
func buildMessages(in adapterInput) ([]inputMessage, error) {
	msgs := make([]inputMessage, 0, len(in.Messages)+1)
	if in.System != "" {
		msgs = append(msgs, inputMessage{Role: "system", Content: in.System})
	}
	for i, m := range in.Messages {
		switch m.Role {
		case "user", "assistant":
		default:
			return nil, fmt.Errorf("messages[%d]: unsupported role %q (want \"user\" or \"assistant\")", i, m.Role)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// lastUserMessage returns the content of the final "user" message, which the
// SPEC.md contract designates as "the one to answer".
func lastUserMessage(in adapterInput) (string, error) {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == "user" {
			return in.Messages[i].Content, nil
		}
	}
	return "", errors.New("no user message found in messages[]")
}

func writeOutput(out *adapterOutput) {
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "eino-live-arena: failed to encode output JSON: "+err.Error())
		fmt.Println(`{"framework":"eino","mode":"","model":"","reply":"","input_tokens":-1,"output_tokens":-1,"latency_ms":0,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"internal: failed to encode response","scoped_recall":null,"leak_detected":null,"resumed":null,"precrash_fact_recalled":null,"state_lost":null}`)
	}
}
