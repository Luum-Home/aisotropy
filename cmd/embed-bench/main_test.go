package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// -- checkPrereqs -------------------------------------------------------

func TestCheckPrereqs(t *testing.T) {
	statOK := func(string) error { return nil }
	statMissing := func(p string) error { return fmt.Errorf("stat %s: no such file", p) }

	t.Run("bin unset", func(t *testing.T) {
		getenv := func(string) string { return "" }
		pr := checkPrereqs(getenv, statOK)
		if !strings.Contains(pr.skipMsg, "AISOTROPY_EMBED_BIN unset") {
			t.Fatalf("skipMsg = %q, want AISOTROPY_EMBED_BIN unset", pr.skipMsg)
		}
	})

	t.Run("bin missing on disk", func(t *testing.T) {
		getenv := func(k string) string {
			if k == "AISOTROPY_EMBED_BIN" {
				return "/nonexistent/embed"
			}
			return ""
		}
		pr := checkPrereqs(getenv, statMissing)
		if !strings.Contains(pr.skipMsg, "not found") {
			t.Fatalf("skipMsg = %q, want not-found", pr.skipMsg)
		}
	})

	t.Run("model dir unset", func(t *testing.T) {
		getenv := func(k string) string {
			if k == "AISOTROPY_EMBED_BIN" {
				return "/bin/embed"
			}
			return ""
		}
		pr := checkPrereqs(getenv, statOK)
		if !strings.Contains(pr.skipMsg, "AISOTROPY_EMBED_MODEL_DIR unset") {
			t.Fatalf("skipMsg = %q, want AISOTROPY_EMBED_MODEL_DIR unset", pr.skipMsg)
		}
	})

	t.Run("model dir missing a required file", func(t *testing.T) {
		env := map[string]string{
			"AISOTROPY_EMBED_BIN":       "/bin/embed",
			"AISOTROPY_EMBED_MODEL_DIR": "/models/foo",
		}
		getenv := func(k string) string { return env[k] }
		statFn := func(p string) error {
			if p == "/bin/embed" {
				return nil
			}
			if strings.HasSuffix(p, "tokenizer.json") {
				return errors.New("missing")
			}
			return nil
		}
		pr := checkPrereqs(getenv, statFn)
		if !strings.Contains(pr.skipMsg, "missing tokenizer.json") {
			t.Fatalf("skipMsg = %q, want missing tokenizer.json", pr.skipMsg)
		}
	})

	t.Run("all present", func(t *testing.T) {
		env := map[string]string{
			"AISOTROPY_EMBED_BIN":       "/bin/embed",
			"AISOTROPY_EMBED_MODEL_DIR": "/models/foo",
		}
		getenv := func(k string) string { return env[k] }
		pr := checkPrereqs(getenv, statOK)
		if pr.skipMsg != "" {
			t.Fatalf("skipMsg = %q, want empty (ok to proceed)", pr.skipMsg)
		}
		if pr.bin != "/bin/embed" || pr.modelDir != "/models/foo" {
			t.Fatalf("got bin=%q modelDir=%q", pr.bin, pr.modelDir)
		}
	})
}

// -- computeSpeedup / verdictLines --------------------------------------

func TestComputeSpeedup(t *testing.T) {
	cases := []struct {
		name       string
		cold, warm time.Duration
		want       float64
	}{
		{"warm zero avoids divide-by-zero", 10 * time.Millisecond, 0, 0},
		{"2x", 20 * time.Millisecond, 10 * time.Millisecond, 2},
		{"0.5x", 5 * time.Millisecond, 10 * time.Millisecond, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeSpeedup(c.cold, c.warm)
			if got != c.want {
				t.Fatalf("computeSpeedup(%v,%v) = %v, want %v", c.cold, c.warm, got, c.want)
			}
		})
	}
}

func TestVerdictLines(t *testing.T) {
	cases := []struct {
		name    string
		speedup float64
		want    string
	}{
		{"dominated by spawn+load at boundary 3x", 3.0, "DOMINATED"},
		{"dominated well above 3x", 10.0, "DOMINATED"},
		{"moderate at boundary 1.3x", 1.3, "moderately"},
		{"moderate mid-range", 2.0, "moderately"},
		{"little to gain below 1.3x", 1.0, "Little to gain"},
		{"little to gain at 1.0 exactly", 1.0, "Little to gain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := verdictLines(c.speedup)
			joined := strings.Join(lines, "\n")
			if !strings.Contains(joined, c.want) {
				t.Fatalf("verdictLines(%v) = %q, want to contain %q", c.speedup, joined, c.want)
			}
		})
	}
}

