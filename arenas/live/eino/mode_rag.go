package main

import (
	"context"
	"fmt"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/qdrant/go-client/qdrant"
)

const ragSystemPrompt = `You are a support assistant for Nimbus Rail. Answer the
user's question using ONLY the context passages below. If the context does not
contain the answer, say plainly that you don't know — never invent facts that
are not in the context.`

const defaultRAGTopK = 4

// runRAG drives real retrieval-augmented generation through an Eino
// compose.Chain: a Lambda node embeds the query with the real e5 server and
// searches the real Qdrant collection directly via the qdrant-go client
// (the eino-ext qdrant retriever component hardcodes payload key "content",
// but arena_rag's payload uses "text"/"title"/"doc_id" — see README.md for
// why this adapter drives Qdrant natively instead of through that
// component), a ChatTemplate node builds the grounded prompt, and the real
// agnes chat model node answers from context only.
func runRAG(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	question, err := lastUserMessage(in)
	if err != nil {
		return err
	}

	history, err := priorHistory(in)
	if err != nil {
		return err
	}

	embedder, err := newQueryEmbedder(ctx, env)
	if err != nil {
		return err
	}
	qdrantClient, err := newQdrantClient(env)
	if err != nil {
		return err
	}
	defer qdrantClient.Close()

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return err
	}

	topK := in.TopK
	if topK <= 0 {
		topK = defaultRAGTopK
	}

	var retrieved []outputRetrievedDoc // captured by the retrieve lambda below

	retrieve := compose.InvokableLambda(func(ctx context.Context, query string) (map[string]any, error) {
		vectors, err := embedder.EmbedStrings(ctx, []string{query})
		if err != nil {
			return nil, fmt.Errorf("embedding query via real e5 server: %w", err)
		}
		if len(vectors) != 1 {
			return nil, fmt.Errorf("e5 embed server: expected 1 vector, got %d", len(vectors))
		}
		vec32 := make([]float32, len(vectors[0]))
		for i, v := range vectors[0] {
			vec32[i] = float32(v)
		}

		resp, err := qdrantClient.Query(ctx, &qdrant.QueryPoints{
			CollectionName: env.qdrantCollection,
			Query:          qdrant.NewQueryDense(vec32),
			Limit:          qdrant.PtrOf(uint64(topK)),
			WithPayload:    qdrant.NewWithPayload(true),
		})
		if err != nil {
			return nil, fmt.Errorf("qdrant search on %s: %w", env.qdrantCollection, err)
		}

		var contextText string
		for _, pt := range resp {
			docID := pt.Payload["doc_id"].GetStringValue()
			title := pt.Payload["title"].GetStringValue()
			text := pt.Payload["text"].GetStringValue()
			retrieved = append(retrieved, outputRetrievedDoc{
				DocID: docID,
				Title: title,
				Score: float64(pt.Score),
				Text:  text,
			})
			contextText += fmt.Sprintf("### %s\n%s\n\n", title, text)
		}

		return map[string]any{
			"context":  contextText,
			"question": query,
			"history":  history,
		}, nil
	})

	tmpl := prompt.FromMessages(schema.FString,
		schema.SystemMessage(ragSystemPrompt),
		schema.MessagesPlaceholder("history", true),
		schema.UserMessage("Context:\n{context}\n\nQuestion: {question}"),
	)

	chain := compose.NewChain[string, *schema.Message]()
	chain.
		AppendLambda(retrieve).
		AppendChatTemplate(tmpl).
		AppendChatModel(chatModel)

	runnable, err := chain.Compile(ctx)
	if err != nil {
		return fmt.Errorf("compiling Eino RAG chain: %w", err)
	}

	start := time.Now()
	reply, err := runnable.Invoke(ctx, question)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("Eino RAG chain invoke: %w", err)
	}

	out.Reply = reply.Content
	out.InputTokens, out.OutputTokens = usageOf(reply)
	out.Retrieved = retrieved
	return nil
}

// priorHistory returns every message except the final user turn (which
// runRAG treats as the question), converted to Eino's native message type,
// for the ChatTemplate's "history" placeholder.
func priorHistory(in adapterInput) ([]*schema.Message, error) {
	if len(in.Messages) == 0 {
		return nil, nil
	}
	last := len(in.Messages) - 1
	for last >= 0 && in.Messages[last].Role != "user" {
		last--
	}
	if last < 0 {
		return nil, nil
	}
	return toSchemaMessages(in.Messages[:last])
}
