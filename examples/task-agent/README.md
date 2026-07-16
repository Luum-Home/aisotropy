# task-agent — the sandboxed task agent (key-brokering) demo

A minimal, runnable demonstration of Aisotropy's **sandboxed task agent**
([ADR-0036](../../docs/decisions/0036-sandboxed-task-agent-key-brokering.md)):
run an agent that does real work with real hands — shell, files, git — inside an
isolated Docker container, **while the provider API key never enters that
container.**

This is a separate Go module (it `replace`s the repo root) that builds on
Aisotropy's **public surface only** — `aisotropy.New()`,
`app.RunTask(...)`, and `aisotropy.SandboxToolScopes()`. It cannot import
`internal/…`, exactly like a third-party consumer.

## The property this shows: the container never gets your API key

Aisotropy is a **key-BROKER, not a key-forwarder.**

```
        HOST process (this program)                 Docker container
   ┌───────────────────────────────────┐       ┌───────────────────────┐
   │  the "brain":                      │       │  the "hands":         │
   │  internal/llm makes the LLM call   │       │  run_shell            │
   │  → spends ANTHROPIC_API_KEY here   │  ──►  │  write_file           │
   │  agent loop decides ONE tool call  │       │  read_file            │
   │                                    │  ◄──  │  (tool result back)   │
   │  KEY LIVES HERE, NEVER LEAVES      │       │  NO API KEY, EVER     │
   └───────────────────────────────────┘       └───────────────────────┘
```

The LLM call — the one privileged operation that spends the key — happens
host-side, in this process. Only the **execution** of a tool the model already
chose ("write this file", "run that shell") is dispatched into the container via
`docker exec`. The container holds the agent's hands, never its wallet.

This is the exact **inverse** of a coding-CLI-in-a-box (sandcastle, Claude Code,
Codex, Aider, Cursor, Gemini CLI, OpenCode): those run the LLM client *inside*
the sandbox, so they **must** hand the raw provider key to the container. If the
agent — or a prompt-injected tool result, or a malicious dependency it installs —
exfiltrates that env var, the key is gone. Here the key was never in the room.

## What the program does

1. Creates a **throwaway git repo** in a temp dir (the demo never touches a real
   checkout).
2. Builds the runtime with `aisotropy.New()` and **no credentials**, so it uses
   the keyless **stub** model.
3. Calls `app.RunTask(...)`, which:
   - opens an isolated git worktree on a fresh task branch off the repo's HEAD,
   - stands up a Docker sandbox bind-mounting that worktree,
   - drives the **host-side** agent loop over the sandboxed hands
     (`run_shell` / `write_file` / `read_file` / `git_commit`),
   - merges the task branch back to HEAD **if** the run produced commits, else
     discards it.
4. Prints the steps, commits, and branch.

## Run it

```bash
cd examples/task-agent
go run .
```

Requirements:

- **Docker** — the sandbox provider shells out to the `docker` CLI (no Docker Go
  SDK). Without a running Docker daemon, `RunTask` fails at sandbox creation and
  the program prints a clear message.
- **git** — used to build the throwaway repo here and by RunTask's worktree
  plumbing.

## Honest note: the stub vs. a real model

With the keyless stub, the model produces a canned response and **no actionable
tool call**. So a stub run completes gracefully with **zero commits** — it
exercises the full plumbing (worktree → container → host-side loop →
merge-or-discard) and proves the container boots **without a key**, but the agent
does not actually edit files, because there is no real model deciding to. The
program says so explicitly in its output.

Point Aisotropy at a real provider and the same `RunTask` call drives a real
model that will use the hands to make edits and commit them — still without the
key ever entering the container:

```bash
export OPENROUTER_API_KEY=sk-or-...   # or OPENAI_API_KEY / ANTHROPIC_API_KEY
go run .
```

Provider selection follows the same precedence as the rest of Aisotropy:
`OPENROUTER_API_KEY` → `OPENAI_API_KEY` → `ANTHROPIC_API_KEY` → keyless stub.

## What ships in v1 (and what does not)

**v1 (this demo exercises it):** one Docker provider (shelling out to `docker`),
one bind-mounted git worktree per task, the four-tool hands set, and a
merge-to-HEAD branch strategy. The key-broker invariant is enforced by
construction (the sandbox spec's environment never carries a provider key) and
checked by a dedicated test in the runtime
(`TestRunTask_BrokerInvariant_KeyNeverEntersContainer`).

**Named follow-ons (NOT in v1):** additional providers (Vercel Sandbox, Daytona,
Fly Machines, Firecracker/gVisor), an isolated copy-in strategy, multi-repo /
multi-worktree orchestration, and richer hands. Running the six coding CLIs
inside the box is explicitly **not** Aisotropy's model — that would reintroduce
the key-in-sandbox problem this primitive exists to invert. See
[ADR-0036](../../docs/decisions/0036-sandboxed-task-agent-key-brokering.md) for
the full scope and rationale.
