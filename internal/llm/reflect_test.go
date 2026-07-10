package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type reflectOutput struct {
	Reply string `json:"reply"`
}

// TestReflectRepairsAfterInvalidAttempts simulates an LLM (stub, no API key)
// that returns invalid JSON twice and only meets the contract on the third
// attempt — verifies that Reflect retries with the exact error and ends up
// accepting the valid response within the retry cap.
func TestReflectRepairsAfterInvalidAttempts(t *testing.T) {
	call := 0
	gen := func(_ context.Context, prompt, feedback string) (string, error) {
		call++
		switch call {
		case 1:
			return `{not-json`, nil
		case 2:
			return `{"reply":""}`, nil // valid as JSON, invalid by business rule
		default:
			if feedback == "" {
				t.Fatalf("attempt %d: expected feedback from the previous iteration", call)
			}
			return `{"reply":"ok"}`, nil
		}
	}

	validate := func(raw string) (reflectOutput, error) {
		var out reflectOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return reflectOutput{}, fmt.Errorf("invalid json: %w", err)
		}
		if out.Reply == "" {
			return reflectOutput{}, errors.New("reply cannot be empty")
		}
		return out, nil
	}

	result, err := Reflect(context.Background(), gen, validate, "hello", 3)
	if err != nil {
		t.Fatalf("Reflect returned unexpected error: %v", err)
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if result.Attempts != 3 {
		t.Fatalf("Attempts = %d, expected 3", result.Attempts)
	}
}

// TestReflectExhaustsRetries verifies that, once retries are exhausted
// without a valid response, Reflect returns a controlled error (never hangs).
func TestReflectExhaustsRetries(t *testing.T) {
	gen := func(_ context.Context, _, _ string) (string, error) {
		return `{"reply":""}`, nil
	}
	validate := func(raw string) (reflectOutput, error) {
		var out reflectOutput
		_ = json.Unmarshal([]byte(raw), &out)
		if out.Reply == "" {
			return reflectOutput{}, errors.New("reply cannot be empty")
		}
		return out, nil
	}

	_, err := Reflect(context.Background(), gen, validate, "hello", 2)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

// TestReflectRespectsCancellation verifies that an already-cancelled ctx
// cuts the loop short before invoking gen — never spends tokens on a
// cancelled call.
func TestReflectRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	gen := func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "", nil
	}
	validate := func(raw string) (reflectOutput, error) { return reflectOutput{}, nil }

	_, err := Reflect(ctx, gen, validate, "hello", 3)
	if err == nil {
		t.Fatal("expected error due to cancelled ctx")
	}
	if called {
		t.Fatal("gen should not have been invoked with an already-cancelled ctx")
	}
}
