package facts

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// maxFactContent bounds a distilled fact's length. Facts are terse statements,
// not transcripts.
const maxFactContent = 280

// HeuristicExtractor is the Phase 3 online extractor: a deterministic,
// dependency-free scan of the user's message for explicit memory-worthy
// statements (durable preferences, identity facts, directives to remember).
// It emits AT MOST ONE fact per turn — the highest-priority signal — because
// the rich, multi-fact distillation (dedup, contradiction resolution,
// summarized episodes) belongs to the sleep cycle (Phase 6), not the hot path.
//
// It intentionally does not call an LLM: it must be cheap and side-effect-free
// to run inline in the post-response goroutine, and deterministic so its
// behavior is testable without credentials.
type HeuristicExtractor struct{}

// extractionRule is one prioritized pattern. The first rule that matches the
// message wins; its capture group (or whole match) becomes the fact content.
type extractionRule struct {
	re         *regexp.Regexp
	importance float64
	// group is the submatch index used as content; 0 means the whole match.
	group int
}

// rules are ordered most-specific/most-important first. The first match wins.
var rules = []extractionRule{
	// Explicit directives to remember carry the most weight.
	{regexp.MustCompile(`(?i)\bremember that\b(.+)`), 0.9, 1},
	{regexp.MustCompile(`(?i)\bremember\b(.+)`), 0.85, 1},
	{regexp.MustCompile(`(?i)\bplease note\b(.+)`), 0.7, 1},
	{regexp.MustCompile(`(?i)\bnote that\b(.+)`), 0.7, 1},
	// Identity / hard preference statements ("my name is Ada", "my timezone is UTC").
	{regexp.MustCompile(`(?i)\bmy\s+[a-z][a-z ]{1,38}\s+is\s+.+`), 0.8, 0},
	// First-person stable preferences.
	{regexp.MustCompile(`(?i)\bi\s+(?:prefer|like|love|hate|always|never|want|need)\b.+`), 0.7, 0},
}

// Extract returns at most one candidate fact distilled from turn.UserMessage.
// The assistant's reply is not mined in Phase 3 (that is a sleep-cycle concern).
func (HeuristicExtractor) Extract(sc Scope, turn Turn) []Fact {
	msg := strings.TrimSpace(turn.UserMessage)
	if msg == "" {
		return nil
	}
	for _, r := range rules {
		m := r.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		raw := m[0]
		if r.group > 0 && r.group < len(m) {
			raw = m[r.group]
		}
		content := cleanContent(raw)
		if content == "" {
			continue
		}
		return []Fact{{
			ID:         factID(sc, content),
			TenantID:   sc.Tenant(),
			UserID:     sc.User(),
			Content:    content,
			Importance: r.importance,
		}}
	}
	return nil
}

// cleanContent trims, collapses internal whitespace, strips leading filler and
// trailing sentence punctuation, and caps length.
func cleanContent(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, " ,:;-")
	s = strings.TrimRight(s, " .,:;!?")
	if len(s) > maxFactContent {
		s = strings.TrimSpace(s[:maxFactContent])
	}
	return s
}

// factID derives a stable, idempotent id from (tenant, user, normalized
// content). Re-stating the same fact upserts the same row rather than
// duplicating; a genuinely different statement gets a new id (and can later be
// linked via Supersede).
func factID(sc Scope, content string) string {
	h := sha256.New()
	h.Write([]byte(sc.Tenant()))
	h.Write([]byte{0})
	h.Write([]byte(sc.User()))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(content)))
	return "fact-" + hex.EncodeToString(h.Sum(nil)[:16])
}
