package main

// durable.go — ADR-0064 Layer 1: a durable, append-on-complete event log
// (RESULTS.records.jsonl) with a derived RESULTS.md render and resume-by-cell.
//
// The pre-Layer-1 runner held every record in memory across all three passes
// (correctness ~9h → bench → dimensions) and wrote RESULTS.md ONCE at the very
// end. Any interruption discarded all in-flight work, and RESULTS.md could not
// be regenerated without re-spending the whole board. This file implements the
// event-log + derived-view fix:
//
//   1. Append-on-complete: each finished cell is appended as one JSON line to
//      RESULTS.records.jsonl (line-atomic: marshal one envelope, one Write under
//      a mutex, fsync). Crash-survivable.
//   2. Derived render: `-render-only` reads the records log (+ matrix.json) and
//      calls the existing writeResults — ZERO model calls, ZERO adapter spawns.
//      The render is pure over the records (no time/random/network), so the
//      deterministic-scoring purity gate is unaffected.
//   3. Resume: on a normal run with `-resume`, cells already present in the log
//      (keyed by framework|mode|lane|form|task-id) are skipped, so a killed
//      board continues instead of re-doing work. A schema-version (`v`) mismatch
//      invalidates the stale file (renamed aside, never crashes).

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// recordsSchemaVersion is stamped on every durable line as `"v"`. Bump it when
// the on-disk envelope shape changes in a way that makes an old file unsafe to
// resume from; loadDurableLog then invalidates any file whose lines carry a
// different version (see its schema-mismatch handling).
const recordsSchemaVersion = 1

// recordsFileName is the durable event log, written next to RESULTS.md under
// the -live directory.
const recordsFileName = "RESULTS.records.jsonl"

// cellKind discriminates which pass produced a durable line, selecting which
// payload field of recordEnvelope is populated.
const (
	kindCorrectness = "correctness"
	kindBench       = "bench"
	kindIso         = "iso"
	kindCrash       = "crash"
	kindObs         = "obs"
)

// recordEnvelope is exactly one line in RESULTS.records.jsonl. Precisely one
// payload pointer is non-nil, selected by Kind. Cell is the stable resume key
// framework|mode|lane|form|task-id (task-id is empty for the pass kinds that
// have no per-task granularity — bench and dimensions).
type recordEnvelope struct {
	V      int    `json:"v"`
	Kind   string `json:"kind"`
	Cell   string `json:"cell"`
	Lane   string `json:"lane"`
	Form   string `json:"form"`
	TaskID string `json:"task_id,omitempty"`

	// At and RunID are the provenance of ONE measurement, and their absence was
	// a real hole rather than a missing nicety.
	//
	// With -resume, a cell already in this log is never re-measured. So a board
	// is a MOSAIC: it can carry cells measured minutes ago beside cells measured
	// hours ago under different infrastructure. On 2026-07-28 one window had
	// Qdrant refusing connections, and afterwards there was no way to ask "which
	// cells were measured during it?" — the records could not answer, because
	// they did not know. A board that cannot state when each number was taken
	// cannot exclude the ones taken while the floor was on fire.
	//
	// It also made duplicates unreadable: iso/crash/obs accumulated 20 records
	// over 9 unique keys (eino x4) with no way to tell which was which. The
	// loader's last-wins dedup picked correctly by luck of file order, not by
	// evidence.
	//
	// Both fields are omitempty and optional on read: records written before
	// this change carry neither and are reported as "unknown (pre-timestamp)",
	// which is the honest label. They are NOT invented a time, and the schema
	// version is deliberately NOT bumped — bumping it would rename the existing
	// log aside and discard a full board's worth of real measurements to fix a
	// metadata gap.
	At    string `json:"at,omitempty"`     // RFC3339 UTC, when this cell was measured
	RunID string `json:"run_id,omitempty"` // which run produced it

	Record *record      `json:"record,omitempty"`
	Bench  *benchCell   `json:"bench,omitempty"`
	Iso    *isoRecord   `json:"iso,omitempty"`
	Crash  *crashRecord `json:"crash,omitempty"`
	Obs    *obsRecord   `json:"obs,omitempty"`
}

