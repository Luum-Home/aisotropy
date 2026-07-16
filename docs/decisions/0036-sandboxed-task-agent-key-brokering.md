---
type: ADR
title: "ADR-0036 — The sandboxed task agent: a native RunTask primitive whose defining property is KEY-BROKERING (the inverse of sandcastle)"
description: >
  ADR-0035 shipped the WASM boundary for pure JSON→JSON plugin tools; this ADR proposes the
  complementary boundary for AGENTIC work that needs real "hands" — shell, git, file ops — against a
  checkout, run inside a container the agent never fully trusts. The defining property is
  KEY-BROKERING and it is the exact inverse of sandcastle: sandcastle (and every coding-CLI-in-a-box)
  MUST place the raw provider API key (ANTHROPIC_API_KEY etc.) inside the sandbox because the coding
  CLI calls the LLM from inside the container; aisotropy already runs inference HOST-SIDE
  (internal/llm), so RunTask dispatches only tool EXECUTION into the sandbox while the model call — and
  the key — stay on the host. The container never sees the key. Design (v1/minimal): a new
  internal/sandbox package with a Provider interface + one Docker provider that shells out to the
  docker CLI via os/exec (no Docker SDK — the sandcastle pattern, keeps the 100%-permissive/no-new-dep
  posture) + a git worktree per task; RunTask(ctx, TaskRequest) -> TaskResult{Commits, Branch, Steps};
  a sandboxExecutor implementing agentloop.ToolExecutor whose Execute runs each of the model's tool
  calls via `docker exec` on the container while the agentloop's LLM turns run host-side; a small
  "hands" toolset (shell/read-file/write-file/git) scope-gated like every other tool; and a
  merge-to-head branch strategy (each task on its own branch off HEAD, results returned as commits for
  the host to merge). Honest scope: ONE Docker provider, bind-mount, a minimal hands-toolset — NOT the
  6 coding CLIs, NOT Vercel/Daytona/Fly providers, NOT multi-repo; those are named follow-ons. The big
  lift is real: the container subsystem is net-new (no internal/sandbox exists today). STATUS: draft —
  verify against shipped code; the Phase-3 implementation agent finalizes this ADR against the code it
  actually lands.
tags: [decisions, adr, sandbox, task-agent, key-brokering, docker, agentloop, tool-execution, isolation, draft, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0036 — The sandboxed task agent: a native `RunTask` primitive whose defining property is KEY-BROKERING (the inverse of sandcastle)

## Status

🟡 **Draft — proposed 2026-07-16.** This ADR records the *decision to build* a native sandboxed
task-agent primitive and the shape it takes; the code is **net-new** (no `internal/sandbox` package
exists in the tree yet — verified at draft time). It is the agentic sibling of
[ADR-0035](0035-wasm-plugin-sandbox.md): where 0035 isolates a **pure `JSON -> JSON` plugin tool**
in-process (wazero), this ADR isolates an **agent doing real work with real hands** (shell / git /
file ops against a checkout) in a **container**. The two sit on the same execution-isolation axis and
are deliberately different tiers of it.

> **STATUS: draft — verify against shipped code.** Every symbol named below (`internal/sandbox`,
> `Provider`, `RunTask`, `TaskRequest`, `TaskResult`, `sandboxExecutor`) is a **design target**, not
> a shipped API. The Phase-3 implementation agent MUST reconcile this document with the code it
> actually lands — rename to match reality, delete claims the code does not honor, and flip the
> status to Accepted only once the named files exist with tests. Treat any divergence between this
> draft and the tree as a defect in this draft.

This ADR **extends** [ADR-0033](0033-open-core-extension-seams.md)'s trust model (the honest
statement that Go cannot sandbox in-process code, so untrusted *agentic* work needs a real OS
boundary — here, a container), is the **agentic tier** alongside [ADR-0035](0035-wasm-plugin-sandbox.md)
(WASM = the pure-function tier; container = the hands tier), reuses
[ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)'s `agentloop.ToolExecutor` seam
verbatim (the sandbox is *just another* executor), and holds
[ADR-0005](0005-datafusion-go-rust-boundary.md)'s subprocess-boundary discipline (shell out to a CLI
via `os/exec`, treat a non-zero exit as a Go-observable error with no in-process crash). It follows
the constitutional license gate: **no new dependency** — the Docker provider drives the `docker` CLI
through `os/exec`, exactly as sandcastle does, rather than pulling the Docker Go SDK.

