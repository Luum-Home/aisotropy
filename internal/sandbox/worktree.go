package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree is the git plumbing that gives each sandboxed task its OWN checkout
// on its OWN branch, physically separate from the operator's working copy.
//
// # Why a worktree, not just a mount
//
// The sandbox runs untrusted, model-driven edits. If those edits landed
// directly on the repo's checked-out branch, a bad run would corrupt the
// operator's working state and there would be no clean "keep it / throw it
// away" boundary. A `git worktree` gives a second working directory backed by
// the same object store but on an isolated task branch: mount THAT into the
// sandbox (as Spec.BindMounts → Spec.Workdir), let the run scribble freely, and
// afterwards either MergeToHead the branch (accept the work) or prune the
// worktree (discard it) — atomically, with full git history of exactly what the
// run changed (CommitsSince).
//
// Worktree holds the git binary to shell out to (os/exec, no libgit2/CGO),
// empty meaning "git" on PATH.
type Worktree struct {
	// Git is the git CLI to invoke; empty means "git" on PATH.
	Git string
}

func (w Worktree) git() string {
	if w.Git != "" {
		return w.Git
	}
	return "git"
}

// AddResult is what [Worktree.Add] hands back: where the isolated checkout
// lives, the branch it is on, and a cleanup that removes it.
type AddResult struct {
	// Path is the absolute host path of the new worktree directory. This is
	// what a task runner bind-mounts into the sandbox (Mount.Source) at
	// Spec.Workdir.
	Path string
	// Branch is the task branch the worktree is checked out on
	// (aisotropy/task/<ts>-<rand>).
	Branch string
	// Base is the commit SHA the branch started from — the merge base and the
	// argument to pass to CommitsSince to enumerate the run's commits.
	Base string
	// Cleanup removes the worktree (git worktree remove --force) and is safe
	// to defer. It does NOT delete the branch, so a merged or inspectable
	// branch survives; prune the branch separately if unwanted.
	Cleanup func() error
}

// Add creates a fresh worktree off repoDir's current HEAD on a new, unique task
// branch aisotropy/task/<unix-ts>-<rand>. The worktree directory is created
// under repoDir/../ (a sibling temp dir) so it never nests inside the repo. The
// name argument, when non-empty, is folded into the branch for readability.
//
// The returned AddResult.Cleanup removes the worktree; the caller owns the
// decision to keep, merge, or delete the branch.
func (w Worktree) Add(ctx context.Context, repoDir, name string) (AddResult, error) {
	base, err := w.revParse(ctx, repoDir, "HEAD")
	if err != nil {
		return AddResult{}, fmt.Errorf("sandbox: resolve HEAD: %w", err)
	}

	branch := taskBranch(name)
	// Place the worktree in a sibling dir of the repo to avoid nesting it
	// inside the tracked tree.
	slug := strings.ReplaceAll(strings.TrimPrefix(branch, "aisotropy/task/"), "/", "-")
	wtPath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-wt-"+slug)

	// git worktree add -b <branch> <path> <base>
	if _, err := w.run(ctx, repoDir, "worktree", "add", "-b", branch, wtPath, base); err != nil {
		return AddResult{}, fmt.Errorf("sandbox: worktree add: %w", err)
	}

	cleanup := func() error {
		if _, err := w.run(context.Background(), repoDir, "worktree", "remove", "--force", wtPath); err != nil {
			return fmt.Errorf("sandbox: worktree remove: %w", err)
		}
		return nil
	}

	return AddResult{Path: wtPath, Branch: branch, Base: base, Cleanup: cleanup}, nil
}

// MergeToHead merges branch into the currently checked-out branch of repoDir
// with an explicit merge commit (--no-ff), so the task's work lands as a
// reviewable, revertable unit rather than being fast-forwarded invisibly into
// history. A merge conflict surfaces as an error carrying git's stderr; the
// caller can then abort (git merge --abort) or resolve out of band.
func (w Worktree) MergeToHead(ctx context.Context, repoDir, branch string) error {
	if _, err := w.run(ctx, repoDir, "merge", "--no-ff", "-m",
		"merge sandbox task branch "+branch, branch); err != nil {
		return fmt.Errorf("sandbox: merge %s: %w", branch, err)
	}
	return nil
}

// CommitsSince returns the SHAs of commits on worktreeDir reachable from HEAD
// but not from base — i.e. exactly the commits the sandboxed run produced —
// newest first. An empty slice means the run committed nothing.
func (w Worktree) CommitsSince(ctx context.Context, worktreeDir, base string) ([]string, error) {
	out, err := w.run(ctx, worktreeDir, "rev-list", base+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("sandbox: rev-list: %w", err)
	}
	var shas []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			shas = append(shas, s)
		}
	}
	return shas, nil
}

// Prune runs `git worktree prune` on repoDir, clearing administrative entries
// for worktrees whose directories were removed out from under git (e.g. a
// crashed run whose Cleanup never ran). Best-effort housekeeping.
func (w Worktree) Prune(ctx context.Context, repoDir string) error {
	if _, err := w.run(ctx, repoDir, "worktree", "prune"); err != nil {
		return fmt.Errorf("sandbox: worktree prune: %w", err)
	}
	return nil
}

func (w Worktree) revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := w.run(ctx, dir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// run executes `git -C <dir> <args…>` and returns stdout, folding stderr into
// the error on failure.
func (w Worktree) run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, w.git(), full...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// taskBranch builds a unique branch name aisotropy/task/<unix>-<rand>[-<name>].
func taskBranch(name string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	branch := fmt.Sprintf("aisotropy/task/%d-%s", time.Now().Unix(), hex.EncodeToString(b))
	if slug := slugify(name); slug != "" {
		branch += "-" + slug
	}
	return branch
}

// slugify lowercases name and keeps [a-z0-9-], collapsing anything else to a
// dash, so a human label is safe in a git ref.
func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
