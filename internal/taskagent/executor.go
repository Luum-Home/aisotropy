// Package taskagent is the kernel of Aisotropy's sandboxed task runner: it
// couples the HOST-SIDE agent loop (internal/agentloop + internal/llm — the
// "brain" that spends the provider key) to an isolated sandbox (internal/sandbox
// — the "hands" that run untrusted, model-chosen tool calls). RunTask is the one
// public entry point; sandboxExecutor is the ToolExecutor that turns each tool
// the model selects into a concrete action inside the sandbox.
//
// # The broker invariant (why this package is the differentiator)
//
// aisotropy is a KEY-BROKER, not a key-forwarder. The provider API key
// (ANTHROPIC_API_KEY, OPENAI_API_KEY, …) is read only by internal/llm, HOST-SIDE.
// The LLM call — the privileged operation that spends the key — happens in the
// host process. What crosses into the sandbox is only the EFFECT of a tool the
// model chose: "run this shell", "write that file". The container holds the
// agent's hands, never its wallet.
//
// This is the deliberate inversion versus sandcastle-style designs, where the
// agent (and its credentials) run INSIDE the box and the box reaches out to the
// model. Here the model stays outside; only the untrusted, side-effecting work is
// boxed. RunTask is the enforcement point the sandbox package's doc names: it
// builds every sandbox.Spec with a NON-SECRET environment and never places a
// provider credential in Spec.Env (see runtask.go's assertNoProviderSecret and
// TestRunTask_BrokerInvariant_KeyNeverEntersContainer).
package taskagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// The hands-toolset: the minimal, documented v1 set of tools the host-side model
// may drive inside the sandbox. Each is scope-gated exactly like any other
// aisotropy tool (internal/tools): a tool whose RequiredScope the caller did not
// grant is never offered to the model and can never execute.
const (
	// ToolRunShell runs a shell command inside the sandbox container.
	ToolRunShell = "run_shell"
	// ToolWriteFile writes a file inside the sandbox container.
	ToolWriteFile = "write_file"
	// ToolReadFile reads a file from inside the sandbox container.
	ToolReadFile = "read_file"
	// ToolGitCommit stages and commits the worktree (see the git note below).
	ToolGitCommit = "git_commit"
)

// Scope constants gating each hand. They are distinct so a caller can grant, say,
// read-only file access without shell or commit rights. HandScopes returns the
// full set for a caller that wants all hands.
const (
	ScopeRunShell  = "sandbox:run_shell"
	ScopeWriteFile = "sandbox:write_file"
	ScopeReadFile  = "sandbox:read_file"
	ScopeGitCommit = "sandbox:git_commit"
)

// HandScopes returns every hands-tool scope, for a caller that wants the model to
// have the full v1 toolset. A caller can instead pass a subset to RunTask to
// restrict which hands the model may use.
func HandScopes() []string {
	return []string{ScopeRunShell, ScopeWriteFile, ScopeReadFile, ScopeGitCommit}
}

// Argument structs. Each is the "struct as the single contract" (ADR-0002) for
// one hand: the same struct drives the JSON Schema the model is shown (via
// llm.SchemaFor in handsRegistry) and the decoding of the model's arguments here.
type (
	runShellArgs struct {
		Cmd string `json:"cmd" jsonschema:"description=The shell command to run inside the sandbox (executed with sh -c)."`
	}
	writeFileArgs struct {
		Path    string `json:"path" jsonschema:"description=File path to write, relative to the workspace root."`
		Content string `json:"content" jsonschema:"description=The full file contents to write."`
	}
	readFileArgs struct {
		Path string `json:"path" jsonschema:"description=File path to read, relative to the workspace root."`
	}
	gitCommitArgs struct {
		Message string `json:"message" jsonschema:"description=The commit message."`
	}
)

