package main

import (
	"regexp"
	"strings"
	"testing"
)

// This file is the OFFLINE gate on the memory-extension dimensions
// (docs/architecture/arena-memory-extensions.md): suite ground truth, the
// varies-session/holds-user work-item shape, the additive user_id wire field,
// scorer dispatch, opt-in/leak containment, and the permanent semantic-recall
// zero-overlap fixture guard. Zero model calls, zero adapters spawned.

// memExtStopWords mirrors the scoring package's stopWords EXACTLY (scoring.go).
// Kept as a test-local copy so the fixture guard below tokenizes identically to
// the scorer without exporting scoring internals — a change to the scorer's set
// that this test must track is a deliberate, visible edit here.
var memExtStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true,
	"were": true, "of": true, "in": true, "on": true, "at": true, "to": true,
	"and": true, "or": true, "it": true, "this": true, "that": true, "be": true,
	"has": true, "have": true, "with": true, "for": true, "as": true,
}

var memExtPunct = regexp.MustCompile(`[^\w\s]`)
var memExtSpace = regexp.MustCompile(`\s+`)

// memExtTokens replicates scoring.normalize+tokenize (byte-identical rules) so
// the zero-overlap assertion measures exactly what the scorer would.
func memExtTokens(s string) map[string]bool {
	s = strings.ToLower(s)
	s = memExtPunct.ReplaceAllString(s, " ")
	s = memExtSpace.ReplaceAllString(s, " ")
	out := map[string]bool{}
	for _, tok := range strings.Fields(s) {
		if tok == "" || memExtStopWords[tok] {
			continue
		}
		out[tok] = true
	}
	return out
}

func loadMemoryExtTasks(t *testing.T) []task {
	t.Helper()
	var out []task
	for _, ts := range loadRealSuite(t) {
		if isMemoryExtKind(ts.Kind) {
			out = append(out, ts)
		}
	}
	return out
}

// TestMemoryExtSuite_GroundTruth asserts every memory-extension task ships the
// ground truth ScoreMemory needs: an answer key that IS satisfied by the
// reference (so a correct framework is never false-failed) and IS NOT satisfied
// by an evasive reply (so the key discriminates), a persona, and — for the
// recall turn — a question that is NOT answerable from the final turn alone.
func TestMemoryExtSuite_GroundTruth(t *testing.T) {
	sg := loadMemoryExtTasks(t)
	if len(sg) != 6 {
		t.Fatalf("want 6 memory-extension suite tasks (2 per dimension), got %d", len(sg))
	}
	perKind := map[string]int{}
	for _, ts := range sg {
		perKind[ts.Kind]++
		if strings.TrimSpace(ts.System) == "" {
			t.Errorf("%s: no system persona — the uniform persona is the fairness contract", ts.ID)
		}
		if strings.TrimSpace(ts.Reference) == "" {
			t.Errorf("%s: no reference", ts.ID)
		}
		if len(ts.Answer) == 0 {
			t.Errorf("%s: no answer key — ScoreMemory would fall back to weaker token-containment", ts.ID)
		}
		// The reference must satisfy its own key; an evasive reply must not.
		if sc := scoreMemoryHelper(ts.Reference, ts.Reference, ts.Answer); !sc {
			t.Errorf("%s: the REFERENCE does not satisfy the answer key %v — would false-fail a correct framework", ts.ID, ts.Answer)
		}
		for _, wrong := range []string{"I don't know.", "I don't have that saved for this session."} {
			if sc := scoreMemoryHelper(wrong, ts.Reference, ts.Answer); sc {
				t.Errorf("%s: evasive reply %q PASSES the key %v — the key does not discriminate", ts.ID, wrong, ts.Answer)
			}
		}
	}
	for _, k := range []string{"memory_cross_session", "memory_semantic_recall", "memory_consolidation"} {
		if perKind[k] != 2 {
			t.Errorf("want 2 tasks of kind %s, got %d", k, perKind[k])
		}
	}
}

