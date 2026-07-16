package aisotropy

// example_test.go is the runnable godoc for the public aisotropy package: a set
// of Example functions that appear in godoc AND execute under `go test`, so the
// "how do I build an agent on top of Aisotropy" story is copy-paste code that is
// verified on every CI run rather than prose that can drift.
//
// App.Run serves HTTP and blocks (there is no in-process handler seam), so each
// example boots the App on a free loopback port in a goroutine and drives it over
// net/http — exactly the harness pattern isolation_e2e_test.go uses. Every
// example runs against the credential-less stub LLM (internal/llm.StubClient),
// so the output is deterministic and NO ANTHROPIC_API_KEY is required.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// Example is the minimal agent: build an App backed by the keyless stub LLM,
// boot it, and drive one non-streaming /chat turn — the "hello agent" in a
// handful of lines. In production you would drop WithLLM entirely (the default,
// llm.FromEnv(), uses your ANTHROPIC_API_KEY) and register a real Authenticator
// instead of NoAuth().
func Example() {
	// Build the runtime. WithLLM(stubLLM()) keeps this example keyless and
	// deterministic; NoAuth() opts out of the secure default (deny-all) so the
	// example can call the route without minting a token — see
	// ExampleWithAuthenticator for the production posture.
	app := New(
		WithAddr(exampleFreeAddr()),
		WithLLM(stubLLM()),
		WithAuthenticator(NoAuth()),
	)

	// App.Run blocks (it serves HTTP), so run it in the background and drive it
	// over loopback. stop() cancels the context and waits for a clean shutdown.
	base, stop := serveExample(app)
	defer stop()

	// One chat turn for tenant "acme", user "alice". Identity lives in the URL
	// path and flows through context.Context — never the request body.
	reply := exampleChat(base, "acme", "alice", "Say hi")
	fmt.Println(reply)

	// Output:
	// stub-llm structured reply for "Say hi" (field "reply")
}

// WeatherArgs is the typed argument for the example tool below. Its json tags are
// the single source of truth: WithTypedTool derives the JSON Schema the model is
// shown from this struct, then validates and decodes the model's arguments back
// into it (a decode failure reflects, it is never coerced).
type WeatherArgs struct {
	City string `json:"city" doc:"City to look up the weather for" example:"Paris"`
}

// getWeather is the tool handler: an ordinary typed Go function. This is the
// whole ergonomic surface for adding a tool — a struct in, a string out — the
// Aisotropy answer to Pydantic AI's @agent.tool decorator.
func getWeather(_ context.Context, args WeatherArgs) (string, error) {
	return "weather in " + args.City + ": sunny, 21C", nil
}

// ExampleWithTypedTool registers a tool whose arguments are a Go struct. The
// runtime advertises WeatherArgs's schema to the model over native tool-calling,
// validates the model's returned arguments against it, decodes them into a
// WeatherArgs, and dispatches to getWeather on the /chat/agent endpoint — all
// scope-gated (requiredScope "" here means "always available").
func ExampleWithTypedTool() {
	// Registration is one call: the typed handler IS the tool.
	app := New(
		WithAddr(exampleFreeAddr()),
		WithLLM(stubLLM()),
		WithAuthenticator(NoAuth()),
		WithTypedTool("get_weather", "Look up the current weather for a city", "", getWeather),
	)
	_ = app // app.Run(ctx) would serve the tool on the /chat/agent endpoint.

	// The agent endpoint (driven by a real provider) would decode the model's
	// tool call into a WeatherArgs and invoke getWeather. Because the handler is
	// just a typed function, you can also call and unit-test it directly:
	out, _ := getWeather(context.Background(), WeatherArgs{City: "Paris"})
	fmt.Println(out)

	// Output:
	// weather in Paris: sunny, 21C
}

// ExampleWithPersona registers a per-product base system prompt (persona). The
// runtime composes it into the system prompt for every turn whose product_id
// matches — memory and tool context are appended to this literal at request
// time. Here the streaming route echoes the hydrated system prompt, proving the
// persona reached the model.
func ExampleWithPersona() {
	app := New(
		WithAddr(exampleFreeAddr()),
		WithLLM(stubLLM()),
		WithAuthenticator(NoAuth()),
		WithPersona("support-bot", Persona{BasePrompt: "You are Ada, a concise support agent for ACME."}),
	)
	base, stop := serveExample(app)
	defer stop()

	// Drive the SSE route with the matching product_id and reassemble the
	// streamed tokens. The stub folds the system prompt into its echo, so the
	// persona text appears in the reply.
	reply := exampleStream(base, "acme", "alice", "support-bot", "hi")
	fmt.Println(strings.Contains(reply, "You are Ada, a concise support agent for ACME."))

	// Output:
	// true
}

