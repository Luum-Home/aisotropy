package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// stubProviderName is the Client.Name() / Registry key for StubClient, shared
// by every place in this package (and its tests) that registers, looks up, or
// asserts against the keyless fallback provider.
const stubProviderName = "stub"

// StubClient is a canned Client: it does not call any real LLM, it emits a
// word-by-word token stream deterministically derived from the received
// prompt. It exists so the full SSE pipeline (HTTP handler -> token channel
// -> per-event flush -> ctx cancellation) can be verified without needing
// any LLM provider key (the runtime is provider-agnostic, ADR-0009:
// OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > this stub) — it is
// the path exercised in this development/CI environment.
type StubClient struct {
	// Delay is the pause between emitted tokens (simulates real streaming
	// latency). Defaults to 15ms.
	Delay time.Duration
}

// NewStubClient creates a StubClient with the default delay.
func NewStubClient() *StubClient {
	return &StubClient{Delay: 15 * time.Millisecond}
}

func (s *StubClient) Name() string { return stubProviderName }

// StreamChat emits, word by word, a canned response that echoes the received
// prompt — unless prompt carries a structured-output schema instruction (see
// stubStructuredReply), in which case it emits one deterministic JSON object
// satisfying that schema instead, so the reflection loop (GollemReflect/
// ReflectAuto, gollem.go/reflect_auto.go) can be exercised end-to-end without
// ANTHROPIC_API_KEY. Also attaches a deterministic fake Usage to the
// terminal Token, so the OTel decorator and CostTracker wiring can be
// verified without a real provider. Respects ctx.Done(): if the client
// cancels midway, the producer goroutine stops immediately and closes the
// channel without emitting more tokens.
func (s *StubClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	delay := s.Delay
	if delay <= 0 {
		delay = 15 * time.Millisecond
	}

	words := stubWordsFor(system, prompt)
	usage := stubUsageFor(system, prompt, words)
	out := make(chan Token)

	go func() {
		defer close(out)

		for i, w := range words {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}

			select {
			case <-ctx.Done():
				return
			case out <- Token{Text: w}:
			}
		}

		select {
		case <-ctx.Done():
		case out <- Token{Done: true, Usage: &usage}:
		}
	}()

	return out, nil
}

// stubUsageFor builds a deterministic, non-zero fake Usage from the request
// and reply sizes. Output tokens are derived from the reply's character
// count (not the number of emitted word-tokens/deltas), so tests can tell
// apart real usage accounting from a naive delta-count proxy — the exact
// defect this type exists to fix (see otel.go's attrGenAIOutputTokens vs.
// attrStreamDeltaCount).
func stubUsageFor(system, prompt string, words []string) Usage {
	return Usage{
		InputTokens:  len(strings.Fields(system)) + len(strings.Fields(prompt)),
		OutputTokens: len(strings.Join(words, "")),
	}
}

// stubWordsFor picks between the schema-aware structured reply (when prompt
// carries a structured-output instruction) and the default free-text echo.
func stubWordsFor(system, prompt string) []string {
	if reply, ok := stubStructuredReply(prompt); ok {
		return []string{reply}
	}
	return buildStubWords(system, prompt)
}

// schemaInstructionMarker is the exact prefix outputInstruction (gollem.go)
// prepends when it asks the model for structured JSON output. StubClient
// looks for it in prompt to decide whether to emit a schema-satisfying JSON
// object instead of its default free-text echo.
const schemaInstructionMarker = "Respond ONLY with a valid JSON object that exactly satisfies"

// stubStructuredReply recognizes a structured-output instruction embedded in
// prompt (outputInstruction, gollem.go) and returns a deterministic JSON
// object satisfying the schema's declared "properties": every string
// property gets a canned, non-empty value (so downstream business validation
// like "reply must be non-empty" has something real to check), and every
// other declared JSON type gets a schema-appropriate valid placeholder.
// Returns ok=false when prompt carries no such instruction, or the embedded
// schema does not parse — callers fall back to the default free-text echo in
// that case, never a silent malformed reply.
func stubStructuredReply(prompt string) (string, bool) {
	idx := strings.Index(prompt, schemaInstructionMarker)
	if idx < 0 {
		return "", false
	}
	nl := strings.IndexByte(prompt[idx:], '\n')
	if nl < 0 {
		return "", false
	}
	schemaText := prompt[idx+nl:]
	userPart := strings.TrimSpace(prompt[:idx])

	var schema map[string]any
	if err := json.Unmarshal([]byte(schemaText), &schema); err != nil {
		return "", false
	}

	props, _ := schema["properties"].(map[string]any)
	obj := make(map[string]any, len(props))
	for name, raw := range props {
		propSchema, _ := raw.(map[string]any)
		obj[name] = stubValueFor(propSchema, name, userPart)
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// stubValueFor builds one deterministic placeholder value for a single JSON
// Schema property, based on its declared "type". userPart is the caller's
// original prompt content (schema instruction already stripped), folded into
// string-typed fields so the reply is recognizably derived from the request.
func stubValueFor(propSchema map[string]any, name, userPart string) any {
	typ, _ := propSchema["type"].(string)
	switch typ {
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "array":
		return []any{}
	case jsonSchemaTypeObject:
		return map[string]any{}
	default: // "string" or unspecified: a canned, always non-empty value.
		return fmt.Sprintf("stub-llm structured reply for %q (field %q)", userPart, name)
	}
}

// buildStubWords builds the canned response as a slice of "words" (each one
// includes the following space/punctuation so concatenating all the tokens
// reconstructs the exact text).
func buildStubWords(system, prompt string) []string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "(no prompt)"
	}
	reply := fmt.Sprintf(
		"stub-llm: received your message \"%s\" (system=%q). "+
			"This is a word-by-word echo to verify SSE streaming "+
			"end-to-end without needing any LLM provider key.",
		prompt, system,
	)

	fields := strings.Fields(reply)
	words := make([]string, len(fields))
	for i, f := range fields {
		if i < len(fields)-1 {
			words[i] = f + " "
		} else {
			words[i] = f
		}
	}
	return words
}
