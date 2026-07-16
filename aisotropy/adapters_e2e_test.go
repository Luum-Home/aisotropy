package aisotropy

// adapters_e2e_test.go is the Phase-3 "demonstrable artifact" for the two
// frontend-UI protocol adapters (internal/api/aisdk.go, internal/api/agui.go):
// a BLACK-BOX test that drives the real public aisotropy.App HTTP surface and
// proves each adapter route, mounted alongside /chat/stream in App.Run,
//
//   1. speaks the correct wire protocol (headers + ordered SSE frame sequence),
//   2. reconstructs the assistant reply from its incremental deltas, and
//   3. preserves multi-tenant isolation END-TO-END THROUGH THE ADAPTER — a
//      second tenant reading through the same protocol never sees the first
//      tenant's data.
//
// It shares the isolation_e2e_test.go harness (startIsolationApp boots the App
// with the credential-less stub LLM, NoAuth, and an effectively unlimited rate
// limit on a free loopback port) and the same leak-probe mechanic: the stub's
// streamed reply echoes the hydrated system prompt verbatim (buildStubWords in
// internal/llm/stub.go emits `system=%q`), and hydrateSystemPrompt is exactly
// where a tenant's working-memory/facts/semantic content lands. So a marker
// that is present in tenant A's SEEDED memory but ABSENT from tenant B's
// *request* can only appear in B's reconstructed reply via a cross-tenant leak.
// Because the adapters run the SAME StreamCore pipeline as /chat/stream, these
// tests confirm the costume does not tear the isolation gate: the identity that
// seeds via the native /chat route is the identity that reads back through the
// adapter, and no other identity can.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// aisdkURL / aguiURL build the two new per-(tenant,user) adapter route paths,
// mirroring chatURL/streamURL in isolation_e2e_test.go.
func aisdkURL(base, tenant, user string) string {
	return fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/aisdk", base, tenant, user)
}
func aguiURL(base, tenant, user string) string {
	return fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/agui", base, tenant, user)
}

// sseFrame is a decode target wide enough to hold every field either protocol
// carries in a `data: {json}` frame. Both the AI SDK UI Message Stream and the
// AG-UI event stream put their discriminator in the JSON `type` field, so one
// struct parses both — a given frame only populates the subset of fields its
// event uses (the rest stay zero).
type sseFrame struct {
	Type string `json:"type"`
	// AI SDK text parts + AG-UI content share these.
	ID    string `json:"id"`
	Delta string `json:"delta"`
	// AG-UI run/message correlation + roles.
	ThreadID  string `json:"threadId"`
	RunID     string `json:"runId"`
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
	// Terminal error payloads (AI SDK `error` vs AG-UI RUN_ERROR).
	ErrorText string `json:"errorText"`
	Message   string `json:"message"`
}

// parseSSE splits a raw SSE body into its ordered `data:` payloads. Each frame
// is `data: <payload>\n\n`; a payload that JSON-decodes becomes an sseFrame in
// frames (in wire order), and the literal `[DONE]` sentinel (AI SDK stream
// terminator, which is NOT JSON) flips doneSeen instead. A payload that is
// neither valid JSON nor the sentinel fails the test — the wire must be
// well-formed. types is the frame `type` sequence, for order assertions.
func parseSSE(t *testing.T, raw string) (frames []sseFrame, types []string, doneSeen bool) {
	t.Helper()
	for _, block := range strings.Split(raw, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		payload, ok := strings.CutPrefix(block, "data: ")
		if !ok {
			t.Fatalf("SSE frame missing %q prefix: %q", "data: ", block)
		}
		if payload == "[DONE]" {
			doneSeen = true
			continue
		}
		var f sseFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			t.Fatalf("SSE frame is not valid JSON (%v): %q", err, payload)
		}
		frames = append(frames, f)
		types = append(types, f.Type)
	}
	return frames, types, doneSeen
}

// postSSE drives an adapter route with a JSON body and an SSE Accept header,
// reading the whole streamed body to completion (the producer closes the stream
// on the server side). Returns the HTTP status, the response headers (so the
// protocol markers can be asserted), and the raw SSE body.
func postSSE(t *testing.T, url string, body []byte) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body) // blocks until the producer closes the stream
	if err != nil {
		t.Fatalf("read SSE body: %v", err)
	}
	return resp.StatusCode, resp.Header, string(raw)
}

// aisdkBody builds a Vercel AI SDK `useChat` request envelope: a single latest
// user message whose text is the prompt, plus the top-level chat `id` that the
// adapter maps onto the working-memory session key (so a read hits the same
// buffer a native /chat write under the same session seeded).
func aisdkBody(t *testing.T, session, prompt string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"id": session,
		"messages": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"type": "text", "text": prompt}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal aisdk body: %v", err)
	}
	return b
}