## Context

There is a class of work an LLM-OS should be able to dispatch but which the current runtime cannot do
safely: *"take this repository, in a scratch checkout, and let an agent run shell commands, edit
files, and make commits to accomplish a task."* That is the coding-agent-in-a-box shape popularized by
sandcastle and the six coding CLIs (Claude Code, Codex, Cursor, Aider, Gemini CLI, OpenCode). Every
one of them shares a structural property that is easy to miss and is the crux of this ADR:

**They run the LLM call from *inside* the sandbox.** The coding CLI is the process that talks to the
model, and it lives in the container. Therefore the container **must be handed the raw provider API
key** (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …) as an environment variable or mounted secret — the
CLI inside cannot reach the model otherwise. The sandbox that is supposed to contain untrusted code
is *also* the sandbox that holds your most sensitive credential. If the agent (or a prompt-injected
tool result, or a malicious dependency the agent installs) exfiltrates that env var, the key is gone.
The isolation boundary and the secret are on the **same side of the wall**.

Aisotropy is architecturally positioned to invert this, and that inversion is the entire reason to
build a native primitive rather than adopt sandcastle. **Inference already happens host-side**
(`internal/llm` — the `llm.Client` `StreamChat` seam; the model call is a host process concern, see
[ADR-0009](0009-provider-agnostic-llm-layer.md)/[ADR-0010](0010-gollem-adoption-scope-and-observability.md)).
The agent loop (`internal/agentloop`) also runs host-side: it asks the model for a structured
`Decision` (which tool, which args), then hands that decision to a `ToolExecutor` to *execute*. Only
the **execution** of a tool call — the "hands" — needs to touch the checkout. So the natural design
is: **keep the brain (LLM + key) on the host, put only the hands (shell/git/file ops) in the
container.** The container never sees the key because the container never makes an LLM call.

This is **key-brokering**: the host brokers the model's *intent* into the sandbox as a concrete,
already-decided command to run, and brokers the sandbox's *output* back to the host as the next tool
result — but the credential that authorizes the model call is spent entirely host-side and never
crosses the boundary. It is the one thing sandcastle structurally cannot do, and it is the
differentiator no coding-CLI-in-a-box competitor offers, because their architecture puts the LLM
client in the box.

## Goals

- A **native `RunTask` primitive** that dispatches an agentic task — model-driven shell/git/file work
  against a checkout — into an OS-isolated container, and returns the resulting commits/branch.
- **Key-brokering as the defining, load-bearing property:** the raw provider API key **MUST NEVER**
  be injected into the sandbox container's environment, mounted into it, or otherwise made reachable
  from inside. The LLM call happens host-side (`internal/llm`); the container runs only tool
  *execution*. This is the constitutional invariant of the whole primitive — a design that leaks the
  key into the container has failed, regardless of what else works.
- **Reuse the existing agent seam:** the in-sandbox executor implements `agentloop.ToolExecutor`
  (`Execute(ctx, name, args) (string, error)`) so the *same* host-side agent loop, scope-gating, and
  structured-output discipline apply — the sandbox is a new *executor*, not a new agent.
- **A subprocess boundary, not a new dependency:** the Docker provider shells out to the `docker` CLI
  via `os/exec` (the sandcastle pattern) — **no Docker Go SDK**, no CGO, preserving the
  single-binary / 100%-permissive-license thesis.
- **An honest, minimal v1:** one Docker provider, one bind-mounted git worktree per task, one small
  "hands" toolset, one merge-to-head branch strategy. Everything larger is a named follow-on, not a
  silent gap.
- **Docker-availability gated tests** (the honesty gate): real-container tests `t.Skip` when the
  `docker` CLI is absent — the same pattern the rqlite / Qdrant / sleep-cycle integrations already
  use. No faked container behavior stands in for a real one.

## Non-goals (v1 scope, held deliberately small)

