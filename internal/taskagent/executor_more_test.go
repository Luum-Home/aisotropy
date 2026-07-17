package taskagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunShellEmptyCmdIsResult(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{}}
	res, err := e.runShell(context.Background(), "   ")
	if err != nil {
		t.Fatalf("runShell empty: %v", err)
	}
	if !strings.Contains(res, "empty cmd") {
		t.Fatalf("result = %q, want an empty-cmd note", res)
	}
}

func TestRunShellExecErrorPropagates(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{execErr: errors.New("container gone")}}
	if _, err := e.runShell(context.Background(), "ls"); err == nil {
		t.Fatal("runShell should surface an exec infrastructure error")
	}
}

func TestWriteFileEmptyPathIsResult(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{}}
	res, err := e.writeFile(context.Background(), "  ", "content")
	if err != nil {
		t.Fatalf("writeFile empty path: %v", err)
	}
	if !strings.Contains(res, "empty path") {
		t.Fatalf("result = %q, want an empty-path note", res)
	}
}

func TestWriteFileExecErrorPropagates(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{execErr: errors.New("boom")}}
	if _, err := e.writeFile(context.Background(), "a.txt", "x"); err == nil {
		t.Fatal("writeFile should surface an exec infrastructure error")
	}
}

func TestWriteFileNonZeroExitFoldedIntoResult(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{exitCode: 1, stderr: "permission denied"}}
	res, err := e.writeFile(context.Background(), "a.txt", "x")
	if err != nil {
		t.Fatalf("writeFile non-zero exit should not be a Go error: %v", err)
	}
	if !strings.Contains(res, "permission denied") {
		t.Fatalf("result = %q, want the stderr folded in", res)
	}
}

func TestReadFileEmptyPathIsResult(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{}}
	res, err := e.readFile(context.Background(), "")
	if err != nil {
		t.Fatalf("readFile empty path: %v", err)
	}
	if !strings.Contains(res, "empty path") {
		t.Fatalf("result = %q, want an empty-path note", res)
	}
}

func TestReadFileExecErrorPropagates(t *testing.T) {
	e := &sandboxExecutor{sb: &fakeSandbox{execErr: errors.New("boom")}}
	if _, err := e.readFile(context.Background(), "a.txt"); err == nil {
		t.Fatal("readFile should surface an exec infrastructure error")
	}
}

// TestGitCommitDefaultMessage covers the empty-message branch: an empty commit
// message is replaced with the default, and a staged change commits.
func TestGitCommitDefaultMessage(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)
	writeFileHost(t, repo, "new.txt", "content")

	e := &sandboxExecutor{sb: &fakeSandbox{}, worktreeDir: repo}
	res, err := e.gitCommit(context.Background(), "   ")
	if err != nil {
		t.Fatalf("gitCommit: %v", err)
	}
	if !strings.Contains(res, "committed") {
		t.Fatalf("result = %q, want a commit confirmation with the default message", res)
	}
	if !strings.Contains(res, "aisotropy sandbox task commit") {
		t.Fatalf("result = %q, want the default commit message", res)
	}
}

// TestGitCommitAddFailure covers the `git add` failure branch: a worktreeDir
// that is not a git repository makes `git add -A` fail, reported as result text.
func TestGitCommitAddFailure(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	notARepo := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &sandboxExecutor{sb: &fakeSandbox{}, worktreeDir: notARepo}
	res, err := e.gitCommit(context.Background(), "msg")
	if err != nil {
		t.Fatalf("gitCommit should fold a git failure into the result, got err: %v", err)
	}
	if !strings.Contains(res, "git add failed") {
		t.Fatalf("result = %q, want a git-add-failed note", res)
	}
}

func TestDefaultConcurrency(t *testing.T) {
	n := defaultConcurrency()
	if n < 1 {
		t.Fatalf("defaultConcurrency = %d, want >= 1", n)
	}
	if n > maxDefaultConcurrency {
		t.Fatalf("defaultConcurrency = %d, want <= %d", n, maxDefaultConcurrency)
	}
}