// TestMemorySemantic_ZeroTokenOverlap is the PERMANENT fixture guard the design
// requires: for every memory_semantic_recall task, the NEEDLE statement (the
// first user message, by convention) and the RECALL question (the last user
// message) must share ZERO non-stopword tokens. This pins "semantic, not
// lexical" as a deterministic property of the fixture, so a raw-transcript
// replayer cannot pass by keyword-matching the question against the needle — it
// must actually retrieve on meaning. A future edit that reintroduces a shared
// content word fails here, offline, before any billable run.
func TestMemorySemantic_ZeroTokenOverlap(t *testing.T) {
	found := 0
	for _, ts := range loadMemoryExtTasks(t) {
		if ts.Kind != "memory_semantic_recall" {
			continue
		}
		found++
		if len(ts.Messages) < 13 {
			t.Errorf("%s: only %d messages — too short to stress long-range semantic recall", ts.ID, len(ts.Messages))
		}
		idx := lastUserIndex(ts.Messages)
		if idx <= 0 || ts.Messages[0].Role != "user" {
			t.Fatalf("%s: expected the needle as the first user message and a recall question last", ts.ID)
		}
		needle := memExtTokens(ts.Messages[0].Content)
		question := memExtTokens(ts.Messages[idx].Content)
		var shared []string
		for tok := range question {
			if needle[tok] {
				shared = append(shared, tok)
			}
		}
		if len(shared) > 0 {
			t.Errorf("%s: needle and recall question share non-stopword tokens %v — that makes the recall LEXICAL, not semantic", ts.ID, shared)
		}
	}
	if found != 2 {
		t.Fatalf("expected 2 memory_semantic_recall tasks, found %d", found)
	}
}

// TestBuildMemoryExt_WireShape proves the load-bearing mechanic: every
// memory-extension work item goes over the wire as ordinary mode:"memory"
// invocations carrying the additive user_id (never a new adapter mode), with
// the user_id HELD constant while the session_id VARIES across invocations, and
// exactly 2 invocations for cross-session/semantic (persist, recall) or 3 for
// consolidation (persist, consolidate, recall).
func TestBuildMemoryExt_WireShape(t *testing.T) {
	tasks := loadRealSuite(t)
	for _, mode := range []string{"memory_cross_session", "memory_semantic_recall", "memory_consolidation"} {
		items := buildWork("fake-fw", mode, tasks, nil, 0)
		if len(items) != 2 {
			t.Fatalf("%s: want 2 work items, got %d", mode, len(items))
		}
		wantInvs := 2
		if mode == "memory_consolidation" {
			wantInvs = 3
		}
		for _, it := range items {
			if it.Mode != mode || it.Kind != mode {
				t.Errorf("%s/%s: Mode=%q Kind=%q, want both %q", mode, it.ID, it.Mode, it.Kind, mode)
			}
			if len(it.Invocations) != wantInvs {
				t.Fatalf("%s/%s: want %d invocations, got %d", mode, it.ID, wantInvs, len(it.Invocations))
			}
			// user_id present, constant, non-empty across every invocation; wire
			// mode is always "memory"; sessions vary (persist != recall).
			user := it.Invocations[0].UserID
			if strings.TrimSpace(user) == "" {
				t.Errorf("%s/%s: first invocation carries no user_id", mode, it.ID)
			}
			sessions := map[string]bool{}
			for i, inv := range it.Invocations {
				if inv.Mode != "memory" {
					t.Errorf("%s/%s inv %d: wire mode %q — a memory-ext invocation must be an ordinary memory turn (zero new adapter mode)", mode, it.ID, i, inv.Mode)
				}
				if inv.UserID != user {
					t.Errorf("%s/%s inv %d: user_id=%q drifted from %q — the user must be HELD across invocations", mode, it.ID, i, inv.UserID, user)
				}
				if strings.TrimSpace(inv.SessionID) == "" {
					t.Errorf("%s/%s inv %d: empty session_id", mode, it.ID, i)
				}
				sessions[inv.SessionID] = true
			}
			// The recall (last) invocation MUST be a DIFFERENT session than the
			// persist (first) — the whole point: a session-keyed store finds
			// nothing, only user-scoped memory recalls.
			first := it.Invocations[0].SessionID
			last := it.Invocations[len(it.Invocations)-1].SessionID
			if first == last {
				t.Errorf("%s/%s: persist and recall share session %q — a cross-session probe MUST vary the session", mode, it.ID, first)
			}
			if len(sessions) < 2 {
				t.Errorf("%s/%s: only %d distinct session(s) — session must vary", mode, it.ID, len(sessions))
			}
			// The consolidation middle invocation carries the directive.
			if mode == "memory_consolidation" {
				if it.Invocations[1].Messages[0].Content != consolidateDirective {
					t.Errorf("%s/%s: middle invocation is not the consolidate directive", mode, it.ID)
				}
				if it.Invocations[1].SessionID != first {
					t.Errorf("%s/%s: consolidate must run on the persist session A, got %q vs %q", mode, it.ID, it.Invocations[1].SessionID, first)
				}
			}
			if len(it.Answer) == 0 {
				t.Errorf("%s/%s: answer key did not reach the workItem", mode, it.ID)
			}
		}
	}
}