// -- stat / ms -----------------------------------------------------------

func TestStatEmpty(t *testing.T) {
	s := stat(nil)
	if s != (stats{}) {
		t.Fatalf("stat(nil) = %+v, want zero value", s)
	}
}

func TestStatComputesPercentiles(t *testing.T) {
	ds := []time.Duration{
		50 * time.Millisecond,
		10 * time.Millisecond,
		30 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
	}
	s := stat(ds)
	if s.min != 10*time.Millisecond {
		t.Errorf("min = %v, want 10ms", s.min)
	}
	// sorted: 10,20,30,40,50 (n=5); p50 index = 5*50/100=2 -> 30ms
	if s.p50 != 30*time.Millisecond {
		t.Errorf("p50 = %v, want 30ms", s.p50)
	}
	// p95 index = min(5*95/100, 4) = min(4,4) = 4 -> 50ms
	if s.p95 != 50*time.Millisecond {
		t.Errorf("p95 = %v, want 50ms", s.p95)
	}
	wantMean := 30 * time.Millisecond
	if s.mean != wantMean {
		t.Errorf("mean = %v, want %v", s.mean, wantMean)
	}
}

func TestStatSingleValueP95ClampsToLastIndex(t *testing.T) {
	s := stat([]time.Duration{7 * time.Millisecond})
	if s.p95 != 7*time.Millisecond || s.p50 != 7*time.Millisecond {
		t.Fatalf("single-value stat = %+v", s)
	}
}

func TestMsFormat(t *testing.T) {
	if got := ms(1500 * time.Microsecond); got != "1.500ms" {
		t.Fatalf("ms() = %q, want 1.500ms", got)
	}
	if got := ms(0); got != "0.000ms" {
		t.Fatalf("ms(0) = %q, want 0.000ms", got)
	}
}

// -- runMain: prereq gating, orchestration, error propagation -----------

// fakeRunner is an in-memory embedRunner: no subprocess, no real infra, fully
// deterministic — the "fake exec seam" for testing runMain's orchestration.
type fakeRunner struct {
	coldOut     []time.Duration
	coldErr     error
	warmStart   time.Duration
	warmOut     []time.Duration
	warmErr     error
	gotColdArgs [3]any // bin, modelDir, n
	gotWarmArgs [3]any
}

func (f *fakeRunner) cold(bin, modelDir string, n int) ([]time.Duration, error) {
	f.gotColdArgs = [3]any{bin, modelDir, n}
	return f.coldOut, f.coldErr
}

func (f *fakeRunner) warm(bin, modelDir string, m int) (time.Duration, []time.Duration, error) {
	f.gotWarmArgs = [3]any{bin, modelDir, m}
	return f.warmStart, f.warmOut, f.warmErr
}

func TestRunMain_SkipsCleanlyWhenBinUnset(t *testing.T) {
	var buf bytes.Buffer
	getenv := func(string) string { return "" }
	code := runMain(nil, &buf, getenv, func(string) error { return nil }, &fakeRunner{})
	if code != 0 {
		t.Fatalf("code = %d, want 0 (SKIP is a clean exit)", code)
	}
	if !strings.Contains(buf.String(), "SKIP:") {
		t.Fatalf("output = %q, want a SKIP line", buf.String())
	}
}

func TestRunMain_FlagParseError(t *testing.T) {
	var buf bytes.Buffer
	code := runMain([]string{"-n", "not-a-number"}, &buf, func(string) string { return "" }, func(string) error { return nil }, &fakeRunner{})
	if code != 2 {
		t.Fatalf("code = %d, want 2 on flag parse error", code)
	}
}

func TestRunMain_ColdError(t *testing.T) {
	var buf bytes.Buffer
	env := map[string]string{"AISOTROPY_EMBED_BIN": "/bin/embed", "AISOTROPY_EMBED_MODEL_DIR": "/models/x"}
	getenv := func(k string) string { return env[k] }
	runner := &fakeRunner{coldErr: errors.New("spawn failed")}
	code := runMain(nil, &buf, getenv, func(string) error { return nil }, runner)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "ERROR cold: spawn failed") {
		t.Fatalf("output = %q, want cold error", buf.String())
	}
}

func TestRunMain_WarmError(t *testing.T) {
	var buf bytes.Buffer
	env := map[string]string{"AISOTROPY_EMBED_BIN": "/bin/embed", "AISOTROPY_EMBED_MODEL_DIR": "/models/x"}
	getenv := func(k string) string { return env[k] }
	runner := &fakeRunner{
		coldOut: []time.Duration{10 * time.Millisecond},
		warmErr: errors.New("daemon crashed"),
	}
	code := runMain(nil, &buf, getenv, func(string) error { return nil }, runner)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "ERROR warm: daemon crashed") {
		t.Fatalf("output = %q, want warm error", buf.String())
	}
}

