package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestListTasks(t *testing.T) {
	var buf bytes.Buffer
	ListTasks(&buf)

	out := buf.String()
	for _, task := range Tasks() {
		if !strings.Contains(out, task.Key) {
			t.Errorf("ListTasks output missing key %q:\n%s", task.Key, out)
		}
	}
}

func TestRunNonInteractiveUnknownTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := RunNonInteractive(ctx, "does-not-exist", &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected nonzero exit code for an unknown task")
	}
	if !strings.Contains(stderr.String(), "unknown task") {
		t.Errorf("stderr = %q, want it to mention \"unknown task\"", stderr.String())
	}
}

func TestRunNonInteractiveFmt(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to gofmt; skipped with -short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := RunNonInteractive(ctx, "fmt", &stdout, &stderr)

	// The repository is expected to be gofmt-clean; if it ever isn't, this
	// still exercises the dispatch path correctly (nonzero + files listed on
	// stdout) rather than failing for the wrong reason.
	if code != 0 && stdout.Len() == 0 {
		t.Fatalf("fmt failed with no output to explain why (stderr: %q)", stderr.String())
	}
}
