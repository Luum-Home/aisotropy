package lang

import (
	"context"
	"testing"
)

// TestDetect_ClassifiesCommonLanguages asserts the seam classifies the core
// Phase-2 languages (research §4) correctly on chat-length text: English,
// Spanish, and Japanese are the acceptance-criteria set. Sentences are used
// (not single words) because low-accuracy mode is tuned for short text but a few
// words give it enough signal to be deterministic.
func TestDetect_ClassifiesCommonLanguages(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Language
	}{
		{"english", "Please summarize the quarterly report and highlight the risks.", English},
		{"spanish", "Por favor resume el informe trimestral y destaca los riesgos principales.", Spanish},
		{"japanese", "四半期報告書を要約して、主なリスクを教えてください。", Japanese},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.text); got != tc.want {
				t.Fatalf("Detect(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestDetect_EmptyIsUndetermined pins the cheap fast-path: empty or whitespace
// input never touches the detector and returns Undetermined.
func TestDetect_EmptyIsUndetermined(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		if got := Detect(in); got != Undetermined {
			t.Fatalf("Detect(%q) = %q, want Undetermined", in, got)
		}
	}
}

// TestWithLanguageFromContext_RoundTrip asserts the canonical ctx contract: a
// value written by WithLanguage is read back byte-identically by FromContext.
func TestWithLanguageFromContext_RoundTrip(t *testing.T) {
	ctx := WithLanguage(context.Background(), Spanish)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatalf("FromContext ok = false, want true after WithLanguage(Spanish)")
	}
	if got != Spanish {
		t.Fatalf("FromContext = %q, want %q", got, Spanish)
	}
}

// TestFromContext_Absent asserts a context with no language reports not-set, so
// consumers fall back to their default behavior.
func TestFromContext_Absent(t *testing.T) {
	got, ok := FromContext(context.Background())
	if ok {
		t.Fatalf("FromContext ok = true on empty ctx, want false")
	}
	if got != Undetermined {
		t.Fatalf("FromContext = %q on empty ctx, want Undetermined", got)
	}
}

// TestFromContext_UndeterminedIsNotSet asserts that explicitly carrying
// Undetermined is reported as not-set — detection ran but produced no usable
// signal, which consumers must treat identically to "absent".
func TestFromContext_UndeterminedIsNotSet(t *testing.T) {
	ctx := WithLanguage(context.Background(), Undetermined)
	if _, ok := FromContext(ctx); ok {
		t.Fatalf("FromContext ok = true for Undetermined, want false")
	}
}

// TestLanguage_Helpers covers the small value-type helpers.
func TestLanguage_Helpers(t *testing.T) {
	if English.String() != "en" {
		t.Fatalf("English.String() = %q, want \"en\"", English.String())
	}
	if !Undetermined.IsUndetermined() {
		t.Fatalf("Undetermined.IsUndetermined() = false, want true")
	}
	if !Language("").IsUndetermined() {
		t.Fatalf("empty Language.IsUndetermined() = false, want true")
	}
	if English.IsUndetermined() {
		t.Fatalf("English.IsUndetermined() = true, want false")
	}
}
