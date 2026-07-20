package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aisotropy-live-runner/scoring"
)

// This file is the OFFLINE gate on the task suite itself. It makes zero model
// calls and needs no adapter: it loads the REAL arenas/live/tasks/suite.jsonl
// and arenas/live/_shared/rag_questions.jsonl, builds the work items for every
// mode, and asserts each item is actually SCORABLE by the deterministic scorer.
//
// WHY IT EXISTS: the suite was saturated (47/48 framework x mode cells at a
// perfect 1.0) because the tasks were trivial for any competent model — the
// board measured adapter plumbing, not framework capability. The fix adds a
// HARD tier whose difficulty lives in the framework's job (multi-hop
// retrieval, overwritten/long-range memory, chained and negative tool use,
// strict schema conformance). A hard task with a sloppy ground truth would
// produce FALSE failures, which is strictly worse than saturation — so every
// new task's ground truth is asserted here, offline, before any billable run.

func liveDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve live dir: %v", err)
	}
	return dir
}

func loadRealSuite(t *testing.T) []task {
	t.Helper()
	tasks, err := loadSuite(filepath.Join(liveDir(t), "tasks", "suite.jsonl"))
	if err != nil {
		t.Fatalf("load suite: %v", err)
	}
	return tasks
}

func loadRealRag(t *testing.T) []ragQuestion {
	t.Helper()
	qs, err := loadRagQuestions(filepath.Join(liveDir(t), "_shared", "rag_questions.jsonl"))
	if err != nil {
		t.Fatalf("load rag questions: %v", err)
	}
	return qs
}

// TestSuite_EveryTaskHasGroundTruth asserts no gradable suite task can silently
// ship without the ground truth its dimension is scored on.
func TestSuite_EveryTaskHasGroundTruth(t *testing.T) {
	tasks := loadRealSuite(t)
	if len(tasks) < 25 {
		t.Fatalf("suite shrank to %d tasks — the easy tier is the regression baseline and must not be deleted", len(tasks))
	}
	for _, ts := range tasks {
		switch ts.Kind {
		case "single_shot_qa", "multi_turn_memory":
			if len(ts.Answer) == 0 {
				t.Errorf("task %s (%s) has no answer key — it would be scored by the weaker legacy token-containment fallback", ts.ID, ts.Kind)
			}
			for _, a := range ts.Answer {
				if strings.TrimSpace(a) == "" {
					t.Errorf("task %s has an empty answer key entry", ts.ID)
				}
			}
			if strings.TrimSpace(ts.Reference) == "" {
				t.Errorf("task %s has no reference", ts.ID)
			}
		case "tool_calling":
			if ts.ExpectedTool == "" {
				t.Errorf("task %s has no expected_tool", ts.ID)
			}
		}
	}
}

// TestSuite_AnswerKeysAreSatisfiableAndDiscriminating is the false-failure
// guard: for every QA/memory task, a MODEL REPLY THAT STATES THE REFERENCE must
// PASS (so a correct framework is never failed by a sloppy key), and an
// obviously wrong or evasive reply must FAIL (so the key actually discriminates).
func TestSuite_AnswerKeysAreSatisfiableAndDiscriminating(t *testing.T) {
	for _, ts := range loadRealSuite(t) {
		if ts.Kind != "single_shot_qa" && ts.Kind != "multi_turn_memory" {
			continue
		}
		if sc := scoring.ScoreQA(ts.Reference, ts.Reference, ts.Answer); !sc.Pass {
			t.Errorf("task %s: the REFERENCE answer itself does not satisfy the answer key %v (%s) — this key would false-fail a correct framework",
				ts.ID, ts.Answer, sc.Detail)
		}
		for _, wrong := range []string{"I don't know.", "I'm not sure, sorry."} {
			if sc := scoring.ScoreQA(wrong, ts.Reference, ts.Answer); sc.Pass {
				t.Errorf("task %s: evasive reply %q PASSES the key %v — the key does not discriminate", ts.ID, wrong, ts.Answer)
			}
		}
	}
}