// cellKey builds the stable per-cell resume key. Order is fixed:
// framework|mode|lane|form|task-id. The pipe separator matches the ADR-0064
// contract and none of the components contain a pipe in practice.
func cellKey(framework, mode, lane, form, taskID string) string {
	return strings.Join([]string{framework, mode, lane, form, taskID}, "|")
}

// ---- durable writer --------------------------------------------------------

// durableLog is the append-only writer. Its mutex makes every append
// line-atomic even when the correctness pass appends from multiple framework
// goroutines concurrently: marshal to a single []byte, one Write, one Sync,
// all under the lock. A nil *durableLog is a no-op writer (append/close on nil
// return nil), so callers never need to nil-check.
type durableLog struct {
	mu    sync.Mutex
	f     *os.File
	path  string
	runID string // stamped onto every envelope this run appends
}

// newRunID mints this run's identifier. Wall-clock based and human-readable on
// purpose: its job is to let a person say "everything from run-20260728T2010Z
// is suspect, re-measure it", which a random opaque id would not support.
func newRunID(now time.Time) string {
	return "run-" + now.UTC().Format("20060102T150405Z")
}

// openDurableLog opens (creating if absent) the append-only records file.
func openDurableLog(path, runID string) (*durableLog, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &durableLog{f: f, path: path, runID: runID}, nil
}

// append stamps the current schema version, marshals the envelope to one line,
// and writes+flushes it atomically.
func (d *durableLog) append(env recordEnvelope) error {
	if d == nil {
		return nil
	}
	env.V = recordsSchemaVersion
	// Stamp provenance at write time. Every cell can then answer "when was I
	// measured, and by which run?" — the question a resumed board could not
	// answer at all before.
	env.At = time.Now().UTC().Format(time.RFC3339)
	env.RunID = d.runID
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.f.Write(line); err != nil {
		return err
	}
	return d.f.Sync()
}

func (d *durableLog) close() error {
	if d == nil {
		return nil
	}
	return d.f.Close()
}

// appendCorrectness is the correctness-pass emit point (append-on-complete):
// one durable line per scored work item, keyed framework|mode|lane|native|id.
// The correctness pass is native-only, hence form="native".
func (d *durableLog) appendCorrectness(r record, lane string) error {
	rc := r
	return d.append(recordEnvelope{
		Kind:   kindCorrectness,
		Cell:   cellKey(r.Framework, r.Mode, lane, "native", r.ID),
		Lane:   lane,
		Form:   "native",
		TaskID: r.ID,
		Record: &rc,
	})
}

// appendBench is the bench-pass emit point: one durable line per bench cell,
// keyed framework|mode|lane|form| (no task-id — bench measures a mode, not a
// task). Bench cells carry their own Form (native|docker).
func (d *durableLog) appendBench(c benchCell, lane string) error {
	cc := c
	return d.append(recordEnvelope{
		Kind:  kindBench,
		Cell:  cellKey(c.Framework, c.Mode, lane, c.Form, ""),
		Lane:  lane,
		Form:  c.Form,
		Bench: &cc,
	})
}

// appendIso / appendCrash / appendObs are the dimension-pass emit points. Each
// dimension record is one per framework and native-only. The mode component of
// the key is the dimension's own label so the three kinds never collide.
func (d *durableLog) appendIso(r isoRecord, lane string) error {
	rc := r
	return d.append(recordEnvelope{
		Kind: kindIso,
		Cell: cellKey(r.Framework, "isolation", lane, "native", ""),
		Lane: lane,
		Form: "native",
		Iso:  &rc,
	})
}

func (d *durableLog) appendCrash(r crashRecord, lane string) error {
	rc := r
	return d.append(recordEnvelope{
		Kind:  kindCrash,
		Cell:  cellKey(r.Framework, "crash_recovery", lane, "native", ""),
		Lane:  lane,
		Form:  "native",
		Crash: &rc,
	})
}

