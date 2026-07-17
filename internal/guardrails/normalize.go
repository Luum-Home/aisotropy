package guardrails

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// This file is the R4 evasion-resistance layer (docs/security/guardrails-i18n-normalization.md).
//
// The built-in injection/toxicity heuristics match English literals. Without
// canonicalization they are trivially defeated — even for the English attacks
// they DO cover — by homoglyph substitution ("Ignоre" with a Cyrillic о),
// full-width forms ("ｉｇｎｏｒｅ"), zero-width joiners inside words, leetspeak
// ("1gn0r3"), or single-character spacing ("i g n o r e"). foldForMatch produces
// a canonical COPY of the text so those obfuscated variants collapse toward the
// detectable form before the existing patterns run. The stored/forwarded text is
// never mutated — only a matching copy is folded.
//
// HONEST SCOPE: this is canonicalization, not comprehension. It hardens the
// EXISTING (mostly English) patterns against obfuscation and makes obfuscated
// variants in ANY script collapse toward their base letters — but it does NOT
// add semantic coverage of a genuinely non-English insult or jailbreak. True
// multilingual toxicity/injection detection is a model problem (see the
// InputScanner seam in injection.go and the OutputCheck seam in output.go; the
// documented follow-on is an opt-in mDeBERTa-class classifier). You cannot regex
// your way to arbitrary-language safety.

// confusables folds a curated set of Cyrillic and Greek look-alike letters to
// their Latin twins. Keys are lower-case (foldForMatch lower-cases first), so
// only the lower-case code points are listed. The map is intentionally limited
// to unambiguous visual twins of ASCII letters that appear in the built-in
// attack/toxicity vocabulary — adding ambiguous glyphs would risk folding benign
// non-Latin text into accidental English triggers.
var confusables = map[rune]rune{
	// Cyrillic -> Latin
	'а': 'a', 'б': 'b', 'в': 'b', 'е': 'e', 'ё': 'e', 'з': 'e',
	'и': 'u', 'к': 'k', 'м': 'm', 'н': 'h', 'о': 'o', 'п': 'n',
	'р': 'p', 'с': 'c', 'т': 't', 'у': 'y', 'х': 'x', 'і': 'i',
	'ј': 'j', 'ѕ': 's', 'ԁ': 'd', 'һ': 'h', 'ԛ': 'q', 'ԝ': 'w',
	// Greek -> Latin
	'α': 'a', 'β': 'b', 'γ': 'y', 'δ': 'd', 'ε': 'e', 'ζ': 'z',
	'η': 'n', 'ι': 'i', 'κ': 'k', 'μ': 'u', 'ν': 'v', 'ο': 'o',
	'π': 'n', 'ρ': 'p', 'τ': 't', 'υ': 'y', 'χ': 'x', 'ϲ': 'c',
	'ѡ': 'w',
}

// leet folds common leetspeak digit/symbol substitutions to the Latin letter an
// evader is standing in for. Kept small and one-directional toward the letters
// that spell the built-in vocabulary. This can, in principle, rewrite a digit in
// benign text — but only a matching copy is affected, and a benign sentence's
// digits do not spell the multi-word English attack phrases the patterns require.
var leet = map[rune]rune{
	'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't',
	'@': 'a', '$': 's',
}

// isIgnorable reports whether r is an invisible/formatting code point that an
// evader inserts to split a keyword ("st​upid", "i‍gnore") without
// changing how the text renders. These are dropped from the matching copy so the
// underlying letters sit adjacent again. Combining marks (Mn) are also dropped so
// a base letter plus a decorative combining glyph folds to the bare letter.
func isIgnorable(r rune) bool {
	switch r {
	case '\u200B', // zero width space
		'\u200C', // zero width non-joiner
		'\u200D', // zero width joiner
		'\u2060', // word joiner
		'\uFEFF', // zero width no-break space / BOM
		'\u00AD', // soft hyphen
		'\u180E': // mongolian vowel separator
		return true
	}
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r)
}

// foldForMatch returns a canonicalized COPY of s for pattern matching. It never
// mutates or is stored in place of the original text. Pipeline:
//
//  1. NFKC — folds full-width, compatibility, and ligature forms to canonical
//     (e.g. "ｉｇｎｏｒｅ" -> "ignore", NBSP -> space).
//  2. lower-case — case-fold so casing tricks ("IgNoRe") collapse.
//  3. drop ignorable code points — remove zero-width joiners / format / combining
//     marks used to split keywords.
//  4. homoglyph + leet fold — map Cyrillic/Greek look-alikes and leet digits to
//     their Latin twins.
//
// Regular spaces are preserved (word-boundary regexes and multi-word toxicity
// keywords depend on them); intra-word spacing evasion is handled separately by
// collapseSpacedLetters.
func foldForMatch(s string) string {
	if s == "" {
		return ""
	}
	s = norm.NFKC.String(s)
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isIgnorable(r) {
			continue
		}
		if m, ok := confusables[r]; ok {
			r = m
		}
		if m, ok := leet[r]; ok {
			r = m
		}
		b.WriteRune(r)
	}
	return b.String()
}

// collapseSpacedLetters joins runs of two-or-more single-character,
// whitespace-separated tokens into one word, leaving multi-character words (and
// lone single characters like the articles "a"/"i") untouched. It turns
// "i g n o r e   the   p r e v i o u s   instructions" into
// "ignore the previous instructions" so the word-boundary injection regexes fire
// on character-spacing evasion, while preserving the inter-word spaces the
// regexes need as gaps.
func collapseSpacedLetters(s string) string {
	if s == "" {
		return ""
	}
	tokens := strings.Fields(s)
	if len(tokens) < 2 {
		return s
	}
	out := make([]string, 0, len(tokens))
	var run []string
	flush := func() {
		switch {
		case len(run) >= 2:
			out = append(out, strings.Join(run, ""))
		case len(run) == 1:
			out = append(out, run[0])
		}
		run = run[:0]
	}
	for _, tok := range tokens {
		if utf8.RuneCountInString(tok) == 1 {
			run = append(run, tok)
			continue
		}
		flush()
		out = append(out, tok)
	}
	flush()
	return strings.Join(out, " ")
}

// matchVariants returns the distinct non-empty strings to run detectors over: the
// original text plus its folded and space-collapsed forms, deduplicated. When the
// text is plain lower-case ASCII with no spacing tricks, all forms coincide and a
// single variant is returned (no extra regex work).
func matchVariants(text string) []string {
	folded := foldForMatch(text)
	collapsed := collapseSpacedLetters(folded)
	variants := make([]string, 0, 3)
	seen := map[string]bool{}
	for _, v := range []string{text, folded, collapsed} {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		variants = append(variants, v)
	}
	return variants
}
