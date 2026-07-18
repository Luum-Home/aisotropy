package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTasksNonEmptyAndUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, task := range Tasks() {
		if task.Key == "" {
			t.Fatalf("task has empty Key: %+v", task)
		}
		if seen[task.Key] {
			t.Fatalf("duplicate task key %q", task.Key)
		}
		seen[task.Key] = true

		if task.Description == "" {
			t.Errorf("task %q has an empty Description", task.Key)
		}
		if len(task.Steps) == 0 && task.NewCmd == nil {
			t.Errorf("leaf task %q has neither NewCmd nor Steps", task.Key)
		}
		if len(task.Steps) > 0 && task.NewCmd != nil {
			t.Errorf("composite task %q must not also set NewCmd", task.Key)
		}
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one registered task")
	}
}

// TestAnalysisTasksRegistered asserts every code-optimization analysis task
// (the "cockpit" expansion: dup, complexity, deadcode, cover, vuln, deps,
// audit, tasks) is registered with a non-empty description, so `x list`/`x
// --help`/the TUI always document them.
func TestAnalysisTasksRegistered(t *testing.T) {
	wantKeys := []string{"dup", "complexity", "deadcode", "cover", "vuln", "deps", "audit", "tasks"}
	for _, key := range wantKeys {
		task, ok := Lookup(key)
		if !ok {
			t.Errorf("expected analysis task %q to be registered", key)
			continue
		}
		if task.Description == "" {
			t.Errorf("analysis task %q has an empty Description", key)
		}
	}
}

func TestAuditStepsReferenceRealTasks(t *testing.T) {
	audit, ok := Lookup("audit")
	if !ok {
		t.Fatal("expected an \"audit\" composite task")
	}
	if len(audit.Steps) == 0 {
		t.Fatal("\"audit\" should have Steps")
	}
	for _, key := range audit.Steps {
		if _, ok := Lookup(key); !ok {
			t.Errorf("audit step %q does not resolve to a registered task", key)
		}
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("Lookup found a task for an unknown key")
	}

	task, ok := Lookup("fmt")
	if !ok {
		t.Fatal("Lookup(\"fmt\") should find the fmt task")
	}
	if task.Key != "fmt" {
		t.Fatalf("Lookup(\"fmt\").Key = %q, want \"fmt\"", task.Key)
	}
}

func TestCheckStepsReferenceRealTasks(t *testing.T) {
	check, ok := Lookup("check")
	if !ok {
		t.Fatal("expected a \"check\" composite task")
	}
	if len(check.Steps) == 0 {
		t.Fatal("\"check\" should have Steps")
	}
	for _, key := range check.Steps {
		if _, ok := Lookup(key); !ok {
			t.Errorf("check step %q does not resolve to a registered task", key)
		}
	}
}

// TestCheckGolangciLintNotOnPath forces exec.LookPath("golangci-lint") to
// fail by pointing PATH at an empty directory, exercising the "not found"
// branch without depending on whether the real tool happens to be installed
// in the environment running this test.
func TestCheckGolangciLintNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	ok, hint := checkGolangciLint()
	if ok {
		t.Fatal("expected golangci-lint to be reported unavailable with an empty PATH")
	}
	if hint == "" {
		t.Fatal("expected a non-empty install hint")
	}
}

// TestCheckGolangciLintOnPath exercises the "found" branch with a fake
// golangci-lint executable placed on a scoped PATH, independent of whether
// the real tool is installed.
func TestCheckGolangciLintOnPath(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "golangci-lint")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing fake golangci-lint: %v", err)
	}
	t.Setenv("PATH", dir)

	ok, hint := checkGolangciLint()
	if !ok {
		t.Fatalf("expected golangci-lint to be reported available, hint = %q", hint)
	}
	if hint != "" {
		t.Fatalf("hint = %q, want empty on success", hint)
	}
}

// TestGofmtTargetsFallsBackWhenGoListFails proves gofmtTargets returns the
// documented "." fallback when `go list` itself cannot run (here: "go" is
// not resolvable on PATH at all).
func TestGofmtTargetsFallsBackWhenGoListFails(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	dirs := gofmtTargets(context.Background())
	if len(dirs) != 1 || dirs[0] != "." {
		t.Fatalf("gofmtTargets() = %v, want [\".\"] when go list fails", dirs)
	}
}

// TestGofmtTargetsListsRealModuleDirs is the complementary success path: on
// this repository's real PATH, gofmt targets resolve to real directories,
// not the "." fallback.
func TestGofmtTargetsListsRealModuleDirs(t *testing.T) {
	dirs := gofmtTargets(context.Background())
	if len(dirs) == 0 {
		t.Fatal("expected at least one directory")
	}
	if len(dirs) == 1 && dirs[0] == "." {
		t.Fatal("expected real module directories, not the \".\" fallback, on a working PATH")
	}
}

// TestEveryLeafTaskNewCmdConstructsARunnableCommand calls NewCmd for every
// registered leaf task (never Start()s it) so every command-builder closure
// in the registry table is exercised — a real assertion on the constructed
// *exec.Cmd, not a call-through — without the cost/flakiness of actually
// running go build/go vet/golangci-lint/etc for real.
func TestEveryLeafTaskNewCmdConstructsARunnableCommand(t *testing.T) {
	for _, task := range Tasks() {
		if task.NewCmd == nil {
			continue
		}
		t.Run(task.Key, func(t *testing.T) {
			cmd := task.NewCmd(context.Background())
			if cmd == nil {
				t.Fatalf("%s: NewCmd returned a nil *exec.Cmd", task.Key)
			}
			if len(cmd.Args) == 0 {
				t.Fatalf("%s: NewCmd command has no Args", task.Key)
			}
		})
	}
}

func TestFmtClean(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stdout   string
		want     bool
	}{
		{"exit 0 empty output passes", 0, "", true},
		{"exit 0 with trailing whitespace passes", 0, "   \n", true},
		{"exit 0 listing a file fails", 0, "main.go\n", false},
		{"nonzero exit fails even with empty output", 1, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtClean(tt.exitCode, tt.stdout); got != tt.want {
				t.Errorf("fmtClean(%d, %q) = %v, want %v", tt.exitCode, tt.stdout, got, tt.want)
			}
		})
	}
}