// aguiBody builds an AG-UI RunAgentInput: threadId (mapped onto the session
// key), a runId (echoed back in RUN_STARTED/RUN_FINISHED), and a single latest
// user message in OpenAI string-content shape.
func aguiBody(t *testing.T, thread, run, prompt string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"threadId": thread,
		"runId":    run,
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		t.Fatalf("marshal agui body: %v", err)
	}
	return b
}

// reconstruct concatenates the Delta of every frame of the given type, in wire
// order — the client-side operation that turns the incremental stream back into
// the full assistant reply.
func reconstruct(frames []sseFrame, deltaType string) string {
	var sb strings.Builder
	for _, f := range frames {
		if f.Type == deltaType {
			sb.WriteString(f.Delta)
		}
	}
	return sb.String()
}

// TestAISDKAdapterEndToEnd drives the mounted /chat/aisdk route over real HTTP
// and asserts the Vercel AI SDK UI Message Stream contract: the version header,
// the ordered part choreography (start -> text-start -> text-delta+ -> text-end
// -> finish), the literal [DONE] terminator, and that the reconstructed reply
// (delta concatenation) is the same stub echo the native pipeline produces —
// proving the adapter is a costume over StreamCore, not a divergent handler.
func TestAISDKAdapterEndToEnd(t *testing.T) {
	base := startIsolationApp(t)
	const session = "aisdk-e2e-session"
	const prompt = "hello over the ai sdk wire"

	status, hdr, raw := postSSE(t, aisdkURL(base, "tenantA", "userA"), aisdkBody(t, session, prompt))
	if status != http.StatusOK {
		t.Fatalf("aisdk stream: status %d, body %q", status, raw)
	}

	// Protocol headers: SSE media type + the AI SDK stream-selector marker.
	if ct := hdr.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want %q", ct, "text/event-stream")
	}
	if v := hdr.Get("x-vercel-ai-ui-message-stream"); v != "v1" {
		t.Errorf("x-vercel-ai-ui-message-stream: got %q, want %q", v, "v1")
	}

	frames, types, doneSeen := parseSSE(t, raw)
	if !doneSeen {
		t.Errorf("AI SDK stream did not end with the [DONE] sentinel")
	}

	// Frame order: exactly start, text-start, then one or more text-delta, then
	// text-end, finish. Assert the fixed head and tail and that the middle is
	// non-empty and homogeneous (all text-delta), which is deterministic for a
	// text-only turn regardless of how many word-tokens the stub emits.
	if len(types) < 5 {
		t.Fatalf("AI SDK stream too short: %v", types)
	}
	if types[0] != "start" || types[1] != "text-start" {
		t.Fatalf("AI SDK stream head: got %v, want [start text-start ...]", types[:2])
	}
	tail := types[len(types)-2:]
	if tail[0] != "text-end" || tail[1] != "finish" {
		t.Fatalf("AI SDK stream tail: got %v, want [... text-end finish]", tail)
	}
	for _, ty := range types[2 : len(types)-2] {
		if ty != "text-delta" {
			t.Fatalf("AI SDK stream body frame %q is not a text-delta; full order: %v", ty, types)
		}
	}
	if len(types)-4 < 1 {
		t.Fatalf("AI SDK stream carried no text-delta frames: %v", types)
	}

	// The single text block uses the stable id "0" across start/delta/end.
	for _, f := range frames {
		switch f.Type {
		case "text-start", "text-delta", "text-end":
			if f.ID != "0" {
				t.Errorf("AI SDK %s frame carried id %q, want %q", f.Type, f.ID, "0")
			}
		}
	}

	// The reconstructed reply is the stub echo, which folds the prompt back in.
	reply := reconstruct(frames, "text-delta")
	if !strings.Contains(reply, prompt) {
		t.Errorf("reconstructed AI SDK reply did not echo the prompt %q; got %q", prompt, reply)
	}
}

