package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFloatFromEnv(t *testing.T) {
	const key = "AISOTROPY_TEST_FLOAT_FROM_ENV"

	t.Setenv(key, "")
	if got := floatFromEnv(key); got != 0 {
		t.Errorf("unset -> %v, want 0", got)
	}

	t.Setenv(key, "3.5")
	if got := floatFromEnv(key); got != 3.5 {
		t.Errorf("\"3.5\" -> %v, want 3.5", got)
	}

	t.Setenv(key, "not-a-number")
	if got := floatFromEnv(key); got != 0 {
		t.Errorf("unparsable -> %v, want fallback 0", got)
	}
}

func TestInt64FromEnv(t *testing.T) {
	const key = "AISOTROPY_TEST_INT64_FROM_ENV"

	t.Setenv(key, "")
	if got := int64FromEnv(key); got != 0 {
		t.Errorf("unset -> %v, want 0", got)
	}

	t.Setenv(key, "42")
	if got := int64FromEnv(key); got != 42 {
		t.Errorf("\"42\" -> %v, want 42", got)
	}

	t.Setenv(key, "not-a-number")
	if got := int64FromEnv(key); got != 0 {
		t.Errorf("unparsable -> %v, want fallback 0", got)
	}
}

func TestDefaultPathsAndSourceDir(t *testing.T) {
	if got := sourceDir(); got == "" || got == "." {
		t.Errorf("sourceDir() = %q, want a real directory (runtime.Caller succeeds for a normal test binary)", got)
	}
	if want := filepath.Join("testdata", "generic.json"); !strings.HasSuffix(defaultDataPath(), want) {
		t.Errorf("defaultDataPath() = %q, want it to end with %q", defaultDataPath(), want)
	}
	if !strings.HasSuffix(defaultOutPath(), "results.txt") {
		t.Errorf("defaultOutPath() = %q, want it to end with results.txt", defaultOutPath())
	}
	// Both paths resolve relative to this source file, so they must exist /
	// be creatable regardless of the test's working directory.
	if _, err := os.Stat(defaultDataPath()); err != nil {
		t.Errorf("defaultDataPath() %q does not exist: %v", defaultDataPath(), err)
	}
}

func TestRun_RejectsNonPositiveN(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if err := run(context.Background(), n, 200, "", ""); err == nil {
			t.Errorf("run with n=%d: expected an error", n)
		}
	}
}

func TestRun_RejectsNExceedingCap(t *testing.T) {
	if err := run(context.Background(), 30, 10, "", ""); err == nil {
		t.Fatal("expected an error when n exceeds cap")
	}
}

func TestRun_PropagatesQuestionLoadError(t *testing.T) {
	if err := run(context.Background(), 1, 10, "/nonexistent/path/to/questions.json", ""); err == nil {
		t.Fatal("expected an error for a nonexistent data path")
	}
}

// TestRun_StubEndToEndWritesReport exercises run()'s full happy path on the
// keyless stub (no network, no credentials): it loads the bundled generic
// question set via loadQuestionsFile/defaultDataPath, runs the pipeline, and
// writes the rendered report to outPath.
func TestRun_StubEndToEndWritesReport(t *testing.T) {
	clearProviderEnv(t)

	dir := t.TempDir()
	out := filepath.Join(dir, "report.txt")

	if err := run(context.Background(), 2, 10, "", out); err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading report: %v", err)
	}
	if !strings.Contains(string(data), "STUB RUN") {
		t.Errorf("report does not mention STUB RUN:\n%s", data)
	}
	if !strings.Contains(string(data), "END-TO-END ANSWER-QUALITY EVAL") {
		t.Errorf("report missing header:\n%s", data)
	}
}

// TestRun_EmptyOutPathSkipsWrite proves outPath="" is honored (no file
// written, no error).
func TestRun_EmptyOutPathSkipsWrite(t *testing.T) {
	clearProviderEnv(t)

	if err := run(context.Background(), 1, 10, "", ""); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestRun_WriteReportErrorPropagates forces os.WriteFile to fail (a
// non-existent parent directory) and asserts run() surfaces that error
// instead of swallowing it.
func TestRun_WriteReportErrorPropagates(t *testing.T) {
	clearProviderEnv(t)

	bad := filepath.Join(t.TempDir(), "no-such-subdir", "report.txt")
	if err := run(context.Background(), 1, 10, "", bad); err == nil {
		t.Fatal("expected an error writing the report to a nonexistent directory")
	}
}

// TestRun_NLargerThanQuestionSetUsesWholeSet proves the "n >= len(questions)"
// path (no slicing) also runs cleanly.
func TestRun_NLargerThanQuestionSetUsesWholeSet(t *testing.T) {
	clearProviderEnv(t)

	if err := run(context.Background(), 1000, 2000, "", ""); err != nil {
		t.Fatalf("run: %v", err)
	}
}
