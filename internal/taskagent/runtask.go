package taskagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/sandbox"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// Defaults for an under-specified Request.
const (
	// defaultImage is the sandbox container image when Request.Image is empty. A
	// small busybox-based image is enough for the v1 hands (sh, cat, base64); the
	// caller overrides it for a language-specific toolchain.
	defaultImage = "alpine:3.20"
	// defaultMaxSteps bounds the agent loop when Request.MaxSteps <= 0.
	defaultMaxSteps = 8
)

// DefaultTaskTimeout is the wall-clock deadline applied to a task whose caller
// supplied neither a ctx deadline nor a Request.MaxDuration. It bounds a runaway
// sandboxed task (an injected loop, a hung command) so it cannot idle the host
// process forever. ctx cancellation already tears down `docker exec`; this
// guarantees the cancellation actually fires without an operator watching.
//
// It is the single source of truth for the default bound: every task run —
// whether a standalone RunTask or one task of a RunTasks fan-out — is bounded by
// taskDeadline inside RunTask, so a batch task is bounded EXACTLY like a single
// one. The public aisotropy façade re-exports this value.
const DefaultTaskTimeout = 10 * time.Minute

// taskDeadline derives the effective ctx + cancel for one task run, enforcing a
// wall-clock bound so the task cannot run unbounded:
//
//   - max > 0 (explicit Request.MaxDuration): always WithTimeout(ctx, max).
//     WithTimeout already honors an earlier parent deadline, so this both caps
//     the task AND respects a tighter caller/batch ctx.
//   - max <= 0 and the ctx already has a deadline (a caller timeout, or the
//     batch's parent ctx): respect it as-is.
//   - max <= 0 and no deadline at all: apply DefaultTaskTimeout (secure default).
//
// The returned cancel is always safe (and required) to call.
func taskDeadline(ctx context.Context, max time.Duration) (context.Context, context.CancelFunc) {
	if max > 0 {
		return context.WithTimeout(ctx, max)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultTaskTimeout)
}

// taskSystemPrompt frames the sandboxed coding task for the host-side model. The
// tools themselves are conveyed structurally (native tool-calling) or via the
// agentloop system fragment, not spelled out here.
const taskSystemPrompt = "You are a coding agent working inside an isolated sandbox with a git " +
	"worktree. Use the provided tools to make the requested changes to the files, then commit " +
	"your work with git_commit. Call a tool only when it helps; when the task is done, answer " +
	"directly without calling a tool."

// knownForbiddenEnvKeys are well-known provider/credential environment variable
// names that MUST NEVER be handed to a sandbox (the broker invariant). This exact
// list is kept for readability and audit; it is NOT the whole check — the
// pattern rules in forbiddenEnvKey below are what actually make the guard
// resistant to a NEW provider key nobody has enumerated yet. RunTask asserts a
// Spec's Env trips none of these before Create — defense-in-depth on top of the
// fact that RunTask never populates Spec.Env from host secrets in the first place.
//
// Keys are compared case-insensitively (see forbiddenEnvKey), so entries here are
// written upper-case.
var knownForbiddenEnvKeys = map[string]bool{
	// Model providers.
	"ANTHROPIC_API_KEY":    true,
	"OPENAI_API_KEY":       true,
	"OPENROUTER_API_KEY":   true,
	"GEMINI_API_KEY":       true,
	"GOOGLE_API_KEY":       true,
	"COHERE_API_KEY":       true,
	"MISTRAL_API_KEY":      true,
	"DEEPSEEK_API_KEY":     true,
	"GROQ_API_KEY":         true,
	"XAI_API_KEY":          true,
	"AZURE_OPENAI_API_KEY": true,
	// Cloud credentials (their standard SDK env names).
	"AWS_ACCESS_KEY_ID":              true,
	"AWS_SECRET_ACCESS_KEY":          true,
	"AWS_SESSION_TOKEN":              true,
	"GOOGLE_APPLICATION_CREDENTIALS": true,
	// Aisotropy's own signing secret.
	"AISOTROPY_JWT_SECRET": true,
}

