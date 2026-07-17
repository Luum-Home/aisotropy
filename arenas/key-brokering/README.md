---
type: Arena
title: "Key-brokering arena — aisotropy RunTask as a clean sandbox-as-tool (Pattern 2) implementation, with the key-broker invariant enforced-by-default vs. coding-CLI-in-a-box orchestrators"
description: An arena that proves aisotropy's OWN key-broker invariant — RunTask keeps the provider key host-side and stands up a key-free task container while the task still completes — and honestly situates that against the field. Host-side inference with only tool execution sandboxed is the STANDARD "sandbox-as-tool" pattern (LangChain's Pattern 2), table stakes across E2B/Daytona/deepagents/OpenAI/Anthropic. aisotropy's real, narrower edge is against coding-CLI-in-a-box orchestrators (e.g. sandcastle) that run an unmodified third-party CLI inside the box and must therefore inject the provider credential. Side B drives the real public App.RunTask and is a genuine proof of that invariant; Side A is a minimal repro of sandcastle's *documented* key injection, and sandcastle's behavior is established by SOURCE CITATION, not by running its binary.
tags: [arenas, key-brokering, sandbox, security, isolation, sandcastle, docker, pattern-2, sandbox-as-tool]
timestamp: 2026-07-16T00:00:00Z
---

# Key-brokering arena

> **Claim under test.** A coding agent needs a provider key to call the model. *Where does that
> key physically live while the agent's "hands" run in a sandbox container?*
> **This arena's job.** Prove aisotropy's own key-broker invariant with running code — `RunTask`
> keeps the key host-side and the task container is key-free while the task still completes — and
> then situate that fact honestly against the wider agent-sandbox field.

## Where aisotropy sits: the standard "sandbox-as-tool" pattern (Pattern 2)

