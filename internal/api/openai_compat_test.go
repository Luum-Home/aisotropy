package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/auth"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// oaiTestSecret is an HS256 signing secret >= auth.MinHS256SecretBytes so the
// verifier does not trip its fail-closed short-secret guard.
var oaiTestSecret = []byte("openai-compat-test-secret-32-bytes!!")

// mintHS256 mints a minimal HS256 JWT (header.payload.signature, base64url, no
// padding) — the same wire format internal/auth's verifier consumes, built with
// only the standard library (mirrors the auth package's own test helper, which
// is unexported and so re-implemented here).
func mintHS256(t *testing.T, secret []byte, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// mintTenantJWT mints a valid HS256 JWT for tenant/user with a 1h expiry.
func mintTenantJWT(t *testing.T, tenant, user string) string {
	t.Helper()
	return mintHS256(t, oaiTestSecret, map[string]any{
		"tenant_id": tenant,
		"user_id":   user,
		"exp":       time.Now().Add(time.Hour).Unix(),
	})
}

// newOAIRouter builds a chi + humachi router with the REAL authn middleware
// (BindAuthMiddleware over the given authenticator) and the OpenAI-compat shim
// mounted — the same registration order aisotropy/app.go uses — plus the AG-UI
// route, so tests can prove the shim's identity posture differs from a normal
// path-identity route on the SAME server (the NoAuth case). All optional tiers
// are nil, exactly as StreamCore documents them to degrade.
func newOAIRouter(t *testing.T, authn auth.Authenticator, client llm.Client, wm *memory.WorkingMemory) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-oai-test", "0.1.0"))
	humaAPI.UseMiddleware(BindAuthMiddleware(humaAPI, authn))
	RegisterOpenAICompat(humaAPI, client, wm,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, 0, nil, nil, nil)
	RegisterAGUI(humaAPI, client, wm,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, 0, nil, nil, nil)
	return router
}

// oaiBody builds an OpenAI Chat Completions request body as a map, mirroring
// what an external client sends over the wire. extra fields are merged in so
// adversarial tests can smuggle body identity/params.
func oaiBody(t *testing.T, userText string, stream bool, extra map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"model":    "test-model",
		"stream":   stream,
		"messages": []map[string]any{{"role": "user", "content": userText}},
	}
	for k, v := range extra {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return b
}

