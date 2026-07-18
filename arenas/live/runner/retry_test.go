package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---- fixtures ---------------------------------------------------------------

// writeFlakyAdapter writes a fake bash "adapter" (standing in for a real
// arenas/live/<fw>/run.sh) that tracks its own invocation count in a sidecar
// file under dir, so successive process invocations (each `bash script` call
// is a fresh process — no in-memory state survives between them) can still
// behave differently on attempt 1 vs attempt 2+. `failFirst` invocations fail
// with `failMode`, then it succeeds; failFirst=0 always fails.
//
//   - failMode "transient-exec":  non-zero exit, stderr carries a transient
//     process-level marker (simulates a context-deadline/timeout/connection
//     drop on the adapter's own call to agnes) — becomes execResult.runErr.
//   - failMode "transient-out":   exit 0, valid JSON with an out.Error that
//     carries a transient HTTP/agnes marker (502) — becomes
//     execResult.out.Error with runErr empty.
//   - failMode "deterministic":   exit 0, valid JSON with a non-transient
//     out.Error (an adapter logic error) — must never be retried.
func writeFlakyAdapter(t *testing.T, dir, name string, failFirst int, failMode string) adapter {
	t.Helper()
	scriptPath := filepath.Join(dir, name+".sh")
	countFile := filepath.Join(dir, name+".count")

	var failBlock string
	switch failMode {
	case "transient-exec":
		failBlock = `echo "simulated agnes call: context deadline exceeded (dialing shared infra)" >&2
  exit 1`
	case "transient-out":
		failBlock = fmt.Sprintf(`echo '{"framework":"%s","mode":"na","error":"agnes returned 502 bad gateway"}'`, name)
	case "deterministic":
		failBlock = fmt.Sprintf(`echo '{"framework":"%s","mode":"na","error":"unknown mode: bogus"}'`, name)
	case "rate-limited":
		// exit 0, valid JSON, out.Error carrying the agnes FREE-TIER 429
		// signature — transient AND rate-limited (isRateLimited true), so
		// retryBackoff must pick the longer 3s/6s/12s schedule.
		failBlock = fmt.Sprintf(`echo '{"framework":"%s","mode":"na","error":"flow run: 429 too many requests (rate limit) for free users"}'`, name)
	default:
		t.Fatalf("unknown failMode %q", failMode)
	}

	script := fmt.Sprintf(`#!/bin/bash
cat > /dev/null
count_file=%q
count=0
if [ -f "$count_file" ]; then count=$(cat "$count_file"); fi
count=$((count+1))
echo "$count" > "$count_file"
if [ "$count" -le %d ]; then
  %s
  exit 0
fi
echo '{"framework":"%s","mode":"na","reply":"ok after retry","input_tokens":1,"output_tokens":1}'
`, countFile, failFirst, failBlock, name)

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write flaky adapter %s: %v", name, err)
	}
	return adapter{name: name, runSh: scriptPath, dir: dir}
}

// readCallCount reads back the sidecar invocation counter writeFlakyAdapter
// maintains, as an independent cross-check against callCounter.total().
func readCallCount(t *testing.T, dir, name string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name+".count"))
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(string(b), "%d", &n)
	return n
}

func trivialWorkItem(mode, id string) workItem {
	return workItem{
		Mode: mode,
		ID:   id,
		Kind: "single_shot_qa",
		Invocations: []adapterInput{
			{Mode: mode, Messages: []inMessage{{Role: "user", Content: "2+2?"}}},
		},
	}
}

// ---- tests --------------------------------------------------------------

// TestRunWorkItem_TransientRetrySucceeds is scenario (a): a transient
// process-level failure (context deadline exceeded) on attempt 1, success on
// attempt 2 — with the default retry budget (1), the record must show NO
// error and exactly 2 billable calls must be counted.
func TestRunWorkItem_TransientRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	fw := writeFlakyAdapter(t, dir, "flaky-transient", 1, "transient-exec")
	restore := stubRetrySleep()
	defer restore()

	budget := &callCounter{}
	rec := runWorkItem(fw, trivialWorkItem("qa", "q1"), budget, 1)

	if rec.failed() {
		t.Fatalf("record failed after a successful retry: RunErr=%q Out.Error=%q", rec.RunErr, rec.Out.Error)
	}
	if got, want := rec.Out.Reply, "ok after retry"; got != want {
		t.Errorf("rec.Out.Reply = %q, want %q", got, want)
	}
	if got, want := budget.total(), 2; got != want {
		t.Errorf("budget.total() = %d, want %d (1 failed attempt + 1 successful retry)", got, want)
	}
	if got, want := readCallCount(t, dir, "flaky-transient"), 2; got != want {
		t.Errorf("adapter invocation count = %d, want %d", got, want)
	}
}

