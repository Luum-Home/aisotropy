//go:build keybroker

package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestParseTaskRunArgs covers the argument contract of `task run`: the required
// prompt, the positional repo-dir (exactly one), the provider allow-list, and
// the flag defaults — all without booting a sandbox.
func TestParseTaskRunArgs(t *testing.T) {
	t.Run("valid with defaults", func(t *testing.T) {
		cfg, err := parseTaskRunArgs([]string{"./repo", "--prompt", "fix the bug"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RepoDir != "./repo" {
			t.Fatalf("RepoDir=%q, want ./repo", cfg.RepoDir)
		}
		if cfg.Prompt != "fix the bug" {
			t.Fatalf("Prompt=%q", cfg.Prompt)
		}
		if cfg.Provider != "docker" {
			t.Fatalf("Provider default=%q, want docker", cfg.Provider)
		}
		if cfg.Image != "" || cfg.MaxSteps != 0 {
			t.Fatalf("expected empty image + 0 max-steps, got image=%q max-steps=%d", cfg.Image, cfg.MaxSteps)
		}
	})

	t.Run("all flags + podman", func(t *testing.T) {
		cfg, err := parseTaskRunArgs([]string{
			"--prompt", "refactor", "--provider", "podman",
			"--image", "golang:1.24", "--max-steps", "12", "/abs/repo",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Provider != "podman" || cfg.Image != "golang:1.24" || cfg.MaxSteps != 12 || cfg.RepoDir != "/abs/repo" {
			t.Fatalf("parsed wrong: %+v", cfg)
		}
	})

	t.Run("provider is case-insensitive", func(t *testing.T) {
		cfg, err := parseTaskRunArgs([]string{"r", "--prompt", "p", "--provider", "Podman"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Provider != "podman" {
			t.Fatalf("Provider=%q, want normalized podman", cfg.Provider)
		}
	})

	t.Run("missing prompt", func(t *testing.T) {
		if _, err := parseTaskRunArgs([]string{"./repo"}); err == nil {
			t.Fatal("expected error for missing --prompt")
		}
	})

	t.Run("missing repo-dir", func(t *testing.T) {
		if _, err := parseTaskRunArgs([]string{"--prompt", "p"}); err == nil {
			t.Fatal("expected error for missing <repo-dir>")
		}
	})

	t.Run("too many positionals", func(t *testing.T) {
		if _, err := parseTaskRunArgs([]string{"a", "b", "--prompt", "p"}); err == nil {
			t.Fatal("expected error for >1 <repo-dir>")
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		_, err := parseTaskRunArgs([]string{"./repo", "--prompt", "p", "--provider", "vercel"})
		if err == nil {
			t.Fatal("expected error for unknown provider")
		}
		if !strings.Contains(err.Error(), "docker or podman") {
			t.Fatalf("error should name the allow-list, got: %v", err)
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		if _, err := parseTaskRunArgs([]string{"./repo", "--prompt", "p", "--nope"}); err == nil {
			t.Fatal("expected error for unknown flag")
		}
	})
}

// TestSelectProvider confirms the provider allow-list resolves the two built-ins
// (and defaults empty to docker) while rejecting anything else.
func TestSelectProvider(t *testing.T) {
	for _, name := range []string{"", "docker", "podman"} {
		p, err := selectProvider(name)
		if err != nil {
			t.Fatalf("selectProvider(%q) unexpected error: %v", name, err)
		}
		if p == nil {
			t.Fatalf("selectProvider(%q) returned nil provider", name)
		}
	}
	if _, err := selectProvider("k8s"); err == nil {
		t.Fatal("expected error for unknown provider k8s")
	}
}

// TestTaskDispatch checks the subcommand routing and usage exits without running
// a sandbox: no args -> usage (exit 2); help -> exit 0; unknown subcommand -> 2;
// a parse failure on `run` -> exit 2 with a usage hint.
func TestTaskDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"no subcommand", []string{"task"}, 2},
		{"help", []string{"task", "help"}, 0},
		{"unknown subcommand", []string{"task", "explode"}, 2},
		{"run missing prompt", []string{"task", "run", "./repo"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(tc.args, &out, &errb); code != tc.wantCode {
				t.Fatalf("run(%v) = %d, want %d (stderr=%q)", tc.args, code, tc.wantCode, errb.String())
			}
		})
	}
}

// TestTaskRunSmoke_Docker is the Docker-gated end-to-end smoke: it drives the
// real `aisotropy task run` path against a throwaway git repo with the keyless
// stub client. It skips when the docker CLI is absent (the honesty gate), so the
// fast test suite never requires a daemon.
func TestTaskRunSmoke_Docker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not found; skipping task-run smoke")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable; skipping task-run smoke")
	}

	// A minimal initialized git repo for the task to work on.
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@example.com", "-c", "user.name=Test", "commit", "--allow-empty", "-q", "-m", "root"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	cfg := taskRunConfig{
		RepoDir:  repo,
		Prompt:   "list the files in this repository",
		Provider: "docker",
		MaxSteps: 2,
	}
	var out, errb bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if code := execTaskRun(ctx, cfg, &out, &errb); code != 0 {
		t.Fatalf("execTaskRun exit=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "steps:") {
		t.Fatalf("expected a steps summary in output, got:\n%s", out.String())
	}
}