// forbiddenKeySubstrings are case-insensitive substrings whose PRESENCE in an env
// key name marks it as secret-shaped. These catch the naming universe of
// credentials regardless of provider — a var literally named with "SECRET",
// "TOKEN", "PASSWORD", or "CREDENTIAL" is refused so a new provider's key cannot
// silently slip through an enumerated allowlist.
var forbiddenKeySubstrings = []string{
	"SECRET",      // AWS_SECRET_ACCESS_KEY, *_CLIENT_SECRET, *_SECRET
	"TOKEN",       // *_API_TOKEN, HF_TOKEN, AWS_SESSION_TOKEN, *_ACCESS_TOKEN
	"PASSWORD",    // *_PASSWORD
	"PASSWD",      // *_PASSWD
	"CREDENTIAL",  // GOOGLE_APPLICATION_CREDENTIALS, *_CREDENTIALS
	"PRIVATE_KEY", // *_PRIVATE_KEY (RSA/EC signing material)
}

// forbiddenKeySuffixes are case-insensitive suffixes that mark a key as an API or
// access credential. Provider keys are near-universally named "<X>_API_KEY"; cloud
// access keys end "_ACCESS_KEY[_ID]". Suffix-matching (not exact-matching) is what
// keeps a brand-new "SOMENEWPROVIDER_API_KEY" from passing.
var forbiddenKeySuffixes = []string{
	"_API_KEY",
	"_APIKEY",
	"_ACCESS_KEY",
	"_ACCESS_KEY_ID",
}

// forbiddenEnvKey reports whether an env var NAME is secret-shaped and thus must
// never enter a sandbox. It is deliberately fail-closed (a pattern/denylist that
// over-matches an ambiguous name is safer than one that lets a real credential
// through): the exact known-key set, plus substring and suffix pattern rules.
// Only the NAME is inspected — the value is never read or logged.
func forbiddenEnvKey(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if u == "" {
		return false
	}
	if knownForbiddenEnvKeys[u] {
		return true
	}
	for _, sub := range forbiddenKeySubstrings {
		if strings.Contains(u, sub) {
			return true
		}
	}
	for _, suf := range forbiddenKeySuffixes {
		if strings.HasSuffix(u, suf) {
			return true
		}
	}
	return false
}

// AgentStep is one iteration of the loop: the tool the model chose and its result.
// Tool == "" means the model answered directly with no tool (the terminal step).
// It mirrors internal/api.AgentStep so the public façade can map to it.
type AgentStep struct {
	Tool   string
	Args   map[string]string
	Result string
}

