package main

import "testing"

// TestIsQuotaExhausted_DistinguishesFromRateLimit is the load-bearing test.
//
// A rate limit and a spent budget both arrive as HTTP 429, and the runner must
// react to them in OPPOSITE ways: back off and retry the first, abandon the run
// on the second. Conflating them is what produced the 2026-07-27 board — 170
// cells dispatched after the free tier died, all failing, all landing on the
// frameworks that happened to be scheduled last.
func TestIsQuotaExhausted_DistinguishesFromRateLimit(t *testing.T) {
	cases := []struct {
		name      string
		outErr    string
		runErr    string
		wantQuota bool
		wantRate  bool
	}{
		{
			name:      "poolside exhausted budget (the real 2026-07-27 payload)",
			outErr:    `429 - {"error":"usage limit exceeded"}`,
			wantQuota: true, wantRate: true, // a 429, so also rate-limit-shaped
		},
		{
			name:      "poolside going too fast — recoverable, must NOT abort",
			outErr:    `429 - {"error":"Rate limit exceeded"}`,
			wantQuota: false, wantRate: true,
		},
		{
			name:      "openai-style insufficient_quota",
			outErr:    `{"error":{"code":"insufficient_quota"}}`,
			wantQuota: true, wantRate: false,
		},
		{
			name:      "anthropic-style credit balance",
			runErr:    "credit balance is too low",
			wantQuota: true, wantRate: false,
		},
		{
			name:      "plain 429 with no body — treat as rate limit, never as quota",
			runErr:    "429 Too Many Requests",
			wantQuota: false, wantRate: true,
		},
		{
			name:   "a healthy result is neither",
			outErr: "",
		},
		{
			name:      "an unrelated failure is neither",
			runErr:    "adapter exec: exit status 1",
			wantQuota: false, wantRate: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := execResult{runErr: tc.runErr}
			res.out.Error = tc.outErr
			if got := isQuotaExhausted(res); got != tc.wantQuota {
				t.Errorf("isQuotaExhausted = %v, want %v", got, tc.wantQuota)
			}
			if got := isRateLimited(res); got != tc.wantRate {
				t.Errorf("isRateLimited = %v, want %v", got, tc.wantRate)
			}
		})
	}
}

// TestQuotaExhausted_ImpliesTransient — a quota kill must still be classified
// transient, because the retry path is what surfaces the error text at all. The
// abort is a separate, run-level decision layered on top of it.
func TestQuotaExhausted_ImpliesTransient(t *testing.T) {
	res := execResult{}
	res.out.Error = `429 - {"error":"usage limit exceeded"}`
	if !isTransientFailure(res) {
		t.Fatal("an exhausted-quota 429 must classify as transient, or the runner never sees it")
	}
}