// postOAI issues a POST /v1/chat/completions with an optional bearer token.
func postOAI(t *testing.T, handler http.Handler, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// pinOAIStream pins the completion id + created timestamp so wire bytes are
// deterministic for golden assertions; restored via t.Cleanup.
func pinOAIStream(t *testing.T) {
	t.Helper()
	origID, origNow := newOAICompletionID, oaiNow
	newOAICompletionID = func() string { return "chatcmpl-test1" }
	oaiNow = func() int64 { return 1700000000 }
	t.Cleanup(func() { newOAICompletionID, oaiNow = origID, origNow })
}

// parseOAIFrames splits a raw SSE body into its `data:` payload strings (the
// JSON chunks plus, possibly, the literal [DONE] sentinel).
func parseOAIFrames(t *testing.T, body []byte) []string {
	t.Helper()
	var frames []string
	for _, block := range strings.Split(string(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Fatalf("SSE frame missing 'data: ' prefix: %q", block)
		}
		frames = append(frames, strings.TrimPrefix(block, "data: "))
	}
	return frames
}

// TestOpenAICompat_NoJWT_401 is the first adversarial merge blocker: without a
// credential (or with garbage) the shim route is 401 — never served on path
// or body identity.
func TestOpenAICompat_NoJWT_401(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), llm.NewStubClient(), wm)

	if rec := postOAI(t, handler, "", oaiBody(t, "hi", true, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-credential status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if rec := postOAI(t, handler, "not-a-jwt", oaiBody(t, "hi", true, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage-token status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	// An expired token is equally rejected.
	expired := mintHS256(t, oaiTestSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice", "exp": time.Now().Add(-time.Hour).Unix(),
	})
	if rec := postOAI(t, handler, expired, oaiBody(t, "hi", true, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired-token status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
}

// TestOpenAICompat_ValidJWT_GoldenStream: with a valid JWT the stream works
// end-to-end against the stub pipeline, the identity bound into memory keys is
// the JWT's, and the wire bytes are the exact OpenAI chunk choreography —
// role+content chunk, content chunk, finish_reason:"stop" chunk, usage-only
// chunk (include_usage was set), data: [DONE].
func TestOpenAICompat_ValidJWT_GoldenStream(t *testing.T) {
	pinOAIStream(t)
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "Hello"},
		{Text: " world"},
		{Done: true, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2}},
	}}
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), stub, wm)

	body := oaiBody(t, "hi", true, map[string]any{
		"stream_options": map[string]any{"include_usage": true},
	})
	rec := postOAI(t, handler, mintTenantJWT(t, "acme", "alice"), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	frames := parseOAIFrames(t, rec.Body.Bytes())
	want := []string{
		`{"id":"chatcmpl-test1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-test1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-test1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-test1","object":"chat.completion.chunk","created":1700000000,"model":"test-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`[DONE]`,
	}
	if len(frames) != len(want) {
		t.Fatalf("got %d frames, want %d:\n%s", len(frames), len(want), rec.Body.String())
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Errorf("frame %d mismatch:\n got: %s\nwant: %s", i, frames[i], want[i])
		}
	}

	// The identity that reached the memory keys is the JWT's claims — the shim
	// used the default "openai" session (no session_id extension was sent).
	key := memory.Key{Tenant: "acme", User: "alice", Session: oaiDefaultSession}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 || got[0].Role != roleUser || got[0].Content != "hi" || got[1].Role != roleAssistant || got[1].Content != "Hello world" {
		t.Fatalf("persisted turn under JWT identity = %+v, want [user hi, assistant Hello world]", got)
	}
}

// TestOpenAICompat_NoUsageChunkWhenNotRequested: without
// stream_options.include_usage the usage-only chunk is omitted even though the
// provider reported usage.
func TestOpenAICompat_NoUsageChunkWhenNotRequested(t *testing.T) {
	pinOAIStream(t)
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "ok"},
		{Done: true, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}},
	}}
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), stub, wm)

	rec := postOAI(t, handler, mintTenantJWT(t, "acme", "alice"), oaiBody(t, "hi", true, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"usage"`) {
		t.Fatalf("usage chunk emitted without include_usage:\n%s", rec.Body.String())
	}
	frames := parseOAIFrames(t, rec.Body.Bytes())
	if frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", frames[len(frames)-1])
	}
}