// sandboxExecutor implements agentloop.ToolExecutor: Execute runs the model's
// selected tool INSIDE a live sandbox.Sandbox (the hands). The host-side loop
// (internal/llm / internal/agentloop) picks the tool; this executor performs the
// side effect and returns its textual result to feed back into the loop.
//
// A command that RAN but exited non-zero (a shell failure, a missing file, an
// empty commit) is NOT a Go error: its failure is folded into the returned result
// STRING so the model can see it and self-correct within the step budget, exactly
// like the HTTP agent route's friction handling. Execute returns a non-nil error
// only for an infrastructure failure — the sandbox backend is unreachable, ctx
// was cancelled, or the model named an unknown tool — which stops the loop.
type sandboxExecutor struct {
	// sb is the live container the file/shell hands operate in.
	sb sandbox.Sandbox
	// worktreeDir is the HOST path of the bind-mounted git worktree. git_commit
	// operates here host-side (see its method doc for why it is not a container
	// exec).
	worktreeDir string
	// gitBin is the git CLI for git_commit; empty means "git" on PATH.
	gitBin string
	// onLine, when non-nil, receives each line of a run_shell's stdout as it is
	// produced (the SSE fan-out hook); it is threaded straight into
	// sandbox.ExecOptions.OnLine.
	onLine func(line string)
}

// Execute dispatches one model-selected hand. Unknown tool names return an error
// (the double scope-gate upstream means this can only happen on a wiring bug, not
// a scope bypass).
func (e *sandboxExecutor) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	switch name {
	case ToolRunShell:
		return e.runShell(ctx, args["cmd"])
	case ToolWriteFile:
		return e.writeFile(ctx, args["path"], args["content"])
	case ToolReadFile:
		return e.readFile(ctx, args["path"])
	case ToolGitCommit:
		return e.gitCommit(ctx, args["message"])
	default:
		return "", fmt.Errorf("taskagent: unknown tool %q", name)
	}
}

// runShell executes cmd inside the container via `sh -c`, streaming stdout to
// e.onLine as it arrives. A non-zero exit is reported in the RESULT text, not as
// an error, so the model self-corrects.
func (e *sandboxExecutor) runShell(ctx context.Context, cmd string) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		return "run_shell: empty cmd", nil
	}
	res, err := e.sb.Exec(ctx, []string{"sh", "-c", cmd}, sandbox.ExecOptions{OnLine: e.onLine})
	if err != nil {
		return "", fmt.Errorf("taskagent: run_shell: %w", err)
	}
	return formatExecResult("run_shell", res), nil
}

// writeFile writes content to path inside the container. Content is transported
// as base64 and decoded in-container (`base64 -d`), so arbitrary bytes — quotes,
// newlines, shell metacharacters — round-trip without any shell-injection surface
// from the content itself. path is single-quoted defensively.
func (e *sandboxExecutor) writeFile(ctx context.Context, path, content string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "write_file: empty path", nil
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	qp := shellSingleQuote(path)
	// Create parent dirs, then decode the base64 payload into the file. printf %s
	// avoids echo's escape-processing; the payload is base64 (safe alphabet) and
	// still single-quoted.
	script := fmt.Sprintf("set -e; mkdir -p \"$(dirname %s)\"; printf %%s %s | base64 -d > %s",
		qp, shellSingleQuote(b64), qp)
	res, err := e.sb.Exec(ctx, []string{"sh", "-c", script}, sandbox.ExecOptions{})
	if err != nil {
		return "", fmt.Errorf("taskagent: write_file: %w", err)
	}
	if res.ExitCode != 0 {
		return formatExecResult("write_file", res), nil
	}
	return fmt.Sprintf("write_file: wrote %d bytes to %s", len(content), path), nil
}

// readFile reads path from inside the container via `cat`. A missing file exits
// non-zero and its stderr is folded into the result text.
func (e *sandboxExecutor) readFile(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "read_file: empty path", nil
	}
	res, err := e.sb.Exec(ctx, []string{"cat", path}, sandbox.ExecOptions{})
	if err != nil {
		return "", fmt.Errorf("taskagent: read_file: %w", err)
	}
	if res.ExitCode != 0 {
		return formatExecResult("read_file", res), nil
	}
	return res.Stdout, nil
}