- **NOT the six coding CLIs.** v1 does not run Claude Code / Codex / Aider / Cursor / Gemini CLI /
  OpenCode inside the box. Doing so would *reintroduce the key-in-sandbox problem* those CLIs have by
  construction — the whole point is that aisotropy's agent is host-side. A "run an external coding CLI
  as a sandboxed sub-agent" mode, if ever wanted, is a separate ADR and would have to solve
  key-brokering for a third-party binary that expects the key inside; it is explicitly out of scope.
- **NOT multiple providers.** One Docker provider ships. Vercel Sandbox, Daytona, Fly Machines,
  Firecracker, gVisor, and a local-`chroot`/`nsjail` provider are named follow-ons behind the
  `Provider` interface — the interface exists in v1 *so that* they can be added without touching
  `RunTask`, but only Docker is implemented.
- **NOT multi-repo / multi-worktree orchestration.** One task = one repo = one worktree = one branch.
  Cross-repo tasks, monorepo sub-path isolation, and parallel-worktree fan-out are follow-ons.
- **NOT a general container platform.** No long-running services, no exposed ports, no inbound
  network for the workload, no persistent volumes beyond the task's worktree bind-mount. A task is a
  bounded unit of work that produces commits and exits.
- **NOT a relaxation of tenant isolation.** This is the *execution*-isolation axis (like ADR-0035).
  Tenant *data* isolation remains the runtime's ctx-scoping and the [ADR-0034](0034-open-core-tier2-sealed-driver.md)
  sealed drivers. A sandboxed task inherits its `tenant_id` from `ctx` like everything else; the
  sandbox does not become a place where two tenants' work shares a buffer.

## Options considered

### Where does the LLM call happen relative to the sandbox?

- **Inside the sandbox (the sandcastle / coding-CLI model).** Rejected — this is the exact failure
  mode this ADR exists to invert. The container must hold the raw API key; the isolation boundary and
  the secret end up on the same side of the wall. It is the right design when the *agent binary* is a
  third-party CLI you cannot split, and the wrong design when you own the agent loop and can keep it
  host-side.
- **Host-side, with only tool execution dispatched into the sandbox (CHOSEN).** The model call is a
  host process concern (`internal/llm`); the agent loop (`internal/agentloop`) runs host-side and
  decides, per turn, one tool call; only that already-decided command is executed inside the
  container. The key is spent host-side and never crosses the boundary. **Key-brokering.** This is the
  differentiator; it is available to aisotropy precisely because inference is already host-side.

### How does the runtime drive the container?

- **The Docker Go SDK (`github.com/docker/docker`).** Rejected for v1 — it is a heavyweight
  dependency (large transitive surface) and is not currently in `go.mod`; the constitutional
  license/dependency posture prefers not adding it. (License itself is Apache-2.0 and would pass the
  gate; the objection is dependency weight and single-binary discipline, not license.)
- **Shell out to the `docker` CLI via `os/exec` (CHOSEN).** The sandcastle pattern, and consistent
  with [ADR-0005](0005-datafusion-go-rust-boundary.md)'s subprocess boundary: `docker run` to create
  the container, `docker exec` to run each brokered tool call, `docker rm` to tear down. No new Go
  dependency; a non-zero exit is a Go-observable `error` with backoff, never an in-process crash. The
  operator must have Docker installed — which is why the tests gate on its availability.

### How does the sandbox plug into the agent?

- **A bespoke in-sandbox agent runtime.** Rejected — it would duplicate `internal/agentloop`'s
  scope-gating and structured-output discipline inside the box, and worse, would want the model there
  (back to key-in-sandbox).
- **A `sandboxExecutor` implementing `agentloop.ToolExecutor` (CHOSEN).** The host-side `agentloop.Loop`
  is unchanged; its `Executor` is swapped for one whose `Execute(ctx, name, args)` runs the named tool
  *inside* the container via `docker exec` and returns stdout as the tool result. The agent loop, the
  permitted-tool set, and the "there is no path from not-in-scopes to executed" invariant all carry
  over verbatim. The sandbox is *just another executor*.

### How are results returned?

- **Mutate the caller's working tree directly.** Rejected — a sandboxed, model-driven agent must not
  be trusted to write straight into HEAD.
- **Merge-to-head branch strategy (CHOSEN).** Each task runs on its own branch off HEAD in a
  dedicated git worktree; the sandbox commits there; `RunTask` returns `TaskResult{Commits, Branch,
  Steps}` and the **host** decides whether/how to merge. The untrusted agent produces a reviewable
  branch, never an unreviewed mutation of the caller's checkout.

