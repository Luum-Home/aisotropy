package o11y

// OWASP LLM02:2025 — Sensitive Information Disclosure (structured logging).
//
// The slog CtxHandler hydrates each log record with ONLY the telemetry and
// identity buckets from ctx (trace_id/span_id/request_id/session_id and
// tenant_id/user_id). There is deliberately NO content bucket in the ADR-0068
// vocabulary, so prompts/responses cannot ride this seam onto a log line: the
// correlation fields survive (needed for trace<->log joins) while content is
// structurally absent. This suite asserts both properties against a real slog
// pipeline writing to a buffer. Deterministic: no model, no network.
// See docs/security/owasp-llm-coverage.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/o11y/keys"
)

// TestOWASP_LLM02_CtxHandlerHydratesIdentityNotContent: a ctx carrying identity +
// telemetry keys hydrates those onto the record; a content value is only present
// if the CALL SITE logged it (the handler never injects content on its own).
func TestOWASP_LLM02_CtxHandlerHydratesIdentityNotContent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewCtxHandler(slog.NewJSONHandler(&buf, nil)))

	ctx := context.Background()
	ctx = keys.With(ctx, keys.TenantID, "acme")
	ctx = keys.With(ctx, keys.UserID, "alice")
	ctx = keys.With(ctx, keys.TraceID, "0123456789abcdef")
	ctx = keys.With(ctx, keys.RequestID, "req-42")

	logger.InfoContext(ctx, "served chat request")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, buf.String())
	}

	// Correlation/identity must survive onto the record.
	for k, want := range map[string]string{
		"tenant_id":  "acme",
		"user_id":    "alice",
		"trace_id":   "0123456789abcdef",
		"request_id": "req-42",
	} {
		if got, _ := rec[k].(string); got != want {
			t.Errorf("log field %q = %v, want %q", k, rec[k], want)
		}
	}

	// The handler must not have invented any content field.
	for _, forbidden := range []string{"prompt", "response", "content", "message_body", "db.statement", "gollem.agent.prompt"} {
		if _, present := rec[forbidden]; present {
			t.Errorf("content field %q must never be auto-hydrated onto a log line", forbidden)
		}
	}
}

// TestOWASP_LLM02_VocabularyHasNoContentKey: the typed key vocabulary the handler
// iterates (telemetry + identity buckets) contains no content-bearing key — a
// structural guarantee that content cannot leak through this seam. keys.All only
// ever surfaces buckets fed by these constants.
func TestOWASP_LLM02_VocabularyHasNoContentKey(t *testing.T) {
	contentish := map[keys.Key]bool{
		"prompt": true, "response": true, "content": true,
		"message": true, "body": true, "text": true, "db.statement": true,
	}
	for _, bucket := range [][]keys.Key{keys.TelemetryBucket, keys.IdentityBucket} {
		for _, k := range bucket {
			if contentish[k] {
				t.Errorf("hydrated bucket contains a content-bearing key %q — telemetry must carry correlation only", k)
			}
		}
	}
}

// TestOWASP_LLM02_NoMetadataIsByteIdenticalPassthrough: a ctx with no metadata
// (any non-request log line) delegates UNCHANGED — the wrapper never adds fields
// out of nowhere.
func TestOWASP_LLM02_NoMetadataIsByteIdenticalPassthrough(t *testing.T) {
	var wrapped, plain bytes.Buffer
	w := slog.New(NewCtxHandler(slog.NewJSONHandler(&wrapped, nil)))
	p := slog.New(slog.NewJSONHandler(&plain, nil))

	w.InfoContext(context.Background(), "startup", "component", "boot")
	p.InfoContext(context.Background(), "startup", "component", "boot")

	// Compare the non-time fields (time differs per call): parse both and drop time.
	dropTime := func(b []byte) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		delete(m, "time")
		return m
	}
	gotW, gotP := dropTime(wrapped.Bytes()), dropTime(plain.Bytes())
	if len(gotW) != len(gotP) {
		t.Errorf("no-metadata ctx must pass through unchanged: wrapped=%v plain=%v", gotW, gotP)
	}
	for k, v := range gotP {
		if gotW[k] != v {
			t.Errorf("field %q differs: wrapped=%v plain=%v", k, gotW[k], v)
		}
	}
}