// TestRunWorkItem_DeterministicFailureNeverRetries is scenario (b): a
// deterministic adapter-logic error (e.g. "unknown mode") must NOT be
// retried even though the retry budget allows it — exactly 1 call, and the
// error is preserved on the record.
func TestRunWorkItem_DeterministicFailureNeverRetries(t *testing.T) {
	dir := t.TempDir()
	fw := writeFlakyAdapter(t, dir, "flaky-deterministic", 999, "deterministic")
	restore := stubRetrySleep()
	defer restore()

	budget := &callCounter{}
	rec := runWorkItem(fw, trivialWorkItem("qa", "q1"), budget, 1)

	if !rec.failed() {
		t.Fatalf("expected the deterministic error to be preserved, got a successful record")
	}
	if got, want := rec.Out.Error, "unknown mode: bogus"; got != want {
		t.Errorf("rec.Out.Error = %q, want %q", got, want)
	}
	if got, want := budget.total(), 1; got != want {
		t.Errorf("budget.total() = %d, want %d (a deterministic failure must not retry)", got, want)
	}
	if got, want := readCallCount(t, dir, "flaky-deterministic"), 1; got != want {
		t.Errorf("adapter invocation count = %d, want %d", got, want)
	}
}

// TestRunWorkItem_TransientExhaustsRetries is scenario (c): a persistently
// transient failure (out.Error carrying a 502) with -retry 2 must attempt
// exactly 3 times (1 initial + 2 retries) and preserve the final error
// rather than hiding a real, persistent outage.
func TestRunWorkItem_TransientExhaustsRetries(t *testing.T) {
	dir := t.TempDir()
	fw := writeFlakyAdapter(t, dir, "flaky-persistent", 999, "transient-out")
	restore := stubRetrySleep()
	defer restore()

	budget := &callCounter{}
	rec := runWorkItem(fw, trivialWorkItem("qa", "q1"), budget, 2)

	if !rec.failed() {
		t.Fatalf("expected the persistent transient failure to be preserved, got a successful record")
	}
	if got, want := rec.Out.Error, "agnes returned 502 bad gateway"; got != want {
		t.Errorf("rec.Out.Error = %q, want %q", got, want)
	}
	if got, want := budget.total(), 3; got != want {
		t.Errorf("budget.total() = %d, want %d (1 initial + 2 retries, all billable)", got, want)
	}
	if got, want := readCallCount(t, dir, "flaky-persistent"), 3; got != want {
		t.Errorf("adapter invocation count = %d, want %d", got, want)
	}
}

// TestRunAdapterRetry_ZeroRetriesIsOldBehavior locks in that -retry 0 is
// exactly the pre-retry code path: one attempt, no sleep, the failure (even
// a transient-looking one) stands.
func TestRunAdapterRetry_ZeroRetriesIsOldBehavior(t *testing.T) {
	dir := t.TempDir()
	fw := writeFlakyAdapter(t, dir, "flaky-zero-retry", 999, "transient-exec")
	restore := stubRetrySleep()
	defer restore()

	budget := &callCounter{}
	rec := runWorkItem(fw, trivialWorkItem("qa", "q1"), budget, 0)

	if !rec.failed() {
		t.Fatalf("expected the transient failure to stand with -retry 0, got success")
	}
	if got, want := budget.total(), 1; got != want {
		t.Errorf("budget.total() = %d, want %d (-retry 0 must not retry)", got, want)
	}
}

// TestIsTransientFailure_Classification is a focused table test on the
// classification rule itself: process-level runErr markers, adapter-reported
// out.Error markers, and confirmation that a clean success and a
// deterministic error are never classified as transient.
func TestIsTransientFailure_Classification(t *testing.T) {
	cases := []struct {
		name string
		res  execResult
		want bool
	}{
		{"runErr context deadline exceeded", execResult{runErr: "adapter exec: signal: killed (stderr: …context deadline exceeded)"}, true},
		{"runErr timeout", execResult{runErr: "adapter exec: exit status 1 (stderr: request TIMEOUT after 30s)"}, true},
		{"runErr connection reset", execResult{runErr: "adapter exec: exit status 1 (stderr: read: connection reset by peer)"}, true},
		{"runErr EOF", execResult{runErr: "adapter exec: exit status 1 (stderr: unexpected EOF)"}, true},
		{"runErr malformed JSON is not transient", execResult{runErr: "parse adapter output: no valid result JSON object on stdout (stdout: garbage)"}, false},
		{"out.Error 429", execResult{out: adapterOutput{Error: "rate limited: 429 too many requests"}}, true},
		{"out.Error 502", execResult{out: adapterOutput{Error: "agnes returned 502 bad gateway"}}, true},
		{"out.Error unavailable", execResult{out: adapterOutput{Error: "model temporarily unavailable"}}, true},
		{"out.Error 400 is not transient", execResult{out: adapterOutput{Error: "bad request: 400 invalid schema"}}, false},
		{"out.Error unknown mode is not transient", execResult{out: adapterOutput{Error: "unknown mode: bogus"}}, false},
		{"clean success is not transient", execResult{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransientFailure(c.res); got != c.want {
				t.Errorf("isTransientFailure(%+v) = %v, want %v", c.res, got, c.want)
			}
		})
	}
}

// stubRetrySleep replaces retrySleep with a no-op for the duration of a test
// so the retry backoff (1s/2s/4s/8s by design) does not make the unit suite
// slow; returns a restore func.
func stubRetrySleep() func() {
	orig := retrySleep
	retrySleep = func(time.Duration) {}
	return func() { retrySleep = orig }
}