func TestRunMain_FullSuccess_DominatedVerdict(t *testing.T) {
	var buf bytes.Buffer
	env := map[string]string{"AISOTROPY_EMBED_BIN": "/bin/embed", "AISOTROPY_EMBED_MODEL_DIR": "/models/x"}
	getenv := func(k string) string { return env[k] }
	runner := &fakeRunner{
		coldOut:   []time.Duration{100 * time.Millisecond, 110 * time.Millisecond, 90 * time.Millisecond},
		warmStart: 50 * time.Millisecond,
		warmOut:   []time.Duration{5 * time.Millisecond, 6 * time.Millisecond, 4 * time.Millisecond},
	}
	code := runMain([]string{"-n", "3", "-m", "3"}, &buf, getenv, func(string) error { return nil }, runner)
	if code != 0 {
		t.Fatalf("code = %d, want 0; output=%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		"binary:   /bin/embed",
		"model:    /models/x",
		"iters:    cold=3  warm=3",
		"COLD subprocess:",
		"WARM daemon:",
		"daemon cold-start",
		"DOMINATED by spawn+model-load",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; full output:\n%s", want, out)
		}
	}
	if runner.gotColdArgs[2] != 3 || runner.gotWarmArgs[2] != 3 {
		t.Errorf("runner did not receive the parsed -n/-m values: %+v %+v", runner.gotColdArgs, runner.gotWarmArgs)
	}
}

func TestRunMain_FullSuccess_LittleToGainVerdict(t *testing.T) {
	var buf bytes.Buffer
	env := map[string]string{"AISOTROPY_EMBED_BIN": "/bin/embed", "AISOTROPY_EMBED_MODEL_DIR": "/models/x"}
	getenv := func(k string) string { return env[k] }
	runner := &fakeRunner{
		coldOut:   []time.Duration{10 * time.Millisecond},
		warmStart: 9 * time.Millisecond,
		warmOut:   []time.Duration{9 * time.Millisecond},
	}
	code := runMain(nil, &buf, getenv, func(string) error { return nil }, runner)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "Little to gain") {
		t.Fatalf("output = %q, want little-to-gain verdict", buf.String())
	}
}

func TestRunMain_FullSuccess_ModerateVerdict(t *testing.T) {
	var buf bytes.Buffer
	env := map[string]string{"AISOTROPY_EMBED_BIN": "/bin/embed", "AISOTROPY_EMBED_MODEL_DIR": "/models/x"}
	getenv := func(k string) string { return env[k] }
	runner := &fakeRunner{
		coldOut:   []time.Duration{13 * time.Millisecond},
		warmStart: 5 * time.Millisecond,
		warmOut:   []time.Duration{10 * time.Millisecond},
	}
	code := runMain(nil, &buf, getenv, func(string) error { return nil }, runner)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "Worth a daemon") {
		t.Fatalf("output = %q, want moderate verdict", buf.String())
	}
}

// -- measureCold / measureWarm: real subprocess exec, fake scripted binary --
//
// These call the real exec.Command path (the actual syscall shim), but the
// "binary" is a tiny shell script this test controls — never the real Rust
// embed crate or model weights. This is the same discipline as bitrate-
// harness's fake scorer: the syscall boundary is real, the infra behind it
// is fully substituted.

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries are POSIX-only")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
	return path
}

// copyScript implements the cold --input/--output contract: copy the request
// file to the output path, simulating a successful embed call.
const copyScript = "#!/bin/sh\nIN=\"\"\nOUT=\"\"\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --input) IN=\"$2\"; shift 2;;\n    --output) OUT=\"$2\"; shift 2;;\n    *) shift;;\n  esac\ndone\ncp \"$IN\" \"$OUT\"\n"

// failScript always exits non-zero, simulating a crashed embed call.
const failScript = "#!/bin/sh\nexit 7\n"

// noOutputScript exits 0 but never writes the --output file.
const noOutputScript = "#!/bin/sh\nexit 0\n"

// echoLoopScript implements the --serve stdin/stdout contract: read a line,
// echo it back, repeat until stdin closes.
const echoLoopScript = "#!/bin/sh\nwhile IFS= read -r line; do\n  echo \"$line\"\ndone\n"

