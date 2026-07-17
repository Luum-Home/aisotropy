package main

import (
	"fmt"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// structuredAnswer is the Go struct Genkit derives a JSON Schema from
// (ai.WithOutputType) and validates the model's response against
// (ai.ModelResponse.Output). A schema violation is a hard error from Genkit
// itself, not something this adapter parses loosely — that is the point of
// exercising native structured output rather than asking the model for JSON
// in free text and best-effort-decoding it.
type structuredAnswer struct {
	Answer     string   `json:"answer" jsonschema_description:"The direct answer to the user's question, in one or two sentences."`
	Confidence float64  `json:"confidence" jsonschema_description:"Self-reported confidence in the answer, from 0.0 (guessing) to 1.0 (certain)."`
	KeyPoints  []string `json:"key_points" jsonschema_description:"0-5 short bullet points supporting the answer."`
}

// runStructured forces agnes's reply into a validated structured object via
// Genkit's native ai.WithOutputType (JSON-Schema-constrained output), then
// decodes it with ai.ModelResponse.Output rather than ad-hoc JSON parsing of
// free text.
func runStructured(rc *runCtx, out *arenaOutput) error {
	question := lastUserMessage(rc.in.Messages)
	if question == "" {
		return fmt.Errorf("structured mode: no user message/question in request")
	}

	opts := []ai.GenerateOption{
		ai.WithModel(rc.model),
		ai.WithPrompt(question),
		ai.WithOutputType(structuredAnswer{}),
	}
	if rc.in.System != "" {
		opts = append(opts, ai.WithSystem(rc.in.System))
	}

	start := time.Now()
	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("structured: genkit.Generate: %w", err)
	}
	applyUsage(out, resp)

	var sa structuredAnswer
	if err := resp.Output(&sa); err != nil {
		return fmt.Errorf("structured: response failed schema validation: %w", err)
	}
	out.Structured = sa
	out.Reply = sa.Answer
	return nil
}
