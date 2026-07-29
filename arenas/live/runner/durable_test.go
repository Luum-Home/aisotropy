package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aisotropy-live-runner/scoring"
)

// synthRecord builds a minimal, non-failed correctness record suitable for the
// durable round-trip / render tests. No network, no model — pure data.
func synthRecord(fw, mode, id string, pass bool) record {
	val := 0.0
	if pass {
		val = 1.0
	}
	return record{
		Framework: fw,
		Mode:      mode,
		ID:        id,
		Kind:      "qa",
		Out: adapterOutput{
			Framework:    fw,
			Mode:         mode,
			Model:        "test-model-x",
			Reply:        "synthetic answer " + id,
			InputTokens:  10,
			OutputTokens: 5,
		},
		WallMs: 42,
		SumIn:  10,
		SumOut: 5,
		HasTok: true,
		Det:    scoring.Score{Value: val, Pass: pass, Detail: "synthetic"},
	}
}

// stripRunBanner removes the single wall-clock "> **Run:** ..." banner line that
// writeResults embeds in the RESULTS.md header (results.go writeHeader). That
// line is the one non-record-derived, time-dependent piece of the render; the
// record-derived content below it is pure. Comparing with it stripped proves the
// record-derived render round-trips identically through the durable log.
func stripRunBanner(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, ln := range lines {
		// Banner lines describe HOW this markdown was produced (when it was
		// rendered, whether any cell was measured this pass, how many cells
		// cannot state their age). They are provenance ABOUT the render, not
		// content DERIVED FROM the records, and the invariant under test is the
		// latter: disk and memory must render the same DATA.
		//
		// Extending this list is only legitimate for lines that carry no
		// record-derived value. A line that reports a NUMBER from the records
		// belongs in the comparison, or this test stops having teeth.
		if strings.HasPrefix(ln, "> **Run:**") ||
			strings.HasPrefix(ln, "> **Measured:**") ||
			strings.HasPrefix(ln, "> **This is a re-render") ||
			strings.Contains(ln, "carry no measurement provenance") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// TestResumableAppendRoundTrip appends synthetic correctness records to the
// durable log, reloads them, and checks the reconstructed records + present-set
// (the resume key space) survive the marshal/unmarshal round-trip.
func TestResumableAppendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordsFileName)

	dl, err := openDurableLog(path, "run-test")
	if err != nil {
		t.Fatalf("openDurableLog: %v", err)
	}
	recs := []record{
		synthRecord("alpha", "qa", "q1", true),
		synthRecord("alpha", "qa", "q2", false),
		synthRecord("beta", "qa", "q1", true),
	}
	for _, r := range recs {
		if err := dl.appendCorrectness(r, "real"); err != nil {
			t.Fatalf("appendCorrectness: %v", err)
		}
	}
	if err := dl.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rs, err := loadDurableLog(path)
	if err != nil {
		t.Fatalf("loadDurableLog: %v", err)
	}
	if rs.loaded != len(recs) {
		t.Errorf("loaded = %d, want %d", rs.loaded, len(recs))
	}
	if len(rs.records) != len(recs) {
		t.Errorf("reconstructed records = %d, want %d", len(rs.records), len(recs))
	}
	if rs.lane != "real" {
		t.Errorf("lane = %q, want real", rs.lane)
	}
	for _, r := range recs {
		k := cellKey(r.Framework, r.Mode, "real", "native", r.ID)
		if !rs.present[k] {
			t.Errorf("present-set missing cell %q", k)
		}
	}
	// Field-level round-trip on one record.
	got := rs.records[0]
	if got.Framework != "alpha" || got.ID != "q1" || !got.Det.Pass || got.Out.Model != "test-model-x" {
		t.Errorf("record[0] did not round-trip: %+v", got)
	}
}

