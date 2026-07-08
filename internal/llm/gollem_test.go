package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fugue-labs/gollem/core"
)

// gollemTestOutput is the output struct used in these tests — analogous to
// ChatOutput.Body (internal/api/chat.go) but minimal, without pulling in the
// api package here.
type gollemTestOutput struct {
	Reply string `json:"reply"`
}

// fakeClient implements llm.Client with canned responses in sequence, no
// network or credentials needed — allows exercising GollemModel/GollemReflect
// (our real integration code) end-to-end and deterministically.
type fakeClient struct {
	responses []string
	calls     int
}

func (f *fakeClient) Name() string { return "fake" }

func (f *fakeClient) StreamChat(_ context.Context, _, _ string) (<-chan Token, error) {
	idx := f.calls
	f.calls++
	text := f.responses[len(f.responses)-1]
	if idx < len(f.responses) {
		text = f.responses[idx]
	}

	out := make(chan Token, 2)
	out <- Token{Text: text}
	out <- Token{Done: true}
	close(out)
	return out, nil
}

// TestGollemReflectRepairsSchemaViaAgentInternalRetry verifies, through our
// real adapter (GollemModel + GollemReflect, gollem.go), that core.Agent[T]
// repairs an invalid JSON on its own: the Client's first response is not
// JSON, Agent[T] retries internally with the exact typing error
// (RetryPromptPart) without going through the homegrown Reflect[T] loop or
// RunWithReflection — this is the mechanism described in
// docs/components/gollem-orchestrator.md and deferred to Phase 2 by ADR-0008.
func TestGollemReflectRepairsSchemaViaAgentInternalRetry(t *testing.T) {
	client := &fakeClient{responses: []string{
		"this is not JSON at all",
		`{"reply":"ok"}`,
	}}

	noopValidate := func(context.Context, gollemTestOutput) (string, error) { return "", nil }

	result, err := GollemReflect[gollemTestOutput](context.Background(), client, "system prompt", "hello", noopValidate, 3)
	if err != nil {
		t.Fatalf("GollemReflect returned unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected Accepted=true")
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, expected 1 (schema repair is internal to Agent.Run, not a RunWithReflection cycle)", result.Iterations)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, expected 2 (1 invalid attempt + 1 repaired, via Agent[T]'s internal retry)", client.calls)
	}
}

// TestGollemReflectRunsBusinessValidationLoop verifies the outer
// RunWithReflection cycle: the first response is mechanically valid JSON but
// invalid by business rule (empty reply); the validator rejects it with
// feedback, Gollem retries with that feedback, and the second response
// corrects it.
func TestGollemReflectRunsBusinessValidationLoop(t *testing.T) {
	client := &fakeClient{responses: []string{
		`{"reply":""}`,
		`{"reply":"ok"}`,
	}}

	businessValidate := func(_ context.Context, out gollemTestOutput) (string, error) {
		if out.Reply == "" {
			return "reply cannot be empty", nil
		}
		return "", nil
	}

	result, err := GollemReflect[gollemTestOutput](context.Background(), client, "system prompt", "hello", businessValidate, 3)
	if err != nil {
		t.Fatalf("GollemReflect returned unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected Accepted=true after the second iteration")
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, expected 2 (1 business rejection + 1 correction)", result.Iterations)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, expected 2", client.calls)
	}
}

// TestGollemAgentReflectionWithCoreTestModel exercises Gollem's reflection
// flow against core.TestModel — the official mock from
// github.com/fugue-labs/gollem/core (no network, no API key, not going
// through our GollemModel adapter) — to independently verify that the real
// Agent[T] API behaves as ADR-0008 describes: by default (OutputModeTool,
// struct T) Agent itself repairs invalid JSON by retrying with the exact
// error before RunWithReflection comes into play.
func TestGollemAgentReflectionWithCoreTestModel(t *testing.T) {
	model := core.NewTestModel(
		core.ToolCallResponse(core.DefaultOutputToolName, `{not-json`),
		core.ToolCallResponse(core.DefaultOutputToolName, `{"reply":"ok"}`),
	)

	agent := core.NewAgent[gollemTestOutput](model, core.WithMaxRetries[gollemTestOutput](3))

	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("agent.Run returned unexpected error: %v", err)
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if len(model.Calls()) != 2 {
		t.Fatalf("len(model.Calls()) = %d, expected 2 (Agent[T]'s internal retry after invalid JSON)", len(model.Calls()))
	}
}

// TestExtractJSONObject verifies that the adapter tolerates surrounding
// prose/markdown backticks around the JSON despite the explicit instruction
// not to add them — necessary because our Client has no native tool-calling
// and we depend on the model respecting the prompt.
func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean", `{"reply":"ok"}`, `{"reply":"ok"}`},
		{"with surrounding prose", "Sure, here it is: {\"reply\":\"ok\"} hope that helps", `{"reply":"ok"}`},
		{"with markdown fences", "```json\n{\"reply\":\"ok\"}\n```", `{"reply":"ok"}`},
		{"with nested braces", `{"a":{"b":1}}`, `{"a":{"b":1}}`},
		{"with braces inside a string", `{"reply":"has } a brace"}`, `{"reply":"has } a brace"}`},
		{"no json", "there is nothing here", "there is nothing here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSONObject(tc.in)
			if got != tc.want {
				t.Fatalf("extractJSONObject(%q) = %q, expected %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestReflectAutoFallsBackToOwnOnGollemFailure verifies ADR-0008's fallback
// contract: if the "own" backend is forced via AISOTROPY_REFLECTION_BACKEND,
// ReflectAuto uses Reflect[T] (reflect.go) instead of Gollem, without
// touching the GollemModel path at all.
func TestReflectAutoFallsBackToOwnOnGollemFailure(t *testing.T) {
	t.Setenv("AISOTROPY_REFLECTION_BACKEND", "own")

	client := &fakeClient{responses: []string{`{"reply":"ok"}`}}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	out, attempts, backend, err := ReflectAuto[gollemTestOutput](context.Background(), client, "system", "hello", decode, nil, 3)
	if err != nil {
		t.Fatalf("ReflectAuto returned unexpected error: %v", err)
	}
	if backend != ReflectionBackendOwn {
		t.Fatalf("backend = %q, expected %q", backend, ReflectionBackendOwn)
	}
	if out.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", out.Reply, "ok")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, expected 1", attempts)
	}
}

// TestReflectAutoDefaultsToGollem verifies that, without forcing
// AISOTROPY_REFLECTION_BACKEND, ReflectAuto uses Gollem by default (ADR-0008).
func TestReflectAutoDefaultsToGollem(t *testing.T) {
	client := &fakeClient{responses: []string{`{"reply":"ok"}`}}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	out, _, backend, err := ReflectAuto[gollemTestOutput](context.Background(), client, "system", "hello", decode, nil, 3)
	if err != nil {
		t.Fatalf("ReflectAuto returned unexpected error: %v", err)
	}
	if backend != ReflectionBackendGollem {
		t.Fatalf("backend = %q, expected %q", backend, ReflectionBackendGollem)
	}
	if out.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", out.Reply, "ok")
	}
}
