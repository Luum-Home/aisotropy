package main

import (
	"strings"
	"testing"
)

// TestBuildForgetful_ShapeAndOmittedScope pins the load-bearing wire mechanic of
// the isolation_forgetful builder: a persist invocation under an explicit,
// unique-per-task user scope, then a RECALL invocation whose user scope is
// DELIBERATELY BLANK (the forgetful-integrator mistake) and whose wire mode is
// isolation_forgetful (so the adapter forwards the omission to the framework's
// own boundary, not adapter glue). The scoped answer key must ride along.
func TestBuildForgetful_ShapeAndOmittedScope(t *testing.T) {
	tasks := []task{{
		ID:   "forget-x",
		Kind: isolationForgetfulMode,
		Messages: []inMessage{
			{Role: "user", Content: "My favourite station is Kings Cross."},
			{Role: "assistant", Content: "Noted."},
			{Role: "user", Content: "What is my favourite station?"},
		},
		Reference: "meta",
		Answer:    []string{"Kings Cross"},
	}}
	items := buildForgetful("fake-fw", tasks, 0)
	if len(items) != 1 {
		t.Fatalf("buildForgetful produced %d items, want 1", len(items))
	}
	it := items[0]
	if it.Kind != isolationForgetfulMode {
		t.Errorf("kind = %q, want %q", it.Kind, isolationForgetfulMode)
	}
	if len(it.Answer) == 0 {
		t.Fatal("work item carries no scoped answer key — ScoreForgetful would have nothing to detect a leak against")
	}
	if len(it.Invocations) != 2 {
		t.Fatalf("want 2 invocations (persist + omitted-scope recall), got %d", len(it.Invocations))
	}
	persist, recall := it.Invocations[0], it.Invocations[1]
	if persist.Mode != "memory" || strings.TrimSpace(persist.UserID) == "" {
		t.Errorf("persist invocation must be wire-mode memory under an explicit user; got mode=%q user=%q", persist.Mode, persist.UserID)
	}
	if recall.Mode != isolationForgetfulMode {
		t.Errorf("recall wire mode = %q, want %q (routes the adapter to the raw-forward handler)", recall.Mode, isolationForgetfulMode)
	}
	if recall.UserID != "" {
		t.Errorf("recall UserID = %q, want \"\" — the omitted scope IS the test", recall.UserID)
	}
}

// TestSoftenForgetfulRefusal pins the runner-side classification: an
// isolation_forgetful record whose omitted-scope read errored with a recognized
// isolation-enforcement signature becomes a scored "refuses" PASS; an
// unrecognized failure stays an error; a non-forgetful kind is never softened.
// The error strings are the EXACT ones the aisotropy adapter emits (verified by
// the ARENA_STUB dry-run and TestRunForgetful_OmittedScopeRefused), so this
// proves the adapter→runner contract end to end offline.
func TestSoftenForgetfulRefusal(t *testing.T) {
	fgItem := workItem{Kind: isolationForgetfulMode, Answer: []string{"Kings Cross"}}

	// (a) the adapter's actual refusal text -> softened, scored "refuses" PASS.
	rec := record{Out: adapterOutput{Error: `App.Turn refused the omitted-scope read (tenant="arena" user="" session="s-b"): invalid tenant/user identity`}}
	if !softenForgetfulRefusal(&rec, fgItem) {
		t.Fatal("the adapter's 'invalid tenant/user identity' refusal must soften a forgetful record")
	}
	if rec.failed() {
		t.Fatalf("softened record must not remain failed: %+v", rec)
	}
	if !rec.Det.Pass || rec.Det.Value != 1 {
		t.Fatalf("softened record must carry a passing Det score, got %+v", rec.Det)
	}
	if !strings.HasPrefix(rec.Det.Detail, "refuses:") {
		t.Fatalf("softened Det.Detail must carry the 'refuses:' verdict, got %q", rec.Det.Detail)
	}

	// (b) the deeper wrapped Key.Validate text is also recognized.
	rec = record{RunErr: "invocation 2/2: adapter: memory: user requires a non-empty value"}
	if !softenForgetfulRefusal(&rec, fgItem) {
		t.Fatal("the wrapped 'requires a non-empty value' text must also soften")
	}

	// (c) an unrecognized failure (a not-yet-wired competitor's unknown-mode
	// error, or a crash/timeout) is NOT softened — an honest run-and-fail.
	rec = record{Out: adapterOutput{Error: `unknown mode "isolation_forgetful"`}}
	if softenForgetfulRefusal(&rec, fgItem) {
		t.Fatal("an unknown-mode / signatureless failure must NOT be softened")
	}
	if !rec.failed() {
		t.Fatal("unsoftened record must remain failed")
	}

	// (d) a non-forgetful kind is never softened, even with a matching signature.
	rec = record{Out: adapterOutput{Error: "invalid tenant/user identity"}}
	if softenForgetfulRefusal(&rec, workItem{Kind: "qa"}) {
		t.Fatal("softening must be scoped to the isolation_forgetful kind only")
	}

	// (e) a successful (non-failed) record is never touched.
	rec = record{Out: adapterOutput{Reply: "fine"}}
	if softenForgetfulRefusal(&rec, fgItem) {
		t.Fatal("a non-failed record must not be softened")
	}
}

// TestScoreDeterministic_ForgetfulLeakAndPartition pins the OTHER two verdicts
// through the runner's own scoreDeterministic dispatch (refusalSeen=false path):
// a recall that returned the scoped fact is a leak; one that returned a
// different/empty scope is a silent partition. This is the symmetric-rigor path
// that would fail aisotropy if it ever leaked here.
func TestScoreDeterministic_ForgetfulLeakAndPartition(t *testing.T) {
	it := workItem{Kind: isolationForgetfulMode, Answer: []string{"Kings Cross"}}

	leak := &record{Out: adapterOutput{Reply: "Your favourite station is Kings Cross."}}
	sc := scoreDeterministic(leak, it)
	if sc.Pass || !strings.HasPrefix(sc.Detail, "leak:") {
		t.Fatalf("a returned-scoped-data recall must score 'leak' FAIL, got %+v", sc)
	}

	partition := &record{Out: adapterOutput{Reply: "I don't have any station on file."}}
	sc = scoreDeterministic(partition, it)
	if sc.Pass || !strings.HasPrefix(sc.Detail, "silent_partition:") {
		t.Fatalf("an empty/different-scope recall must score 'silent_partition' FAIL, got %+v", sc)
	}
}
