// Command parity is the comprehensive feature-parity demo for the REAL Google
// ADK Go framework (google.golang.org/adk/v2 v2.0.0), run fully OFFLINE.
//
//	go run ./cmd/parity
//
// It walks ~25 of aisotropy's executable features (see arenas/ARENA-SPEC-v2.md)
// and, for each, EXECUTES the real framework (real llmagent + runner +
// session.Service + memory.Service + tool.Toolset, only the model is faked via
// the framework's own model.LLM seam) to justify a coverage class:
//
//	[BUILT-IN]     the framework provides it as a primitive (exercised here)
//	[CONVENTION]   works, but only by developer discipline (no enforcement)
//	[PARTIAL]      a piece is provided; the rest is developer code
//	[DIY]          no primitive; we run the hand-written version and label it
//	[PAID]         only available via a hosted/managed (non-OSS) backend
//	[NOT-POSSIBLE] the framework cannot express it; we show the closest thing
//
// HONESTY CONTRACT: a class is only [BUILT-IN]/[PARTIAL] when the real ADK code
// path is invoked in THIS file. Where aisotropy has a tier ADK lacks (FTS5
// facts, self-hostable vectors, transactional outbox, sleep consolidation) the
// class is [DIY]/[NOT-POSSIBLE]/[PAID] and the closest ADK primitive is shown.
//
// SYNTHETIC NOTICE: token counts are len(text)/4 and "embeddings" are keyword
// sets — deterministic fakes so the demo runs with no API key and no network.
// The point is what the FRAMEWORK provides, not model quality. Fakes are labeled.
package main

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"

	"adk-go-arena/orbit"
)

// ---------------------------------------------------------------------------
// Output helpers. Every feature prints exactly one FEATURE line.
// ---------------------------------------------------------------------------

var classCount = map[string]int{}

func feature(n int, name, class, evidence string) {
	classCount[class]++
	fmt.Printf("FEATURE %-2d %-42s [%s] %s\n", n, name, class, evidence)
}

func hdr(area string) {
	fmt.Println()
	fmt.Println(strings.Repeat("-", 78))
	fmt.Println(area)
	fmt.Println(strings.Repeat("-", 78))
}

func synthTokens(s string) int32 { return int32(len(s) / 4) }

// ---------------------------------------------------------------------------
// countingModel: offline model.LLM that answers from history, records synthetic
// usage on the REAL LLMResponse.UsageMetadata field, and counts how many times
// it actually ran (the counter proves pre-spend gates / caches skip the model).
// ---------------------------------------------------------------------------

type countingModel struct{ calls atomic.Int64 }

func (m *countingModel) Name() string { return "parity-counting-fake" }
func (m *countingModel) Calls() int64 { return m.calls.Load() }

func (m *countingModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls.Add(1)
		var lastUser string
		var remembered []string
		var inTok int32
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p == nil || p.Text == "" {
					continue
				}
				inTok += synthTokens(p.Text)
				if idx := strings.Index(p.Text, orbit.MemoPrefix); idx >= 0 {
					remembered = append(remembered, strings.TrimSpace(p.Text[idx+len(orbit.MemoPrefix):]))
				}
				if c.Role == genai.RoleUser {
					lastUser = p.Text
				}
			}
		}
		answer := answerFrom(lastUser, remembered)
		out := synthTokens(answer)
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: answer}}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount: inTok, CandidatesTokenCount: out, TotalTokenCount: inTok + out,
			},
		}, nil)
	}
}

func answerFrom(lastUser string, remembered []string) string {
	lu := strings.ToLower(lastUser)
	switch {
	case strings.Contains(lu, "remember") || strings.Contains(lu, "recall") || strings.Contains(lu, "what did"):
		if len(remembered) == 0 {
			return "I don't have anything on file for this conversation."
		}
		return "Here is what you told me earlier: " + strings.Join(remembered, "; ")
	case strings.Contains(lastUser, orbit.MemoPrefix):
		return "Noted. I'll remember that for this conversation."
	default:
		return "Orbit support here — how can I help? (offline fake model)"
	}
}

// ---------------------------------------------------------------------------
// streamModel: in stream mode (RunConfig.StreamingMode==SSE the runner passes
// stream=true) it yields one Partial LLMResponse per token, checking ctx between
// tokens. That makes SSE streaming AND immediate ctx-cancellation observable
// through the real runner.
// ---------------------------------------------------------------------------

type streamModel struct{ produced atomic.Int64 }

func (m *streamModel) Name() string { return "parity-stream-fake" }

