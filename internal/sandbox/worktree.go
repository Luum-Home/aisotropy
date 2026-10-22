package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/gitenv"
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

// BranchStrategy decides what happens to a sandboxed task's branch once the run
// finishes. It is the honest, closed set of dispositions the sandbox layer will
// perform on behalf of the task runner (which selects one and passes it to
// [Worktree.Finalize]); anything outside this set is deliberately not offered.
//
// A conspicuous OMISSION: there is no "head" strategy in which the agent writes
// straight into the operator's host checkout. That would defeat the entire
// point of the worktree — the isolation boundary that lets a run be reviewed or
// thrown away — so it is intentionally not modelled here.
type BranchStrategy int

const (
	// MergeToHead is the default: after the run, merge the task branch into the
	// currently checked-out branch of the repo with an explicit merge commit
	// (--no-ff) IF the run produced commits, and otherwise do nothing (the
	// branch carried no work, so there is nothing to land — it is simply
	// discarded by the caller's worktree Cleanup). This is the "accept the
	// work automatically" disposition: the run's output flows into HEAD as one
	// reviewable, revertable merge.
	MergeToHead BranchStrategy = iota

	// ReturnBranch keeps the task branch and does NOT merge it. Finalize leaves
	// the branch present and unmerged and hands it back in
	// [FinalizeResult.Branch] for the caller to review, merge, open a PR from,
	// or delete out of band. This is the "human/caller decides" disposition:
	// the sandbox produced isolated work on a named branch and stops there,
	// touching neither HEAD nor the operator's working copy. (Also reachable as
	// the alias [NamedBranch].)
	ReturnBranch
)

// NamedBranch is an alias for [ReturnBranch]: both name the "leave the task
// branch present and unmerged, return it to the caller" disposition. The two
// spellings exist because callers reason about it either as "give me back the
// named branch" or as "return the branch, don't merge".
const NamedBranch = ReturnBranch

// String renders the strategy for logs/errors.
func (s BranchStrategy) String() string {
	switch s {
	case MergeToHead:
		return "merge-to-head"
	case ReturnBranch:
		return "return-branch"
	default:
		return fmt.Sprintf("BranchStrategy(%d)", int(s))
	}
}

// FinalizeResult reports what [Worktree.Finalize] did with a run's branch.
type FinalizeResult struct {
	// Strategy is the disposition that was applied.
	Strategy BranchStrategy
	// Branch is the task branch. For [ReturnBranch] this is the still-present,
	// unmerged branch the caller now owns; for [MergeToHead] it is the branch
	// that was merged (or that carried no commits).
	Branch string
	// Commits are the SHAs the run produced on the branch (newest first, from
	// [Worktree.CommitsSince]). Empty means the run committed nothing.
	Commits []string
	// Merged reports whether Finalize merged the branch into HEAD. It is true
	// only for [MergeToHead] with a non-empty Commits set; [ReturnBranch] never
	// merges, and a MergeToHead run that produced no commits has nothing to
	// merge.
	Merged bool
}

// Finalize applies strategy to a completed run's branch, reading the run's
// commits from worktreeDir (against base) and, for [MergeToHead], merging into
// repoDir's HEAD when there is work to land. It is the single entry point the
// task runner calls after a sandbox exits, so the branch-disposition policy
// lives in one place rather than being re-implemented per caller.
//
//   - MergeToHead: if the run produced commits, merge the branch into HEAD
//     (--no-ff, via [Worktree.MergeToHead]) and report Merged=true; if it
//     produced none, do nothing and report Merged=false (the empty branch is
//     left for the caller's Cleanup to discard).
//   - ReturnBranch / NamedBranch: never merge; report the branch and its
//     commits so the caller can review or merge it itself. Merged=false.
//
// A merge conflict under MergeToHead surfaces as an error carrying git's
// stderr, exactly as [Worktree.MergeToHead] documents.
func (w Worktree) Finalize(ctx context.Context, repoDir, worktreeDir, branch, base string, strategy BranchStrategy) (FinalizeResult, error) {
	commits, err := w.CommitsSince(ctx, worktreeDir, base)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("sandbox: finalize %s: %w", strategy, err)
	}
	res := FinalizeResult{Strategy: strategy, Branch: branch, Commits: commits}

	switch strategy {
	case MergeToHead:
		if len(commits) == 0 {
			// Nothing to land; leave HEAD untouched. The empty task branch is
			// the caller's to discard via the worktree Cleanup.
			return res, nil
		}
		if err := w.MergeToHead(ctx, repoDir, branch); err != nil {
			return res, err
		}
		res.Merged = true
		return res, nil
	case ReturnBranch:
		// Isolated work stays on its named branch; the caller decides its fate.
		return res, nil
	default:
		return res, fmt.Errorf("sandbox: unknown branch strategy %s", strategy)
	}
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
	cmd := gitenv.Command(ctx, w.git(), full...)
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
