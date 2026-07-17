package facts

import (
	"strings"
	"unicode"
)

// Language-aware FTS tokenization shim (ADR-0051).
//
// The facts FTS5 index uses `tokenize='unicode61 remove_diacritics 2'`.
// unicode61 accent-folds Latin/Cyrillic/Greek and splits on spaces and
// punctuation, but it does NOT word-segment space-less scripts: a run of Han,
// Kana, Hangul, or Thai characters with no interior spaces collapses to a
// single token, so substring/full-text search over such text returns nothing.
// The correct fix is the ICU tokenizer, but modernc.org/sqlite is pure-Go and
// does not link ICU (verified: `CREATE VIRTUAL TABLE ... tokenize='icu'` fails
// with "no such tokenizer: icu"). The pure-Go workaround is to re-express each
// space-less run as overlapping character bigrams so unicode61 emits one token
// per bigram. ftsIndexText does this at write time; ftsQuery does the identical
// transform at query time, so index and query agree.
//
// This is an honest partial fix: bigrams give substring recall for space-less
// scripts but NOT true morphological segmentation (word boundaries, stemming) —
// that needs ICU or a language-specific segmenter, neither available in-process
// under the pure-Go driver. Latin/Cyrillic/Greek search is unchanged apart from
// the deliberate loss of English porter stemming (see schema.go / ADR-0051).

// segmentlessScripts are the Unicode scripts unicode61 cannot word-segment
// because they are conventionally written without spaces between words. Runs of
// these characters get the bigram treatment.
var segmentlessScripts = []*unicode.RangeTable{
	unicode.Han,      // CJK ideographs — Chinese, Japanese Kanji, Korean Hanja
	unicode.Hiragana, // Japanese
	unicode.Katakana, // Japanese
	unicode.Hangul,   // Korean
	unicode.Thai,     // Thai
}

// isSegmentless reports whether r belongs to a space-less script that unicode61
// will not split into word tokens.
func isSegmentless(r rune) bool {
	return unicode.In(r, segmentlessScripts...)
}

// ftsIndexText prepares raw fact text for the unicode61 FTS tokenizer. Every
// character that is NOT part of a space-less script is emitted verbatim, so
// unicode61 tokenizes Latin/Cyrillic/Greek exactly as it would the original.
// Each maximal run of space-less characters is rewritten as space-separated
// overlapping character bigrams (a 1-character run becomes that single
// character), each surrounded by spaces so it cannot fuse with adjacent Latin
// text. The result is stored in facts.fts_content and fed to the index.
func ftsIndexText(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/2)
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if !isSegmentless(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && isSegmentless(runes[j]) {
			j++
		}
		b.WriteByte(' ')
		writeBigrams(&b, runes[i:j])
		b.WriteByte(' ')
		i = j
	}
	return b.String()
}

// writeBigrams appends the space-separated overlapping character bigrams of run
// to b. A single-character run yields that one character (so lone ideographs
// stay searchable). Each bigram is two adjacent characters with no interior
// space, so unicode61 tokenizes it as exactly one token.
func writeBigrams(b *strings.Builder, run []rune) {
	if len(run) == 1 {
		b.WriteRune(run[0])
		return
	}
	for k := 0; k+1 < len(run); k++ {
		if k > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(run[k])
		b.WriteRune(run[k+1])
	}
}

// ftsQuery turns arbitrary user text into a safe FTS5 MATCH expression: it
// extracts word tokens and OR-joins them as quoted terms, so punctuation or
// FTS5 operator characters in the input can never produce a syntax error or
// injection. Space-less-script runs are decomposed into the same character
// bigrams ftsIndexText produced, so they match the bigram-indexed content;
// unicode61's own remove_diacritics folding is applied by FTS to each quoted
// term, so accented Latin queries match accent-folded indexed text without any
// Go-side normalization. Returns "" when the input has no usable tokens.
func ftsQuery(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		terms = appendQueryTerms(terms, f)
	}
	return strings.Join(terms, " OR ")
}

// appendQueryTerms decomposes one whitespace/punctuation-delimited field into
// FTS MATCH terms, mirroring ftsIndexText: a maximal non-space-less subrun
// becomes a single quoted, lower-cased term (single-character noise dropped);
// a maximal space-less subrun becomes one quoted term per character bigram.
func appendQueryTerms(terms []string, field string) []string {
	runes := []rune(field)
	for i := 0; i < len(runes); {
		seg := isSegmentless(runes[i])
		j := i
		for j < len(runes) && isSegmentless(runes[j]) == seg {
			j++
		}
		sub := runes[i:j]
		switch {
		case seg:
			terms = appendBigramTerms(terms, sub)
		case len(sub) >= 2: // drop single-character Latin noise, as before
			terms = append(terms, `"`+strings.ToLower(string(sub))+`"`)
		}
		i = j
	}
	return terms
}

// appendBigramTerms appends one quoted MATCH term per overlapping character
// bigram of run (a single-character run yields one single-character term).
func appendBigramTerms(terms []string, run []rune) []string {
	if len(run) == 1 {
		return append(terms, `"`+string(run[0])+`"`)
	}
	for k := 0; k+1 < len(run); k++ {
		terms = append(terms, `"`+string(run[k:k+2])+`"`)
	}
	return terms
}
