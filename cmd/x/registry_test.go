package main

import "testing"

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
