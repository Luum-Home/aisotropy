package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// defaultRagCollection is used only when QDRANT_COLLECTION is unset. The
// live-arena runner (arenas/live/runner/main.go frameworkCollectionEnv)
// ALWAYS sets QDRANT_COLLECTION to a unique arena_rag_<framework> value per
// adapter, to prevent cross-framework vector bleed when multiple adapters
// share one real Qdrant instance — this const is only the standalone-run
// fallback (e.g. a manual `bash run.sh` outside the runner).
const defaultRagCollection = "arena_rag"

// ragCollectionName resolves the collection to search: QDRANT_COLLECTION if
// set (the runner always sets it), else defaultRagCollection.
func ragCollectionName() string {
	if c := os.Getenv("QDRANT_COLLECTION"); c != "" {
		return c
	}
	return defaultRagCollection
}

// ragFlowInput/ragFlowOutput are the typed contract for the Genkit flow
// defined by runRAG below.
type ragFlowInput struct {
	System   string   `json:"system"`
	Question string   `json:"question"`
	Passages []string `json:"passages"`
}

type ragFlowOutput struct {
	Answer string `json:"answer"`
	// usage is not part of the flow's own return contract (a flow may fan out
	// to multiple model calls with their own usage); this adapter captures
	// usage from the underlying ai.ModelResponse via a closure, see runRAG.
}

// runRAG performs real retrieval-augmented generation against the Nimbus
// Rail corpus (arenas/live/_shared/corpus, collection "arena_rag"):
//  1. embed the question with the e5 "query:" role (EMBED_URL);
//  2. retrieve top-K passages from the real Qdrant collection (QDRANT_URL) via
//     github.com/qdrant/go-client — Genkit-Go ships no FIRST-PARTY Qdrant
//     plugin (a third-party Go one, qdrant/qdrant-genkit, exists but is not
//     adopted here — see README.md), so this half is driven directly (see
//     qdrant_client.go);
//  3. run a genkit.DefineFlow that calls agnes ONLY with the retrieved
//     passages as grounding context, instructed to answer only from that
//     context or say it does not know — this is what lets the two trap
//     questions in _shared/rag_questions.jsonl catch hallucination.
func runRAG(rc *runCtx, out *arenaOutput) error {
	question := lastUserMessage(rc.in.Messages)
	if question == "" {
		return fmt.Errorf("rag mode: no user message/question in request")
	}

	topK := rc.in.TopK
	if topK <= 0 {
		topK = 4
	}

	start := time.Now()

	fmt.Fprintf(os.Stderr, "PROGRESS: rag: embedding query (%d chars)\n", len(question))
	vec, err := embedQuery(rc.ctx, rc.embedURL, question)
	if err != nil {
		return fmt.Errorf("rag: embed query: %w", err)
	}

	ragCollection := ragCollectionName()
	fmt.Fprintf(os.Stderr, "PROGRESS: rag: searching qdrant collection %q (top_k=%d)\n", ragCollection, topK)
	docs, err := searchQdrant(rc.ctx, rc.qdrantURL, ragCollection, vec, topK)
	if err != nil {
		return fmt.Errorf("rag: qdrant search: %w", err)
	}
	out.Retrieved = docs
	fmt.Fprintf(os.Stderr, "PROGRESS: rag: retrieved %d passage(s)\n", len(docs))

	passages := make([]string, 0, len(docs))
	for _, d := range docs {
		passages = append(passages, fmt.Sprintf("[%s] %s", d.Title, d.Text))
	}

	var usage *ai.ModelResponse
	ragFlow := genkit.DefineFlow(rc.g, "ragAnswerFlow", func(ctx context.Context, in ragFlowInput) (ragFlowOutput, error) {
		system := in.System
		if system == "" {
			system = "You are a precise assistant answering questions about Nimbus Rail."
		}
		system += "\n\nAnswer ONLY using the context passages below. " +
			"If the answer is not contained in the context, say plainly that you " +
			"don't know / the information is not available — never invent an answer.\n\n" +
			"CONTEXT:\n" + strings.Join(in.Passages, "\n\n")

		resp, err := genkit.Generate(ctx, rc.g,
			ai.WithModel(rc.model),
			ai.WithSystem(system),
			ai.WithPrompt(in.Question),
		)
		if err != nil {
			return ragFlowOutput{}, err
		}
		usage = resp
		return ragFlowOutput{Answer: resp.Text()}, nil
	})

	res, err := ragFlow.Run(rc.ctx, ragFlowInput{
		System:   rc.in.System,
		Question: question,
		Passages: passages,
	})
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("rag: flow run: %w", err)
	}

	out.Reply = res.Answer
	if usage != nil {
		applyUsage(out, usage)
	}
	return nil
}

// lastUserMessage returns the content of the final user-role message, or
// empty string if there is none.
func lastUserMessage(msgs []inMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" || msgs[i].Role == "" {
			return msgs[i].Content
		}
	}
	return ""
}
