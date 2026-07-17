package memory

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAppendOversizedValueTruncatedAndPersisted covers case (a): a message whose
// Content exceeds the per-value cap is truncated (with the marker) and the write
// SUCCEEDS — the oversized value never reaches BadgerDB to fail there — and it
// reads back truncated.
func TestAppendOversizedValueTruncatedAndPersisted(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	// A value comfortably past the cap. Using a single-byte rune ('a') keeps the
	// arithmetic exact for the assertions below.
	huge := strings.Repeat("a", wm.maxMessageValueBytes+5000)
	if err := wm.Append(ctx, key, Message{Role: "assistant", Content: huge}); err != nil {
		t.Fatalf("Append of oversized value must SUCCEED (truncation guard), got: %v", err)
	}

	got, err := wm.Recent(ctx, key, 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent returned %d messages, want 1", len(got))
	}
	stored := got[0].Content

	if len(stored) >= len(huge) {
		t.Fatalf("stored content len %d not shorter than original %d — not truncated", len(stored), len(huge))
	}
	if !strings.Contains(stored, "[truncated:") {
		t.Errorf("stored content missing truncation marker; tail=%q", tail(stored, 60))
	}
	// The kept prefix (everything before the marker) must be within the cap.
	prefix := stored[:strings.Index(stored, " …[truncated:")]
	if len(prefix) > wm.maxMessageValueBytes {
		t.Errorf("kept prefix len %d exceeds cap %d", len(prefix), wm.maxMessageValueBytes)
	}
	// The full stored value (prefix + marker) must stay well under BadgerDB's
	// ~64 KiB per-value ceiling — that is the whole point of the guard.
	if len(stored) >= 64<<10 {
		t.Errorf("stored value len %d is not under BadgerDB's ~64 KiB ceiling", len(stored))
	}
}

// TestAppendNormalValueUntouched covers case (b): a message that fits under the
// cap is persisted and read back byte-for-byte, with no marker.
func TestAppendNormalValueUntouched(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "bob", Session: "s1"}

	want := "a perfectly normal chat message with some UTF-8: café, naïve, 日本語 😀"
	if err := wm.Append(ctx, key, Message{Role: "user", Content: want}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := wm.Recent(ctx, key, 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent returned %d messages, want 1", len(got))
	}
	if got[0].Content != want {
		t.Errorf("normal value was altered:\n got=%q\nwant=%q", got[0].Content, want)
	}
	if strings.Contains(got[0].Content, "[truncated:") {
		t.Errorf("normal value must not carry a truncation marker: %q", got[0].Content)
	}
}

// TestCapMessageContentUTF8Safe covers case (c): truncation never splits a
// multi-byte rune, even when the cut point lands mid-rune.
func TestCapMessageContentUTF8Safe(t *testing.T) {
	// 4-byte runes (😀 = U+1F600, 4 bytes in UTF-8). With any cap that is not a
	// multiple of 4, a naive byte cut would land inside a rune. capMessageContent
	// must back up to a rune boundary.
	emoji := strings.Repeat("😀", 4000) // 16000 bytes
	for _, cap := range []int{10, 11, 12, 13, 4001, 4002, 4003} {
		capped, over, truncated := capMessageContent(emoji, cap)
		if !truncated {
			t.Fatalf("cap=%d: expected truncation of a %d-byte value", cap, len(emoji))
		}
		if over != len(emoji)-cap {
			t.Errorf("cap=%d: over=%d, want %d", cap, over, len(emoji)-cap)
		}
		prefix := capped[:strings.Index(capped, " …[truncated:")]
		if !utf8.ValidString(prefix) {
			t.Errorf("cap=%d: kept prefix is not valid UTF-8 (rune split)", cap)
		}
		// The marker itself is valid UTF-8, so the whole value must be too.
		if !utf8.ValidString(capped) {
			t.Errorf("cap=%d: full capped value is not valid UTF-8", cap)
		}
	}
}

// TestAppendTruncationWarnsWithoutContent covers case (d): truncation emits a
// WARN that carries identity + sizes but never the message content.
func TestAppendTruncationWarnsWithoutContent(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "carol", Session: "s1"}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A distinctive marker string in the content lets us prove it is NOT logged.
	secretNeedle := "SUPERSECRET_CANARY_STRING"
	huge := secretNeedle + strings.Repeat("z", wm.maxMessageValueBytes+1000)
	if err := wm.Append(ctx, key, Message{Role: "assistant", Content: huge}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "truncated") {
		t.Errorf("expected a WARN about truncation, got: %q", logged)
	}
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("expected WARN level, got: %q", logged)
	}
	if !strings.Contains(logged, "tenant=acme") || !strings.Contains(logged, "user=carol") {
		t.Errorf("expected identity (tenant/user) in the WARN, got: %q", logged)
	}
	if !strings.Contains(logged, "over_bytes=") || !strings.Contains(logged, "cap_bytes=") {
		t.Errorf("expected size fields in the WARN, got: %q", logged)
	}
	if strings.Contains(logged, secretNeedle) {
		t.Errorf("WARN leaked message content (found canary): %q", logged)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
