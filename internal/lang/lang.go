// Package lang is aisotropy's language-detection seam (research R1,
// docs/research/i18n-multilingual-readiness-2026-07-17.md).
//
// The runtime historically had NO i18n seam: nothing detected, carried, or
// routed on the user's language, so every semantic layer that has to understand
// text (the injection/toxicity heuristics, the persona selection, the facts FTS
// tokenizer, the default embedder) was hardcoded to English. This package is the
// missing keystone: it detects the human language of a chat prompt ONCE, at the
// request boundary, and carries the result on context.Context alongside the
// tenant_id/user_id identity that already propagates there.
//
// # The context contract (canonical)
//
// WithLanguage and FromContext, together with the Language type, are the single
// canonical contract downstream consumers read. The detection layer (the chat
// handlers, internal/api) is the ONLY writer — it calls WithLanguage once per
// request. Every consumer (personas — R5; and, later, language-conditional
// guardrails/tokenizer/embedder selection — R3/R4) is a reader: it calls
// FromContext rather than re-deriving the language, so detection happens exactly
// once and every layer agrees on the same value. Using a different ctx key than
// the one defined here would silently sever detection from its consumers, so
// there is intentionally exactly one key, defined in this package.
package lang

import "context"

// Language is a detected human language, represented as its lowercase ISO 639-1
// two-letter code — i.e. a BCP-47 primary language subtag (e.g. "en", "es",
// "ja", "zh"). Undetermined is the zero-signal value used when detection could
// not confidently classify the text; consumers treat it as "no language set"
// and fall back to their English/default behavior.
type Language string

// Language constants for the subset the default detector is restricted to. They
// are the lowercase ISO 639-1 codes and double as the persona-registration keys
// (internal/api/persona.go normalizes to the same lowercased form).
const (
	// Undetermined means detection ran but could not classify the text (empty
	// input, too short, or no confident match). FromContext reports it as "not
	// set" so consumers keep their default behavior.
	Undetermined Language = "und"

	English    Language = "en"
	Spanish    Language = "es"
	Portuguese Language = "pt"
	French     Language = "fr"
	German     Language = "de"
	Italian    Language = "it"
	Japanese   Language = "ja"
	Chinese    Language = "zh"
	Korean     Language = "ko"
	Arabic     Language = "ar"
	Russian    Language = "ru"
)

// String returns the ISO 639-1 code as a plain string. It lets a Language be
// used directly anywhere a string language tag is expected (e.g. the persona
// layer's string-keyed registry) without an explicit conversion at every call.
func (l Language) String() string { return string(l) }

// IsUndetermined reports whether l carries no usable language signal — either
// the explicit Undetermined value or the empty zero value.
func (l Language) IsUndetermined() bool { return l == "" || l == Undetermined }

// langCtxKey is the private context key under which the detected request
// language travels. It is unexported and package-private so the ONLY way to set
// or read the value is through WithLanguage/FromContext — there is exactly one
// key for the whole runtime, mirroring how internal/api scopes tenant/user with
// their own private ctx keys.
type langCtxKey struct{}

// WithLanguage returns a child context carrying l as the request's detected
// language. It is called ONCE per request by the detection layer (the chat
// handlers), at the same boundary where tenant_id/user_id are injected. Passing
// Undetermined is harmless: FromContext reports it as "not set".
func WithLanguage(ctx context.Context, l Language) context.Context {
	return context.WithValue(ctx, langCtxKey{}, l)
}

// FromContext returns the detected language carried on ctx and whether a usable
// one is present. The bool is false when no language was set OR when the carried
// value is Undetermined, so a consumer can branch on `if l, ok := FromContext(
// ctx); ok { ... }` and safely fall back to its default (English) behavior in
// the not-ok case.
func FromContext(ctx context.Context) (Language, bool) {
	l, ok := ctx.Value(langCtxKey{}).(Language)
	if !ok || l.IsUndetermined() {
		return Undetermined, false
	}
	return l, true
}