// Request is the input to RunTask. It is a pure data struct: it carries WHAT to do
// (RepoDir, Prompt) and the isolation knobs (Image, MaxSteps, Scopes, Provider),
// and deliberately NO provider credential and NO tenant field — tenant identity,
// when present, travels via ctx (see RunTask), never as a data field, per the
// multi-tenancy constitutional gate.
type Request struct {
	// RepoDir is the host path of the git repository to run the task against. A
	// fresh worktree on a new task branch is created off its HEAD; the operator's
	// checkout is never touched.
	RepoDir string
	// Prompt is the task instruction handed to the host-side model.
	Prompt string
	// Image is the sandbox container image; empty defaults to defaultImage.
	Image string
	// MaxSteps bounds the agent loop; <= 0 defaults to defaultMaxSteps.
	MaxSteps int
	// MaxDuration is the wall-clock deadline for this task run. <= 0 applies
	// DefaultTaskTimeout when the ctx has no deadline of its own (a caller/batch
	// ctx deadline is always respected). It is the time-based companion to
	// MaxSteps and is enforced inside RunTask via taskDeadline, so it bounds a
	// standalone RunTask AND each task of a RunTasks fan-out identically — one
	// runaway task in a batch is torn down exactly like a runaway single task.
	MaxDuration time.Duration
	// Scopes are the granted tool scopes (see HandScopes). A hand whose scope is
	// not granted is never offered to the model nor executable.
	Scopes []string
	// Provider is the sandbox backend; nil defaults to a Docker provider.
	Provider sandbox.Provider
	// BranchStrategy decides what happens to the commits the run produced: the
	// zero value sandbox.MergeToHead merges the task branch into HEAD (the prior
	// behavior); sandbox.ReturnBranch leaves them on the task branch, unmerged.
	//
	// SECURITY: the zero value auto-merges an agent's commits into the operator's
	// HEAD. When the PROMPT is untrusted (attacker-influenced), that is a
	// supply-chain path — the model can be steered to commit hostile code that
	// then lands in HEAD with no human gate. For untrusted task sources set
	// ReturnBranch (review-before-merge); reserve MergeToHead for trusted/reviewed
	// prompts. See docs/architecture/task-branch-strategy-trust.md.
	BranchStrategy sandbox.BranchStrategy
	// OnLine, when non-nil, receives each line of a run_shell's stdout live (the
	// SSE fan-out hook). Optional.
	OnLine func(line string)
	// OnReady, when non-nil, is invoked with the live Sandbox right after it is
	// created and before the loop runs. It is an inspection/wiring hook (register
	// cleanup, assert the container env, attach observability); it must not retain
	// the Sandbox past RunTask's return. A returned error aborts the task.
	OnReady func(ctx context.Context, sb sandbox.Sandbox) error

	// SessionID, when non-empty AND paired with a non-nil SessionStore, makes
	// this run part of a resumable, tenant-scoped task session: after the run its
	// step trace + prompt are persisted under (tenant, user, SessionID) — tenant
	// and user read from ctx, never a data field (the multi-tenancy gate) — so a
	// later run with Resume set can rehydrate the agent's context. Empty (or a nil
	// SessionStore) disables persistence entirely. See session.go.
	SessionID string
	// Resume, when true, rehydrates a prior run's step trace + prompt for this
	// SessionID and injects it into the agent loop's prompt (see resumePrompt)
	// instead of starting cold. It requires a non-empty SessionID and a non-nil
	// SessionStore; set without them it is a hard error (a resume that could
	// silently start cold would be a lie). A resume of a session with no prior
	// state is NOT an error — it simply starts cold. The resumed run gets a FRESH
	// sandbox + worktree; only the agent's CONTEXT is restored, never the
	// filesystem (the prior commits are already merged into HEAD).
	Resume bool
	// SessionStore is the persistence seam a SessionID run writes to and a Resume
	// run reads from. nil (the default) disables session persistence/resume. It
	// MUST scope every read/write to the full (tenant, user, session) key so one
	// session can never observe another's state.
	SessionStore SessionStore

	// repoGate, when non-nil, serializes this task's SHARED-REPO git-metadata
	// mutations — the worktree Add and the merge-to-HEAD — against sibling tasks
	// running concurrently against the SAME RepoDir. It is set only by RunTasks
	// (one gate per distinct RepoDir); a standalone RunTask leaves it nil and needs
	// no gate. The gate is held only for the brief git plumbing; the container
	// work, the agent loop, and the in-worktree commits all run concurrently and
	// ungated (git writes loose objects into the shared object store atomically, so
	// only HEAD/branch-ref/index mutations need serializing). This is what keeps N
	// concurrent merges from corrupting HEAD — see RunTasks.
	repoGate sync.Locker

	// onStep, when non-nil, is invoked after each successfully-run hand with the
	// tool name and its result — the live per-step hook RunTasks uses to emit a
	// "step" event. Set only by RunTasks; nil for a standalone RunTask.
	onStep func(tool, result string)
}

// Result is the outcome of a RunTask run.
type Result struct {
	// Commits are the SHAs the run produced on the task branch, newest first
	// (empty when the model committed nothing).
	Commits []string
	// Branch is the task branch the run's commits live on. When Commits is
	// non-empty the branch was merged into RepoDir's HEAD (--no-ff); when empty the
	// branch and its worktree were discarded.
	Branch string
	// Steps is the ordered trace of tool decisions and results.
	Steps []AgentStep
	// Output is a best-effort final output: the last non-empty tool result. The
	// structured Steps carry the full trace.
	Output string
}