func (d *durableLog) appendObs(r obsRecord, lane string) error {
	rc := r
	return d.append(recordEnvelope{
		Kind: kindObs,
		Cell: cellKey(r.Framework, "observability", lane, "native", ""),
		Lane: lane,
		Form: "native",
		Obs:  &rc,
	})
}

// ---- resume loader ---------------------------------------------------------

// resumeState is the deduplicated view of an existing RESULTS.records.jsonl:
// the set of cell keys already present (for resume-skip) plus the reconstructed
// records for each pass (for the derived render and for seeding a resumed run's
// report). Duplicate cell keys (from repeated non-resume runs) are collapsed
// last-line-wins.
type resumeState struct {
	present   map[string]bool
	records   []record
	benches   []benchCell
	isoRecs   []isoRecord
	crashRecs []crashRecord
	obsRecs   []obsRecord
	lane      string // lane observed on the records (first seen); "" when empty
	loaded    int    // total non-empty lines parsed (before dedup)

	// The measurement window of the cells that SURVIVED dedup. A resumed board
	// is a mosaic, and these let it say so out loud instead of presenting cells
	// measured hours apart as one instant.
	oldestAt    string // earliest RFC3339 stamp among winning cells
	newestAt    string // latest
	unstamped   int    // winning cells written before provenance existed
	invalidated int    // cells dropped by -invalidate-* so they re-measure
}

func emptyResumeState() *resumeState {
	return &resumeState{present: map[string]bool{}}
}

// loadDurableLog reads path and returns the deduplicated resumeState. A missing
// file is not an error — it yields an empty state (a fresh run). If any line
// carries a schema version other than recordsSchemaVersion, the whole file is
// treated as stale: it is renamed aside (path + ".stale-v<n>") and an empty
// state is returned, so a schema change never crashes a resume and never mixes
// incompatible records.
// invalidateSpec selects previously-measured cells that must NOT be resumed, so
// they are measured again. It exists because a resumed cell is otherwise frozen
// forever: -resume skips whatever is present, so a cell measured during a known
// bad window (2026-07-28: Qdrant refusing connections) would keep its value
// through every subsequent board, indistinguishable from a fresh one.
//
// Dropping a record is always SAFE in the sense that matters: the worst case is
// re-spending that cell. Keeping a bad one is the unsafe direction, because it
// publishes.
type invalidateSpec struct {
	beforeAt  string // drop cells stamped strictly before this RFC3339 instant
	runID     string // drop cells produced by this run id
	unstamped bool   // drop cells that carry no provenance at all
}

func (s invalidateSpec) empty() bool {
	return s.beforeAt == "" && s.runID == "" && !s.unstamped
}

// drops reports whether this envelope must be re-measured rather than resumed.
func (s invalidateSpec) drops(env recordEnvelope) bool {
	if s.empty() {
		return false
	}
	if s.unstamped && env.At == "" {
		return true
	}
	if s.runID != "" && env.RunID == s.runID {
		return true
	}
	if s.beforeAt != "" && env.At != "" && env.At < s.beforeAt {
		// RFC3339 UTC with a fixed layout sorts lexicographically, so a string
		// compare is a time compare here and needs no parsing.
		return true
	}
	return false
}

func loadDurableLog(path string) (*resumeState, error) {
	return loadDurableLogFiltered(path, invalidateSpec{})
}

