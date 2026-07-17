package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// flushRecorder is an io.Writer that also implements http.Flusher, so the
// flusher-discovery walk in newAISDKStream finds it directly.
type flushRecorder struct {
	bytes.Buffer
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++ }

// errWriter fails every Write, so the aisdkStream.part write-error branches
// (which abort the turn on a client disconnect) are exercised.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

// TestNewAISDKStream_FlusherDiscovery covers both flusher-discovery outcomes: a
// plain writer yields a nil flusher (no panic on flush), and a writer that
// implements http.Flusher is discovered so per-part flushing works.
func TestNewAISDKStream_FlusherDiscovery(t *testing.T) {
	t.Run("plain writer has no flusher", func(t *testing.T) {
		s := newAISDKStream(&bytes.Buffer{})
		if s.flusher != nil {
			t.Error("a plain io.Writer must yield a nil flusher")
		}
		s.flush() // must be a no-op, not a panic
	})
	t.Run("flusher discovered", func(t *testing.T) {
		fr := &flushRecorder{}
		s := newAISDKStream(fr)
		if s.flusher == nil {
			t.Fatal("http.Flusher writer must be discovered")
		}
		if err := s.part(map[string]string{"type": "text-delta", "delta": "hi"}); err != nil {
			t.Fatalf("part: %v", err)
		}
		if fr.flushes == 0 {
			t.Error("part must flush after writing a frame")
		}
	})
}

// TestAISDKStream_PartFraming covers the SSE framing: a part is written as
// `data: ${JSON}\n\n` with HTML escaping disabled so <, >, & stream verbatim.
func TestAISDKStream_PartFraming(t *testing.T) {
	fr := &flushRecorder{}
	s := newAISDKStream(fr)
	if err := s.part(map[string]string{"delta": "a < b & c > d"}); err != nil {
		t.Fatalf("part: %v", err)
	}
	out := fr.String()
	if !strings.HasPrefix(out, "data: ") || !strings.HasSuffix(out, "\n\n") {
		t.Errorf("frame = %q, want data: ...\\n\\n framing", out)
	}
	if !strings.Contains(out, "a < b & c > d") {
		t.Errorf("frame = %q, want the un-escaped literal < > &", out)
	}
}

// TestAISDKStream_PartWriteErrorPropagates covers the write-error branch: when
// the underlying writer fails (client disconnect), part returns the error so
// StreamCore aborts without recording cost.
func TestAISDKStream_PartWriteErrorPropagates(t *testing.T) {
	s := newAISDKStream(errWriter{})
	if err := s.part(map[string]string{"delta": "x"}); err == nil {
		t.Fatal("part must propagate a write failure so the turn can abort")
	}
}

// TestAISDKStream_PartEncodeError covers the JSON-encode error branch: a value
// that cannot be marshalled (a channel) returns an error before any write.
func TestAISDKStream_PartEncodeError(t *testing.T) {
	s := newAISDKStream(&bytes.Buffer{})
	if err := s.part(make(chan int)); err == nil {
		t.Fatal("part must return the encode error for an unmarshalable value")
	}
}

// TestAISDKStream_Done writes the [DONE] terminator and flushes.
func TestAISDKStream_Done(t *testing.T) {
	fr := &flushRecorder{}
	s := newAISDKStream(fr)
	s.done()
	if !strings.Contains(fr.String(), "[DONE]") {
		t.Errorf("done frame = %q, want the [DONE] sentinel", fr.String())
	}
}

// TestWriteAGUIFrame covers the AG-UI SSE frame encoder: a normal event is
// framed as `data: ${JSON}\n\n` and flushed; a failing writer surfaces the
// write error.
func TestWriteAGUIFrame(t *testing.T) {
	t.Run("normal frame", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ev := map[string]string{"type": "TEXT_MESSAGE_CONTENT", "delta": "hello"}
		if err := writeAGUIFrame(rec, rec, ev); err != nil {
			t.Fatalf("writeAGUIFrame: %v", err)
		}
		out := rec.Body.String()
		if !strings.HasPrefix(out, "data: ") || !strings.HasSuffix(out, "\n\n") {
			t.Errorf("frame = %q, want data: ...\\n\\n", out)
		}
		if !strings.Contains(out, "hello") {
			t.Errorf("frame = %q, want the event payload", out)
		}
	})
	t.Run("write error", func(t *testing.T) {
		if err := writeAGUIFrame(errWriter{}, nil, map[string]string{"type": "x"}); err == nil {
			t.Fatal("writeAGUIFrame must surface a write failure")
		}
	})
}

// TestAGUIMessageText covers content normalization: the bare-string form, the
// array-of-parts form (text parts contribute, others ignored), an empty
// content, and unparsable content (leniently "").
func TestAGUIMessageText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", ``, ""},
		{"string form", `"hello world"`, "hello world"},
		{"typed text parts", `[{"type":"text","text":"a"},{"type":"image","text":"IGNORED"},{"type":"text","text":"b"}]`, "ab"},
		{"untyped part carries text", `[{"text":"x"}]`, "x"},
		{"unparsable yields empty", `{"unexpected":true}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			if got := aguiMessageText(raw); got != tc.want {
				t.Errorf("aguiMessageText(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestLatestUserMessageText picks the newest user message and ignores later
// assistant/system messages.
func TestLatestUserMessageText(t *testing.T) {
	msgs := []aguiMessage{
		{Role: "user", Content: json.RawMessage(`"first"`)},
		{Role: "assistant", Content: json.RawMessage(`"reply"`)},
		{Role: "user", Content: json.RawMessage(`"latest question"`)},
		{Role: "system", Content: json.RawMessage(`"policy"`)},
	}
	if got := latestUserMessageText(msgs); got != "latest question" {
		t.Errorf("latestUserMessageText = %q, want the newest user message", got)
	}
	if got := latestUserMessageText([]aguiMessage{{Role: "assistant", Content: json.RawMessage(`"x"`)}}); got != "" {
		t.Errorf("latestUserMessageText with no user message = %q, want empty", got)
	}
}

// TestAGUISession maps a threadId onto a session key, substituting the fallback
// when omitted and passing a provided one through.
func TestAGUISession(t *testing.T) {
	if got := aguiSession(""); got != aguiDefaultThread {
		t.Errorf("aguiSession(\"\") = %q, want the default thread %q", got, aguiDefaultThread)
	}
	if got := aguiSession("  "); got != aguiDefaultThread {
		t.Errorf("aguiSession(whitespace) = %q, want the default thread", got)
	}
	if got := aguiSession("thread-42"); got != "thread-42" {
		t.Errorf("aguiSession(\"thread-42\") = %q, want it passed through", got)
	}
}