// TestAGUIAdapterEndToEnd drives the mounted /chat/agui route over real HTTP and
// asserts the AG-UI event contract: the SSE media type, the ordered event
// sequence (RUN_STARTED -> TEXT_MESSAGE_START -> TEXT_MESSAGE_CONTENT+ ->
// TEXT_MESSAGE_END -> RUN_FINISHED), threadId/runId echo, a stable messageId
// across the message frames, and that the reconstructed reply matches the stub
// echo — the same StreamCore output the AI SDK adapter and /chat/stream produce.
func TestAGUIAdapterEndToEnd(t *testing.T) {
	base := startIsolationApp(t)
	const thread = "agui-e2e-thread"
	const run = "agui-e2e-run"
	const prompt = "hello over the ag-ui wire"

	status, hdr, raw := postSSE(t, aguiURL(base, "tenantA", "userA"), aguiBody(t, thread, run, prompt))
	if status != http.StatusOK {
		t.Fatalf("agui stream: status %d, body %q", status, raw)
	}
	if ct := hdr.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want %q", ct, "text/event-stream")
	}

	frames, types, _ := parseSSE(t, raw)
	if len(types) < 5 {
		t.Fatalf("AG-UI stream too short: %v", types)
	}
	if types[0] != "RUN_STARTED" || types[1] != "TEXT_MESSAGE_START" {
		t.Fatalf("AG-UI stream head: got %v, want [RUN_STARTED TEXT_MESSAGE_START ...]", types[:2])
	}
	tail := types[len(types)-2:]
	if tail[0] != "TEXT_MESSAGE_END" || tail[1] != "RUN_FINISHED" {
		t.Fatalf("AG-UI stream tail: got %v, want [... TEXT_MESSAGE_END RUN_FINISHED]", tail)
	}
	for _, ty := range types[2 : len(types)-2] {
		if ty != "TEXT_MESSAGE_CONTENT" {
			t.Fatalf("AG-UI stream body frame %q is not a TEXT_MESSAGE_CONTENT; full order: %v", ty, types)
		}
	}
	if len(types)-4 < 1 {
		t.Fatalf("AG-UI stream carried no TEXT_MESSAGE_CONTENT frames: %v", types)
	}

	// threadId/runId echo verbatim in the run lifecycle frames.
	for _, f := range frames {
		if f.Type == "RUN_STARTED" || f.Type == "RUN_FINISHED" {
			if f.ThreadID != thread || f.RunID != run {
				t.Errorf("%s did not echo thread/run: got thread=%q run=%q, want %q/%q", f.Type, f.ThreadID, f.RunID, thread, run)
			}
		}
	}

	// A single, stable, non-empty messageId ties START/CONTENT/END together.
	var messageID string
	for _, f := range frames {
		switch f.Type {
		case "TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END":
			if f.MessageID == "" {
				t.Errorf("AG-UI %s frame carried an empty messageId", f.Type)
			}
			if messageID == "" {
				messageID = f.MessageID
			} else if f.MessageID != messageID {
				t.Errorf("AG-UI messageId not stable: %s frame had %q, want %q", f.Type, f.MessageID, messageID)
			}
		}
	}
	// The TEXT_MESSAGE_START role is "assistant".
	for _, f := range frames {
		if f.Type == "TEXT_MESSAGE_START" && f.Role != "assistant" {
			t.Errorf("TEXT_MESSAGE_START role: got %q, want %q", f.Role, "assistant")
		}
	}

	reply := reconstruct(frames, "TEXT_MESSAGE_CONTENT")
	if !strings.Contains(reply, prompt) {
		t.Errorf("reconstructed AG-UI reply did not echo the prompt %q; got %q", prompt, reply)
	}
}

// TestAISDKAdapterTenantIsolationEndToEnd proves the AI SDK adapter preserves
// tenant isolation END TO END: tenant A seeds a secret into its working memory
// via the native /chat route, reads it back through the AI SDK adapter (positive
// control), and NO other identity reading through the same adapter — including
// same-tenant/different-user and different-tenant/same-user — can reconstruct
// that secret. Because the adapter drives the same StreamCore over the same
// per-(tenant,user,session) keyed stores, a leak here would be a leak in the
// native pipeline too; this asserts the costume adds none.
func TestAISDKAdapterTenantIsolationEndToEnd(t *testing.T) {
	base := startIsolationApp(t)
	const session = "aisdk-iso-session" // deliberately reused across tenants
	const neutralRead = "please give a status update"
	secretA := marker(t, "aisdkalpha")

	// Seed tenantA/userA's working memory via the NATIVE writer route.
	if st, _ := postChat(t, base, "tenantA", "userA", session, "Store this note verbatim: "+secretA); st != http.StatusOK {
		t.Fatalf("seed write for tenantA: status %d", st)
	}

	// POSITIVE CONTROL: reading through the AI SDK adapter as the owner hydrates
	// secretA into the reconstructed reply — proving the probe exercises
	// hydration, so the negative assertions are not vacuous.
	readAISDK := func(tenant, user string) (int, string) {
		st, _, raw := postSSE(t, aisdkURL(base, tenant, user), aisdkBody(t, session, neutralRead))
		if st != http.StatusOK {
			return st, ""
		}
		frames, _, _ := parseSSE(t, raw)
		return st, reconstruct(frames, "text-delta")
	}

	st, ownReply := readAISDK("tenantA", "userA")
	if st != http.StatusOK {
		t.Fatalf("positive-control aisdk read for tenantA: status %d", st)
	}
	if !strings.Contains(ownReply, secretA) {
		t.Fatalf("positive control failed: tenantA reading its own memory through the AI SDK adapter did not surface %q; the isolation probe is not exercising hydration", secretA)
	}

	// NEGATIVE: every other identity reading the SAME session id through the
	// adapter must NOT reconstruct secretA.
	for _, id := range []struct{ tenant, user string }{
		{"tenantB", "userB"},
		{"tenantA", "userB"},
		{"tenantB", "userA"},
	} {
		st, reply := readAISDK(id.tenant, id.user)
		if st != http.StatusOK {
			t.Errorf("aisdk read for %s/%s: status %d", id.tenant, id.user, st)
			continue
		}
		if strings.Contains(reply, secretA) {
			t.Errorf("CROSS-TENANT LEAK THROUGH AI SDK ADAPTER: %s/%s reading session %q reconstructed tenantA/userA's secret %q", id.tenant, id.user, session, secretA)
		}
	}
}