func loadDurableLogFiltered(path string, inv invalidateSpec) (*resumeState, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyResumeState(), nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Ordered dedup: last envelope for a given cell key wins, but preserve the
	// first-seen order of keys so the reconstructed slices are stable.
	order := []string{}
	byCell := map[string]recordEnvelope{}
	invalidated := 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // records can be long (full reply text)
	loaded := 0
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var env recordEnvelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			// A malformed line is a corrupt tail (e.g. a torn write on crash).
			// Skip it rather than aborting the whole resume.
			continue
		}
		if inv.drops(env) {
			// Not resumed: the cell re-measures this run. Counted, never silent —
			// an invalidation the operator cannot see is indistinguishable from
			// the staleness it was meant to remove.
			invalidated++
			continue
		}
		if env.V != recordsSchemaVersion {
			// Stale schema: retire the file and start clean.
			_ = f.Close()
			stale := fmt.Sprintf("%s.stale-v%d", path, env.V)
			if rerr := os.Rename(path, stale); rerr != nil {
				return nil, fmt.Errorf("invalidate stale records file (schema v%d != v%d): %w", env.V, recordsSchemaVersion, rerr)
			}
			logf("WARNING: %s had schema v%d (current v%d) — renamed stale file to %s and starting a fresh records log",
				path, env.V, recordsSchemaVersion, stale)
			return emptyResumeState(), nil
		}
		loaded++
		if _, seen := byCell[env.Cell]; !seen {
			order = append(order, env.Cell)
		}
		byCell[env.Cell] = env
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	rs := emptyResumeState()
	rs.loaded = loaded
	rs.invalidated = invalidated
	for _, cell := range order {
		env := byCell[cell]
		rs.present[cell] = true
		if rs.lane == "" && env.Lane != "" {
			rs.lane = env.Lane
		}
		// Measurement window over the cells that WON dedup — those are the ones
		// the board will render, so those are the ones whose age matters.
		if env.At == "" {
			rs.unstamped++
		} else {
			if rs.oldestAt == "" || env.At < rs.oldestAt {
				rs.oldestAt = env.At
			}
			if env.At > rs.newestAt {
				rs.newestAt = env.At
			}
		}
		switch env.Kind {
		case kindCorrectness:
			if env.Record != nil {
				rs.records = append(rs.records, *env.Record)
			}
		case kindBench:
			if env.Bench != nil {
				rs.benches = append(rs.benches, *env.Bench)
			}
		case kindIso:
			if env.Iso != nil {
				rs.isoRecs = append(rs.isoRecs, *env.Iso)
			}
		case kindCrash:
			if env.Crash != nil {
				rs.crashRecs = append(rs.crashRecs, *env.Crash)
			}
		case kindObs:
			if env.Obs != nil {
				rs.obsRecs = append(rs.obsRecs, *env.Obs)
			}
		}
	}
	return rs, nil
}

// ---- derived render --------------------------------------------------------

// sortRecords orders correctness records deterministically by
// (framework, mode, id). Used by the derived render and by the resumed-run
// merge so RESULTS.md's per-item detail table is stable regardless of the
// order cells landed in the log.
func sortRecords(recs []record) {
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].Framework != recs[j].Framework {
			return recs[i].Framework < recs[j].Framework
		}
		if recs[i].Mode != recs[j].Mode {
			return recs[i].Mode < recs[j].Mode
		}
		return recs[i].ID < recs[j].ID
	})
}

