package main

import (
	"sync"
	"testing"
	"time"
)

// ---- rateLimiter ------------------------------------------------------------

// TestRateLimiter_Disabled locks in that -rps 0 (and a nil receiver) is a true
// no-op: no sleep, exact old behavior. This is the default, so every existing
// run and every unit test that does not opt in must be unaffected.
func TestRateLimiter_Disabled(t *testing.T) {
	var slept []time.Duration
	orig := rateSleep
	rateSleep = func(d time.Duration) { slept = append(slept, d) }
	defer func() { rateSleep = orig }()

	rl := newRateLimiter(0)
	rl.wait()
	rl.wait()

	var nilRL *rateLimiter
	nilRL.wait() // must not panic

	if len(slept) != 0 {
		t.Errorf("disabled limiter slept %v, want no sleeps", slept)
	}
}

// TestRateLimiter_PacesSuccessiveCalls drives a frozen clock so the reservation
// spacing is deterministic: with a 100ms interval (10/s), the first call fires
// immediately and each subsequent call reserves one interval further out.
func TestRateLimiter_PacesSuccessiveCalls(t *testing.T) {
	rl := newRateLimiter(10) // 10/s -> 100ms interval
	if rl.interval != 100*time.Millisecond {
		t.Fatalf("interval = %s, want 100ms", rl.interval)
	}

	base := time.Unix(1_700_000_000, 0)
	origNow, origSleep := rateNow, rateSleep
	rateNow = func() time.Time { return base } // frozen clock
	var sleeps []time.Duration
	rateSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	defer func() { rateNow, rateSleep = origNow, origSleep }()

	rl.wait() // slot=base,        sleep 0   (skipped)
	rl.wait() // slot=base+100ms,  sleep 100ms
	rl.wait() // slot=base+200ms,  sleep 200ms

	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleeps[%d] = %s, want %s", i, sleeps[i], want[i])
		}
	}
	if rl.next != base.Add(300*time.Millisecond) {
		t.Errorf("rl.next = %s, want base+300ms", rl.next.Sub(base))
	}
}

// TestRateLimiter_ConcurrentNoRace runs many waiters at once under a real
// (sub-millisecond) interval — the acquire path must be race-free (run under
// `go test -race`). Each waiter reserves a distinct slot under the mutex.
func TestRateLimiter_ConcurrentNoRace(t *testing.T) {
	rl := newRateLimiter(100000) // 10µs interval — tiny real sleeps, no test drag
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rl.wait() }()
	}
	wg.Wait()
}

// ---- retryBackoff schedules -------------------------------------------------

// TestRetryBackoff_Schedules verifies both schedules and the overflow clamp:
// generic transient is 1/2/4/8s (cap 8s); rate-limited is 3/6/12/24s (cap 30s).
func TestRetryBackoff_Schedules(t *testing.T) {
	genWant := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range genWant {
		if got := retryBackoff(i, false); got != w {
			t.Errorf("retryBackoff(%d,false) = %s, want %s", i, got, w)
		}
	}
	rlWant := []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 24 * time.Second, 30 * time.Second}
	for i, w := range rlWant {
		if got := retryBackoff(i, true); got != w {
			t.Errorf("retryBackoff(%d,true) = %s, want %s", i, got, w)
		}
	}
	// A pathological attempt count must clamp to the ceiling, never overflow
	// the left-shift into a negative/tiny duration.
	if got := retryBackoff(100, true); got != 30*time.Second {
		t.Errorf("retryBackoff(100,true) = %s, want 30s (overflow clamp)", got)
	}
	if got := retryBackoff(100, false); got != 8*time.Second {
		t.Errorf("retryBackoff(100,false) = %s, want 8s (overflow clamp)", got)
	}
}

// ---- isRateLimited classification -------------------------------------------

// TestIsRateLimited_Classification separates the FREE-TIER 429 from the generic
// transients (502/timeout): only the former rides the long backoff.
func TestIsRateLimited_Classification(t *testing.T) {
	cases := []struct {
		name string
		res  execResult
		want bool
	}{
		{"429 in out.Error", execResult{out: adapterOutput{Error: "flow run: 429 Too Many Requests"}}, true},
		{"rate-limit phrase in out.Error", execResult{out: adapterOutput{Error: "You have reached the API rate limit for free users"}}, true},
		{"429 in runErr", execResult{runErr: "adapter exec: exit status 1 (stderr: HTTP 429 too many requests)"}, true},
		{"502 is transient but NOT rate-limited", execResult{out: adapterOutput{Error: "agnes returned 502 bad gateway"}}, false},
		{"timeout is transient but NOT rate-limited", execResult{runErr: "context deadline exceeded"}, false},
		{"clean success is not rate-limited", execResult{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRateLimited(c.res); got != c.want {
				t.Errorf("isRateLimited(%+v) = %v, want %v", c.res, got, c.want)
			}
		})
	}
}

// TestRunAdapterRetry_RateLimitedUsesLongBackoff is the integration proof: a
// persistent 429 out.Error retries on the LONGER rate-limit schedule (3s, 6s),
// not the generic 1s/2s one — so a free-tier throttle window gets real room to
// clear instead of being hammered every second.
func TestRunAdapterRetry_RateLimitedUsesLongBackoff(t *testing.T) {
	dir := t.TempDir()
	fw := writeFlakyAdapter(t, dir, "flaky-429", 999, "rate-limited")

	var sleeps []time.Duration
	orig := retrySleep
	retrySleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	defer func() { retrySleep = orig }()

	budget := &callCounter{}
	rec := runWorkItem(fw, trivialWorkItem("qa", "q1"), budget, 2)

	if !rec.failed() {
		t.Fatalf("expected the persistent 429 to remain failed, got success")
	}
	if got, want := budget.total(), 3; got != want {
		t.Errorf("budget.total() = %d, want %d (1 initial + 2 retries)", got, want)
	}
	want := []time.Duration{3 * time.Second, 6 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("backoff sleeps = %v, want %v (rate-limit schedule)", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleeps[%d] = %s, want %s (rate-limit schedule)", i, sleeps[i], want[i])
		}
	}
}

// TestRpsLabel documents the header rendering: off when disabled, else the rate.
func TestRpsLabel(t *testing.T) {
	cases := map[float64]string{0: "off", -1: "off", 1.5: "1.5/s", 2: "2/s", 10: "10/s"}
	for in, want := range cases {
		if got := rpsLabel(in); got != want {
			t.Errorf("rpsLabel(%v) = %q, want %q", in, got, want)
		}
	}
}