// gitCommit stages and commits the worktree. It runs HOST-SIDE against
// e.worktreeDir rather than as a container exec, on purpose:
//
//   - The Phase-1 git worktree (internal/sandbox.Worktree) stores its linked
//     gitdir under <repo>/.git/worktrees/<name>, OUTSIDE the single bind mount the
//     sandbox gets, so an in-container `git commit` cannot reach the object store.
//   - The sandbox image (e.g. alpine) is not assumed to even contain a git binary.
//   - Committing is a worktree-lifecycle operation the host already owns:
//     CommitsSince and MergeToHead (runtask.go) are host-side too. The container
//     mutates FILES (the real hands); the host snapshots those edits into the
//     isolated task branch.
//
// This does not weaken the broker invariant: the invariant is that the provider
// KEY never enters the container and inference stays host-side — both still hold.
// git_commit touches only the host-visible worktree and carries no secret.
//
// A committer identity is supplied inline (-c user.*) so a commit never fails on a
// repo/host that lacks a configured git identity. A no-op commit (nothing staged)
// is reported in the result text, not as an error.
func (e *sandboxExecutor) gitCommit(ctx context.Context, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		message = "aisotropy sandbox task commit"
	}
	if out, err := e.git(ctx, "add", "-A"); err != nil {
		return fmt.Sprintf("git_commit: git add failed: %v\n%s", err, out), nil
	}
	out, err := e.git(ctx,
		"-c", "user.name=aisotropy",
		"-c", "user.email=aisotropy@local",
		"commit", "-m", message)
	if err != nil {
		// A non-zero commit is most often "nothing to commit" — surface it as
		// result text (the model may then decide to make an edit first), not a
		// hard loop error.
		return fmt.Sprintf("git_commit: nothing committed or commit failed:\n%s", strings.TrimSpace(out)), nil
	}
	sha, _ := e.git(ctx, "rev-parse", "HEAD")
	return fmt.Sprintf("git_commit: committed %q at %s", message, strings.TrimSpace(sha)), nil
}

// git runs `git -C <worktreeDir> <args…>` on the host, returning combined output.
func (e *sandboxExecutor) git(ctx context.Context, args ...string) (string, error) {
	bin := e.gitBin
	if bin == "" {
		bin = "git"
	}
	full := append([]string{"-C", e.worktreeDir}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// handsRegistry builds the scope-gated registry of the v1 hands-toolset. Each
// tool carries the JSON Schema derived from its argument struct (SchemaFor) so the
// native tool-calling path advertises the exact argument shape to the model.
func handsRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.Tool{Name: ToolRunShell, Description: "Run a shell command inside the sandbox workspace.", RequiredScope: ScopeRunShell, Parameters: schemaFor[runShellArgs]()},
		tools.Tool{Name: ToolWriteFile, Description: "Write a file inside the sandbox workspace.", RequiredScope: ScopeWriteFile, Parameters: schemaFor[writeFileArgs]()},
		tools.Tool{Name: ToolReadFile, Description: "Read a file from the sandbox workspace.", RequiredScope: ScopeReadFile, Parameters: schemaFor[readFileArgs]()},
		tools.Tool{Name: ToolGitCommit, Description: "Stage all changes and create a git commit on the task branch.", RequiredScope: ScopeGitCommit, Parameters: schemaFor[gitCommitArgs]()},
	)
}

// formatExecResult renders a completed Exec into the result string fed back to the
// model: stdout, plus a non-zero exit note and stderr when the command failed.
func formatExecResult(tool string, res sandbox.ExecResult) string {
	if res.ExitCode == 0 {
		return res.Stdout
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: exited %d", tool, res.ExitCode)
	if s := strings.TrimSpace(res.Stdout); s != "" {
		sb.WriteString("\nstdout:\n")
		sb.WriteString(s)
	}
	if s := strings.TrimSpace(res.Stderr); s != "" {
		sb.WriteString("\nstderr:\n")
		sb.WriteString(s)
	}
	return sb.String()
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes the
// POSIX way ('\” — close, escaped quote, reopen), so s is a single opaque
// argument to sh with no metacharacter interpretation.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// decodeStringArgs flattens the model's raw JSON tool arguments into the
// string-keyed map the ToolExecutor seam consumes (non-string scalars are
// stringified). Empty/`{}`/`null` decode to an empty map. It is the taskagent-local
// twin of internal/llm's unexported decoder, used to bridge the native
// tool-calling exec signature (raw JSON) to Execute.
func decodeStringArgs(argsJSON string) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return out
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		switch s := v.(type) {
		case string:
			out[k] = s
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}