## Decision (v1 / minimal)

### 1. A new `internal/sandbox` package (net-new subsystem)

`internal/sandbox` is created from scratch (it does not exist today). It owns the container lifecycle
and the task contract. Its public shape (draft — verify against shipped code):

- **`Provider`** — the pluggable boundary over *where* a sandbox runs. v1 method set (indicative):
  `Create(ctx, spec) (Session, error)` to stand up an isolated environment over a worktree, and a
  `Session` with `Exec(ctx, cmd, args) (stdout, stderr string, code int, err error)` and `Close(ctx)`.
  One implementation ships: **`dockerProvider`**, driving the `docker` CLI via `os/exec`
  (`docker run --rm -d …` to create, `docker exec` per call, `docker rm -f` to tear down). The
  interface exists so Vercel/Daytona/Fly/local providers are *addable* — but only Docker is *added*.

- **`RunTask(ctx, TaskRequest) (TaskResult, error)`** — the primitive. It: creates a git worktree on a
  fresh branch off HEAD; asks the `Provider` for a `Session` over that worktree (bind-mounted into the
  container); constructs a host-side `agentloop.Loop` whose `Executor` is the `sandboxExecutor`
  (below) and whose `Client` is the ordinary host-side `internal/llm` client; runs the loop to
  completion (bounded by `MaxSteps`); collects the commits the agent made on the branch; tears the
  container down; and returns `TaskResult`.

- **`TaskRequest`** — inputs: the repo/worktree root, the task prompt, the permitted tool scopes
  (gating the hands toolset), the branch name (or a derive-from-task default), resource/step bounds.
  `tenant_id` is **not** a field — it travels via `context.Context` per the constitutional gate and is
  read from `ctx`, never accepted as a parameter.