// TestResumeSkipFiltersPresentCells is the resume half: a cell already durable
// is in the present-set (so runCorrectnessPass skips it), and a cell that was
// never appended is absent (so it runs). This is exactly the `resume[key]`
// predicate the correctness pass consults.
func TestResumeSkipFiltersPresentCells(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordsFileName)

	dl, err := openDurableLog(path, "run-test")
	if err != nil {
		t.Fatalf("openDurableLog: %v", err)
	}
	if err := dl.appendCorrectness(synthRecord("alpha", "qa", "q1", true), "real"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := dl.appendCorrectness(synthRecord("alpha", "tools", "t1", true), "real"); err != nil {
		t.Fatalf("append: %v", err)
	}
	dl.close()

	rs, err := loadDurableLog(path)
	if err != nil {
		t.Fatalf("loadDurableLog: %v", err)
	}

	present := cellKey("alpha", "qa", "real", "native", "q1")
	if !rs.present[present] {
		t.Errorf("expected present cell %q to be skipped-eligible", present)
	}
	// Absent: never appended (different task).
	absentTask := cellKey("alpha", "qa", "real", "native", "q2")
	if rs.present[absentTask] {
		t.Errorf("cell %q was never appended but shows present", absentTask)
	}
	// Absent: same cell but a DIFFERENT lane must not collide — the lane is part
	// of the key, so a stub-lane record does not skip a real-lane cell.
	absentLane := cellKey("alpha", "qa", "stub", "native", "q1")
	if rs.present[absentLane] {
		t.Errorf("cell %q (different lane) must not be present", absentLane)
	}
}

// TestResumableDedupLastWins proves duplicate cell keys (from repeated
// non-resume runs appending to the same file) collapse to one record, last line
// winning — the property -render-only relies on to render a clean table.
func TestResumableDedupLastWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordsFileName)

	dl, _ := openDurableLog(path, "run-test")
	// Same cell appended twice: first a FAIL, then a PASS.
	_ = dl.appendCorrectness(synthRecord("alpha", "qa", "q1", false), "real")
	_ = dl.appendCorrectness(synthRecord("alpha", "qa", "q1", true), "real")
	dl.close()

	rs, err := loadDurableLog(path)
	if err != nil {
		t.Fatalf("loadDurableLog: %v", err)
	}
	if rs.loaded != 2 {
		t.Errorf("loaded (raw lines) = %d, want 2", rs.loaded)
	}
	if len(rs.records) != 1 {
		t.Fatalf("deduped records = %d, want 1", len(rs.records))
	}
	if !rs.records[0].Det.Pass {
		t.Errorf("last-wins failed: expected the PASS record to win, got Pass=false")
	}
}

// TestResumeSchemaMismatchInvalidates: a records file whose lines carry a
// different schema version is retired (renamed aside) and yields an empty state,
// never a crash — so a schema bump can't corrupt a resume.
func TestResumeSchemaMismatchInvalidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordsFileName)

	// A line with a future schema version.
	stale := `{"v":999,"kind":"correctness","cell":"alpha|qa|real|native|q1","lane":"real","form":"native","task_id":"q1","record":{"Framework":"alpha","Mode":"qa","ID":"q1"}}` + "\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}

	rs, err := loadDurableLog(path)
	if err != nil {
		t.Fatalf("loadDurableLog on stale file must not error: %v", err)
	}
	if rs.loaded != 0 || len(rs.present) != 0 || len(rs.records) != 0 {
		t.Errorf("stale file must yield empty state, got loaded=%d present=%d records=%d", rs.loaded, len(rs.present), len(rs.records))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original records file should have been renamed away")
	}
	if _, err := os.Stat(path + ".stale-v999"); err != nil {
		t.Errorf("expected the stale file at %s.stale-v999: %v", path, err)
	}
}

