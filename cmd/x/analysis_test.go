package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCoverageMeetsThreshold(t *testing.T) {
	t.Setenv("AISOTROPY_X_COVER_THRESHOLD", "")

	tests := []struct {
		name     string
		exitCode int
		stdout   string
		want     bool
	}{
		{
			name:     "at default threshold passes",
			exitCode: 0,
			stdout:   "total:\t\t\t\t\t(statements)\t\t60.0%\n",
			want:     true,
		},
		{
			name:     "above default threshold passes",
			exitCode: 0,
			stdout:   "total:\t\t\t\t\t(statements)\t\t85.4%\n",
			want:     true,
		},
		{
			name:     "below default threshold fails",
			exitCode: 0,
			stdout:   "total:\t\t\t\t\t(statements)\t\t12.3%\n",
			want:     false,
		},
		{
			name:     "nonzero exit fails even with a high total line",
			exitCode: 1,
			stdout:   "total:\t\t\t\t\t(statements)\t\t99.9%\n",
			want:     false,
		},
		{
			name:     "missing total line fails",
			exitCode: 0,
			stdout:   "ok  \tsomepkg\t0.010s\tcoverage: 45.2% of statements\n",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := coverageMeetsThreshold(tt.exitCode, tt.stdout); got != tt.want {
				t.Errorf("coverageMeetsThreshold(%d, %q) = %v, want %v", tt.exitCode, tt.stdout, got, tt.want)
			}
		})
	}
}

func TestCoverageThresholdEnvOverride(t *testing.T) {
	t.Setenv("AISOTROPY_X_COVER_THRESHOLD", "")
	if got := coverageThreshold(); got != defaultCoverageThresholdPercent {
		t.Fatalf("coverageThreshold() with unset env = %v, want default %v", got, defaultCoverageThresholdPercent)
	}

	t.Setenv("AISOTROPY_X_COVER_THRESHOLD", "80")
	if got := coverageThreshold(); got != 80 {
		t.Fatalf("coverageThreshold() with env=80 = %v, want 80", got)
	}

	t.Setenv("AISOTROPY_X_COVER_THRESHOLD", "not-a-number")
	if got := coverageThreshold(); got != defaultCoverageThresholdPercent {
		t.Fatalf("coverageThreshold() with unparsable env = %v, want default %v (fallback)", got, defaultCoverageThresholdPercent)
	}
}

// TestLayerCheckPassCatchesForbiddenImport proves the "deps" task's
// EvalOutput would fail a memory->facts (or memory->semantic) import if one
// existed: it feeds layerCheckPass a synthetic `go list -json` payload
// exactly like real output, but with internal/memory importing
// internal/facts, and asserts the check reports it (satisfies the acceptance
// criterion that this test "proves it would catch a memory->facts import if
// present" without needing to actually introduce the violation into the
// tree).
func TestLayerCheckPassCatchesForbiddenImport(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stdout   string
		want     bool
	}{
		{
			name:     "clean imports pass",
			exitCode: 0,
			stdout:   `{"ImportPath":"github.com/Luum-Home/aisotropy/internal/memory","Imports":["context","github.com/Luum-Home/aisotropy/internal/llm","github.com/dgraph-io/badger/v4"]}`,
			want:     true,
		},
		{
			name:     "import of internal/facts is caught",
			exitCode: 0,
			stdout:   `{"ImportPath":"github.com/Luum-Home/aisotropy/internal/memory","Imports":["context","github.com/Luum-Home/aisotropy/internal/facts"]}`,
			want:     false,
		},
		{
			name:     "import of internal/semantic is caught",
			exitCode: 0,
			stdout:   `{"ImportPath":"github.com/Luum-Home/aisotropy/internal/memory","Imports":["context","github.com/Luum-Home/aisotropy/internal/semantic"]}`,
			want:     false,
		},
		{
			name:     "multiple back-to-back package objects are all checked",
			exitCode: 0,
			stdout: `{"ImportPath":"github.com/Luum-Home/aisotropy/internal/memory","Imports":["context"]}
{"ImportPath":"github.com/Luum-Home/aisotropy/internal/memory/sub","Imports":["github.com/Luum-Home/aisotropy/internal/facts"]}`,
			want: false,
		},
		{
			name:     "nonzero exit fails regardless of stdout",
			exitCode: 1,
			stdout:   `{"ImportPath":"github.com/Luum-Home/aisotropy/internal/memory","Imports":["context"]}`,
			want:     false,
		},
		{
			name:     "empty stdout fails (nothing was checked)",
			exitCode: 0,
			stdout:   "",
			want:     false,
		},
		{
			name:     "malformed JSON fails closed",
			exitCode: 0,
			stdout:   "{not valid json",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := layerCheckPass(tt.exitCode, tt.stdout); got != tt.want {
				t.Errorf("layerCheckPass(%d, %q) = %v, want %v", tt.exitCode, tt.stdout, got, tt.want)
			}
		})
	}
}

// TestDepsTaskCleanOnCurrentTree runs the real "deps" task end to end
// (shells out to `go list -json ./internal/memory/...`) and asserts it
// reports no violation on the current, clean tree — the other half of
// acceptance criterion 6 (the synthetic case above proves detection; this
// proves the real command reports a pass today).
func TestDepsTaskCleanOnCurrentTree(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; skipped with -short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := depsCmd(ctx)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -json ./internal/memory/... failed: %v", err)
	}
	if !layerCheckPass(0, string(out)) {
		t.Errorf("layerCheckPass reported a violation on the current tree; output:\n%s", out)
	}
}

