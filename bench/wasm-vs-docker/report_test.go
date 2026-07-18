package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFmtDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0ns"},
		{500 * time.Nanosecond, "500ns"},
		{1500 * time.Nanosecond, "1.5µs"},
		{999999 * time.Nanosecond, "1000.0µs"},
		{2500 * time.Microsecond, "2.50ms"},
		{1 * time.Millisecond, "1.00ms"},
	}
	for _, c := range cases {
		if got := fmtDur(c.d); got != c.want {
			t.Errorf("fmtDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFmtBytes(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0.00 KiB"},
		{1024, "1.00 KiB"},
		{5000, "4.88 KiB"},
		{1 << 20, "1.00 MiB"},
		{3334472, "3.18 MiB"}, // ~3.18 * 1 MiB
	}
	for _, c := range cases {
		if got := fmtBytes(c.n); got != c.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestMedian(t *testing.T) {
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %v, want 0", got)
	}
	d := []time.Duration{5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond}
	if got := median(d); got != 3*time.Millisecond {
		t.Errorf("median = %v, want 3ms", got)
	}
	// median must not mutate the caller's slice order.
	if d[0] != 5*time.Millisecond {
		t.Errorf("median mutated input slice: %v", d)
	}
}

func TestPercentiles(t *testing.T) {
	p50, p95, p99 := percentiles(nil)
	if p50 != 0 || p95 != 0 || p99 != 0 {
		t.Errorf("percentiles(nil) = %v/%v/%v, want zeros", p50, p95, p99)
	}

	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	p50, p95, p99 = percentiles(d)
	if p50 != 50*time.Millisecond {
		t.Errorf("p50 = %v, want 50ms", p50)
	}
	if p95 != 95*time.Millisecond {
		t.Errorf("p95 = %v, want 95ms", p95)
	}
	if p99 != 99*time.Millisecond {
		t.Errorf("p99 = %v, want 99ms", p99)
	}
}

func TestResultRow(t *testing.T) {
	r := &result{
		name:      "go",
		moduleMiB: "3.18 MiB",
		setup:     585 * time.Millisecond,
		firstCall: 2 * time.Millisecond,
		opsPerSec: 123456,
		p50:       1 * time.Microsecond,
		p95:       2 * time.Microsecond,
		p99:       3 * time.Microsecond,
		rss:       "6.1 MiB (in-proc)",
	}
	row := r.row()
	for _, want := range []string{"go", "3.18 MiB", "585.00ms", "123456", "6.1 MiB (in-proc)"} {
		if !strings.Contains(row, want) {
			t.Errorf("row() = %q, want it to contain %q", row, want)
		}
	}
}

func TestWinLoss(t *testing.T) {
	if got := winLoss(1*time.Millisecond, 10*time.Millisecond); !strings.Contains(got, "WASM wins") {
		t.Errorf("winLoss(wasm faster) = %q, want WASM wins", got)
	}
	if got := winLoss(10*time.Millisecond, 1*time.Millisecond); !strings.Contains(got, "Docker wins") {
		t.Errorf("winLoss(docker faster) = %q, want Docker wins", got)
	}
}

func TestHigherWins(t *testing.T) {
	if got := higherWins(100, 10); got != "WASM wins" {
		t.Errorf("higherWins(wasm>docker) = %q, want WASM wins", got)
	}
	if got := higherWins(10, 100); got != "Docker wins" {
		t.Errorf("higherWins(docker>wasm) = %q, want Docker wins", got)
	}
	if got := higherWins(10, 10); got != "Docker wins" {
		t.Errorf("higherWins(tie) = %q, want Docker wins (not strictly greater)", got)
	}
}

func TestRenderVerdict_NoDocker(t *testing.T) {
	var lines []string
	emit := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	renderVerdict(emit, []*result{{name: "go"}}, map[string][2]time.Duration{}, nil)

	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "no head-to-head verdict") {
		t.Errorf("renderVerdict(dock=nil) output missing the no-verdict note: %q", joined)
	}
}

func TestRenderVerdict_WithDocker(t *testing.T) {
	var out strings.Builder
	emit := func(format string, args ...any) { out.WriteString(fmt.Sprintf(format, args...)) }

	wasmResults := []*result{
		{name: "go", moduleMiB: "3.18 MiB", setup: 585 * time.Millisecond, firstCall: 2 * time.Millisecond, opsPerSec: 500000, p50: 1 * time.Microsecond, rss: "6.1 MiB (in-proc)"},
		{name: "rust", moduleMiB: "4.9 KiB", setup: 2 * time.Millisecond, firstCall: 1 * time.Microsecond, opsPerSec: 900000, p50: 500 * time.Nanosecond, rss: "0.3 MiB (in-proc)"},
	}
	cacheRows := map[string][2]time.Duration{
		"go":   {585 * time.Millisecond, 1 * time.Millisecond},
		"rust": {2 * time.Millisecond, 100 * time.Microsecond},
	}
	dock := &result{name: "docker", setup: 50 * time.Millisecond, firstCall: 10 * time.Millisecond, opsPerSec: 100, p50: 5 * time.Millisecond, rss: "3.9 MiB (container)"}

	renderVerdict(emit, wasmResults, cacheRows, dock)

	got := out.String()
	for _, want := range []string{
		"[go guest]", "[rust guest]",
		"cold-start  : uncached", "CACHE-AMORTIZED",
		"warm ops/s  : wasm 500000  vs docker 100",
		"BOTTOM LINE",
		"THE CAVEAT THAT DOES NOT MOVE",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderVerdict output missing %q; full output:\n%s", want, got)
		}
	}
}