// ExampleWithAuthenticator shows the security posture. With NO authenticator the
// runtime is default-deny: every /v1 request is rejected 401 — running without a
// credential check is an explicit opt-in via NoAuth(), never the silent default.
// A JWT authenticator verifies a bearer token and binds it must-agree to the URL
// path: a valid token for the right tenant passes (200), the same token on
// another tenant's path is forbidden (403).
func ExampleWithAuthenticator() {
	// (1) No authenticator configured -> default-deny.
	denyApp := New(WithAddr(exampleFreeAddr()), WithLLM(stubLLM()))
	denyBase, denyStop := serveExample(denyApp)
	defer denyStop()
	fmt.Println("no authenticator:", exampleChatStatus(denyBase, "acme", "alice", "", "hi"))

	// (2) NoAuth() -> explicit opt-out, for local/dev or an authenticating proxy.
	devApp := New(WithAddr(exampleFreeAddr()), WithLLM(stubLLM()), WithAuthenticator(NoAuth()))
	devBase, devStop := serveExample(devApp)
	defer devStop()
	fmt.Println("NoAuth():", exampleChatStatus(devBase, "acme", "alice", "", "hi"))

	// (3) JWT authenticator -> verify + must-agree bind. Secret must be >= 32 bytes.
	secret := []byte("example-hs256-secret-at-least-32b!")
	jwtApp := New(WithAddr(exampleFreeAddr()), WithLLM(stubLLM()), WithAuthenticator(NewJWTAuthenticator(secret)))
	jwtBase, jwtStop := serveExample(jwtApp)
	defer jwtStop()
	token := exampleMintHS256(secret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	fmt.Println("JWT, right tenant:", exampleChatStatus(jwtBase, "acme", "alice", token, "hi"))
	fmt.Println("JWT, wrong tenant:", exampleChatStatus(jwtBase, "other", "alice", token, "hi"))

	// Output:
	// no authenticator: 401
	// NoAuth(): 200
	// JWT, right tenant: 200
	// JWT, wrong tenant: 403
}

// ExampleWithMCPServer registers a Model Context Protocol (MCP) server whose
// tools the runtime exposes as scope-gated Aisotropy tools — the polyglot tool
// path. The server is any executable speaking MCP over stdio, in any language;
// the consumer never imports an MCP library. This example is illustrative only
// (it is not executed under `go test` because it would spawn an external
// subprocess): connection is best-effort, so a server that fails to start is
// logged and skipped rather than downing the runtime.
func ExampleWithMCPServer() {
	app := New(
		WithLLM(stubLLM()),
		WithAuthenticator(NoAuth()),
		// Expose an off-the-shelf filesystem MCP server's tools, gated behind the
		// "read:files" scope. At Run time the runtime spawns the command, lists
		// its tools, and registers each one carrying the server's JSON Schema.
		WithMCPServer("read:files", "npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"),
	)
	_ = app // app.Run(ctx) would boot the runtime with the MCP tools wired in.
}

// --- test-only helpers (hidden from the rendered godoc examples above) ---

// stubLLM returns the credential-less stub client, so the examples run
// deterministically without an ANTHROPIC_API_KEY. A near-zero inter-token delay
// keeps the streaming example fast.
func stubLLM() *llm.StubClient { return &llm.StubClient{Delay: time.Nanosecond} }

// exampleFreeAddr reserves an ephemeral loopback port and returns its address.
func exampleFreeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// serveExample boots app.Run in a goroutine and blocks until the liveness
// endpoint answers, then returns the base URL and a stop func that cancels the
// context and waits for a clean shutdown. Helpers panic on error (an example
// fixture failure is a test failure).
func serveExample(app *App) (base string, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	base = "http://" + app.addr
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz") //nolint:noctx,gosec // loopback readiness poll in a test helper; addr is a reserved ephemeral port
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base, func() { cancel(); <-done }
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	panic("example server did not become ready")
}

// exampleChat POSTs one non-streaming /chat turn and returns the decoded reply.
func exampleChat(base, tenant, user, prompt string) string {
	body, _ := json.Marshal(map[string]any{"prompt": prompt, "product_id": "demo", "session_id": "s1"})
	req, _ := http.NewRequest(http.MethodPost, chatURL(base, tenant, user), bytes.NewReader(body)) //nolint:noctx // test helper
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		panic(err)
	}
	return out.Reply
}

// exampleChatStatus POSTs a /chat turn with an optional bearer token and returns
// the HTTP status code (used to demonstrate the auth gate).
func exampleChatStatus(base, tenant, user, bearer, prompt string) int {
	body, _ := json.Marshal(map[string]any{"prompt": prompt, "product_id": "demo", "session_id": "s1"})
	req, _ := http.NewRequest(http.MethodPost, chatURL(base, tenant, user), bytes.NewReader(body)) //nolint:noctx // test helper
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// exampleStream drives the SSE /chat/stream route and returns the assistant text
// reassembled from the streamed token events.
func exampleStream(base, tenant, user, product, prompt string) string {
	body, _ := json.Marshal(map[string]any{"prompt": prompt, "product_id": product, "session_id": "s1"})
	req, _ := http.NewRequest(http.MethodPost, streamURL(base, tenant, user), bytes.NewReader(body)) //nolint:noctx // test helper
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var text strings.Builder
	for _, line := range strings.Split(readAll(resp.Body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var tok struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(data), &tok) == nil {
			text.WriteString(tok.Text)
		}
	}
	return text.String()
}

// readAll drains r to a string, panicking on error (test helper).
func readAll(r io.Reader) string {
	b, err := io.ReadAll(r)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// mintHS256 builds a minimal HS256 JWT (header.payload.signature, base64url, no
// padding) using only the standard library — mirroring the stdlib-only verifier
// NewJWTAuthenticator installs.
func exampleMintHS256(secret []byte, claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
