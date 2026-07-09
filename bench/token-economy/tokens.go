package main

import (
	"math"
	"strings"
)

// This benchmark has no LLM API access (no ANTHROPIC_API_KEY in this
// environment), so there is no real tokenizer to call. Token counts below
// are approximations using two independent, widely cited heuristics — both
// are reported side by side everywhere in this harness's output so a reader
// can judge the sensitivity of the results to the estimation method, rather
// than being given a single number that looks more precise than it is.

// EstimateTokensByBytes approximates a token count as len(text)/4, rounded
// up. "~4 bytes per token" is a commonly cited rule of thumb for English
// text under modern BPE tokenizers (OpenAI's own docs use this rule; see
// README.md "Token counting methodology").
func EstimateTokensByBytes(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

// EstimateTokensByWords approximates a token count as
// whitespace_word_count * 1.33, another commonly cited rule of thumb
// ("~0.75 words per token", i.e. ~1.33 tokens per word, for English BPE
// tokenizers; see README.md "Token counting methodology").
func EstimateTokensByWords(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	words := strings.Fields(s)
	return int(math.Ceil(float64(len(words)) * 1.33))
}

// TokenEstimate bundles both heuristics for a single piece of text so
// callers never have to pick one estimator over the other; both are always
// carried and reported together.
type TokenEstimate struct {
	ByBytes int
	ByWords int
}

// Estimate computes both token-count heuristics for s.
func Estimate(s string) TokenEstimate {
	return TokenEstimate{
		ByBytes: EstimateTokensByBytes(s),
		ByWords: EstimateTokensByWords(s),
	}
}

// Add returns the component-wise sum of two TokenEstimates.
func (a TokenEstimate) Add(b TokenEstimate) TokenEstimate {
	return TokenEstimate{ByBytes: a.ByBytes + b.ByBytes, ByWords: a.ByWords + b.ByWords}
}

// ReductionPercent returns how much smaller estimate a is than baseline b,
// as a percentage: (1 - a/b) * 100. A positive result means a is cheaper
// than b; a negative result means a is more expensive than b. Returns 0 if
// b is 0 (undefined reduction against an empty baseline).
func ReductionPercent(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return (1 - float64(a)/float64(b)) * 100
}
