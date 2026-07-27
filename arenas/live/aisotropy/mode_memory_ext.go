// mode_memory_ext.go — the OPT-IN memory-EXTENSION dimensions
// (memory_cross_session / memory_semantic_recall / memory_consolidation, see
// arenas/live/runner/tasks.go's buildMemoryExt and
// docs/architecture/arena-memory-extensions.md).
//
// These ride the wire as ordinary mode:"memory" invocations, BUT they VARY the
// session_id across a task's invocations while HOLDING a per-task user_id
// constant (plain `memory` sends no user_id and reuses ONE session). That is the
// signal this file keys on: a mode:"memory" request that carries a non-empty
// user_id is a memory-EXTENSION turn (main.go's dispatch routes it here);
// user_id-less `memory` stays on the original runMemory working-memory path.
//
// WHY A DISTINCT PATH (the bug this fixes): the previous runMemory drove EVERY
// persist turn through App.Turn — one real agnes generation per user message. A
// memory_semantic_recall task's persist invocation carries ~12 user turns in ONE
// adapter process, so that was ~12 sequential model calls. Under real agnes
// latency (observed 7-27s/turn) the cumulative burst reliably crossed
// runMemory's 110s per-turn ctx timeout; the fail-closed reflection loop then
// returned `ctx.Err() != nil` and the adapter reported
// "agent failed to produce a valid structured reply after 0 attempt(s)"
// (attempts==0 is the ctx-cancel branch of internal/llm.ReflectAuto) — the
// ERRORS the memory-ext calibration saw on semantic + consolidation. It was also
// a MISS on paraphrased recalls: App.Turn (aisotropy/turn.go) maps onto the
// NON-streaming ChatCore, whose chatDeps wire NO semantic store (internal/api/
// chat.go's chatBuildSystemPrompt passes nil semStore) — so cross-session recall
// could only hit the rqlite facts tier's FTS5 KEYWORD search, which cannot bridge
// a paraphrase ("caterer must avoid" vs the stated "shellfish allergy").
//
// THE HONEST MECHANISM (aisotropy's OWN user-scoped tiered memory, no scorer
// trick): the memory-extension dimensions test durable, cross-SESSION recall of a
// fact stated earlier — exactly what aisotropy's L3 long-term associative tier
// (the user-scoped Qdrant semantic memory) is for. So:
//
//   - PERSIST invocation (the one buildMemoryExt stamps with the task System):
//     write every stated user turn into that tier via App.IngestKnowledge
//     (real e5 embed -> real Qdrant, KindDocument, scoped BY CONSTRUCTION to
//     (tenant, user) — semantic.NewScope, an in-scan payload filter, not an
//     adapter-side filter). ZERO model calls: the fact is durable the moment the
//     upsert returns, so there is no burst to time out. Qdrant is a NETWORKED
//     tier, so the write survives the process boundary the recall crosses.
//   - RECALL / consolidate invocation (no System): answer the final user turn
//     through aisotropy's REAL semantic-hydrated streaming route
//     (POST /v1/.../chat/stream — RegisterStream wires semStore+embedder+reranker,
//     internal/api/chat_stream.go), under the SAME (tenant, user) and the fresh
//     recall session. The route's searchSemanticContext vector-searches the
//     user-scoped KindDocument points the persist invocation wrote, folds the
//     top matches into the system prompt, and agnes answers from them — bridging
//     both exact-keyword AND paraphrased recall. A memory_consolidation task's
//     synthetic "consolidate" directive turn is a no-op trigger for aisotropy
//     (IngestKnowledge already made the fact durable), answered and discarded
//     like any non-final turn.
//
// This uses only public façade seams a third-party memory bot would use
// (App.IngestKnowledge + POST /chat/stream), keeps aisotropy's by-construction
// user isolation intact (the fact is user-scoped; a different user's recall never
// matches it), and pays one model call per RECALL/consolidate invocation and none
// per persist — a strict reduction from the old per-turn generation.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"strings"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// httpUserIDPattern is the EXACT constraint aisotropy's HTTP path params enforce
// on user_id (internal/api/chat.go: `pattern:"^[a-zA-Z0-9_-]{1,64}$"`). The
// in-process seams this mode also uses (App.IngestKnowledge -> semantic.NewScope,
// App.Turn -> memory.Key.Validate) enforce only the reserved-char + non-empty
// rule and NOT this 64-char cap, so a long user_id is accepted in-process but
// REJECTED with HTTP 422 by the /chat/stream route. The memory-ext runner mints
// per-task user_ids ("u-<mode>-<taskID>-<runNonce>") that exceed 64 chars for the
// longer task names, so the recall (HTTP) invocation 422'd while persist
// (in-process) silently accepted them. boundedUser reconciles the two by mapping
// any identity that would fail the HTTP pattern to a stable, pattern-safe token —
// applied to BOTH the persist scope and the recall path so they always agree.
var httpUserIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// runMemoryExt dispatches a memory-EXTENSION invocation to its persist or
// recall half. The split signal is req.System: buildMemoryExt (runner) carries
// the task's system prompt ONLY on the persist invocation; the recall and
// consolidate invocations send none. A misclassification is correctness-safe:
// the worst case is a spurious 1-call stream answer that is discarded, never a
// wrong scored reply.
func runMemoryExt(req *request, out *result) error {
	if strings.TrimSpace(req.UserID) == "" {
		return fmt.Errorf("memory-ext requires a non-empty user_id (the scope held across the varying session boundary)")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("memory-ext requires at least one message this turn")
	}
	// Bound the identity to aisotropy's HTTP user_id pattern BEFORE it is used as
	// either the persist scope or the recall path segment, so an over-long runner
	// user_id can never make persist (in-process, no cap) and recall (HTTP, 64-char
	// cap) disagree on the scope — the exact split that surfaced as a recall-only
	// 422. boundedUser is deterministic, so persist and recall derive the SAME
	// scope from the SAME req.UserID; isolation holds (distinct users -> distinct
	// tokens).
	user := boundedUser(req.UserID)

	agnes, err := newProviderClient()
	if err != nil {
		return err
	}

	if strings.TrimSpace(req.System) != "" {
		return persistMemoryExt(req, out, agnes, user)
	}
	return recallMemoryExt(req, out, agnes, user)
}