// TestRenderFromRecordsMatchesDirect proves the derived render is decoupled from
// the run: rendering from an in-memory record set and rendering the SAME records
// after a full durable-log disk round-trip produce byte-identical RESULTS.md
// (modulo the one wall-clock banner line). It reuses the real writeResults
// renderer — no fork.
func TestRenderFromRecordsMatchesDirect(t *testing.T) {
	dir := t.TempDir()
	recs := []record{
		synthRecord("alpha", "qa", "q1", true),
		synthRecord("alpha", "qa", "q2", false),
		synthRecord("beta", "qa", "q1", true),
		synthRecord("beta", "tools", "t1", true),
	}

	// Direct: reportData straight from an in-memory resumeState.
	rsMem := &resumeState{present: map[string]bool{}, records: recs, lane: "real", loaded: len(recs)}
	repMem := reportFromResume(rsMem, nil)
	memPath := filepath.Join(dir, "mem.md")
	if err := writeResults(memPath, repMem); err != nil {
		t.Fatalf("writeResults(mem): %v", err)
	}

	// Disk: append the same records, reload, render.
	logPath := filepath.Join(dir, recordsFileName)
	dl, _ := openDurableLog(logPath, "run-test")
	for _, r := range recs {
		if err := dl.appendCorrectness(r, "real"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	dl.close()
	rsDisk, err := loadDurableLog(logPath)
	if err != nil {
		t.Fatalf("loadDurableLog: %v", err)
	}
	repDisk := reportFromResume(rsDisk, nil)
	diskPath := filepath.Join(dir, "disk.md")
	if err := writeResults(diskPath, repDisk); err != nil {
		t.Fatalf("writeResults(disk): %v", err)
	}

	memBytes, _ := os.ReadFile(memPath)
	diskBytes, _ := os.ReadFile(diskPath)
	if stripRunBanner(string(memBytes)) != stripRunBanner(string(diskBytes)) {
		t.Errorf("render from disk differs from render from memory (record-derived content is not decoupled)")
	}

	// Teeth: the render actually reflects the records.
	disk := string(diskBytes)
	for _, want := range []string{"alpha", "beta", "test-model-x"} {
		if !strings.Contains(disk, want) {
			t.Errorf("rendered RESULTS.md missing %q", want)
		}
	}
}

// TestRenderOnlyWritesResultsFromRecords exercises the full -render-only entry
// point against a temp -live directory: it writes RESULTS.md purely from the
// durable log, with no model calls and no adapter spawns (structurally — the
// path constructs no judge and spawns no adapter).
func TestRenderOnlyWritesResultsFromRecords(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, recordsFileName)

	dl, _ := openDurableLog(logPath, "run-test")
	_ = dl.appendCorrectness(synthRecord("alpha", "qa", "q1", true), "real")
	_ = dl.appendBench(benchCell{Framework: "alpha", Form: "native", Mode: "qa", Iters: 5, WarmP50: 100, WarmP95: 120}, "real")
	dl.close()

	if err := renderOnly(dir, ""); err != nil {
		t.Fatalf("renderOnly: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "RESULTS.md"))
	if err != nil {
		t.Fatalf("read RESULTS.md: %v", err)
	}
	if !strings.Contains(string(out), "alpha") {
		t.Errorf("render-only RESULTS.md missing framework row")
	}

	// A directory with no records must be a clean error, not a crash or a
	// truncated file.
	empty := t.TempDir()
	if err := renderOnly(empty, ""); err == nil {
		t.Errorf("renderOnly on an empty dir should error (nothing to render)")
	}
}

// TestInvalidate_ReMeasuresAWindow pins the answer to a question the records
// could not answer on 2026-07-28: "which cells were measured while Qdrant was
// refusing connections?" They did not know, because nothing stamped them, so a
// bad cell resumed forever — -resume skips whatever is present, and presence
// was the only fact a record carried.
func TestInvalidate_ReMeasuresAWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordsFileName)

	write := func(runID, cell string) {
		l, err := openDurableLog(path, runID)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := l.append(recordEnvelope{
			Kind: kindCorrectness, Cell: cell, Lane: "real", Form: "native", TaskID: cell,
			Record: &record{Framework: "fw", Mode: "qa", ID: cell},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
		l.close()
	}
	write("run-BAD", "fw|qa|real|native|bad")
	write("run-GOOD", "fw|qa|real|native|good")

	// Baseline: both resume.
	rs, err := loadDurableLog(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rs.present) != 2 {
		t.Fatalf("baseline present = %d, want 2", len(rs.present))
	}
	if rs.oldestAt == "" || rs.newestAt == "" {
		t.Error("stamped records produced no measurement window — a resumed board could not state its own age")
	}
	if rs.unstamped != 0 {
		t.Errorf("unstamped = %d on freshly stamped records, want 0", rs.unstamped)
	}

	// Invalidate the bad run: its cell must NOT resume, so it re-measures.
	rs, err = loadDurableLogFiltered(path, invalidateSpec{runID: "run-BAD"})
	if err != nil {
		t.Fatalf("load filtered: %v", err)
	}
	if rs.present["fw|qa|real|native|bad"] {
		t.Error("the invalidated cell still resumed — it would keep a value measured during a known-bad window")
	}
	if !rs.present["fw|qa|real|native|good"] {
		t.Error("invalidating one run dropped an unrelated run's cell — that re-spends good measurements")
	}
	if rs.invalidated != 1 {
		t.Errorf("invalidated count = %d, want 1 (a silent invalidation is indistinguishable from the staleness it removes)", rs.invalidated)
	}
}

// TestInvalidate_Unstamped covers the records that predate provenance: they
// cannot say when they were measured, so -invalidate-unstamped is the only way
// to guarantee a board contains nothing from an unknown time.
func TestInvalidate_Unstamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordsFileName)
	// Hand-write an envelope with no At/RunID, exactly as pre-change runs did.
	line, _ := json.Marshal(recordEnvelope{
		V: recordsSchemaVersion, Kind: kindCorrectness,
		Cell: "fw|qa|real|native|old", Lane: "real", Form: "native", TaskID: "old",
		Record: &record{Framework: "fw", Mode: "qa", ID: "old"},
	})
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := loadDurableLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if rs.unstamped != 1 {
		t.Errorf("unstamped = %d, want 1 — a pre-provenance cell must be COUNTED, not silently treated as fresh", rs.unstamped)
	}
	if rs.oldestAt != "" {
		t.Error("an unstamped record invented a timestamp; it must stay unknown")
	}

	rs, err = loadDurableLogFiltered(path, invalidateSpec{unstamped: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.present) != 0 || rs.invalidated != 1 {
		t.Errorf("-invalidate-unstamped left present=%d invalidated=%d, want 0 and 1", len(rs.present), rs.invalidated)
	}
}

// TestResultsOut_LeavesPublishedBoardUntouched pins the whole reason the flag
// exists. Validating a change to one adapter's stub needs a scoped run, a scoped
// run that completes overwrites the published board with its own narrow slice
// (1525 lines -> 315, observed 2026-07-28), and the narrowing gate correctly
// refuses it. Without an escape the only options were "cannot validate" or
// "validate by destroying the board" — so the escape must be proven to actually
// leave the board alone, not merely documented as doing so.
func TestResultsOut_LeavesPublishedBoardUntouched(t *testing.T) {
	dir := t.TempDir()
	published := filepath.Join(dir, "RESULTS.md")
	const sentinel = "PUBLISHED BOARD — must survive\n"
	if err := os.WriteFile(published, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	// One durable record so renderOnly has something to render.
	l, err := openDurableLog(filepath.Join(dir, recordsFileName), "run-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.appendCorrectness(record{Framework: "fw", Mode: "qa", ID: "q1"}, "real"); err != nil {
		t.Fatal(err)
	}
	l.close()

	out := filepath.Join(dir, "validation.md")
	if err := renderOnly(dir, out); err != nil {
		t.Fatalf("renderOnly with -results-out: %v", err)
	}

	got, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Error("the published board was modified despite -results-out — the escape does not escape, " +
			"which leaves validation and board-safety mutually exclusive again")
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Errorf("nothing was written to the -results-out path (err=%v) — the run cannot be inspected", err)
	}
}