// RunTask runs one sandboxed agentic task end-to-end:
//
//	Worktree.Add(RepoDir)                → an isolated checkout on a new task branch
//	Provider.Create(Spec{Image, mount})  → a live sandbox with NO provider key
//	host-side agent loop over sandboxExecutor, bounded by MaxSteps
//	CommitsSince → MergeToHead (if any)  → accept the work, else discard
//	Close + worktree cleanup
//
// The LLM inference happens HOST-SIDE via client (the broker invariant): the
// container only executes the tools the model chose. client is resolved by the
// caller (the aisotropy façade passes the App's client or llm.FromEnv()); a
// keyless stub client still drives the loop to a graceful completion.
//
// tenant identity, when the caller injected it into ctx, is inherited by every
// downstream call through ctx — RunTask never reads a tenant from the Request.
//
// tracer instruments the native-tool path; nil falls back to the global OTel
// provider (a no-op unless one is configured).
func RunTask(ctx context.Context, client llm.Client, tracer oteltrace.TracerProvider, req Request) (Result, error) {
	if client == nil {
		return Result{}, errors.New("taskagent: nil llm client")
	}
	if req.RepoDir == "" {
		return Result{}, errors.New("taskagent: Request.RepoDir is required")
	}

	// Enforce the per-task wall-clock deadline HERE, at the single task-runner
	// entry point, so every task is bounded identically: a standalone RunTask and
	// each task of a RunTasks fan-out (which calls RunTask per task) both get the
	// same treatment relative to their ctx. This bounds worktree/sandbox creation,
	// the agent loop, and the merge alike; ctx cancellation tears down the live
	// `docker exec`, and this guarantees the cancellation actually fires.
	ctx, cancel := taskDeadline(ctx, req.MaxDuration)
	defer cancel()

	image := req.Image
	if image == "" {
		image = defaultImage
	}
	maxSteps := req.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	provider := req.Provider
	if provider == nil {
		provider = sandbox.NewDockerProvider()
	}

	// Session resume (see session.go): a run is "session-active" only when both a
	// store and a SessionID are supplied. Resume without them is a hard error — a
	// resume that silently started cold would misrepresent what happened. When
	// resuming, the prior step trace is loaded and folded into the prompt the loop
	// sees (resumePrompt); the prior steps are also carried forward so the saved
	// trace accumulates across resumes. The run itself still gets a fresh
	// sandbox+worktree below — only the agent's CONTEXT is rehydrated, never the
	// filesystem (the prior commits are already at HEAD).
	sessionActive := req.SessionStore != nil && req.SessionID != ""
	if req.Resume && !sessionActive {
		return Result{}, errors.New("taskagent: Resume requires a non-empty SessionID and a non-nil SessionStore")
	}
	var (
		sessKey    SessionKey
		priorSteps []AgentStep
	)
	prompt := req.Prompt
	if sessionActive {
		var err error
		sessKey, err = resolveSessionKey(ctx, req.SessionID)
		if err != nil {
			return Result{}, fmt.Errorf("taskagent: resolve session key: %w", err)
		}
		if req.Resume {
			prior, found, lerr := req.SessionStore.Load(ctx, sessKey)
			if lerr != nil {
				return Result{}, fmt.Errorf("taskagent: load session %q: %w", req.SessionID, lerr)
			}
			if found {
				priorSteps = prior.Steps
				prompt = resumePrompt(prior, req.Prompt)
			}
		}
	}

	// Isolated worktree on a fresh task branch. Creating a worktree mutates the
	// shared repo (a new branch ref, the worktree admin dir, a HEAD reflog entry),
	// so under a fan-out it is serialized per-repo via repoGate — concurrent
	// `git worktree add` on one repo can otherwise race on the repo lock.
	wt := sandbox.Worktree{}
	add, err := gatedWorktreeAdd(ctx, wt, req.repoGate, req.RepoDir)
	if err != nil {
		return Result{}, fmt.Errorf("taskagent: worktree add: %w", err)
	}
	// The worktree is discarded on every non-merge exit path (error, or a run that
	// committed nothing); on a merged run it is cleaned after the merge. cleanupDone
	// guards against a double cleanup.
	cleanupDone := false
	cleanup := func() {
		if cleanupDone {
			return
		}
		cleanupDone = true
		_ = add.Cleanup()
	}
	defer cleanup()

	// Build the sandbox spec. BROKER ENFORCEMENT: Env is left nil — no host secret
	// is ever read into it — and assertNoProviderSecret double-checks that whatever
	// is about to be handed to the provider carries no known provider credential.
	// This is the enforcement point internal/sandbox's package doc names.
	spec := sandbox.Spec{
		Image:      image,
		BindMounts: []sandbox.Mount{{Source: add.Path, Target: sandbox.DefaultWorkdir}},
		User:       sandbox.CurrentUserSpec(os.Getuid(), os.Getgid()),
	}
	if err := assertNoProviderSecret(spec.Env); err != nil {
		return Result{}, err
	}

	sb, err := provider.Create(ctx, spec)
	if err != nil {
		return Result{}, fmt.Errorf("taskagent: sandbox create: %w", err)
	}
	defer func() { _ = sb.Close(context.WithoutCancel(ctx)) }()

	if req.OnReady != nil {
		if err := req.OnReady(ctx, sb); err != nil {
			return Result{}, fmt.Errorf("taskagent: OnReady: %w", err)
		}
	}

	exec := &sandboxExecutor{sb: sb, worktreeDir: add.Path, onLine: req.OnLine, onStep: req.onStep}
	registry := handsRegistry()

	steps, err := runLoop(ctx, client, tracer, registry, exec, req.Scopes, prompt, maxSteps)
	if err != nil {
		return Result{}, err
	}

	// Snapshot the commits the run produced. If any, accept the work by merging the
	// task branch into HEAD; otherwise the deferred cleanup discards the branch.
	commits, err := wt.CommitsSince(ctx, add.Path, add.Base)
	if err != nil {
		return Result{}, fmt.Errorf("taskagent: commits since: %w", err)
	}

	res := Result{Steps: steps, Output: lastResult(steps)}
	if len(commits) > 0 {
		res.Commits = commits
		res.Branch = add.Branch
		// Close + remove the worktree BEFORE merging so `git merge` in the primary
		// checkout does not race the linked worktree.
		_ = sb.Close(context.WithoutCancel(ctx))
		cleanup()
		// MergeToHead (the default/zero value) lands the work into HEAD;
		// ReturnBranch leaves the commits on the task branch for the caller to
		// merge/review/PR — removing the worktree does not remove the branch.
		if req.BranchStrategy == sandbox.MergeToHead {
			if err := gatedMergeToHead(ctx, wt, req.repoGate, req.RepoDir, add.Branch); err != nil {
				return res, fmt.Errorf("taskagent: merge to head: %w", err)
			}
		}
	}

	// Persist the session's step trace for a future Resume. The run's real work
	// (the merged commits) has already landed, so a persistence failure must NOT
	// fail the task and discard a successful result — it is logged best-effort,
	// exactly like the chat path treats a working-memory write failure. The saved
	// trace accumulates: prior steps (from a resume) are carried forward ahead of
	// this run's, so the next resume sees the whole history.
	if sessionActive {
		merged := append(append([]AgentStep(nil), priorSteps...), steps...)
		if err := req.SessionStore.Save(ctx, sessKey, SessionState{Prompt: req.Prompt, Steps: merged}); err != nil {
			slog.WarnContext(ctx, "taskagent: failed to persist task session state", "session", req.SessionID, "err", err)
		}
	}
	return res, nil
}