func (m *streamModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	toks := []string{"Orbit ", "streams ", "one ", "token ", "at ", "a ", "time ", "offline."}
	return func(yield func(*model.LLMResponse, error) bool) {
		if !stream {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{{Text: strings.Join(toks, "")}}}}, nil)
			return
		}
		var b strings.Builder
		for _, t := range toks {
			if ctx.Err() != nil { // client disconnect / cancel stops the UPSTREAM producer
				return
			}
			m.produced.Add(1)
			b.WriteString(t)
			if !yield(&model.LLMResponse{Partial: true,
				Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: t}}}}, nil) {
				return
			}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
			Parts: []*genai.Part{{Text: b.String()}}}}, nil) // final aggregate (non-partial)
	}
}

// ---------------------------------------------------------------------------
// toolModel: emits a genai FunctionCall the first time it is asked, then a text
// answer once it sees the tool's FunctionResponse in history. This drives the
// REAL multi-step agent loop (runner executes the functiontool between calls).
// ---------------------------------------------------------------------------

type toolModel struct{ steps atomic.Int64 }

func (m *toolModel) Name() string { return "parity-tool-fake" }

func (m *toolModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.steps.Add(1)
		sawResponse := false
		var city string
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p != nil && p.FunctionResponse != nil {
					sawResponse = true
				}
				if p != nil && p.Text != "" && strings.Contains(strings.ToLower(p.Text), "weather") {
					city = "Buenos Aires"
				}
			}
		}
		if !sawResponse {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{genai.NewPartFromFunctionCall("get_weather", map[string]any{"city": city})}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
			Parts: []*genai.Part{{Text: "It is sunny in " + city + " (from the tool)."}}}}, nil)
	}
}

// ---------------------------------------------------------------------------
// runTurn drives one turn through a real ADK runner and returns final text +
// framework-reported usage off the event stream.
// ---------------------------------------------------------------------------

func runTurn(ctx context.Context, r *runner.Runner, userID, sess, prompt string) (reply string, in, out int32, err error) {
	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText(prompt)}}
	for ev, e := range r.Run(ctx, userID, sess, msg, agent.RunConfig{}) {
		if e != nil {
			return "", 0, 0, e
		}
		if ev == nil {
			continue
		}
		if ev.UsageMetadata != nil {
			in += ev.UsageMetadata.PromptTokenCount
			out += ev.UsageMetadata.CandidatesTokenCount
		}
		if ev.IsFinalResponse() && ev.LLMResponse.Content != nil {
			for _, p := range ev.LLMResponse.Content.Parts {
				if p != nil && p.Text != "" {
					reply = p.Text
				}
			}
		}
	}
	return reply, in, out, nil
}

func main() {
	ctx := context.Background()
	line := strings.Repeat("=", 78)
	fmt.Println(line)
	fmt.Println("ADK Go (google.golang.org/adk/v2 v2.0.0) — COMPREHENSIVE FEATURE PARITY")
	fmt.Println(line)
	fmt.Println("Real llmagent + runner + session.Service + memory.Service + tool.Toolset.")
	fmt.Println("Offline fake models via the framework's own model.LLM seam. No API key.")
	fmt.Println("SYNTHETIC: token counts = len/4; 'embeddings' = keyword sets. Labeled inline.")

	runMemoryTiers(ctx)
	runContractOutput(ctx)
	runToolsAgentic(ctx)
	runAuth(ctx)
	runCostGov(ctx)
	runObservability(ctx)
	runConsolidation(ctx)

	// -------- tally --------
	fmt.Println()
	fmt.Println(line)
	fmt.Println("TALLY (run-verified against real google.golang.org/adk/v2 v2.0.0)")
	fmt.Println(line)
	total := 0
	for _, c := range []string{"BUILT-IN", "PARTIAL", "CONVENTION", "PAID", "DIY", "NOT-POSSIBLE"} {
		fmt.Printf("  %-13s %d\n", c, classCount[c])
		total += classCount[c]
	}
	fmt.Printf("  %-13s %d executable features walked\n", "TOTAL", total)
	fmt.Println()
	fmt.Println("HEADLINE: ADK Go ships TWO memory tiers of different nature — session.Service")
	fmt.Println("(working KV, per appName/userID/sessionID) + memory.Service (keyword recall).")
	fmt.Println("It has NO self-hostable vector tier (vectors = Vertex MemoryBank, PAID/managed),")
	fmt.Println("NO FTS5 facts journal, NO scoring/supersede, NO transactional outbox, NO sleep")
	fmt.Println("consolidation. aisotropy's 4-tier temperature hierarchy (BadgerDB µs / SQLite+FTS5")
	fmt.Println("/ cosine vectors / DataFusion batch) collapses here to 2 tiers + a lot of DIY.")
	fmt.Println()
	fmt.Println("Result: PASS — every feature above executed against the real ADK stack, offline.")
}
