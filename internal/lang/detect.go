package lang

import (
	"strings"
	"sync"

	lingua "github.com/pemistahl/lingua-go"
)

// defaultLanguages is the subset the shared detector is restricted to. lingua-go
// is deliberately NOT loaded with all 75 languages: FromAllLanguages in
// high-accuracy mode holds ~1.8 GB of n-gram models resident, which is
// unacceptable for an in-process µs-cheap request-path seam. Restricting the set
// to the languages we actually care about AND enabling low-accuracy mode (see
// newDetector) bounds the loaded models — the memory cost scales with the number
// of selected languages, not the full catalogue.
//
// The set covers the Phase-2 multilingual corpus (research §4): Latin-script
// ES/PT/FR/DE/IT, CJK JA/ZH/KO, Cyrillic RU, Arabic AR, plus the English
// baseline. It is a package var (not a literal) so a future integrator can widen
// or narrow it without touching Detect.
var defaultLanguages = []lingua.Language{
	lingua.English,
	lingua.Spanish,
	lingua.Portuguese,
	lingua.French,
	lingua.German,
	lingua.Italian,
	lingua.Japanese,
	lingua.Chinese,
	lingua.Korean,
	lingua.Arabic,
	lingua.Russian,
}

// detector is the process-wide lingua-go detector, built once and reused. It is
// safe for concurrent use (lingua's LanguageDetector is read-only after Build),
// so a single instance serves every request goroutine. Building it is the
// expensive step (it loads the n-gram models), which is exactly why it is
// memoized behind sync.Once rather than constructed per call.
var (
	detectorOnce sync.Once
	detector     lingua.LanguageDetector
)

// newDetector builds a low-accuracy, language-restricted lingua-go detector.
// Low-accuracy mode trades a small amount of precision for a large memory
// reduction and is the documented choice for short text (the chat case) — the
// n-gram models loaded are a fraction of the high-accuracy set.
func newDetector() lingua.LanguageDetector {
	return lingua.NewLanguageDetectorBuilder().
		FromLanguages(defaultLanguages...).
		WithLowAccuracyMode().
		Build()
}

// sharedDetector returns the memoized process-wide detector, building it on
// first use.
func sharedDetector() lingua.LanguageDetector {
	detectorOnce.Do(func() { detector = newDetector() })
	return detector
}

// Detect classifies the human language of text and returns its lowercase ISO
// 639-1 code, or Undetermined for empty/whitespace input or when lingua-go
// cannot confidently classify it.
//
// It is cheap (µs–ms on chat-length text) and safe for concurrent use: the
// underlying detector is built once (sharedDetector) and only read here. Detect
// inspects only the text it is given — the chat wiring passes the REDACTED
// prompt, so no secret text is examined beyond what already flows downstream.
func Detect(text string) Language {
	if strings.TrimSpace(text) == "" {
		return Undetermined
	}
	l, ok := sharedDetector().DetectLanguageOf(text)
	if !ok {
		return Undetermined
	}
	return fromLingua(l)
}

// fromLingua maps a lingua.Language to our ISO 639-1 Language. lingua's
// IsoCode639_1().String() yields the uppercase code (e.g. "EN", "JA"); we
// lowercase it to match the persona-registry key convention. An empty or
// "Unknown" code degrades to Undetermined rather than surfacing a bogus tag.
func fromLingua(l lingua.Language) Language {
	code := strings.ToLower(l.IsoCode639_1().String())
	if code == "" || code == "unknown" {
		return Undetermined
	}
	return Language(code)
}
