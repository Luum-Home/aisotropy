//go:build manual

// Package llm live validation for the Gollem reflection path (ADR-0008).
//
// This file is behind the `manual` build tag, so it is EXCLUDED from the
// default `go test ./...` run and from CI — it makes a real, paid LLM call and
// requires credentials. Run it deliberately, with a provider key exported,
// to confirm the Phase 2 Gollem backend works end-to-end against a live model:
//
//	set -a; source /path/to/.env; set +a   # exports OPENROUTER_API_KEY, etc.
//	go test -tags manual -run TestGollemReflectLive -v ./internal/llm
//
// It validates the full production path: FromEnv() selects a real Client, our
// GollemModel adapter drives core.Agent[T] structured output, and
// RunWithReflection[T] returns a schema-valid, business-valid T.
package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestGollemReflectLive exercises GollemReflect against whatever real provider
// FromEnv() resolves (OpenRouter first). It skips cleanly when no key is set,
// so it is safe to invoke unconditionally in a manual run.
func TestGollemReflectLive(t *testing.T) {
	client := FromEnv()
	if strings.HasPrefix(client.Name(), "stub") {
		t.Skip("no provider key in env (FromEnv resolved the keyless stub); export OPENROUTER_API_KEY to run this live validation")
	}
	t.Logf("live provider: %s", client.Name()) // Name() carries the model id, never the key

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Business rule: the reply must be non-empty. This forces the outer
	// RunWithReflection cycle to reject an empty structured reply and request
	// a correction, so a converging run proves the loop actually runs.
	validate := func(_ context.Context, out gollemTestOutput) (string, error) {
		if strings.TrimSpace(out.Reply) == "" {
			return "the 'reply' field must be a non-empty greeting", nil
		}
		return "", nil
	}

	system := "You are a terse assistant. Answer strictly as JSON matching the requested schema."
	prompt := "Reply with a one-word friendly greeting in the 'reply' field."

	result, err := GollemReflect[gollemTestOutput](ctx, client, system, prompt, validate, 3)
	if err != nil {
		t.Fatalf("GollemReflect (live) returned an error: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("live run not accepted after %d iterations", result.Iterations)
	}
	if strings.TrimSpace(result.Output.Reply) == "" {
		t.Fatal("live run accepted but Reply is empty — validator contract violated")
	}
	t.Logf("live GollemReflect OK: reply=%q iterations=%d accepted=%v",
		result.Output.Reply, result.Iterations, result.Accepted)
}