aisotropy's `RunTask` is a **clean implementation of the standard "sandbox-as-tool" pattern** —
what LangChain names ["Pattern 2"](https://www.langchain.com/blog/the-two-patterns-by-which-agents-connect-sandboxes):
**the model runs host-side; only tool execution is sandboxed.** The provider key stays in the host
process and is never handed to the sandbox.

This is **not** an aisotropy innovation. Host-side inference with a key-free sandbox is **table
stakes** across the agent-sandbox field — E2B, Runloop, Modal, Daytona,
[deepagents](https://docs.langchain.com/oss/python/deepagents/sandboxes), and the
OpenAI Agents SDK / Anthropic code-interpreter integrations all run the model host-side and
[never give the sandbox the provider key](https://e2b.dev/docs/quickstart/connect-llms). Against
that broader field, aisotropy has no key-brokering *differentiator*; it is simply doing the
standard thing correctly.

**What aisotropy actually contributes here** is narrower and concrete:

1. The pattern is **wired-by-default with the invariant enforced in code** — `Spec.Env` is left
   `nil` by construction and `assertNoProviderSecret(spec.Env)` double-checks that what reaches the
   provider carries no known provider credential. The key-free sandbox is a checked invariant, not
   an implicit convention.
2. A **real, narrower edge** exists specifically against **coding-CLI-in-a-box orchestrators** —
   tools that run an *unmodified third-party coding CLI* (Claude Code / Codex / Cursor / …) *inside*
   the sandbox. Because the model-calling process is in the box, those tools must inject the
   provider credential into the container env. **sandcastle** is the concrete example this arena
   cites and reproduces. That is where "the key is / is not readable inside the box" becomes a real,
   security-relevant divergence.

## The two execution models

| | Coding-CLI-in-a-box (the *sandcastle* pattern) | aisotropy `RunTask` (Pattern 2, key-broker invariant enforced) |
|---|---|---|
| Where the **model call** happens | **Inside** the container (an unmodified Claude Code / Codex / Cursor / … runs in the box) | **Host-side** — the `llm.Client` in the host process (standard Pattern 2) |
| Therefore the **provider key** must be… | …**injected into the container env** so the in-box CLI can reach the model (sandcastle's no-proxy design — see caveat below) | …**never placed in the sandbox**; `Spec.Env` is left `nil` and `assertNoProviderSecret` enforces it |
| What runs **in** the container | the coding CLI *and* its tool executions | **only** the tool executions ("hands": `write_file`, `run_shell`, …) the host-side model chose |
| `printenv` from inside the box shows the key? | **YES — present** (established by source citation, below) | **NO — absent** (proven by Side B running the real `App.RunTask`) |

Both are legitimate designs. The security-relevant difference is real, but it is specifically
against the coding-CLI-in-a-box niche — **not** against the broader agent-sandbox field, where
host-side inference is the norm.

## What the arena runs

`run.sh` is docker-gated (exit 0 + `SKIP` when Docker is absent) and has two parts. Note the
**asymmetry** in what each side proves:

- **PART 1 — pure-`docker`, language-agnostic** (no Go build; runs even if the surrounding tree is
  mid-edit). This is a **demonstration**, not an adversarial contest:
  - *Side A (minimal repro of sandcastle's documented injection):* `docker run -d -e
    ANTHROPIC_API_KEY=<canary> alpine …` then `docker exec <c> printenv` → the key is **PRESENT**.
    This is **our own container** showing that `-e KEY=VALUE` injection is visible via `printenv` —
    i.e. a minimal reproduction of what sandcastle's `DockerLifecycle.ts` documents doing. It does
    **not** run sandcastle's binary and proves nothing sandcastle-specific on its own; sandcastle's
    behavior is established by the **source citation** below.
  - *Side B (aisotropy's invariant):* the canary is exported into **this shell's** env (the host
    holds it), a container is started **without** `-e ANTHROPIC_API_KEY`, and `docker exec printenv`
    shows the key is **ABSENT** — while a sandboxed command still writes a file (the "hands" work
    with no key present).
- **PART 2 — the genuine proof of aisotropy's invariant** (`TestKeyBrokering_HeadToHead`,
  docker+git-gated): drives aisotropy's **public `App.RunTask`** with a live provider key in the
  **host** process and a probe wrapped around the Docker sandbox provider. It asserts the task
  container env carries **neither the key's value nor its name**, while the host-side (scripted,
  keyless) model drives a real `write_file → git_commit` that **merges a commit to HEAD**. This is
  the sound part: it exercises aisotropy's real public API and proves aisotropy's own invariant end
  to end. It is the public-API twin of `internal/taskagent`'s
  `TestRunTask_BrokerInvariant_KeyNeverEntersContainer`.

**What is genuinely proven, and what is not.** Side B (PART 2) is a real proof of aisotropy's
key-broker invariant. Side A (PART 1) is aisotropy's own container demonstrating that `-e` injection
is `printenv`-visible; it is a faithful minimal repro of sandcastle's *documented* behavior, not a
head-to-head against sandcastle's binary. The divergence between the two models is real, but the
sandcastle half of it rests on the **source citation**, not on this arena executing sandcastle.

### Commands it runs

```bash
# from the repo root (Docker required; git required for PART 2)
bash arenas/key-brokering/run.sh

# PART 2 alone (the real-code proof of aisotropy's invariant, scoped to this package only):
go test ./arenas/key-brokering/ -run TestKeyBrokering_HeadToHead -count=1 -v
```

Nothing needs a network or a real provider key: the host-side "model" is a deterministic scripted
`llm.Client`, and the key is a dummy canary assembled at runtime (no key-shaped literal in source).

## Result (executed ▶, Docker 29.4.0, darwin/arm64)

```
== key-brokering arena ==

-- PART 1: pure-docker demonstration (language-agnostic) --
Side A  sandcastle injection repro : ANTHROPIC_API_KEY PRESENT in container env  (our own '-e' repro of sandcastle's documented behavior)
Side B  aisotropy RunTask          : ANTHROPIC_API_KEY ABSENT in container env; hands still ran (wrote note.txt)

PART 1 PASS: '-e' injection is printenv-visible (sandcastle model); aisotropy RunTask: key ABSENT, hands still ran

-- PART 2: real-code proof of aisotropy's invariant (public App.RunTask) --
=== RUN   TestKeyBrokering_HeadToHead
    broker_arena_test.go:204: Side A  sandcastle injection repro : ANTHROPIC_API_KEY PRESENT in container env (our own '-e' repro of sandcastle's documented behavior)
    broker_arena_test.go:256: Side B  aisotropy RunTask          : ANTHROPIC_API_KEY ABSENT in container env; task still ran (commit a99676dd10 merged to HEAD)
    broker_arena_test.go:258: PASS: '-e' injection is printenv-visible (sandcastle model); aisotropy RunTask: key ABSENT, task still ran
--- PASS: TestKeyBrokering_HeadToHead (1.10s)
PASS
ok  	github.com/Luum-Home/aisotropy/arenas/key-brokering	1.764s

PART 2 PASS: App.RunTask stood up a key-free task container and merged a real commit.
```

## How the *sandcastle* side was established (source citation — NOT executed here)

The coding-CLI-in-a-box model is **not run** by this arena; it is cited from the pinned upstream
clone of **sandcastle** (`github.com/mattpocock/sandcastle`, MIT, read at merge commit `e99f832`),
which orchestrates *coding* agents in Docker/Podman sandboxes. Its own README states the design
plainly: *"No API key proxy exists. The key flows directly into the container environment."* Its
key-injection flow, read line-by-line:

- **`src/EnvResolver.ts`** — `resolveEnv()` reads `.sandcastle/.env` with a `process.env` fallback:
  *"Precedence: `.sandcastle/.env` > `process.env` … Only keys declared in `.sandcastle/.env` are
  resolved from `process.env`."* Provider credentials (e.g. `ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_KEY`, `CURSOR_API_KEY`, …; see the `envExample` templates in
  `src/InitService.ts`) are declared here.
- **`src/mergeProviderEnv.ts`** — merges resolver output with the agent- and sandbox-provider env
  into one `Record<string, string>`.
- **`src/createSandbox.ts` → `src/startSandbox.ts`** — hands that merged `env` to
  `provider.create({ env })`.
- **`src/DockerLifecycle.ts:126`** — `startContainer(...)` turns the env into container flags:
  ```ts
  const envFlags = Object.entries(env).flatMap(([k, v]) => ["-e", `${k}=${v}`]);
  ```
  i.e. `docker run … -e KEY=VALUE …`. The provider credential is therefore in the **container's
  environment**. PART 1's Side A reproduces *that `-e` mechanism* at the container-env level using
  aisotropy's own Docker provider — a minimal repro of the cited behavior, not a run of sandcastle.

This matches the honest, adjacent note already in [`../README.md`](../README.md): sandcastle is an
out-of-category tool (it orchestrates coding CLIs in sandboxes, not a multi-tenant conversational
runtime) whose secrets flow is plain container env.

## How the *aisotropy* side is enforced (code citation)

- **Public seam** — `aisotropy/runtask.go`: `TaskRequest` "deliberately [carries] **NO** provider
  credential (the LLM call stays host-side; the key never enters the sandbox)"; `App.RunTask`
  drives "the same `llm.Client` this App uses for chat" host-side over the sandboxed hands.
- **Enforcement point** — `internal/taskagent/runtask.go`: the sandbox `Spec.Env` is left `nil`
  ("no host secret is ever read into it") and `assertNoProviderSecret(spec.Env)` double-checks what
  is handed to the provider carries no known provider credential.
- **Provider** — `internal/sandbox/docker.go`: `Spec.Env` is documented as **non-secret config
  only**; the provider "accepts a `Spec` of non-secret config and never reads a provider API key."

## What sandcastle does genuinely well (and aisotropy does NOT)

The key-exposure contrast is **not** a verdict that aisotropy is "better than sandcastle." They
occupy **different niches**, and sandcastle is strong at things aisotropy's `RunTask` does not even
attempt:

- **It runs *whole, unmodified* third-party coding CLIs in a box.** sandcastle's job is to take an
  existing agentic coding tool — Claude Code, OpenAI Codex / OpenAI-key CLIs, Cursor, and the other
  agents its `envExample` templates provision credentials for (`ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_KEY`, `CURSOR_API_KEY`, … in `src/InitService.ts`) — and run it
  **as-is** inside Docker/Podman. That is real, valuable, and hard: you get the full capability of a
  mature coding agent, sandboxed, with zero changes to the CLI. **aisotropy cannot do this.**
  `RunTask` runs aisotropy's *own* host-side loop over sandboxed "hands"; you cannot drop Claude
  Code or Cursor into it. If what you want is "run the real Claude Code, but in a box," sandcastle
  is the tool and aisotropy is not.
- **Provider breadth by design.** Because the credential lives in the box and the in-box CLI does
  its own inference, sandcastle is naturally agnostic to *which* coding agent and provider you use —
  its resolver (`src/EnvResolver.ts`) is built to provision whatever set of provider credentials the
  chosen CLI needs. aisotropy's broker path is scoped to the single host-side `llm.Client` the App
  is built with.
- **Per-agent isolation and merge-back workflow.** sandcastle is designed around orchestrating
  coding agents in sandboxes with isolated workspaces and a merge-back path — the ergonomics of
  "spin up an agent on a task, let it work in its own space, take the diff." That is a genuinely
  useful orchestration surface for parallel agentic coding, and it is squarely sandcastle's
  problem domain, not aisotropy's.

The honest, two-sided thesis: **sandcastle = run whole coding CLIs in a box** (the provider key
*must* be in-box because the CLI does its own inference — sandcastle's explicit no-proxy design);
**aisotropy `RunTask` = host-side inference + sandboxed hands with the key-broker invariant** (the
key *never* enters the box). Each is strong at its own thing. The key-exposure contrast this arena
measures only matters **if** your requirement is specifically *untrusted-agent tool execution
without exposing the provider credential to the sandbox* — which is aisotropy's design center, and
not what sandcastle set out to solve.

### The broader ecosystem is mature

Zooming out, the general "sandbox-as-tool" capability aisotropy uses is well served by a mature
field: **E2B**, **Daytona**, **Modal**, Runloop, and the LangChain/OpenAI/Anthropic integrations
all offer production-grade sandboxes for host-side agents, most with far more operational surface
than this arena's minimal Docker provider — snapshotting, warm pools, persistence, richer runtimes.
aisotropy is not competing with those on breadth; it uses the same pattern and adds an enforced
key-broker invariant wired into its own multi-tenant runtime.

## Honest caveats

- **Host-side inference is table stakes, not an aisotropy innovation.** The differentiator this
  arena demonstrates is specifically against **coding-CLI-in-a-box** orchestrators (sandcastle's
  niche). Against the broader agent-sandbox field (E2B, Daytona, deepagents, the OpenAI/Anthropic
  integrations), Pattern 2 is the norm and aisotropy has no key-brokering edge — it is doing the
  standard thing, with the invariant checked in code. Do not read this arena as a claim of novelty
  over that field.
- **Side A is a repro, not an adversarial head-to-head.** Side A stands up *aisotropy's own*
  `DockerSandbox()` with `Spec.Env{ANTHROPIC_API_KEY:canary}` and shows `printenv` sees it — i.e. it
  proves the near-tautology "if you pass `-e KEY=VALUE`, `printenv` shows `KEY`". It does **not** run
  sandcastle's binary and proves nothing sandcastle-specific by itself. Sandcastle's key-in-container
  behavior is established by the **source citation** above. The genuinely sound proof in this arena
  is Side B / PART 2 (aisotropy's own invariant, via the real `App.RunTask`).
- **Key-in-container is intrinsic to sandcastle's *no-proxy* design, not a law of the category.**
  sandcastle's README is explicit — *"No API key proxy exists. The key flows directly into the
  container environment."* — so for sandcastle the credential must live in the box. But that is a
  property of running an unmodified CLI *without a proxy*, not a hard consequence of "the CLI runs
  in the box." A proxy sidecar (a LiteLLM-style broker, or Claude Code pointed at
  `ANTHROPIC_BASE_URL` with a dummy token) could keep the real provider key host-side even with a
  coding CLI inside the container. The divergence this arena shows is sandcastle-specific, not a
  universal truth about coding-CLI-in-a-box.
- **This arena does not run a full sandcastle task.** A complete sandcastle run needs a real coding
  CLI *and* a real model + credential inside the box; standing that up is out of scope and would
  require a live provider key. What Side A reproduces is the **`-e` injection mechanism** at the
  container-env level; what actually settles sandcastle's behavior is the cited source.
- **`printenv` is the honest probe** and also its own boundary: it proves the key is/reachable in
  the container's **process environment**. A key injected via `-e` is additionally visible to
  `docker inspect` on the host and to any child process in the box; a key kept host-side is none of
  those. The arena does not model exotic exfiltration channels (shared mounts, side-channels) — it
  measures the first-order, security-relevant fact.
- **PART 2 uses a keyless scripted host client**, not a live provider. That is deliberate: it holds
  the "model" constant and free so the *only* variable is *where the key lives*. The canary stands
  in for a real key; the enforcement path (`Spec.Env = nil`, `assertNoProviderSecret`) is identical
  whether the key is real or a canary.
- **Docker/git-gated.** With Docker absent, `run.sh` prints `SKIP` and exits 0, and the Go test
  `t.Skip`s — the same honesty gate the rqlite/qdrant/sleepcycle suites use. Reproduced here on
  Docker 29.4.0; your absolute timings will differ, the PASS/FAIL shape will not.

## Files

- [`broker_arena_test.go`](broker_arena_test.go) — the docker+git-gated proof of aisotropy's
  invariant (`TestKeyBrokering_HeadToHead`) plus its probe and scripted host client. Side B is the
  genuine proof; Side A is the `-e` injection repro.
- [`run.sh`](run.sh) — the runnable driver: PART 1 (pure-docker demonstration, language-agnostic) +
  PART 2 (the Go proof of aisotropy's invariant).
