package main

import (
	"runtime"
	"strings"
	"testing"
)

// shell resolves a POSIX shell binary present on both the macOS and Linux CI
// runners this harness targets, so execRunner's real os/exec path can be
// exercised deterministically without depending on any bench toolchain.
func shell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("execRunner tests assume a POSIX shell")
	}
	return "/bin/sh"
}

func TestExecRunner_LookPath(t *testing.T) {
	r := execRunner{}

	if _, err := r.LookPath("sh"); err != nil {
		t.Errorf("LookPath(sh): unexpected error: %v", err)
	}
	if _, err := r.LookPath("definitely-not-a-real-binary-xyz123"); err == nil {
		t.Error("LookPath(missing): expected error, got nil")
	}
}

func TestExecRunner_Output(t *testing.T) {
	r := execRunner{}
	sh := shell(t)

	out, err := r.Output(cmdSpec{Name: sh, Args: []string{"-c", "printf hello"}})
	if err != nil {
		t.Fatalf("Output: unexpected error: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("Output = %q, want %q", out, "hello")
	}

	if _, err := r.Output(cmdSpec{Name: sh, Args: []string{"-c", "exit 3"}}); err == nil {
		t.Error("Output(exit 3): expected error, got nil")
	}
}

func TestExecRunner_CombinedOutput(t *testing.T) {
	r := execRunner{}
	sh := shell(t)

	out, err := r.CombinedOutput(cmdSpec{Name: sh, Args: []string{"-c", "printf out; printf err 1>&2"}})
	if err != nil {
		t.Fatalf("CombinedOutput: unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "out") || !strings.Contains(string(out), "err") {
		t.Errorf("CombinedOutput = %q, want it to contain both out and err", out)
	}

	if _, err := r.CombinedOutput(cmdSpec{Name: sh, Args: []string{"-c", "exit 1"}}); err == nil {
		t.Error("CombinedOutput(exit 1): expected error, got nil")
	}
}

func TestExecRunner_Run(t *testing.T) {
	r := execRunner{}
	sh := shell(t)

	if err := r.Run(cmdSpec{Name: sh, Args: []string{"-c", "exit 0"}}); err != nil {
		t.Errorf("Run(exit 0): unexpected error: %v", err)
	}
	if err := r.Run(cmdSpec{Name: sh, Args: []string{"-c", "exit 1"}}); err == nil {
		t.Error("Run(exit 1): expected error, got nil")
	}
}

// TestExecRunner_DirAndEnv exercises the Dir/Env plumbing in execRunner.build,
// which Output/CombinedOutput/Run all funnel through.
func TestExecRunner_DirAndEnv(t *testing.T) {
	r := execRunner{}
	sh := shell(t)
	dir := t.TempDir()

	out, err := r.Output(cmdSpec{Name: sh, Args: []string{"-c", "pwd"}, Dir: dir})
	if err != nil {
		t.Fatalf("Output with Dir: unexpected error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	// macOS /tmp is a symlink to /private/tmp, so compare the resolved suffix
	// rather than exact equality.
	if !strings.HasSuffix(got, strings.TrimPrefix(dir, "/private")) && got != dir {
		t.Errorf("pwd = %q, want it to reference tempdir %q", got, dir)
	}

	out, err = r.Output(cmdSpec{Name: sh, Args: []string{"-c", "printf $BENCH_TEST_VAR"}, Env: []string{"BENCH_TEST_VAR=marker"}})
	if err != nil {
		t.Fatalf("Output with Env: unexpected error: %v", err)
	}
	if string(out) != "marker" {
		t.Errorf("env var readback = %q, want %q", out, "marker")
	}
}

// TestFakeRunner_Unstubbed verifies the fake's discipline: an unstubbed call
// on any method returns a descriptive error rather than zero-valuing silently.
func TestFakeRunner_Unstubbed(t *testing.T) {
	f := &fakeRunner{}

	if _, err := f.LookPath("x"); err == nil {
		t.Error("LookPath: expected unstubbed error")
	}
	if _, err := f.Output(cmdSpec{Name: "x"}); err == nil {
		t.Error("Output: expected unstubbed error")
	}
	if _, err := f.CombinedOutput(cmdSpec{Name: "x"}); err == nil {
		t.Error("CombinedOutput: expected unstubbed error")
	}
	if err := f.Run(cmdSpec{Name: "x"}); err == nil {
		t.Error("Run: expected unstubbed error")
	}
	if len(f.calls) != 3 {
		t.Errorf("calls recorded = %d, want 3 (Output/CombinedOutput/Run record; LookPath does not)", len(f.calls))
	}
}
