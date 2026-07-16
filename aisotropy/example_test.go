package aisotropy

// example_test.go is the runnable godoc for the public aisotropy package: a set
// of Example functions that appear in godoc AND execute under `go test`, so the
// "how do I build an agent on top of Aisotropy" story is copy-paste code that is
// verified on every CI run rather than prose that can drift.
//
// The library-first surface is App.Turn: build an App, call Turn like a function,
// read the reply — no HTTP server, no goroutine, no SSE reassembly. That is the
// shape these examples lead with (the answer to Pydantic AI's run_sync). App.Run
// (serve the same runtime over HTTP) is the deployment option, shown separately.
// Every example runs against the credential-less stub LLM (internal/llm.StubClient),
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
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// Example is the minimal agent, library-first: build an App, call Turn, print the
// reply. No server, no port, no goroutine — Turn runs the full chat pipeline
// (identity->ctx isolation, quota gate, guardrails, memory hydration, fail-closed
// reflection, cost) in-process and returns the structured reply. In production you
// would drop WithLLM entirely (the default, llm.FromEnv(), uses your
// ANTHROPIC_API_KEY); identity is supplied by your own trusted code here, so there
// is no auth middleware to configure (see ExampleWithAuthenticator for the HTTP
// posture).
func Example() {
	app := New(WithLLM(stubLLM()))
	defer func() { _ = app.Close() }()

	// One chat turn for tenant "acme", user "alice". TenantID/UserID are the
	// isolation identity: they enter context.Context inside Turn and scope every
	// memory tier — they never travel any other way.
	out, err := app.Turn(context.Background(), ChatInput{
		TenantID:  "acme",
		UserID:    "alice",
		SessionID: "s1",
		Prompt:    "Say hi",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(out.Reply)

	// Output:
	// stub-llm structured reply for "Say hi" (field "reply")
}

// ExampleApp_Turn_memory shows the payoff of the in-process runtime: it is built
// once on the first Turn and reused, so working memory persists between calls.
// Reusing the SAME SessionID is what threads a conversation — the second turn
// hydrates the first. Both turns run against the same tenant/user, fully isolated
// from any other tenant by construction.
func ExampleApp_Turn_memory() {
	app := New(WithLLM(stubLLM()))
	defer func() { _ = app.Close() }()

	ctx := context.Background()
	first, _ := app.Turn(ctx, ChatInput{TenantID: "acme", UserID: "alice", SessionID: "chat-1", Prompt: "My name is Alice."})
	second, _ := app.Turn(ctx, ChatInput{TenantID: "acme", UserID: "alice", SessionID: "chat-1", Prompt: "What did I just tell you?"})

	// Both turns served, same tenant echoed back both times — the second ran
	// against the runtime (and the working-memory buffer) the first one seeded.
	fmt.Println(first.TenantID, second.TenantID, second.Stub)

	// Output:
	// acme acme true
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
// WeatherArgs, and dispatches to getWeather on the agent endpoint — all
// scope-gated (requiredScope "" here means "always available").
func ExampleWithTypedTool() {
	// Registration is one call: the typed handler IS the tool.
	app := New(
		WithLLM(stubLLM()),
		WithTypedTool("get_weather", "Look up the current weather for a city", "", getWeather),
	)
	defer func() { _ = app.Close() }()

	// A real provider on the agent endpoint would decode the model's tool call
	// into a WeatherArgs and invoke getWeather. Because the handler is just a
	// typed function, you can also call and unit-test it directly:
	out, _ := getWeather(context.Background(), WeatherArgs{City: "Paris"})
	fmt.Println(out)

	// Output:
	// weather in Paris: sunny, 21C
}

// ExampleWithPersona registers a per-product base system prompt (persona). The
// runtime composes it into the system prompt for every turn whose ProductID
// matches — memory and tool context are appended to this literal at request time.
// Selecting the persona is just setting ChatInput.ProductID.
func ExampleWithPersona() {
	app := New(
		WithLLM(stubLLM()),
		WithPersona("support-bot", Persona{BasePrompt: "You are Ada, a concise support agent for ACME."}),
	)
	defer func() { _ = app.Close() }()

	// ProductID "support-bot" selects Ada's persona for this turn; an unregistered
	// product id falls back to the built-in default persona.
	out, err := app.Turn(context.Background(), ChatInput{
		TenantID:  "acme",
		UserID:    "alice",
		SessionID: "s1",
		ProductID: "support-bot",
		Prompt:    "hi",
	})
	if err != nil {
		panic(err)
	}
	// The turn was served under the persona; the stub echoes the prompt.
	fmt.Println(out.Reply)

	// Output:
	// stub-llm structured reply for "hi" (field "reply")
}

// ExampleWithAuthenticator shows the security posture — which is an HTTP concern.
// In-process, App.Turn trusts the caller's supplied identity: your own code is
// inside the trust boundary, exactly as the code that mints a JWT for a user is,
// so there is no credential to verify. The credential check exists only on the
// network boundary that App.Run serves, where identity arrives from an untrusted
// client. This example drives that HTTP path to make the distinction concrete:
// with NO authenticator the runtime is default-deny (every /v1 request is 401 —
// running without a check is an explicit NoAuth() opt-in, never the silent
// default); a JWT authenticator verifies a bearer token and binds it must-agree
// to the URL path (right tenant 200, same token on another tenant's path 403).
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
		// "read:files" scope. On the first Turn (or at Run time) the runtime spawns
		// the command, lists its tools, and registers each one carrying the
		// server's JSON Schema.
		WithMCPServer("read:files", "npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"),
	)
	_ = app // app.Turn(ctx, ...) or app.Run(ctx) would boot the runtime with the MCP tools wired in.
}

// --- test-only helpers (hidden from the rendered godoc examples above) ---

// stubLLM returns the credential-less stub client, so the examples run
// deterministically without an ANTHROPIC_API_KEY. A near-zero inter-token delay
// keeps the streaming HTTP example fast.
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
// fixture failure is a test failure). Used only by ExampleWithAuthenticator,
// which is inherently about the HTTP boundary.
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

// exampleMintHS256 builds a minimal HS256 JWT (header.payload.signature,
// base64url, no padding) using only the standard library — mirroring the
// stdlib-only verifier NewJWTAuthenticator installs.
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
