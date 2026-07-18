package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAICompat_EnableReasoning proves EnableReasoning(effort) makes the
// request payload advertise reasoning_effort (ADR-0061 request-side gate),
// that "off" is a no-op that leaves it disabled, and that an unset (default)
// client never sends the field — mirroring the Anthropic client's equivalent
// behavior, now exercised for the OpenAI-compatible client too.
func TestOpenAICompat_EnableReasoning(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	t.Run("disabled by default", func(t *testing.T) {
		c := NewOpenAICompatClient("k", "m", srv.URL, "test")
		ch, err := c.StreamChat(context.Background(), "", "hi")
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		for range ch { //nolint:revive // drain
		}
		if strings.Contains(gotBody, "reasoning_effort") {
			t.Fatalf("default client sent reasoning_effort unexpectedly: %s", gotBody)
		}
	})

	t.Run("enabled at medium", func(t *testing.T) {
		c := NewOpenAICompatClient("k", "m", srv.URL, "test")
		c.EnableReasoning(ReasoningEffortMedium)
		ch, err := c.StreamChat(context.Background(), "", "hi")
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		for range ch { //nolint:revive // drain
		}
		if !strings.Contains(gotBody, `"reasoning_effort":"medium"`) {
			t.Fatalf("expected reasoning_effort=medium in request body: %s", gotBody)
		}
	})

	t.Run("off is a no-op that disables reasoning", func(t *testing.T) {
		c := NewOpenAICompatClient("k", "m", srv.URL, "test")
		c.EnableReasoning(ReasoningEffortMedium)
		c.EnableReasoning(ReasoningEffortOff)
		ch, err := c.StreamChat(context.Background(), "", "hi")
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		for range ch { //nolint:revive // drain
		}
		if strings.Contains(gotBody, "reasoning_effort") {
			t.Fatalf("EnableReasoning(off) did not disable reasoning: %s", gotBody)
		}
	})
}

// TestSendToken_CtxCancelReturnsFalse proves sendToken's ctx-cancellation
// branch: with an unbuffered channel nobody reads and an already-cancelled
// ctx, sendToken must return false instead of blocking forever on the send.
func TestSendToken_CtxCancelReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan Token) // unbuffered, no reader
	if sendToken(ctx, ch, Token{Text: "x"}) {
		t.Fatal("expected sendToken to return false for a cancelled ctx with no reader")
	}
}

// TestOpenAICompat_TruncatedStreamPropagatesError reproduces the failure mode
// where the provider's SSE body is cut mid-stream (network drop / truncated
// response). bufio.Scanner.Scan() returns false on such a read error exactly
// like it does on a clean EOF, so without checking sc.Err() the client would
// emit a normal Done and the caller would mistake a truncated answer for a
// finished one. This test asserts the error is surfaced as a Token.Err instead.
func TestOpenAICompat_TruncatedStreamPropagatesError(t *testing.T) {
	// Server that sends one valid SSE token then abruptly closes the TCP
	// connection mid-chunked-body (no terminating 0-chunk, no [DONE]) — the
	// HTTP client then reads an unexpected EOF, i.e. sc.Err() != nil.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter is not a Hijacker")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\n" +
			"Content-Type: text/event-stream\r\n" +
			"Transfer-Encoding: chunked\r\n\r\n")
		chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
		// Write one well-formed chunk, flush, then close without the final
		// zero-length chunk → truncated body.
		_, _ = fmt.Fprintf(bufrw, "%x\r\n%s\r\n", len(chunk), chunk)
		_ = bufrw.Flush()
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "test-model", srv.URL, "test")
	ch, err := c.StreamChat(context.Background(), "", "hello")
	if err != nil {
		t.Fatalf("StreamChat setup failed: %v", err)
	}

	var (
		gotText string
		lastErr error
		sawDone bool
	)
	for tok := range ch {
		switch {
		case tok.Err != nil:
			lastErr = tok.Err
		case tok.Done:
			sawDone = true
		default:
			gotText += tok.Text
		}
	}

	if gotText != "hi" {
		t.Errorf("expected the pre-truncation token %q, got %q", "hi", gotText)
	}
	if lastErr == nil {
		t.Fatal("expected a Token.Err from the truncated stream, got none " +
			"(the read error was masked as a clean finish — the bug)")
	}
	if sawDone {
		t.Error("a truncated stream must NOT emit Done=true")
	}
}