// persistMemoryExt writes every stated user turn into aisotropy's user-scoped
// semantic memory tier (App.IngestKnowledge -> real e5 -> real Qdrant,
// KindDocument), with NO model call. Idempotent under the runner's retry: each
// doc id is a content hash, so re-ingesting the same fact replaces its chunks
// rather than duplicating them.
func persistMemoryExt(req *request, out *result, agnes aisotropy.LLMClient, user string) error {
	app := newApp(agnes, effectiveMemorySystem(req.System))
	defer func() { _ = app.Close() }()

	turns := userMessages(req.Messages)
	if len(turns) == 0 {
		return fmt.Errorf("memory-ext persist requires at least one user message to remember")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ingested := 0
	for _, content := range turns {
		n, err := app.IngestKnowledge(ctx, arenaTenant, user, aisotropy.Document{
			ID:      memoryDocID(user, content),
			Content: content,
			Source:  "arena-memory-ext",
		})
		if err != nil {
			setUsage(out, app)
			return fmt.Errorf("memory-ext persist: IngestKnowledge failed for user %q: %w", user, err)
		}
		ingested += n
	}

	// No provider generation happened on this invocation, so App.Usage() is
	// zero and setUsage honestly reports -1/-1 (no provider tokens spent on
	// persist). The reply is not the scored one (recall is the final
	// invocation); surface a deterministic ack for observability.
	out.Reply = fmt.Sprintf("persisted %d user turn(s) into semantic memory (%d chunk(s)) for user %q", len(turns), ingested, user)
	setUsage(out, app)
	logf("memory-ext persist: ingested %d user turn(s) (%d chunk(s)) under user=%q into the semantic memory tier (no model call)", len(turns), ingested, user)
	return nil
}

// recallMemoryExt answers the final user turn through aisotropy's real
// semantic-hydrated streaming route, so the KindDocument facts the persist
// invocation wrote (user-scoped) are vector-retrieved and folded into the
// answer. The recall session is fresh (session B) by design; only the
// user-scoped semantic tier — not session working memory — can carry the fact
// across, which is exactly what this dimension measures.
func recallMemoryExt(req *request, out *result, agnes aisotropy.LLMClient, user string) error {
	query, err := lastUserMessage(req.Messages)
	if err != nil {
		return fmt.Errorf("memory-ext recall requires a user message to ask: %w", err)
	}

	srv, err := bootServer(agnes, effectiveMemorySystem(req.System))
	if err != nil {
		return err
	}
	defer srv.stop()

	reply, ttft, err := streamOneTurn(srv.baseURL, user, req.SessionID, query)
	if err != nil {
		setUsage(out, srv.app)
		return fmt.Errorf("memory-ext recall via /chat/stream (user=%q session=%q): %w", user, req.SessionID, err)
	}
	out.Reply = reply
	out.TTFTMs = ttft
	setUsage(out, srv.app)
	logf("memory-ext recall: streamed answer for user=%q session=%q (ttft=%dms, %d chars)", user, req.SessionID, ttft, len(reply))
	return nil
}

// boundedUser returns user unchanged when it already satisfies aisotropy's HTTP
// user_id pattern (httpUserIDPattern), else a stable, pattern-safe surrogate
// ("uhx" + a 64-bit FNV of the original, 19 chars, [a-z0-9] only). The surrogate
// is deterministic — the same input always maps to the same token, so a task's
// persist and recall invocations resolve to ONE scope — and collision-resistant,
// so two distinct runner user_ids never alias into the same memory scope
// (isolation is preserved). The "uhx" prefix has no hyphen, so it cannot collide
// with a raw runner user (which always contains hyphens).
func boundedUser(user string) string {
	if httpUserIDPattern.MatchString(user) {
		return user
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(user))
	return fmt.Sprintf("uhx%016x", h.Sum64())
}

// memoryDocID derives a stable, per-(user,content) KindDocument id so a retried
// persist invocation re-ingests idempotently instead of duplicating the fact.
func memoryDocID(user, content string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(user))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(content))
	return fmt.Sprintf("mem-ext-%016x", h.Sum64())
}