- **`TaskResult`** — `{Commits []Commit, Branch string, Steps []Step}`: the commits the sandboxed
  agent produced, the branch they live on (for the host's merge-to-head decision), and a per-step
  trace (which tool, which args, exit code, truncated output) for observability and audit.

### 2. `sandboxExecutor` — the key-brokering seam, an `agentloop.ToolExecutor`

`sandboxExecutor` implements `agentloop.ToolExecutor`:
`Execute(ctx, name string, args map[string]string) (string, error)`. Its `Execute` maps the model's
already-decided tool call to a concrete command and runs it **inside the container** via the
`Session.Exec` (`docker exec`), returning stdout as the tool result string that the host-side loop
feeds back to the model for the next turn.

The invariant lives here: **the LLM turn that produced `name`/`args` ran host-side, against the
host-side `internal/llm` client, using the host-side key. `Execute` performs no model call — it only
runs a command in the box.** The container's environment is constructed by the `Provider` with the
key **absent** (and the design should assert this: the container env is an explicit allowlist that
does not, and cannot, include `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`/provider credentials). Brain on the
host, hands in the box, key never brokered across.

### 3. The "hands" toolset (small, scope-gated)

A minimal first-party toolset the sandboxed agent may be offered, each an ordinary scope-gated tool in
the same registry as every other tool (so `requiredScope` gates it exactly like MCP / typed / WASM
tools — the sandbox is a *second* line of defense, not a replacement for scope-gating):

- `shell` — run a shell command in the worktree (`docker exec`), return stdout/stderr/exit code.
- `read_file` / `write_file` — read/edit a file within the worktree (path confined to the mount).
- `git` — staged git operations (status/diff/add/commit) confined to the task branch.

The set is intentionally tiny in v1; richer hands (patch application, test runners, package installs
behind a mediated network policy) are follow-ons, each a deliberate widening.

### 4. Merge-to-head branch strategy

Each task is a branch off HEAD in its own worktree; the sandboxed agent commits there; `RunTask`
returns the branch and its commits. The **host** owns the merge decision — the untrusted agent's
output is a reviewable branch, never a direct write to the caller's HEAD. v1 returns the branch;
auto-merge policies (fast-forward-only, require-clean, squash) are host-side follow-ons.

### The differentiator, restated in one line

Every coding-agent-in-a-box puts the LLM client — and therefore the API key — **inside** the sandbox.
Aisotropy runs the brain host-side and dispatches only the hands into the box, so **the agent can run
untrusted and never holds the key.** That inversion is the whole point, and it is only available
because inference was host-side from ADR-0009 onward.

## Consequences

- **A capability no competitor offers.** "The agent runs untrusted, in a container, and never holds
  the provider key" is structurally impossible for sandcastle and the coding-CLI-in-a-box designs,
  because their LLM client lives in the box. For aisotropy it falls out of the existing host-side
  inference architecture. This is the headline differentiator.
- **Reuses, does not fork, the agent.** Because the sandbox is an `agentloop.ToolExecutor`, the
  host-side scope-gating, structured-output `Decision` contract, and the "no path from not-in-scopes
  to executed" invariant apply unchanged. No second agent runtime, no second security model.
- **No new dependency; single-binary posture preserved.** Driving the `docker` CLI via `os/exec`
  (sandcastle's pattern) adds zero Go modules and no CGO. A container failure is a Go-observable
  non-zero exit with backoff, never an in-process crash (ADR-0005 discipline).
- **Honest, small v1 — the gaps are named, not hidden.** ONE Docker provider (not Vercel/Daytona/Fly),
  bind-mount + one worktree (not multi-repo), a tiny hands toolset (not the six coding CLIs), and a
  return-the-branch merge strategy (not auto-merge). Each is a `Provider`-shaped or policy-shaped
  follow-on, explicitly out of v1.
- **The big lift is real.** `internal/sandbox` is a **net-new subsystem** — container lifecycle, git
  worktree management, the executor bridge, the hands toolset, and the Docker-gated test harness are
  all new code. This ADR is the *decision and shape*; it does not pretend the subsystem is small or
  already present.
- **Tests tell the truth.** Real-container tests gate on `docker` availability (`t.Skip` when absent),
  matching the rqlite/Qdrant/sleep-cycle pattern; the key-brokering invariant gets an explicit test
  (assert the container env contains no provider key) so the differentiator is a *checked* claim, not
  a promise.
- **Execution isolation, not data isolation.** Like ADR-0035, this hardens the axis of *running
  untrusted code*; tenant *data* isolation is unchanged (ctx-scoping + ADR-0034 sealed drivers). A
  sandboxed task is still a single-tenant unit of work scoped by `ctx`.

## References

- **Sibling / same axis:** [ADR-0035](0035-wasm-plugin-sandbox.md) (WASM = the pure `JSON -> JSON`
  plugin tier of execution isolation; this ADR = the agentic "hands" tier — container + real
  shell/git/file work) · **trust model:** [ADR-0033](0033-open-core-extension-seams.md) (Go cannot
  sandbox in-process code → untrusted *agentic* work needs a real OS boundary: a container)
- **Seam reused:** [ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) /
  `internal/agentloop` (`ToolExecutor` — the sandbox is a new executor, not a new agent) ·
  **inference is host-side:** [ADR-0009](0009-provider-agnostic-llm-layer.md) /
  [ADR-0010](0010-gollem-adoption-scope-and-observability.md) / `internal/llm` (why key-brokering is
  possible at all — the model call never needed to be in the box)
- **Subprocess boundary discipline:** [ADR-0005](0005-datafusion-go-rust-boundary.md) (shell out via
  `os/exec`, non-zero exit as a Go error with backoff, no in-process crash) — applied here to the
  `docker` CLI
- **Different axis (held, not relaxed):** [ADR-0034](0034-open-core-tier2-sealed-driver.md) /
  [ADR-0011](0011-tenant-scoped-statement-builder.md) / [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md)
  (tenant *data* isolation — ctx-scoping + sealed drivers; the sandbox is single-tenant by `ctx`)
- **Prior art inverted:** sandcastle and the coding-CLI-in-a-box family (Claude Code / Codex / Aider /
  Cursor / Gemini CLI / OpenCode) — they run the LLM client, and therefore the API key, *inside* the
  sandbox; this ADR runs the brain host-side and brokers only the hands into the box.
- **Design targets (net-new, verify against shipped code):** `internal/sandbox` (`Provider`,
  `dockerProvider`, `RunTask`, `TaskRequest`, `TaskResult`, `sandboxExecutor`) — none present at draft
  time; the Phase-3 implementation agent reconciles this ADR with the code it lands.
