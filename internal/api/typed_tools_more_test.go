package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// cityArgs is a string-only typed tool argument struct, so its flattened
// string-map form round-trips through the fallback path's JSON re-marshal.
type cityArgs struct {
	City string `json:"city" jsonschema:"description=the city to look up"`
}

// TestToolArgsError_ErrorAndUnwrap covers the error type's string formatting and
// errors.Unwrap contract (the fail-closed reflection signal must be
// distinguishable from a plain tool error via errors.As).
func TestToolArgsError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("days must be an integer")
	e := &ToolArgsError{Tool: "get_weather", Err: inner}

	if got := e.Error(); !strings.Contains(got, "get_weather") || !strings.Contains(got, inner.Error()) {
		t.Errorf("Error() = %q, want it to name the tool and wrap the inner error", got)
	}
	if !errors.Is(e, inner) {
		t.Error("errors.Is(ToolArgsError, inner) = false, want true (Unwrap must expose the cause)")
	}
	var ae *ToolArgsError
	if !errors.As(e, &ae) {
		t.Error("errors.As failed to recognize *ToolArgsError")
	}
}

// TestCombinedMapExecutor_Execute covers the structured-output fallback adapter:
// a typed tool is dispatched through the typed executor (re-marshalling the flat
// string args to JSON), an invalid-arg *ToolArgsError is surfaced as a
// reflect-able RESULT (not an error), and an untyped tool falls through to the
// wrapped map executor.
func TestCombinedMapExecutor_Execute(t *testing.T) {
	// A string-only arg struct: the fallback path flattens args to a
	// string-keyed map and re-marshals to JSON, so only string fields survive
	// the round-trip (the documented 0.x limitation). Using string args exercises
	// the successful typed dispatch on the fallback path.
	citySchema, _ := llm.SchemaFor[cityArgs]()
	var handlerCalls int
	cityHandler := TypedHandlerFor("lookup_city", citySchema, func(_ context.Context, a cityArgs) (string, error) {
		handlerCalls++
		return "info for " + a.City, nil
	})
	// A constrained (int) tool to exercise the invalid-args reflect branch.
	weatherSchema, _ := llm.SchemaFor[weatherArgs]()
	weatherHandler := TypedHandlerFor("get_weather", weatherSchema, func(_ context.Context, a weatherArgs) (string, error) {
		return "forecast for " + a.City, nil
	})
	typed := NewTypedToolExecutor(map[string]TypedToolHandler{
		"lookup_city": cityHandler,
		"get_weather": weatherHandler,
	})
	exec := combinedMapExecutor{typed: typed, fallback: DemoExecutor{}}
	ctx := context.Background()

	t.Run("typed tool dispatched via JSON re-marshal", func(t *testing.T) {
		out, err := exec.Execute(ctx, "lookup_city", map[string]string{"city": "Paris"})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !strings.Contains(out, "Paris") {
			t.Errorf("result = %q, want the info for Paris", out)
		}
		if handlerCalls != 1 {
			t.Errorf("typed handler called %d times, want 1", handlerCalls)
		}
	})

	t.Run("invalid typed args surface as a reflect-able result", func(t *testing.T) {
		// days out of the schema's range -> *ToolArgsError -> surfaced as a
		// tool RESULT ("invalid arguments: ...") with a nil error so the fallback
		// loop reflects rather than fails.
		out, err := exec.Execute(ctx, "get_weather", map[string]string{"city": "Paris", "days": "999"})
		if err != nil {
			t.Fatalf("invalid args must not error (they reflect): %v", err)
		}
		if !strings.Contains(out, "invalid arguments") {
			t.Errorf("result = %q, want an 'invalid arguments' reflect message", out)
		}
	})

	t.Run("untyped tool falls through to the map executor", func(t *testing.T) {
		out, err := exec.Execute(ctx, "not_typed", map[string]string{"q": "x"})
		if err != nil {
			t.Fatalf("Execute untyped: %v", err)
		}
		// DemoExecutor echoes the call name.
		if !strings.Contains(out, "not_typed") {
			t.Errorf("untyped result = %q, want the DemoExecutor echo of the tool name", out)
		}
	})
}

// TestTypedToolExecutor_RunNotRegistered covers the not-registered branch of
// Run: dispatching an unknown typed tool returns an error naming it.
func TestTypedToolExecutor_RunNotRegistered(t *testing.T) {
	exec := NewTypedToolExecutor(nil)
	if _, err := exec.Run(context.Background(), "ghost", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Run on an unregistered typed tool must return an error")
	}
}

// TestDecodeStringArgs covers the flattening of raw JSON tool args: string
// scalars pass through, non-string values are stringified, and empty/invalid
// JSON yields an empty map (so an untyped tool still runs).
func TestDecodeStringArgs(t *testing.T) {
	t.Run("empty and null yield empty map", func(t *testing.T) {
		for _, in := range []string{"", "  ", "{}", "null"} {
			if got := decodeStringArgs(in); len(got) != 0 {
				t.Errorf("decodeStringArgs(%q) = %v, want empty", in, got)
			}
		}
	})
	t.Run("invalid JSON yields empty map", func(t *testing.T) {
		if got := decodeStringArgs("{not json"); len(got) != 0 {
			t.Errorf("decodeStringArgs(invalid) = %v, want empty", got)
		}
	})
	t.Run("mixed scalar types", func(t *testing.T) {
		got := decodeStringArgs(`{"city":"Paris","days":3,"metric":true}`)
		if got["city"] != "Paris" {
			t.Errorf("string scalar not passed through: %v", got)
		}
		if got["days"] != "3" {
			t.Errorf("int not stringified: got %q, want \"3\"", got["days"])
		}
		if got["metric"] != "true" {
			t.Errorf("bool not stringified: got %q, want \"true\"", got["metric"])
		}
	})
}