// gatedWorktreeAdd runs Worktree.Add under gate (when non-nil) so concurrent
// fan-out tasks on one repo do not race the repo lock. The gate is released the
// moment Add returns — the isolated worktree is then operated on ungated.
func gatedWorktreeAdd(ctx context.Context, wt sandbox.Worktree, gate sync.Locker, repoDir string) (sandbox.AddResult, error) {
	if gate != nil {
		gate.Lock()
		defer gate.Unlock()
	}
	return wt.Add(ctx, repoDir, "")
}

// gatedMergeToHead runs MergeToHead under gate (when non-nil) so at most one merge
// touches the shared repo HEAD at a time — the fix for the concurrent-merge race.
// If a gated merge FAILS (most often a genuine content conflict between two task
// branches, which git leaves half-applied), it best-effort `git merge --abort`s so
// the shared HEAD is restored to a clean state and the NEXT gated task's merge is
// not poisoned by a lingering conflicted index. The original merge error still
// propagates as this task's failure. A standalone (ungated) RunTask keeps the
// prior behavior: the error surfaces and the caller resolves out of band.
func gatedMergeToHead(ctx context.Context, wt sandbox.Worktree, gate sync.Locker, repoDir, branch string) error {
	if gate == nil {
		return wt.MergeToHead(ctx, repoDir, branch)
	}
	gate.Lock()
	defer gate.Unlock()
	err := wt.MergeToHead(ctx, repoDir, branch)
	if err != nil {
		// Restore HEAD; ignore the abort's own error (there may be nothing to
		// abort if the merge failed before starting).
		_ = osexec.CommandContext(context.WithoutCancel(ctx), "git", "-C", repoDir, "merge", "--abort").Run()
	}
	return err
}

