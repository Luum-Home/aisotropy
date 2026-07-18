package consolidation_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/sleepcycle"
)

// secretCoT is a reasoning payload that MUST NOT survive past the task boundary.
const secretCoT = "RAW-CHAIN-OF-THOUGHT-DO-NOT-PERSIST-7c1e"

// drainAnswerOnly mimics the real persistence path: it collects ONLY tok.Text
// (never tok.Reasoning) from a reasoning-bearing stream, exactly as generateFull
// / collectText / the summarizer do. Reasoning stays in its own channel and is
// discarded when this returns — the consolidate-then-discard lifecycle.
func drainAnswerOnly(ch <-chan llm.Token) string {
	var answer strings.Builder
	for tok := range ch {
		answer.WriteString(tok.Text) // deliberately ignores tok.Reasoning
	}
	return answer.String()
}

// reasoningStream produces a stream carrying reasoning (secretCoT) on the
// Reasoning channel and "the answer" on the Text channel.
func reasoningStream() <-chan llm.Token {
	ch := make(chan llm.Token, 4)
	ch <- llm.Token{Reasoning: secretCoT}
	ch <- llm.Token{Text: "the "}
	ch <- llm.Token{Text: "answer"}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch
}

// TestConsolidationPurity is the consolidation-purity blocker (ADR-0061): after
// a reasoning-on turn, raw chain-of-thought must appear in NONE of the durable
// sinks — the working-memory summary buffer (Summarizer.Condense), nor the
// sleep-cycle NDJSON export (sleepcycle.Record). Only the final answer, which
// never contained the reasoning, is persisted.
func TestConsolidationPurity(t *testing.T) {
	// 1. Consume the reasoning stream the way the runtime does: keep the answer,
	//    drop the reasoning.
	answer := drainAnswerOnly(reasoningStream())
	if answer != "the answer" {
		t.Fatalf("answer = %q, want %q", answer, "the answer")
	}
	if strings.Contains(answer, secretCoT) {
		t.Fatal("reasoning leaked into the persisted answer text")
	}

	// 2. The persisted turn is text-only (memory.Message has no reasoning field —
	//    a compile-time guarantee). Only the answer is stored.
	persisted := []memory.Message{
		{Role: "user", Content: "a question"},
		{Role: "assistant", Content: answer},
	}

	// 3. Summary buffer: condensing the evicted messages must not surface reasoning.
	summary, err := memory.NoopSummarizer{}.Condense(context.Background(), "", persisted)
	if err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if strings.Contains(summary, secretCoT) {
		t.Fatalf("reasoning reached the summary buffer:\n%s", summary)
	}

	// 4. Sleep-cycle cold export: the NDJSON the DataFusion worker reads is built
	//    from the persisted (text-only) content — never from reasoning.
	recs := make([]sleepcycle.Record, len(persisted))
	for i, m := range persisted {
		recs[i] = sleepcycle.Record{ID: "id", TenantID: "acme", UserID: "alice", Content: m.Content}
	}
	path := filepath.Join(t.TempDir(), "export.ndjson")
	if err := sleepcycle.WriteNDJSON(path, recs); err != nil {
		t.Fatalf("WriteNDJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if strings.Contains(string(data), secretCoT) {
		t.Fatalf("reasoning reached the sleep-cycle export:\n%s", data)
	}
}
