package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot resolves the aisotropy module root from this test file's own
// path (tools/analyzers/scopecheck/cmd/scopecheck/main_test.go is five
// directories below it), rather than hardcoding an absolute path.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("computed repo root %q does not contain go.mod: %v", root, err)
	}
	return root
}

// exitCodeOf extracts the child process's exit code from the error returned
// by exec.Cmd.CombinedOutput/Run, treating a nil error as a clean exit 0.
func exitCodeOf(t *testing.T, err error, out []byte) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("unexpected exec error: %v, output: %s", err, out)
	return -1
}

// TestMain_ExitCodes drives the real main() — a one-line wrapper around
// golang.org/x/tools' singlechecker.Main, which unconditionally calls
// os.Exit (no args -> exit 1 after printing usage; otherwise
// exit(checker.Run(...))) — in a re-exec'd subprocess, the standard Go
// pattern for exercising an os.Exit path without killing the test binary
// (cf. os/exec_test.go's TestHelperProcess). The child is the SAME
// coverage-instrumented test binary, re-invoked with an env sentinel that
// makes it call main() instead of running the normal suite; go test's own
// flag parsing stops at the first non-flag argument, so a trailing package
// pattern like "./tools/analyzers/scopecheck" is left in flag.Args() for
// singlechecker's own (later) flag.Parse to pick up exactly as it would from
// a real command line.
func TestMain_ExitCodes(t *testing.T) {
	if os.Getenv("SCOPECHECK_TEST_BE_MAIN") == "1" {
		main()
		return
	}

	root := repoRoot(t)

	t.Run("no_package_argument_exits_1", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMain_ExitCodes$")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "SCOPECHECK_TEST_BE_MAIN=1")
		out, err := cmd.CombinedOutput()
		if code := exitCodeOf(t, err, out); code != 1 {
			t.Errorf("exit code = %d, want 1 (usage error, no package given); output: %s", code, out)
		}
	})

	t.Run("clean_package_exits_0", func(t *testing.T) {
		// tools/analyzers/scopecheck itself never constructs a bare
		// facts.Scope{}/semantic.Scope{} literal, so the analyzer reports no
		// diagnostics against its own defining package — a real, fast,
		// always-available "clean" fixture with no throwaway package needed.
		cmd := exec.Command(os.Args[0], "-test.run=^TestMain_ExitCodes$", "./tools/analyzers/scopecheck")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "SCOPECHECK_TEST_BE_MAIN=1")
		out, err := cmd.CombinedOutput()
		if code := exitCodeOf(t, err, out); code != 0 {
			t.Errorf("exit code = %d, want 0 (no diagnostics); output: %s", code, out)
		}
	})
}