// TestMemoryExt_IsOptInAndLeakProof pins the cost/containment contract: the
// default mode set excludes the three memory-extension modes (a default run
// pays zero extra calls), the vocabulary includes them (so `-modes
// memory_cross_session` works), and no memory-ext task leaks into the plain
// memory/qa/rag builds.
func TestMemoryExt_IsOptInAndLeakProof(t *testing.T) {
	for _, m := range defaultModes {
		if isMemoryExtKind(m) {
			t.Fatalf("%s is in defaultModes — the memory-extension dimensions must be opt-in", m)
		}
	}
	for _, want := range []string{"memory_cross_session", "memory_semantic_recall", "memory_consolidation"} {
		found := false
		for _, m := range allModes {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from allModes — -modes %s would be silently dropped", want, want)
		}
	}

	tasks, rag := loadRealSuite(t), loadRealRag(t)
	for _, mode := range []string{"qa", "structured", "stream", "rag", "memory"} {
		for _, it := range buildWork("fake-fw", mode, tasks, rag, 0) {
			if isMemoryExtKind(it.Kind) {
				t.Errorf("memory-ext item %s leaked into default mode %s — a default sweep would silently pay for it", it.ID, mode)
			}
		}
	}
	// And the reverse: a memory-ext build never pulls in plain multi_turn_memory.
	for _, it := range buildWork("fake-fw", "memory_cross_session", tasks, rag, 0) {
		if !strings.HasPrefix(it.ID, "xsession-") {
			t.Errorf("memory_cross_session pulled in a non-cross-session task: %s", it.ID)
		}
	}
}

// TestScoreDeterministic_MemoryExtDispatch proves the three memory-ext kinds
// reach ScoreMemory: a reply containing the answer key passes, an evasive reply
// fails — identical mechanics to plain memory recall.
func TestScoreDeterministic_MemoryExtDispatch(t *testing.T) {
	cases := []struct {
		kind, reply, ref, key string
		wantPass              bool
	}{
		{"memory_cross_session", "Your employee ID is 4471-ZP.", "Your employee ID is 4471-ZP.", "4471-ZP", true},
		{"memory_cross_session", "I have no record of that.", "Your employee ID is 4471-ZP.", "4471-ZP", false},
		{"memory_semantic_recall", "It's under the third flowerpot on the balcony.", "The spare key is under the third flowerpot.", "flowerpot", true},
		{"memory_semantic_recall", "I'm not sure where that is.", "The spare key is under the third flowerpot.", "flowerpot", false},
		{"memory_consolidation", "You chose Marigold as the codename.", "You chose Marigold as the codename.", "Marigold", true},
		{"memory_consolidation", "You didn't tell me a codename.", "You chose Marigold as the codename.", "Marigold", false},
	}
	for _, c := range cases {
		it := workItem{Kind: c.kind, Reference: c.ref, Answer: []string{c.key}}
		rec := &record{Out: adapterOutput{Reply: c.reply}}
		sc := scoreDeterministic(rec, it)
		if sc.Pass != c.wantPass {
			t.Errorf("%s reply=%q: Pass=%v want %v (%s)", c.kind, c.reply, sc.Pass, c.wantPass, sc.Detail)
		}
	}
}

// scoreMemoryHelper is a tiny wrapper so the ground-truth test reads clearly:
// true iff the reply passes ScoreMemory against reference+answer.
func scoreMemoryHelper(reply, reference string, answers []string) bool {
	it := workItem{Kind: "memory_cross_session", Reference: reference, Answer: answers}
	rec := &record{Out: adapterOutput{Reply: reply}}
	return scoreDeterministic(rec, it).Pass
}
