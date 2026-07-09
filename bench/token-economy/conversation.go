// Package main implements bench/token-economy: an offline, deterministic
// harness that compares input-token consumption between two context-assembly
// strategies over the same synthetic multi-turn conversation. See README.md
// in this directory for methodology, assumptions, and limitations.
package main

import (
	"fmt"
	"math/rand"
	"strings"
)

// Turn is one synthetic chat exchange: a user message and the assistant's
// reply to it.
type Turn struct {
	User      string
	Assistant string
}

// Config controls synthetic-conversation generation. All fields are
// deterministic given the same Seed: GenerateConversation(cfg) called twice
// with an identical Config produces byte-identical output (see
// TestGenerateConversationDeterministic in bench_test.go).
type Config struct {
	// Seed drives the PRNG. The same Seed always yields the same
	// conversation, which is what makes this benchmark reproducible without
	// any network access or API key.
	Seed int64
	// Turns is the number of user/assistant exchange pairs to generate.
	Turns int
	// UserMinWords/UserMaxWords bound the (inclusive) word count of each
	// synthetic user message — a realistic short chat-input range.
	UserMinWords, UserMaxWords int
	// AssistantMinWords/AssistantMaxWords bound the (inclusive) word count
	// of each synthetic assistant reply — assistant replies are modeled as
	// longer than user prompts, which matches typical chat-agent transcripts.
	AssistantMinWords, AssistantMaxWords int
}

// DefaultConfig returns the harness's default synthetic-conversation
// parameters: a 50-turn conversation (matching the depths this benchmark
// reports at — turns 10/25/50), short user prompts (8-40 words) and longer
// assistant replies (20-150 words), seeded for reproducibility.
func DefaultConfig() Config {
	return Config{
		Seed:              42,
		Turns:             50,
		UserMinWords:      8,
		UserMaxWords:      40,
		AssistantMinWords: 20,
		AssistantMaxWords: 150,
	}
}

// vocabulary is a small, fixed word list used to synthesize pseudo-realistic
// sentences. It is deliberately generic chat-support/product-question
// vocabulary rather than lorem-ipsum filler, so message length statistics
// (word count, average word length) resemble real chat text without
// depending on any external corpus or network fetch.
var vocabulary = []string{
	"account", "after", "again", "answer", "api", "app", "application",
	"available", "billing", "browser", "bug", "button", "cancel", "cannot",
	"change", "chat", "check", "click", "code", "config", "configuration",
	"connect", "could", "create", "current", "dashboard", "data", "delete",
	"details", "device", "different", "does", "download", "email", "enable",
	"endpoint", "error", "example", "expected", "export", "feature", "field",
	"file", "fix", "following", "format", "found", "happens", "help", "here",
	"however", "import", "install", "integration", "invoice", "issue",
	"latest", "link", "list", "log", "login", "make", "message", "method",
	"missing", "need", "network", "notification", "option", "page",
	"password", "payment", "permission", "plan", "please", "preview",
	"problem", "product", "profile", "project", "provide", "receive",
	"recent", "refresh", "region", "reload", "report", "request",
	"response", "restart", "result", "retry", "screen", "screenshot",
	"search", "section", "server", "session", "setting", "should", "show",
	"since", "status", "step", "still", "subscription", "support", "sync",
	"system", "tenant", "thanks", "that", "then", "there", "these", "this",
	"time", "timeout", "token", "trying", "unable", "update", "upgrade",
	"usage", "user", "using", "value", "version", "want", "webhook", "when",
	"where", "which", "while", "window", "with", "without", "working",
	"workspace", "would",
}

// GenerateConversation deterministically synthesizes cfg.Turns user/assistant
// exchange pairs. The same Config always produces the same output — no
// wall-clock time, no global rand state, no filesystem or network access —
// which is what lets this benchmark print identical numbers across runs
// (acceptance criterion: "two consecutive runs print identical numbers").
func GenerateConversation(cfg Config) []Turn {
	rng := rand.New(rand.NewSource(cfg.Seed))
	out := make([]Turn, cfg.Turns)
	for i := range out {
		out[i] = Turn{
			User:      randomSentence(rng, cfg.UserMinWords, cfg.UserMaxWords),
			Assistant: randomSentence(rng, cfg.AssistantMinWords, cfg.AssistantMaxWords),
		}
	}
	return out
}

// randomSentence builds a deterministic pseudo-sentence of between minWords
// and maxWords (inclusive) words drawn from vocabulary, capitalized and
// terminated like a real chat message.
func randomSentence(rng *rand.Rand, minWords, maxWords int) string {
	n := minWords
	if maxWords > minWords {
		n += rng.Intn(maxWords - minWords + 1)
	}
	words := make([]string, n)
	for i := range words {
		words[i] = vocabulary[rng.Intn(len(vocabulary))]
	}
	sentence := strings.Join(words, " ")
	if sentence == "" {
		return ""
	}
	return fmt.Sprintf("%s%s.", strings.ToUpper(sentence[:1]), sentence[1:])
}