// oneShotServeScript answers exactly one request then exits, simulating a
// daemon that dies immediately after its first response.
const oneShotServeScript = "#!/bin/sh\nIFS= read -r line\necho \"$line\"\n"

// silentServeScript exits immediately without answering anything, simulating
// a daemon that never produces a cold-start response.
const silentServeScript = "#!/bin/sh\nexit 0\n"

func TestMeasureCold_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "embed", copyScript)
	out, err := measureCold(bin, dir, 3)
	if err != nil {
		t.Fatalf("measureCold: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
}

func TestMeasureCold_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "embed", failScript)
	_, err := measureCold(bin, dir, 1)
	if err == nil {
		t.Fatal("expected an error from a non-zero exit")
	}
	if !strings.Contains(err.Error(), "iter 0") {
		t.Fatalf("error = %v, want to mention iter 0", err)
	}
}

func TestMeasureCold_NoOutputFile(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "embed", noOutputScript)
	_, err := measureCold(bin, dir, 1)
	if err == nil {
		t.Fatal("expected an error when the output file is never written")
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Fatalf("error = %v, want 'no output'", err)
	}
}

func TestMeasureWarm_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "embed-serve", echoLoopScript)
	coldStart, out, err := measureWarm(bin, dir, 4)
	if err != nil {
		t.Fatalf("measureWarm: %v", err)
	}
	if coldStart <= 0 {
		t.Errorf("coldStart = %v, want > 0", coldStart)
	}
	if len(out) != 4 {
		t.Fatalf("len(out) = %d, want 4", len(out))
	}
}

func TestMeasureWarm_StartError(t *testing.T) {
	_, _, err := measureWarm(filepath.Join(t.TempDir(), "does-not-exist"), "/models/x", 1)
	if err == nil {
		t.Fatal("expected a start error for a nonexistent binary")
	}
}

func TestMeasureWarm_ColdStartDaemonClosesImmediately(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "embed-serve", silentServeScript)
	_, _, err := measureWarm(bin, dir, 1)
	if err == nil {
		t.Fatal("expected a warm cold-start error when the daemon answers nothing")
	}
	if !strings.Contains(err.Error(), "warm cold-start") {
		t.Fatalf("error = %v, want 'warm cold-start'", err)
	}
}

func TestMeasureWarm_IterationErrorAfterColdStart(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "embed-serve", oneShotServeScript)
	_, _, err := measureWarm(bin, dir, 1)
	if err == nil {
		t.Fatal("expected a warm iteration error when the daemon dies after one response")
	}
	if !strings.Contains(err.Error(), "warm iter 0") {
		t.Fatalf("error = %v, want 'warm iter 0'", err)
	}
}

// NOTE on the roundtrip's `sc.Err()` branch (a real bufio.ErrTooLong, distinct
// from the plain "daemon closed stdout" EOF case above): forcing it requires a
// script that writes a single line past the 1MiB scanner buffer while nothing
// keeps draining its stdout after Go stops reading — the deferred cmd.Wait()
// then blocks forever on the child's stuck write, hanging the test. Left
// uncovered as a genuinely pathological-daemon defensive branch; documented
// rather than risking a hung test suite.

// TestMeasureCold_MkdirTempFailure forces os.MkdirTemp to fail by pointing
// TMPDIR at a path that cannot hold a new subdirectory, covering the "scratch
// dir" error return without needing filesystem-permission tricks.
func TestMeasureCold_MkdirTempFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TMPDIR is not honored on windows")
	}
	badTemp := filepath.Join(t.TempDir(), "does-not-exist-as-a-dir")
	t.Setenv("TMPDIR", badTemp)
	_, err := measureCold("/bin/true", "/models/x", 1)
	if err == nil {
		t.Fatal("expected an error when TMPDIR does not exist")
	}
}

// -- subprocessRunner wiring ----------------------------------------------

func TestSubprocessRunner_DelegatesToRealMeasureFuncs(t *testing.T) {
	dir := t.TempDir()
	coldBin := writeScript(t, dir, "cold", copyScript)
	r := subprocessRunner{}
	out, err := r.cold(coldBin, dir, 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("cold() = %v, %v", out, err)
	}

	warmBin := writeScript(t, dir, "warm", echoLoopScript)
	start, warmOut, err := r.warm(warmBin, dir, 1)
	if err != nil || start <= 0 || len(warmOut) != 1 {
		t.Fatalf("warm() = %v, %v, %v", start, warmOut, err)
	}
}

func TestStatFile(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "f")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := statFile(present); err != nil {
		t.Errorf("statFile(present) = %v, want nil", err)
	}
	if err := statFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("statFile(missing) = nil, want an error")
	}
}
