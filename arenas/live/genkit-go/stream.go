package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// runStream drives Genkit's native streaming generation (ai.WithStreaming)
// against agnes's real SSE stream (github.com/firebase/genkit/go/plugins/
// compat_oai supports streaming chat completions), recording ttft_ms — the
// wall-clock time from request start to the FIRST streamed chunk — which a
// non-streaming call cannot report at all.
func runStream(rc *runCtx, out *arenaOutput) error {
	opts := buildBaseOpts(rc)

	var once sync.Once
	var ttft time.Duration
	start := time.Now()

	cb := func(_ context.Context, chunk *ai.ModelResponseChunk) error {
		once.Do(func() { ttft = time.Since(start) })
		return nil
	}
	opts = append(opts, ai.WithStreaming(cb))

	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("stream: genkit.Generate: %w", err)
	}
	if ttft > 0 {
		out.TTFTMs = ttft.Milliseconds()
	}
	applyUsage(out, resp)
	out.Reply = resp.Text()
	return nil
}