// TestAGUIAdapterTenantIsolationEndToEnd is the AG-UI twin of the isolation
// probe above: tenantA seeds via the native route, reads its secret back through
// the AG-UI adapter (positive control), and no other identity reconstructing the
// AG-UI TEXT_MESSAGE_CONTENT stream can see it.
func TestAGUIAdapterTenantIsolationEndToEnd(t *testing.T) {
	base := startIsolationApp(t)
	const thread = "agui-iso-thread" // reused across tenants (the session key)
	const run = "agui-iso-run"
	const neutralRead = "please give a status update"
	secretA := marker(t, "aguialpha")

	if st, _ := postChat(t, base, "tenantA", "userA", thread, "Store this note verbatim: "+secretA); st != http.StatusOK {
		t.Fatalf("seed write for tenantA: status %d", st)
	}

	readAGUI := func(tenant, user string) (int, string) {
		st, _, raw := postSSE(t, aguiURL(base, tenant, user), aguiBody(t, thread, run, neutralRead))
		if st != http.StatusOK {
			return st, ""
		}
		frames, _, _ := parseSSE(t, raw)
		return st, reconstruct(frames, "TEXT_MESSAGE_CONTENT")
	}

	st, ownReply := readAGUI("tenantA", "userA")
	if st != http.StatusOK {
		t.Fatalf("positive-control agui read for tenantA: status %d", st)
	}
	if !strings.Contains(ownReply, secretA) {
		t.Fatalf("positive control failed: tenantA reading its own memory through the AG-UI adapter did not surface %q; the isolation probe is not exercising hydration", secretA)
	}

	for _, id := range []struct{ tenant, user string }{
		{"tenantB", "userB"},
		{"tenantA", "userB"},
		{"tenantB", "userA"},
	} {
		st, reply := readAGUI(id.tenant, id.user)
		if st != http.StatusOK {
			t.Errorf("agui read for %s/%s: status %d", id.tenant, id.user, st)
			continue
		}
		if strings.Contains(reply, secretA) {
			t.Errorf("CROSS-TENANT LEAK THROUGH AG-UI ADAPTER: %s/%s reading thread %q reconstructed tenantA/userA's secret %q", id.tenant, id.user, thread, secretA)
		}
	}
}

// TestAdapterMalformedRequestsAre4xx confirms both adapters reject a request
// that carries no user message with a clean HTTP 4xx BEFORE any SSE stream
// opens — the pre-stream validation contract (a bad request is a status code, a
// pipeline failure is an in-band error frame). A 2xx here would mean the adapter
// opened a stream for an empty turn.
func TestAdapterMalformedRequestsAre4xx(t *testing.T) {
	base := startIsolationApp(t)

	// AI SDK: an envelope with no user message.
	emptyAISDK, err := json.Marshal(map[string]any{"id": "s", "messages": []map[string]any{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if st, _, body := postSSE(t, aisdkURL(base, "tenantA", "userA"), emptyAISDK); st < 400 || st >= 500 {
		t.Errorf("AI SDK empty-messages request: got status %d (body %q), want 4xx", st, body)
	}

	// AG-UI: a RunAgentInput with no user message.
	emptyAGUI, err := json.Marshal(map[string]any{"threadId": "t", "runId": "r", "messages": []map[string]any{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if st, _, body := postSSE(t, aguiURL(base, "tenantA", "userA"), emptyAGUI); st < 400 || st >= 500 {
		t.Errorf("AG-UI empty-messages request: got status %d (body %q), want 4xx", st, body)
	}

	// AG-UI: genuinely malformed JSON is also a pre-stream 4xx.
	if st, _, body := postSSE(t, aguiURL(base, "tenantA", "userA"), []byte("{not json")); st < 400 || st >= 500 {
		t.Errorf("AG-UI malformed JSON: got status %d (body %q), want 4xx", st, body)
	}
}
