package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// weatherArgs is a typed tool's argument struct exercising fields the
// map[string]string path could not carry: a non-string int (Days) and
// constraints (Days range, Units enum) that must be enforced server-side.
type weatherArgs struct {
	City  string `json:"city" jsonschema:"description=the city to look up"`
	Units string `json:"units,omitempty" jsonschema:"enum=c,enum=f"`
	Days  int    `json:"days" jsonschema:"minimum=1,maximum=14"`
}

// typedWeatherExecutor builds a TypedToolExecutor whose get_weather handler is
// the REAL one the façade's WithTypedTool[T] produces (api.TypedHandlerFor):
// validate the args against the tool's schema, decode into weatherArgs, then
// invoke — an invalid/out-of-constraint decode returns a *ToolArgsError.
func typedWeatherExecutor(calls *int, got *weatherArgs) *TypedToolExecutor {
	schema, _ := llm.SchemaFor[weatherArgs]()
	h := TypedHandlerFor("get_weather", schema, func(_ context.Context, a weatherArgs) (string, error) {
		*calls++
		*got = a
		return "forecast for " + a.City, nil
	})
	return NewTypedToolExecutor(map[string]TypedToolHandler{"get_weather": h})
}

// TestNativeToolAgent_TypedTool_DecodesNestedArgs verifies the typed path end to
// end on the native tool-calling route: the model's RAW JSON (with a non-string
// field) survives to the handler decoded into the arg struct — the fidelity the
// map[string]string seam lacked (SDD R2/R3/R5).
func TestNativeToolAgent_TypedTool_DecodesNestedArgs(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{
		Name:        "get_weather",
		Description: "get the weather",
		Parameters:  mustSchema[weatherArgs](t),
	})
	var calls int
	var got weatherArgs
	typed := typedWeatherExecutor(&calls, &got)

	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "get_weather", RawArgs: json.RawMessage(`{"city":"Paris","days":3}`)}}},
			{Message: "here is the forecast"},
		},
	}

	steps, err := runNativeToolAgent(context.Background(), tc, noop.NewTracerProvider(), reg, DemoExecutor{}, typed, nil, "weather in paris", 3)
	if err != nil {
		t.Fatalf("runNativeToolAgent: %v", err)
	}
	if calls != 1 {
		t.Fatalf("typed handler called %d times, want 1", calls)
	}
	if got.City != "Paris" || got.Days != 3 {
		t.Fatalf("decoded args = %+v, want {Paris 3} (nested int must survive)", got)
	}
	var ran bool
	for _, s := range steps {
		if s.Tool == "get_weather" && strings.Contains(s.Result, "Paris") {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("expected a get_weather step with the forecast, got %+v", steps)
	}
}

// TestNativeToolAgent_TypedTool_InvalidArgsReflect verifies fail-closed
// reflection on inputs (SDD R4): when the model's args do not decode into the
// arg struct (a string where an int is required), the handler is NOT run with
// coerced data; instead the validation error is surfaced back as the tool result
// so the model can correct itself, and the run completes without error.
func TestNativeToolAgent_TypedTool_InvalidArgsReflect(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{
		Name:        "get_weather",
		Description: "get the weather",
		Parameters:  mustSchema[weatherArgs](t),
	})
	var calls int
	var got weatherArgs
	typed := typedWeatherExecutor(&calls, &got)

	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			// days is a string, not an int -> decode fails -> reflect
			{Calls: []llm.ToolCall{{Name: "get_weather", RawArgs: json.RawMessage(`{"city":"Paris","days":"three"}`)}}},
			{Message: "sorry, corrected"},
		},
	}

	steps, err := runNativeToolAgent(context.Background(), tc, noop.NewTracerProvider(), reg, DemoExecutor{}, typed, nil, "weather in paris", 3)
	if err != nil {
		t.Fatalf("invalid typed args must not fail the run (they reflect), got: %v", err)
	}
	if calls != 0 {
		t.Fatalf("handler ran %d times on invalid args, want 0 (no coerced call)", calls)
	}
	var reflected bool
	for _, s := range steps {
		if strings.Contains(s.Result, "invalid arguments") {
			reflected = true
		}
	}
	if !reflected {
		t.Fatalf("expected a reflected 'invalid arguments' step, got %+v", steps)
	}
}

// TestNativeToolAgent_TypedTool_ConstraintViolationReflects verifies
// constraint-level validation (SDD R3): the arguments are well-typed and
// structurally valid but violate the schema's constraints (Days above maximum,
// Units outside the enum). The handler must NOT run; the violation reflects back
// to the model. Proves validation goes beyond decode — it enforces the exact
// schema the model was shown.
func TestNativeToolAgent_TypedTool_ConstraintViolationReflects(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{
		Name:        "get_weather",
		Description: "get the weather",
		Parameters:  mustSchema[weatherArgs](t),
	})
	var calls int
	var got weatherArgs
	typed := typedWeatherExecutor(&calls, &got)

	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			// well-typed but out of constraints: days > maximum(14), units not in enum(c,f)
			{Calls: []llm.ToolCall{{Name: "get_weather", RawArgs: json.RawMessage(`{"city":"Paris","days":99,"units":"kelvin"}`)}}},
			{Message: "corrected"},
		},
	}

	steps, err := runNativeToolAgent(context.Background(), tc, noop.NewTracerProvider(), reg, DemoExecutor{}, typed, nil, "weather in paris", 3)
	if err != nil {
		t.Fatalf("constraint violation must reflect, not fail the run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("handler ran %d times on out-of-constraint args, want 0", calls)
	}
	var reflected bool
	for _, s := range steps {
		if strings.Contains(s.Result, "invalid arguments") {
			reflected = true
		}
	}
	if !reflected {
		t.Fatalf("expected a reflected 'invalid arguments' step for the constraint violation, got %+v", steps)
	}
}

// mustSchema derives a tool's parameter schema for tests, failing on error.
func mustSchema[T any](t *testing.T) json.RawMessage {
	t.Helper()
	s, err := llm.SchemaFor[T]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	return s
}
