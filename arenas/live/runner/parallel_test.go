package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// ---- fixtures ---------------------------------------------------------------

// fakeQATasks builds n single_shot_qa tasks with a non-empty Reference so the
// qa mode also drives a judge call (exercising judgeClient.score concurrently,
// not just runWorkItem/budget).
func fakeQATasks(n int) []task {
	out := make([]task, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, task{
			ID:        fmt.Sprintf("qa-%d", i),
			Kind:      "single_shot_qa",
			System:    "system prompt",
			Messages:  []inMessage{{Role: "user", Content: fmt.Sprintf("question %d", i)}},
			Reference: fmt.Sprintf("reference answer %d", i),
		})
	}
	return out
}

// writeFakeAdapter writes a tiny bash "adapter" that drains stdin and emits one
// valid adapterOutput JSON line, standing in for a real arenas/live/<fw>/run.sh
// without any network dependency. It never blocks — a `sleep` gives the
// scheduler a real chance to interleave goroutines instead of finishing so
// fast the race window never opens.
func writeFakeAdapter(t *testing.T, dir, name string) adapter {
	t.Helper()
	scriptPath := filepath.Join(dir, name+".sh")
	script := fmt.Sprintf(`#!/bin/bash
cat > /dev/null
sleep 0.02
echo '{"framework":"%s","mode":"na","reply":"the answer is 4183","input_tokens":2,"output_tokens":3,"tool_calls":[{"name":"calculate","arguments":{},"result":"4183"}]}'
`, name)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake adapter %s: %v", name, err)
	}
	return adapter{name: name, runSh: scriptPath, dir: dir}
}

// fakeJudgeServer stands in for agnes's /chat/completions endpoint, returning a
// canned score for every request so scoreRecord's judgeInto path runs for real
// (concurrently, across framework goroutines) without hitting the network.
func fakeJudgeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"content": `{"correctness":4,"helpfulness":4,"faithfulness":4,"rationale":"looks fine"}`,
				}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// ---- tests --------------------------------------------------------------

// TestRunCorrectnessPass_ConcurrentNoRace is the acceptance test for bounded
// parallelism: N fake frameworks run concurrently (bounded to `concurrency` in
// flight) sharing ONE *callCounter and being merged into ONE []record. Run
// with `go test -race` — this is the test that would fail (data race
// detected) if callCounter.add or the record merge were not race-free.
func TestRunCorrectnessPass_ConcurrentNoRace(t *testing.T) {
	dir := t.TempDir()
	const numFrameworks = 5
	const concurrency = 3

	var frameworks []adapter
	for i := 0; i < numFrameworks; i++ {
		frameworks = append(frameworks, writeFakeAdapter(t, dir, fmt.Sprintf("fw-%d", i)))
	}

	srv := fakeJudgeServer(t)
	defer srv.Close()
	judge := &judgeClient{apiKey: "test-key", model: "test-model", baseURL: srv.URL, http: srv.Client()}

	tasks := fakeQATasks(3)
	modes := []string{"qa", "tools"}
	budget := &callCounter{}

	records := runCorrectnessPass(frameworks, modes, tasks, nil, 0, judge, budget, concurrency)

	// qa: 3 items * (1 runWorkItem call + 1 judge call) = 6 calls, 3 records.
	// tools: fixed 3-item arithmeticTasks * 1 runWorkItem call = 3 calls, 3 records.
	const wantRecordsPerFW = 6
	const wantCallsPerFW = 9

	if got, want := len(records), numFrameworks*wantRecordsPerFW; got != want {
		t.Fatalf("len(records) = %d, want %d", got, want)
	}
	if got, want := budget.total(), numFrameworks*wantCallsPerFW; got != want {
		t.Fatalf("budget.total() = %d, want %d", got, want)
	}

	// Every record must have run without a runner-side error against the fake
	// adapter (adapter itself never reports a logical error).
	for _, r := range records {
		if r.RunErr != "" {
			t.Errorf("record %s/%s/%s has unexpected RunErr: %s", r.Framework, r.Mode, r.ID, r.RunErr)
		}
	}

	assertDeterministicOrder(t, records, frameworks, modes)
}

// TestRunCorrectnessPass_ConcurrencyOneIsSequential proves concurrency=1
// reproduces the exact old triple-loop order: the semaphore acquire happens in
// the driving goroutine BEFORE each worker is spawned, so with a buffer of 1 a
// worker can't start until the previous one has fully finished — frameworks
// run strictly one at a time, in slice order, exactly like the removed
// `for _, fw := range frameworks { for _, mode := range modes { ... } }` loop.
func TestRunCorrectnessPass_ConcurrencyOneIsSequential(t *testing.T) {
	dir := t.TempDir()
	frameworks := []adapter{
		writeFakeAdapter(t, dir, "alpha"),
		writeFakeAdapter(t, dir, "beta"),
		writeFakeAdapter(t, dir, "gamma"),
	}
	tasks := fakeQATasks(2)
	modes := []string{"qa", "tools"}
	budget := &callCounter{}

	// judge=nil (skip-judge path) keeps this test focused on ordering/budget,
	// mirroring a `-skip-judge` sequential run.
	records := runCorrectnessPass(frameworks, modes, tasks, nil, 0, nil, budget, 1)

	wantOrder := []string{
		"alpha/qa/qa-0", "alpha/qa/qa-1",
		"alpha/tools/tool-mul", "alpha/tools/tool-expr", "alpha/tools/tool-add",
		"beta/qa/qa-0", "beta/qa/qa-1",
		"beta/tools/tool-mul", "beta/tools/tool-expr", "beta/tools/tool-add",
		"gamma/qa/qa-0", "gamma/qa/qa-1",
		"gamma/tools/tool-mul", "gamma/tools/tool-expr", "gamma/tools/tool-add",
	}
	if len(records) != len(wantOrder) {
		t.Fatalf("len(records) = %d, want %d", len(records), len(wantOrder))
	}
	for i, r := range records {
		got := r.Framework + "/" + r.Mode + "/" + r.ID
		if got != wantOrder[i] {
			t.Errorf("records[%d] = %q, want %q (concurrency=1 must match the old sequential order exactly)", i, got, wantOrder[i])
		}
	}

	// qa: 2 items * 1 call (no judge, judge=nil short-circuits) = 2.
	// tools: 3 items * 1 call = 3. Per framework = 5. * 3 frameworks = 15.
	if got, want := budget.total(), 15; got != want {
		t.Fatalf("budget.total() = %d, want %d", got, want)
	}
}

// assertDeterministicOrder checks the merge+sort guarantee: records are
// non-decreasing in (framework index, mode index) regardless of which
// goroutine finished first.
func assertDeterministicOrder(t *testing.T, records []record, frameworks []adapter, modes []string) {
	t.Helper()
	fwIdx := make(map[string]int, len(frameworks))
	for i, fw := range frameworks {
		fwIdx[fw.name] = i
	}
	modeIdx := make(map[string]int, len(modes))
	for i, m := range modes {
		modeIdx[m] = i
	}
	prevFW, prevMode := -1, -1
	for _, r := range records {
		fi, mi := fwIdx[r.Framework], modeIdx[r.Mode]
		if fi < prevFW || (fi == prevFW && mi < prevMode) {
			t.Fatalf("records not sorted by (framework, mode): got framework idx %d mode idx %d after framework idx %d mode idx %d",
				fi, mi, prevFW, prevMode)
		}
		prevFW, prevMode = fi, mi
	}
}