// TestSuite_HardTierPresent pins the discriminating tier so a future edit
// cannot quietly revert the suite to the saturated easy tier.
func TestSuite_HardTierPresent(t *testing.T) {
	byID := map[string]task{}
	for _, ts := range loadRealSuite(t) {
		byID[ts.ID] = ts
	}
	for _, id := range []string{
		"qa-h01-bones", "qa-h02-canberra", "qa-h03-iron", "qa-h04-berlin", "qa-h05-boiling", "qa-h06-minutes",
		"mem-h01-overwrite", "mem-h02-interference", "mem-h03-longhaul", "mem-h04-multifact", "mem-h05-derived",
	} {
		if _, ok := byID[id]; !ok {
			t.Errorf("hard-tier task %s is missing from the suite", id)
		}
	}
	// The easy tier is the regression baseline — it must survive too.
	for _, id := range []string{"qa-01-capital", "qa-02-gold", "mem-01-color", "mem-02-pets"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("easy-tier baseline task %s was deleted — it is the saturation regression baseline", id)
		}
	}
}

// TestMemoryHardTier_RecallIsNotTheLastTurn proves the hard memory tasks are
// actually hard: the recalled fact must NOT be restatable from the final user
// turn alone, and (for the overwrite task) a stale earlier value must FAIL.
func TestMemoryHardTier_RecallIsNotTheLastTurn(t *testing.T) {
	byID := map[string]task{}
	for _, ts := range loadRealSuite(t) {
		byID[ts.ID] = ts
	}
	for id, staleWrongReply := range map[string]string{
		"mem-h01-overwrite":    "You are booked in seat 14C.", // the ORIGINAL, since-overwritten seat
		"mem-h02-interference": "Cleo is at desk 47.",         // another team member's desk
		"mem-h04-multifact":    "The codename is Harrow.",     // partial recall: 1 of 3 facts
		"mem-h05-derived":      "You have 1200 credits left.", // the un-decremented budget
	} {
		ts, ok := byID[id]
		if !ok {
			t.Fatalf("task %s missing", id)
		}
		if sc := scoring.ScoreMemory(staleWrongReply, ts.Reference, ts.Answer); sc.Pass {
			t.Errorf("task %s: stale/partial reply %q PASSES — the task does not discriminate (%s)", id, staleWrongReply, sc.Detail)
		}
		if sc := scoring.ScoreMemory(ts.Reference, ts.Reference, ts.Answer); !sc.Pass {
			t.Errorf("task %s: the correct reply does not pass its own key (%s)", id, sc.Detail)
		}
	}
	// The long-haul needle must be stated far from the recall turn: at least
	// 12 messages of distance, or a full-history replay is indistinguishable
	// from real long-range memory.
	lh := byID["mem-h03-longhaul"]
	if len(lh.Messages) < 13 {
		t.Errorf("mem-h03-longhaul has only %d messages — too short to stress long-range recall", len(lh.Messages))
	}
}

