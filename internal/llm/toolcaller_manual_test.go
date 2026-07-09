//go:build manual

// Live validation for the ToolCaller native tool-calling path (ADR-0012).
// Behind the `manual` build tag so it is EXCLUDED from `go test ./...` and CI:
// it makes a real, billable provider request. It exercises the OpenAI-compatible
// CallTools implementation (OpenRouter or OpenAI) end to end: advertise a tool,
// prompt the model to use it, and assert the returned tool call round-trips
// through the parser.
//
// Run (loading the project's .env without echoing secrets):
//
//	set -a && . ./.env && set +a && \
//	  go test -tags manual -run TestToolCallerLive -v ./internal/llm
//
// It self-skips when no OpenAI-compatible key is in the environment.
package llm

import (
	"context"
	"os"
	"testing"
)

func TestToolCallerLive(t *testing.T) {
	var client *OpenAICompatClient
	switch {
	case os.Getenv("OPENROUTER_API_KEY") != "":
		client = NewOpenRouterClient(os.Getenv("OPENROUTER_API_KEY"),
			firstNonEmpty(os.Getenv("OPENROUTER_MODEL"), os.Getenv("AISOTROPY_MODEL")))
	case os.Getenv("OPENAI_API_KEY") != "":
		client = NewOpenAICompatClient(os.Getenv("OPENAI_API_KEY"),
			firstNonEmpty(os.Getenv("OPENAI_MODEL"), os.Getenv("AISOTROPY_MODEL")),
			os.Getenv("OPENAI_BASE_URL"), "openai")
	default:
		t.Skip("no OPENROUTER_API_KEY / OPENAI_API_KEY set; skipping live ToolCaller validation")
	}

	// Compile-time + runtime confirmation this really is a ToolCaller.
	var tc ToolCaller = client

	tools := []ToolSpec{
		{Name: "get_weather", Description: "Get the current weather for a given city."},
		{Name: "search_docs", Description: "Search the tenant knowledge base."},
	}
	system := "You are an assistant that may call the provided tools. Call a tool when it helps."
	prompt := "What is the weather in Paris right now? Use a tool."

	res, err := tc.CallTools(context.Background(), system, prompt, tools)
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}

	t.Logf("live CallTools (%s) returned %d tool call(s), message=%q", client.Name(), len(res.Calls), res.Message)

	// The model is free to answer directly, but if it DID call a tool, the name
	// must be one we offered (the parser must not fabricate names) — this is the
	// round-trip the test exists to prove.
	offered := map[string]bool{"get_weather": true, "search_docs": true}
	for _, call := range res.Calls {
		if !offered[call.Name] {
			t.Fatalf("model returned a tool not offered: %q", call.Name)
		}
		t.Logf("  -> tool=%q args=%v", call.Name, call.Args)
	}
	if len(res.Calls) == 0 {
		t.Logf("model answered directly (no tool call) — round-trip still valid, but weak signal")
	}
}
