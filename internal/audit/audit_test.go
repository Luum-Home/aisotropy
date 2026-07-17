package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleEvent() Event {
	return Event{
		Timestamp: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		TenantID:  "acme",
		UserID:    "alice",
		Method:    "POST",
		Route:     "/v1/tenant/acme/user/alice/chat",
		Status:    200,
		RequestID: "req-123",
		LatencyMS: 42,
	}
}

func TestJSONLSink_AppendsDurableLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	sink.Record(sampleEvent())
	e2 := sampleEvent()
	e2.UserID = "bob"
	e2.Status = 403
	sink.Record(e2)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open to prove the lines are durably on disk (survived Close/reopen).
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var lines []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal line %q: %v", sc.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 durable lines, got %d", len(lines))
	}
	if lines[0].TenantID != "acme" || lines[0].UserID != "alice" || lines[0].Status != 200 {
		t.Errorf("line 0 mismatch: %+v", lines[0])
	}
	if lines[1].UserID != "bob" || lines[1].Status != 403 {
		t.Errorf("line 1 mismatch: %+v", lines[1])
	}
}

func TestJSONLSink_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	s1, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	s1.Record(sampleEvent())
	if err := s1.Close(); err != nil {
		t.Fatalf("close s1: %v", err)
	}

	// A fresh sink on the same path must APPEND (O_APPEND), not truncate.
	s2, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink reopen: %v", err)
	}
	s2.Record(sampleEvent())
	if err := s2.Close(); err != nil {
		t.Fatalf("close s2: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := bytes.Count(data, []byte("\n")); got != 2 {
		t.Fatalf("want 2 appended lines, got %d", got)
	}
}

// TestEvent_NoBodyOrSecretFields is a schema guard: the Event JSON must carry
// identity + request metadata ONLY. If someone adds a field that could leak a
// prompt body, response, header, or secret, this test fails and forces a review.
func TestEvent_NoBodyOrSecretFields(t *testing.T) {
	e := sampleEvent()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	allowed := map[string]bool{
		"timestamp": true, "tenant_id": true, "user_id": true,
		"method": true, "route": true, "status": true,
		"request_id": true, "latency_ms": true,
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("unexpected audit field %q: audit Events must carry identity + request metadata only (no body/response/headers/secrets)", k)
		}
	}

	// Belt-and-suspenders: no forbidden substring anywhere in the serialized form.
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"prompt", "response", "password", "secret", "token", "authorization", "api_key", "apikey"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("serialized audit event contains forbidden token %q: %s", forbidden, raw)
		}
	}
}

func TestSlogSink_EmitsAccessRecordWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	sink := NewSlogSink(logger)
	sink.Record(sampleEvent())
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out := buf.String()
	// Structured fields present.
	for _, want := range []string{`"tenant_id":"acme"`, `"user_id":"alice"`, `"route":"/v1/tenant/acme/user/alice/chat"`, `"status":200`, `"component":"access_audit"`, `"audit":true`} {
		if !strings.Contains(out, want) {
			t.Errorf("slog output missing %s\ngot: %s", want, out)
		}
	}
	// No secret-shaped fields.
	if strings.Contains(strings.ToLower(out), "authorization") || strings.Contains(strings.ToLower(out), "password") {
		t.Errorf("slog output leaked a secret-shaped field: %s", out)
	}
}

func TestJSONLSink_ConcurrentRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}

	const goroutines = 32
	const perG = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				sink.Record(sampleEvent())
			}
		}()
	}
	wg.Wait()
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Every line must be a complete, parseable JSON object — proving no two
	// concurrent writes interleaved a partial line.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("interleaved/partial line detected: %q: %v", sc.Text(), err)
		}
		count++
	}
	if count != goroutines*perG {
		t.Fatalf("want %d lines, got %d", goroutines*perG, count)
	}
}

func TestFromEnv_Selection(t *testing.T) {
	t.Run("default is slog sink", func(t *testing.T) {
		t.Setenv(EnvLogPath, "")
		t.Setenv(EnvDisable, "")
		s, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		defer s.Close()
		if _, ok := s.(*SlogSink); !ok {
			t.Fatalf("want *SlogSink, got %T", s)
		}
	})

	t.Run("path selects JSONL sink", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a.jsonl")
		t.Setenv(EnvDisable, "")
		t.Setenv(EnvLogPath, path)
		s, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		defer s.Close()
		if _, ok := s.(*JSONLSink); !ok {
			t.Fatalf("want *JSONLSink, got %T", s)
		}
	})

	t.Run("disable selects NopSink", func(t *testing.T) {
		t.Setenv(EnvLogPath, filepath.Join(t.TempDir(), "a.jsonl"))
		t.Setenv(EnvDisable, "true")
		s, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		defer s.Close()
		if _, ok := s.(NopSink); !ok {
			t.Fatalf("want NopSink, got %T", s)
		}
	})

	t.Run("unopenable path fails the boot", func(t *testing.T) {
		t.Setenv(EnvDisable, "")
		// A path under a non-existent directory cannot be created.
		t.Setenv(EnvLogPath, filepath.Join(t.TempDir(), "nope", "deep", "a.jsonl"))
		if _, err := FromEnv(); err == nil {
			t.Fatal("want error for unopenable durable log path, got nil")
		}
	})
}
