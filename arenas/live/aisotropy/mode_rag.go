package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultRAGTopK = 4

// ragPersona instructs the model to answer ONLY from aisotropy's hydrated memory
// context and to refuse otherwise — this is what makes the trap questions catch
// hallucination. aisotropy's /ask route injects the retrieved corpus passages
// into the system prompt around this persona (product persona base prompt).
const ragPersona = `You are a support assistant for Nimbus Rail. Answer the user's
question using ONLY the context the system has retrieved for you from Nimbus Rail's
knowledge base (it appears in the system prompt). If that context does not contain
the answer, say plainly "I don't know" — never invent facts that are not in the
retrieved context.`

// runRAG exercises aisotropy's REAL native RAG over its OWN tiered memory. Because
// aisotropy's semantic-RAG hydration lives on the HTTP retrieval routes (the
// in-process App.Turn / ChatCore deliberately wires NO semantic store — see
// internal/api chat.go), this mode boots App.Run and drives those real routes:
//
//  1. INGEST the 22-doc Nimbus-Rail corpus via POST /v1/.../knowledge — aisotropy
//     chunks + embeds each doc through the REAL e5 server and stores KindDocument
//     vectors in the REAL Qdrant collection, scoped to (arena, live). Idempotent.
//  2. ANSWER via POST /v1/.../ask, whose handler hydrates the system prompt
//     through aisotropy's REAL semantic retrieval (semStore + embedder + reranker)
//     — the genuine tiered-memory RAG path — then calls agnes.
//  3. REPORT `retrieved` by mirroring aisotropy's own retrieval: embed the query
//     with the same e5 model id and search the same Qdrant collection with the
//     same (tenant, user, kind=document) payload filter.
func runRAG(req *request, out *result) error {
	question, err := lastUserMessage(req.Messages)
	if err != nil {
		return err
	}
	topK := req.TopK
	if topK <= 0 {
		topK = defaultRAGTopK
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	srv, err := bootServer(agnes, ragPersona)
	if err != nil {
		return err
	}
	defer srv.stop()

	docs, _, err := loadCorpus()
	if err != nil {
		return err
	}

	// 1) Ingest the corpus through aisotropy's real HTTP knowledge route.
	knowledgeDocs := make([]map[string]string, 0, len(docs))
	for _, d := range docs {
		knowledgeDocs = append(knowledgeDocs, map[string]string{"id": d.ID, "content": d.Content, "source": d.Source})
	}
	kBody, _ := json.Marshal(map[string]any{"documents": knowledgeDocs})
	kURL := fmt.Sprintf("%s/v1/tenant/%s/user/%s/knowledge", srv.baseURL, arenaTenant, arenaUser)
	var kResp struct {
		ChunksIngested int `json:"chunks_ingested"`
	}
	if err := postJSON(90*time.Second, kURL, kBody, &kResp); err != nil {
		return fmt.Errorf("ingesting corpus via /knowledge: %w", err)
	}
	logf("rag: ingested %d chunks from %d corpus docs via /knowledge", kResp.ChunksIngested, len(docs))

	// Mirror aisotropy's retrieval for the `retrieved` field (best-effort).
	retCtx, cancelRet := context.WithTimeout(context.Background(), 40*time.Second)
	if vec, verr := embedQuery(retCtx, question); verr == nil {
		if hits, serr := qdrantSearch(retCtx, vec, arenaTenant, arenaUser, topK); serr == nil {
			out.Retrieved = hits
			logf("rag: mirrored aisotropy retrieval -> %d passages (top=%v)", len(hits), topOf(hits))
		} else {
			logf("rag: retrieval mirror search failed (reporting only): %v", serr)
		}
	} else {
		logf("rag: retrieval mirror embed failed (reporting only): %v", verr)
	}
	cancelRet()

	// 2) Answer via the real semantic-hydrated /ask route.
	aBody, _ := json.Marshal(map[string]any{"prompt": question, "product_id": productID})
	aURL := fmt.Sprintf("%s/v1/tenant/%s/user/%s/ask", srv.baseURL, arenaTenant, arenaUser)
	var aResp struct {
		Reply    string `json:"reply"`
		TenantID string `json:"tenant_id"`
		Cached   bool   `json:"cached"`
	}
	start := time.Now()
	if err := postJSON(110*time.Second, aURL, aBody, &aResp); err != nil {
		setUsage(out, agnes)
		return fmt.Errorf("answering via /ask: %w", err)
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	out.Reply = aResp.Reply
	setUsage(out, agnes)
	return nil
}

func topOf(hits []retrievedDoc) string {
	if len(hits) == 0 {
		return ""
	}
	return hits[0].DocID
}

// postJSON POSTs body to url and decodes the JSON response into v.
func postJSON(timeout time.Duration, url string, body []byte, v any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: timeout + 5*time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d for %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