// TestOpenAICompat_NoAuthServer_Still401 is the constitutional NoAuth blocker:
// on a server explicitly configured with NoAuth (path identity trusted), the
// shim route STILL 401s — NoAuth yields an empty unverified principal on a
// path-param-less route, so there is no identity to serve. The AG-UI route on
// the SAME server keeps working on path identity, proving the 401 is the
// shim's posture, not a broken server.
func TestOpenAICompat_NoAuthServer_Still401(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newOAIRouter(t, auth.NoAuth(), llm.NewStubClient(), wm)

	rec := postOAI(t, handler, "", oaiBody(t, "hi", true, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("NoAuth shim status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	// Even WITH a bearer token: NoAuth never verifies it, so the principal
	// stays empty/unverified and the route still refuses.
	rec = postOAI(t, handler, mintTenantJWT(t, "acme", "alice"), oaiBody(t, "hi", true, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("NoAuth shim (with token) status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}

	// A normal path-identity route on the same server still works under NoAuth.
	if rec := postAGUI(t, handler, "acme", "alice", aguiUserBody("t1", "r1", "hi")); rec.Code != http.StatusOK {
		t.Fatalf("NoAuth AG-UI status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

// TestOpenAICompat_BodyCannotInfluenceIdentity is the body-smuggling blocker:
// tenant/user/model supplied in the body never override the JWT identity — the
// turn lands in the JWT tenant's memory, the smuggled tenant's memory stays
// empty, and the echoed model is advisory only.
func TestOpenAICompat_BodyCannotInfluenceIdentity(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), llm.NewStubClient(), wm)

	body := oaiBody(t, "smuggle-test", true, map[string]any{
		"tenant_id":  "evil",
		"user_id":    "mallory",
		"tenant":     "evil",
		"user":       "mallory",
		"session_id": "s1",
	})
	rec := postOAI(t, handler, mintTenantJWT(t, "acme", "alice"), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The JWT identity is the one that reached the memory keys...
	acmeKey := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), acmeKey, 10)
	if err != nil {
		t.Fatalf("Recent (acme): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("JWT-tenant memory has %d messages, want 2: %+v", len(got), got)
	}
	// ...and the smuggled body identity reached nothing.
	for _, u := range []string{"mallory", "alice"} {
		evilKey := memory.Key{Tenant: "evil", User: u, Session: "s1"}
		evil, err := wm.Recent(context.Background(), evilKey, 10)
		if err != nil {
			t.Fatalf("Recent (evil/%s): %v", u, err)
		}
		if len(evil) != 0 {
			t.Fatalf("body-smuggled tenant 'evil' (user %s) received %d messages, want 0: %+v", u, len(evil), evil)
		}
	}
}

// TestOpenAICompat_RoundTripInRepoClient closes the loop the ADR designed for:
// this repo's OWN OpenAI-compat CLIENT (internal/llm/openai_compat.go — the
// same code that consumes OpenRouter/OpenAI) consumes the shim over a real
// HTTP server, with the tenant JWT passed AS the api_key. The client's
// stream_options.include_usage request must surface the usage the stub
// pipeline reported, and the assembled text must round-trip exactly.
func TestOpenAICompat_RoundTripInRepoClient(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "round"},
		{Text: "-trip"},
		{Done: true, Usage: &llm.Usage{InputTokens: 7, OutputTokens: 4}},
	}}
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), stub, wm)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := llm.NewOpenAICompatClient(mintTenantJWT(t, "acme", "alice"), "test-model", srv.URL+"/v1", "shim-roundtrip")
	tokens, err := client.StreamChat(context.Background(), "", "hello shim")
	if err != nil {
		t.Fatalf("StreamChat against the shim: %v", err)
	}

	var sb strings.Builder
	var usage *llm.Usage
	for tok := range tokens {
		if tok.Err != nil {
			t.Fatalf("client surfaced stream error: %v", tok.Err)
		}
		if tok.Done {
			usage = tok.Usage
			break
		}
		sb.WriteString(tok.Text)
	}
	if sb.String() != "round-trip" {
		t.Errorf("round-tripped text = %q, want %q", sb.String(), "round-trip")
	}
	if usage == nil || usage.InputTokens != 7 || usage.OutputTokens != 4 {
		t.Errorf("round-tripped usage = %+v, want {7 4}", usage)
	}

	// And a bad api_key over the same real server is a client-visible 401.
	badClient := llm.NewOpenAICompatClient("not-a-jwt", "test-model", srv.URL+"/v1", "shim-bad-key")
	if _, err := badClient.StreamChat(context.Background(), "", "hello"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad api_key error = %v, want a 401 status error", err)
	}
}

// TestOpenAICompat_NonStreaming: stream:false drives the SAME pipeline with a
// buffering sink and returns one chat.completion object with usage.
func TestOpenAICompat_NonStreaming(t *testing.T) {
	pinOAIStream(t)
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "full "},
		{Text: "reply"},
		{Done: true, Usage: &llm.Usage{InputTokens: 5, OutputTokens: 2}},
	}}
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), stub, wm)

	rec := postOAI(t, handler, mintTenantJWT(t, "acme", "alice"), oaiBody(t, "hi", false, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var out oaiCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode completion: %v; body = %s", err, rec.Body.String())
	}
	if out.ID != "chatcmpl-test1" || out.Object != oaiObjectCompletion || out.Created != 1700000000 || out.Model != "test-model" {
		t.Errorf("completion envelope = %+v, want pinned id/object/created/model", out)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Role != roleAssistant || out.Choices[0].Message.Content != "full reply" || out.Choices[0].FinishReason != oaiFinishStop {
		t.Errorf("completion choice = %+v, want assistant %q finish_reason stop", out.Choices, "full reply")
	}
	if out.Usage == nil || out.Usage.PromptTokens != 5 || out.Usage.CompletionTokens != 2 || out.Usage.TotalTokens != 7 {
		t.Errorf("completion usage = %+v, want {5 2 7}", out.Usage)
	}
}

// TestOpenAICompat_BadRequestsAreOpenAIEnveloped: malformed JSON and
// user-message-less bodies are rejected with a real 400 whose body is the
// OpenAI error envelope {"error":{message,type}} — never a 200 + stream.
func TestOpenAICompat_BadRequestsAreOpenAIEnveloped(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), llm.NewStubClient(), wm)
	token := mintTenantJWT(t, "acme", "alice")

	cases := []struct {
		name string
		body []byte
		// wantEnvelope: handler-produced rejections carry the OpenAI error
		// envelope. The empty-body case is rejected by Huma ITSELF ("request
		// body is required", RFC-7807 shape) before the handler runs — still a
		// real 400 and never a stream, but not the OpenAI envelope; a stock
		// OpenAI SDK never sends an empty body.
		wantEnvelope bool
	}{
		{"malformed json", []byte("{not json"), true},
		{"empty body", []byte(""), false},
		{"no user message", []byte(`{"model":"m","messages":[{"role":"assistant","content":"prior"}]}`), true},
		{"blank user message", oaiBody(t, "   ", true, nil), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postOAI(t, handler, token, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if !tc.wantEnvelope {
				return
			}
			var envelope struct {
				Error *oaiErrorBody `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Error == nil {
				t.Fatalf("body is not an OpenAI error envelope: %s (err=%v)", rec.Body.String(), err)
			}
			if envelope.Error.Type != oaiErrTypeInvalidRequest || envelope.Error.Message == "" {
				t.Errorf("error = %+v, want type %q with a message", envelope.Error, oaiErrTypeInvalidRequest)
			}
		})
	}
}

// TestOpenAICompat_MidStreamErrorFrameNoDone: a mid-stream pipeline failure is
// surfaced as a data: {"error":{...}} frame and the stream closes WITHOUT the
// [DONE] terminator, so a client never mistakes a truncated stream for success.
func TestOpenAICompat_MidStreamErrorFrameNoDone(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "par"},
		{Err: errors.New("boom-upstream-failure")},
	}}
	handler := newOAIRouter(t, auth.NewJWTBearer(oaiTestSecret), stub, wm)

	rec := postOAI(t, handler, mintTenantJWT(t, "acme", "alice"), oaiBody(t, "hi", true, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	frames := parseOAIFrames(t, rec.Body.Bytes())
	if len(frames) == 0 {
		t.Fatal("expected at least an error frame")
	}
	last := frames[len(frames)-1]
	if last == "[DONE]" {
		t.Fatalf("stream terminated with [DONE] despite a mid-stream error:\n%s", rec.Body.String())
	}
	var frame oaiErrorFrame
	if err := json.Unmarshal([]byte(last), &frame); err != nil {
		t.Fatalf("last frame is not an error envelope: %q (%v)", last, err)
	}
	if !strings.Contains(frame.Error.Message, "boom-upstream-failure") {
		t.Errorf("error frame message = %q, want it to carry the upstream error", frame.Error.Message)
	}
}

// TestOpenAICompat_HandlerWithoutMiddlewareFailsClosed: a wiring that mounts
// the route WITHOUT the authn middleware (as a unit test or a foreign
// composition root could) still refuses to serve — the handler's own
// defense-in-depth principal check 401s rather than inventing an identity.
func TestOpenAICompat_HandlerWithoutMiddlewareFailsClosed(t *testing.T) {
	wm := newTestWorkingMemory(t)
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-oai-nomw-test", "0.1.0"))
	// Deliberately NO UseMiddleware here.
	RegisterOpenAICompat(humaAPI, llm.NewStubClient(), wm,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, 0, nil, nil, nil)

	rec := postOAI(t, router, mintTenantJWT(t, "acme", "alice"), oaiBody(t, "hi", true, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("middleware-less shim status = %d, want 401 (fail closed); body = %s", rec.Code, rec.Body.String())
	}
}
