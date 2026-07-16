package taskagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

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

// taskSystemPrompt frames the sandboxed coding task for the host-side model. The
// tools themselves are conveyed structurally (native tool-calling) or via the
// agentloop system fragment, not spelled out here.
const taskSystemPrompt = "You are a coding agent working inside an isolated sandbox with a git " +
	"worktree. Use the provided tools to make the requested changes to the files, then commit " +
	"your work with git_commit. Call a tool only when it helps; when the task is done, answer " +
	"directly without calling a tool."

// forbiddenEnvKeys are provider-secret environment variable names that MUST NEVER
// be handed to a sandbox (the broker invariant). RunTask asserts a Spec's Env
// contains none of these before Create — a defense-in-depth check on top of the
// fact that RunTask never populates Spec.Env from host secrets in the first place.
var forbiddenEnvKeys = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"OPENROUTER_API_KEY",
	"AISOTROPY_JWT_SECRET",
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
	// Scopes are the granted tool scopes (see HandScopes). A hand whose scope is
	// not granted is never offered to the model nor executable.
	Scopes []string
	// Provider is the sandbox backend; nil defaults to a Docker provider.
	Provider sandbox.Provider
	// OnLine, when non-nil, receives each line of a run_shell's stdout live (the
	// SSE fan-out hook). Optional.
	OnLine func(line string)
	// OnReady, when non-nil, is invoked with the live Sandbox right after it is
	// created and before the loop runs. It is an inspection/wiring hook (register
	// cleanup, assert the container env, attach observability); it must not retain
	// the Sandbox past RunTask's return. A returned error aborts the task.
	OnReady func(ctx context.Context, sb sandbox.Sandbox) error
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

	// Isolated worktree on a fresh task branch.
	wt := sandbox.Worktree{}
	add, err := wt.Add(ctx, req.RepoDir, "")
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

	exec := &sandboxExecutor{sb: sb, worktreeDir: add.Path, onLine: req.OnLine}
	registry := handsRegistry()

	steps, err := runLoop(ctx, client, tracer, registry, exec, req.Scopes, req.Prompt, maxSteps)
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
		if err := wt.MergeToHead(ctx, req.RepoDir, add.Branch); err != nil {
			return res, fmt.Errorf("taskagent: merge to head: %w", err)
		}
	}
	return res, nil
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

// assertNoProviderSecret fails if env carries any known provider-secret key — the
// broker invariant's last line of defense before a Spec reaches the provider.
func assertNoProviderSecret(env map[string]string) error {
	for _, k := range forbiddenEnvKeys {
		if _, ok := env[k]; ok {
			return fmt.Errorf("taskagent: refusing to run: sandbox Spec.Env carries provider secret %q (broker invariant: the provider key must never enter the sandbox)", k)
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
