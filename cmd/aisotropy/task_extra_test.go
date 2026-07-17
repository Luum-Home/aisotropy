//go:build keybroker

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestExecTaskRunRejectsBadProvider covers execTaskRun's defense against an
// unvalidated provider (a path the parser normally catches first). It lives in
// this keybroker-tagged file because execTaskRun/taskRunConfig are compiled only
// under -tags keybroker; the reorderFlagsFirst tests it used to sit beside stay
// in cli_extra_test.go since that helper is now in the always-built flags.go.
func TestExecTaskRunRejectsBadProvider(t *testing.T) {
	var out, errb bytes.Buffer
	code := execTaskRun(context.Background(), taskRunConfig{RepoDir: "x", Prompt: "p", Provider: "bogus"}, &out, &errb)
	if code != 2 {
		t.Fatalf("execTaskRun bad provider: code=%d want 2 (stderr=%q)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "unknown --provider") {
		t.Fatalf("expected provider error, got %q", errb.String())
	}
}