// runLoop drives the host-side agent loop, choosing the native tool-calling path
// when client implements llm.ToolCaller and the structured-output agentloop
// otherwise — the same dispatch as the HTTP agent route (internal/api). Both
// enforce the identical double scope-gate: only Permitted(scopes) tools are
// offered, and an unoffered/hallucinated name can never reach the executor.
func runLoop(ctx context.Context, client llm.Client, tracer oteltrace.TracerProvider, registry *tools.Registry, exec *sandboxExecutor, scopes []string, prompt string, maxSteps int) ([]AgentStep, error) {
	if tc, ok := client.(llm.ToolCaller); ok {
		permitted := registry.Permitted(scopes)
		specs := make([]llm.ToolSpec, 0, len(permitted))
		for _, t := range permitted {
			specs = append(specs, llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
		}
		run := func(ctx context.Context, name, argsJSON string) (string, error) {
			return exec.Execute(ctx, name, decodeStringArgs(argsJSON))
		}
		toolSteps, err := llm.RunToolAgent(ctx, tc, tracer, taskSystemPrompt, prompt, specs, run, maxSteps)
		if err != nil {
			return nil, err
		}
		steps := make([]AgentStep, 0, len(toolSteps))
		for _, ts := range toolSteps {
			steps = append(steps, AgentStep{Tool: ts.Tool, Args: ts.Args, Result: ts.Result})
		}
		return steps, nil
	}

	loop := &agentloop.Loop{Client: client, Registry: registry, Executor: exec, MaxSteps: maxSteps}
	loopSteps, err := loop.Run(ctx, scopes, prompt)
	if err != nil && !errors.Is(err, agentloop.ErrMalformedDecision) {
		// A malformed/empty decision from a degraded (e.g. keyless stub) client is a
		// graceful no-op completion, not a task failure: the model produced no
		// actionable tool call. Any OTHER loop error (provider down, ctx cancelled,
		// executor infrastructure failure) is a real failure and propagates.
		return nil, err
	}
	steps := make([]AgentStep, 0, len(loopSteps))
	for _, s := range loopSteps {
		steps = append(steps, AgentStep{Tool: s.Decision.Tool, Args: s.Decision.Args, Result: s.Result})
	}
	return steps, nil
}

// lastResult returns the last non-empty tool result across steps, or "".
func lastResult(steps []AgentStep) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Result != "" {
			return steps[i].Result
		}
	}
	return ""
}

// assertNoProviderSecret fails if env carries any secret-shaped key — the broker
// invariant's last line of defense before a Spec reaches the provider. It iterates
// the ACTUAL env (not a fixed denylist) and applies forbiddenEnvKey's pattern
// rules, so an unenumerated provider key (GEMINI/AWS/AZURE/… or any *_API_KEY /
// *_SECRET / *_TOKEN) is caught rather than silently passing. Only the offending
// key NAME is surfaced in the error; the value is never read or logged.
func assertNoProviderSecret(env map[string]string) error {
	for k := range env {
		if forbiddenEnvKey(k) {
			return fmt.Errorf("taskagent: refusing to run: sandbox Spec.Env carries a secret-shaped key %q (broker invariant: no provider credential may enter the sandbox)", k)
		}
	}
	return nil
}

// schemaFor derives the JSON Schema bytes for a hand's argument struct T. A
// non-schema-representable T (or reflect error) yields empty Parameters, which the
// tool advertises as a permissive object — never a panic in the registry builder.
func schemaFor[T any]() json.RawMessage {
	schema, err := llm.SchemaFor[T]()
	if err != nil {
		return nil
	}
	return schema
}