// TestRagSuite_GroundTruthIsCoherent checks every rag question against the
// corpus that actually exists on disk: a gold doc that is not a real file is a
// guaranteed false failure, and a trap with gold docs is a contradiction.
func TestRagSuite_GroundTruthIsCoherent(t *testing.T) {
	qs := loadRealRag(t)
	if len(qs) < 19 {
		t.Fatalf("rag question set shrank to %d — the hard tier or the baseline was deleted", len(qs))
	}
	corpus := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(liveDir(t), "_shared", "corpus"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	for _, e := range entries {
		corpus[strings.ToLower(e.Name())] = true
	}
	multiHop := 0
	traps := 0
	for _, q := range qs {
		if q.Trap {
			traps++
			if len(q.SupportingDocs) != 0 {
				t.Errorf("rag %s is a trap but declares gold docs %v", q.ID, q.SupportingDocs)
			}
			continue
		}
		if len(q.SupportingDocs) == 0 {
			t.Errorf("rag %s is grounded but declares no gold doc", q.ID)
		}
		for _, d := range q.SupportingDocs {
			if !corpus[strings.ToLower(d)] {
				t.Errorf("rag %s cites gold doc %q which is not in _shared/corpus — guaranteed false failure", q.ID, d)
			}
		}
		if keys := ragAnswerKeys[q.ID]; len(keys) > 0 {
			multiHop++
			if len(q.SupportingDocs) < 2 {
				t.Errorf("rag %s carries a multi-hop answer key but only %d gold doc(s)", q.ID, len(q.SupportingDocs))
			}
			// The reference must satisfy the key, or a correct framework fails.
			sc := scoring.ScoreRAGMultiHop(q.SupportingDocs, q.SupportingDocs, q.Reference, keys)
			if !sc.Pass {
				t.Errorf("rag %s: its own reference does not satisfy the multi-hop key %v (%s)", q.ID, keys, sc.Detail)
			}
			if sc := scoring.ScoreRAGMultiHop(q.SupportingDocs[:1], q.SupportingDocs, q.Reference, keys); sc.Pass {
				t.Errorf("rag %s: retrieving only ONE hop passes — multi-hop scoring is not discriminating", q.ID)
			}
		}
	}
	if multiHop < 4 {
		t.Errorf("only %d multi-hop rag questions — the hard tier requires at least 4", multiHop)
	}
	if traps < 6 {
		t.Errorf("only %d trap questions — the hard tier requires at least 6", traps)
	}
}

// TestBuildWork_AllModesOffline drives the real files through buildWork for
// every mode and asserts each produced item carries the ground truth its Kind
// will be scored on. Pure/offline: no adapter is spawned, no model is called.
func TestBuildWork_AllModesOffline(t *testing.T) {
	tasks := loadRealSuite(t)
	rag := loadRealRag(t)
	total := 0
	for _, mode := range allModes {
		items := buildWork("fake-fw", mode, tasks, rag, 0)
		if len(items) == 0 {
			t.Fatalf("mode %s produced no work items", mode)
		}
		total += len(items)
		for _, it := range items {
			if len(it.Invocations) == 0 {
				t.Errorf("%s/%s has no invocations", mode, it.ID)
			}
			switch it.Kind {
			case "rag_grounded":
				if len(it.SupportingDocs) == 0 {
					t.Errorf("%s: grounded rag item with no gold docs", it.ID)
				}
			case "rag_trap":
				// nothing to carry: the signal is refusal.
			case "tools":
				if it.ExpectedNumber == "" && len(it.Answer) == 0 {
					t.Errorf("%s: tools item has neither an expected number nor an abstain answer key", it.ID)
				}
			case "qa", "memory", "structured", "stream":
				if strings.TrimSpace(it.Reference) == "" {
					t.Errorf("%s/%s has no reference", mode, it.ID)
				}
			}
		}
	}
	t.Logf("offline dry run: %d work items built across %d modes", total, len(allModes))
}

// TestStrictStructured_IsOptInAndValid pins the two things that make the strict
// structured envelope safe to ship: it is INERT unless the operator opts in
// (no adapter reads adapterInput.Schema yet — see structuredSchemas), and the
// schema it would enforce is itself valid, non-trivial JSON Schema.
func TestStrictStructured_IsOptInAndValid(t *testing.T) {
	t.Setenv("ARENA_STRICT_STRUCTURED", "")
	tasks := loadRealSuite(t)
	for _, it := range buildWork("fake-fw", "structured", tasks, nil, 0) {
		if len(it.Invocations[0].Schema) != 0 {
			t.Errorf("%s: a schema was attached with ARENA_STRICT_STRUCTURED unset — the default run must be byte-identical to the old behaviour", it.ID)
		}
	}

	t.Setenv("ARENA_STRICT_STRUCTURED", "1")
	withSchema := 0
	for _, it := range buildWork("fake-fw", "structured", tasks, nil, 0) {
		if len(it.Invocations[0].Schema) == 0 {
			continue
		}
		withSchema++
		if !strings.Contains(it.Invocations[0].System, "conforms EXACTLY") {
			t.Errorf("%s: schema attached but the system prompt does not carry it — adapters that ignore the schema field would be unfairly failed", it.ID)
		}
		var sch map[string]any
		if err := json.Unmarshal(it.Invocations[0].Schema, &sch); err != nil {
			t.Fatalf("%s: attached schema is not valid JSON: %v", it.ID, err)
		}
	}
	if withSchema != 6 {
		t.Errorf("want 6 hard-tier structured tasks carrying the strict schema, got %d", withSchema)
	}

	// A bare object — what every framework passes today — must fail the strict
	// schema, and a conformant one must pass. This is the saturation fix.
	if sc := scoring.ScoreStructuredWithSchema(map[string]any{"answer": "Canberra"}, []byte(strictAnswerSchema)); sc.Pass {
		t.Errorf("a bare JSON object must NOT satisfy the strict schema, got %+v", sc)
	}
	ok := map[string]any{
		"answer": "Canberra", "confidence": 0.9, "category": "geography",
		"sources": []any{"general knowledge"},
		"meta":    map[string]any{"reasoning_steps": 1},
	}
	if sc := scoring.ScoreStructuredWithSchema(ok, []byte(strictAnswerSchema)); !sc.Pass {
		t.Errorf("a conformant object must satisfy the strict schema, got %+v", sc)
	}
}