// streamOneTurn POSTs one prompt to aisotropy's real SSE chat/stream route under
// (arenaTenant, user, session) and concatenates the "token" events into the
// reply, timing the first. It mirrors runStream (mode_stream.go) but takes an
// explicit user_id so the memory-ext scope (user held across sessions) travels
// the real identity path — the memory tiers scope every read from it.
func streamOneTurn(baseURL, user, session, prompt string) (string, int64, error) {
	body, _ := json.Marshal(map[string]any{
		"prompt":     prompt,
		"product_id": productID,
		"session_id": session,
	})
	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/stream", baseURL, arenaTenant, user)

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 115 * time.Second}).Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("POST /chat/stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("/chat/stream returned status %d", resp.StatusCode)
	}

	var (
		reply        strings.Builder
		ttft         int64
		gotFirst     bool
		currentEvent string
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if currentEvent == "token" {
				var ev struct {
					Text string `json:"text"`
				}
				if json.Unmarshal([]byte(data), &ev) == nil && ev.Text != "" {
					if !gotFirst {
						gotFirst = true
						ttft = time.Since(start).Milliseconds()
					}
					reply.WriteString(ev.Text)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", 0, fmt.Errorf("reading SSE stream: %w", err)
	}
	if !gotFirst {
		return "", 0, fmt.Errorf("/chat/stream produced no token events")
	}
	return reply.String(), ttft, nil
}