func TestDecodeGoListPackages(t *testing.T) {
	pkgs, err := decodeGoListPackages(`{"ImportPath":"a","Imports":["x"]}
{"ImportPath":"b","Imports":["y","z"]}`)
	if err != nil {
		t.Fatalf("decodeGoListPackages returned error: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("decodeGoListPackages returned %d packages, want 2", len(pkgs))
	}
	if pkgs[0].ImportPath != "a" || pkgs[1].ImportPath != "b" {
		t.Errorf("unexpected package order/content: %+v", pkgs)
	}

	if _, err := decodeGoListPackages("not json"); err == nil {
		t.Error("expected an error decoding malformed JSON, got nil")
	}
}

func TestTasksCmdListsEveryTask(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to cat; skipped with -short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := tasksCmd(ctx)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("tasksCmd failed: %v", err)
	}
	for _, task := range Tasks() {
		if !strings.Contains(string(out), task.Key) {
			t.Errorf("tasksCmd output missing key %q:\n%s", task.Key, out)
		}
	}
}

func TestCoverProfilePathIsUniqueAndWritable(t *testing.T) {
	p1, err := coverProfilePath()
	if err != nil {
		t.Fatalf("coverProfilePath() error: %v", err)
	}
	defer os.Remove(p1)

	p2, err := coverProfilePath()
	if err != nil {
		t.Fatalf("coverProfilePath() error: %v", err)
	}
	defer os.Remove(p2)

	if p1 == p2 {
		t.Fatalf("coverProfilePath() returned the same path twice: %q", p1)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Errorf("coverProfilePath() path %q is not accessible: %v", p1, err)
	}
}

// The following tests assert command CONSTRUCTION only (Args), never
// executing dup/gocognit/deadcode/govulncheck for real: those tools either
// require a real golangci-lint binary or fetch a package via network
// (`go run pkg@latest`), which would make tests slow, flaky, and
// network-dependent. Asserting cmd.Args is a real, load-bearing assertion —
// it is what streamCmd actually executes — without paying that cost.

func TestDupCmdArgs(t *testing.T) {
	cmd := dupCmd(context.Background())
	want := []string{"golangci-lint", "run", "--enable-only", "dupl", "./..."}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("dupCmd Args = %v, want %v", cmd.Args, want)
	}
}

func TestGocognitCmdArgs(t *testing.T) {
	cmd := gocognitCmd(context.Background())
	wantPrefix := []string{"go", "run", "github.com/uudashr/gocognit/cmd/gocognit@latest", "-over", "15"}
	if len(cmd.Args) <= len(wantPrefix) {
		t.Fatalf("gocognitCmd Args = %v, want at least %d elements (prefix + >=1 target dir)", cmd.Args, len(wantPrefix)+1)
	}
	for i, want := range wantPrefix {
		if cmd.Args[i] != want {
			t.Fatalf("gocognitCmd Args[%d] = %q, want %q (full: %v)", i, cmd.Args[i], want, cmd.Args)
		}
	}
}

func TestDeadcodeCmdArgs(t *testing.T) {
	cmd := deadcodeCmd(context.Background())
	want := []string{"go", "run", "golang.org/x/tools/cmd/deadcode@latest", "./..."}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("deadcodeCmd Args = %v, want %v", cmd.Args, want)
	}
}

func TestGovulncheckCmdArgs(t *testing.T) {
	cmd := govulncheckCmd(context.Background())
	want := []string{"go", "run", "golang.org/x/vuln/cmd/govulncheck@latest", "./..."}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("govulncheckCmd Args = %v, want %v", cmd.Args, want)
	}
}

func TestCoverCmdBuildsChainedShellScript(t *testing.T) {
	cmd := coverCmd(context.Background())
	if len(cmd.Args) != 3 || cmd.Args[0] != "sh" || cmd.Args[1] != "-c" {
		t.Fatalf("coverCmd Args = %v, want [sh -c <script>]", cmd.Args)
	}
	script := cmd.Args[2]
	if !strings.Contains(script, "go test -coverprofile=") {
		t.Errorf("script missing coverprofile step: %q", script)
	}
	if !strings.Contains(script, "go tool cover -func=") {
		t.Errorf("script missing cover -func step: %q", script)
	}
	if !strings.Contains(script, "&&") {
		t.Errorf("script should chain both commands with &&: %q", script)
	}
}

// TestCoverCmdProfileCreateFailureFailsLoudly forces coverProfilePath's
// os.CreateTemp to fail (TMPDIR pointed at a nonexistent directory) and
// asserts coverCmd falls back to the documented loud-failure script instead
// of silently skipping coverage enforcement.
func TestCoverCmdProfileCreateFailureFailsLoudly(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	cmd := coverCmd(context.Background())
	if len(cmd.Args) != 3 || cmd.Args[0] != "sh" {
		t.Fatalf("coverCmd Args = %v, want a 3-element [sh -c <script>] fallback", cmd.Args)
	}
	if !strings.Contains(cmd.Args[2], "failed to create temp profile") {
		t.Fatalf("script = %q, want it to mention the temp profile failure", cmd.Args[2])
	}
}

// TestTasksCmdFileCreateFailureFailsLoudly is the same failure-injection
// proof for tasksCmd's own os.CreateTemp call.
func TestTasksCmdFileCreateFailureFailsLoudly(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	cmd := tasksCmd(context.Background())
	if len(cmd.Args) != 3 || cmd.Args[0] != "sh" {
		t.Fatalf("tasksCmd Args = %v, want a 3-element [sh -c <script>] fallback", cmd.Args)
	}
	if !strings.Contains(cmd.Args[2], "failed to create temp file") {
		t.Fatalf("script = %q, want it to mention the temp file failure", cmd.Args[2])
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	got := shellQuote(`a'b`)
	want := `'a'\''b'`
	if got != want {
		t.Fatalf("shellQuote(%q) = %q, want %q", `a'b`, got, want)
	}
}