// reportFromResume reconstructs a reportData from durable records (+ matrix
// docs) for the derived render. It reuses the existing reportData/writeResults
// renderer verbatim — it does not fork it. Run-configuration-only header fields
// that are not encoded in the records (bench-iters, cold-probes, concurrency,
// rps) render as their zero values; every field derivable from the records is
// derived. The result is pure over its inputs — no time, random, or network.
func reportFromResume(rs *resumeState, matrixDocs []matrixDoc) reportData {
	recs := append([]record(nil), rs.records...)
	sortRecords(recs)

	// Frameworks: distinct across every pass, sorted (matches discoverAdapters,
	// which sorts adapters by name).
	fwSet := map[string]bool{}
	model := ""
	for _, r := range recs {
		fwSet[r.Framework] = true
		if model == "" && r.Out.Model != "" {
			model = r.Out.Model
		}
	}
	for _, c := range rs.benches {
		fwSet[c.Framework] = true
	}
	for _, r := range rs.isoRecs {
		fwSet[r.Framework] = true
	}
	for _, r := range rs.crashRecs {
		fwSet[r.Framework] = true
	}
	for _, r := range rs.obsRecs {
		fwSet[r.Framework] = true
	}
	frameworks := sortedKeys(fwSet)
	if model == "" {
		model = modelName()
	}

	// Modes present in correctness records, ordered by the canonical vocabulary.
	modeSet := map[string]bool{}
	for _, r := range recs {
		modeSet[r.Mode] = true
	}
	modes := orderedSubset(allModes, modeSet)

	// Bench modes present (skip the "*" placeholder used for skipped cells) and
	// the execution forms observed.
	benchModeSet := map[string]bool{}
	formSet := map[string]bool{}
	for _, c := range rs.benches {
		if c.Mode != "" && c.Mode != "*" {
			benchModeSet[c.Mode] = true
		}
		if c.Form != "" {
			formSet[c.Form] = true
		}
	}
	benchModes := orderedSubset(allModes, benchModeSet)
	forms := orderedSubset([]string{"native", "docker"}, formSet)
	if len(forms) == 0 {
		forms = []string{"native"}
	}

	// Dimensions present, ordered by the canonical dimension vocabulary.
	dimSet := map[string]bool{}
	if len(rs.isoRecs) > 0 {
		dimSet["memory_scoped"] = true
		dimSet["isolation_leak"] = true
	}
	if len(rs.crashRecs) > 0 {
		dimSet["crash_recovery"] = true
	}
	if len(rs.obsRecs) > 0 {
		dimSet["observability"] = true
	}
	dims := orderedSubset(allDimensions, dimSet)

	lane := rs.lane
	if lane == "" {
		lane = "real"
	}

	return reportData{
		Model:      model,
		Frameworks: frameworks,
		Modes:      modes,
		Dimensions: dims,
		BenchModes: benchModes,
		Forms:      forms,
		Lane:       lane,
		LaneLeaks:  laneLeakFrameworks(recs, model),
		Records:    recs,
		Benches:    rs.benches,
		IsoRecords: rs.isoRecs,
		CrashRecs:  rs.crashRecs,
		ObsRecords: rs.obsRecs,
		MatrixDocs: matrixDocs,

		MeasuredFrom: rs.oldestAt,
		MeasuredTo:   rs.newestAt,
		Unstamped:    rs.unstamped,
		RenderOnly:   true,
	}
}

// renderOnly regenerates RESULTS.md purely from the durable records log and the
// matrix manifests — no model calls, no adapter spawns. It is the `-render-only`
// entry point.
//
// renderOnly regenerates the board from the durable records. resultsOut, when
// non-empty, redirects the write away from the published RESULTS.md — the same
// validation escape the run path has.
func renderOnly(liveDir, resultsOut string) error {
	recordsPath := filepath.Join(liveDir, recordsFileName)
	rs, err := loadDurableLog(recordsPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", recordsPath, err)
	}
	if rs.loaded == 0 {
		return fmt.Errorf("no durable records at %s — nothing to render (run the board once to produce it)", recordsPath)
	}
	matrixDocs, err := loadMatrixDocs(liveDir)
	if err != nil {
		return fmt.Errorf("load matrix.json manifests: %w", err)
	}
	rep := reportFromResume(rs, matrixDocs)
	resultsPath := filepath.Join(liveDir, "RESULTS.md")
	if resultsOut != "" {
		resultsPath = resultsOut
	}
	if err := writeResults(resultsPath, rep); err != nil {
		return fmt.Errorf("write results: %w", err)
	}
	logf("render-only: wrote %s from %d durable records (%d correctness, %d bench, %d iso, %d crash, %d obs) — zero model calls",
		resultsPath, rs.loaded, len(rs.records), len(rs.benches), len(rs.isoRecs), len(rs.crashRecs), len(rs.obsRecs))
	return nil
}

// ---- small helpers ---------------------------------------------------------

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// orderedSubset returns the members of order that are present in set, preserving
// order. Keeps rendered tables in a stable, canonical sequence.
func orderedSubset(order []string, set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for _, k := range order {
		if set[k] {
			out = append(out, k)
		}
	}
	return out
}

// orDash renders an empty provenance stamp as an explicit dash. A blank in a
// log line reads as "nothing to report"; a dash reads as "asked, no answer" —
// which is the true state for records written before provenance existed.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
