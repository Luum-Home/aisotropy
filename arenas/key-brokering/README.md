---
type: Arena
title: "Key-brokering arena — coding-CLI-in-a-box vs. aisotropy RunTask (head-to-head, executed)"
description: An adversarial arena that upgrades the key-brokering claim from "asserted (we read their code)" to "proven adversarially". It runs BOTH execution models against a live Docker daemon and probes the container's own environment with `printenv` — the ground truth of what a process inside the sandbox can read. The coding-CLI-in-a-box model injects the provider key into the container env; aisotropy's RunTask keeps it host-side and the task container is key-free while the task still completes.
tags: [arenas, key-brokering, sandbox, security, isolation, sandcastle, docker, adversarial, competitive]
timestamp: 2026-07-16T00:00:00Z
---

# Key-brokering arena

> **Claim under test.** A coding agent needs a provider key to call the model. *Where does that
> key physically live while the agent's "hands" run in a sandbox container?*
> **This arena's job.** Turn that from *asserted* ("we read their code") into *proven
> adversarially* — the same discipline the [isolation matrix](../README.md) uses to turn "should
> isolate" into a reproduced cross-tenant leak. We run **both** execution models against **real
> Docker** and read the container's own environment with `printenv`.

## The two execution models

| | Coding-CLI-in-a-box (the *sandcastle* pattern) | aisotropy `RunTask` (the broker) |
|---|---|---|
| Where the **model call** happens | **Inside** the container (Claude Code / Codex / Cursor / … run in the box) | **Host-side** — the `llm.Client` in the host process |
| Therefore the **provider key** must be… | …**injected into the container env** so the in-box CLI can reach the model | …**never placed in the sandbox**; `Spec.Env` is left `nil` by construction |
| What runs **in** the container | the coding CLI *and* its tool executions | **only** the tool executions ("hands": `write_file`, `run_shell`, …) the host-side model chose |
| `printenv` from inside the box shows the key? | **YES — present** | **NO — absent** |

Both are legitimate designs. The security-relevant difference is empirical and this arena measures
it directly: **in one model the key is readable by any process (or prompt-injected command) inside
the sandbox; in the other it is not.**

## What the arena runs

`run.sh` is docker-gated (exit 0 + `SKIP` when Docker is absent) and has two parts:

- **PART 1 — pure-`docker`, language-agnostic** (no Go build; runs even if the surrounding tree is
  mid-edit):
  - *Side A (coding-CLI-in-a-box):* `docker run -d -e ANTHROPIC_API_KEY=<canary> alpine …` then
    `docker exec <c> printenv` → the key is **PRESENT**. This is exactly how a coding-CLI-in-a-box
    injects the key, because the CLI runs inside the box.
  - *Side B (aisotropy model):* the canary is exported into **this shell's** env (the host holds
    it), a container is started **without** `-e ANTHROPIC_API_KEY`, and `docker exec printenv`
    shows the key is **ABSENT** — while a sandboxed command still writes a file (the "hands" work
    with no key present).
- **PART 2 — the real-code proof** (`TestKeyBrokering_HeadToHead`, docker+git-gated): drives
  aisotropy's **public `App.RunTask`** with a live provider key in the **host** process and a
  probe wrapped around the Docker sandbox provider. It asserts the task container env carries
  **neither the key's value nor its name**, while the host-side (scripted, keyless) model drives a
  real `write_file → git_commit` that **merges a commit to HEAD**. This is the public-API twin of
  `internal/taskagent`'s `TestRunTask_BrokerInvariant_KeyNeverEntersContainer`.

### Commands it runs

```bash
# from the repo root (Docker required; git required for PART 2)
bash arenas/key-brokering/run.sh

# PART 2 alone (the real-code proof, scoped to this package only):
go test ./arenas/key-brokering/ -run TestKeyBrokering_HeadToHead -count=1 -v
```

Nothing needs a network or a real provider key: the host-side "model" is a deterministic scripted
`llm.Client`, and the key is a dummy canary assembled at runtime (no key-shaped literal in source).

## Result (executed ▶, Docker 29.4.0, darwin/arm64)

```
== key-brokering arena ==

-- PART 1: pure-docker head-to-head (language-agnostic) --
Side A  coding-CLI-in-a-box : ANTHROPIC_API_KEY PRESENT in container env  (injected via 'docker run -e', sandcastle model)
Side B  aisotropy RunTask   : ANTHROPIC_API_KEY ABSENT in container env; hands still ran (wrote note.txt)

PART 1 PASS: coding-CLI-in-a-box: key PRESENT in container env; aisotropy RunTask: key ABSENT, hands still ran

-- PART 2: real-code proof (aisotropy public App.RunTask) --
=== RUN   TestKeyBrokering_HeadToHead
    broker_arena_test.go:192: Side A  coding-CLI-in-a-box : ANTHROPIC_API_KEY PRESENT in container env (injected via docker -e, sandcastle model)
    broker_arena_test.go:244: Side B  aisotropy RunTask   : ANTHROPIC_API_KEY ABSENT in container env; task still ran (commit a94c6bd747 merged to HEAD)
    broker_arena_test.go:246: PASS: coding-CLI-in-a-box: key PRESENT in container env; aisotropy RunTask: key ABSENT, task still ran
--- PASS: TestKeyBrokering_HeadToHead (1.10s)
PASS
ok  	github.com/Luum-Home/aisotropy/arenas/key-brokering	1.764s

PART 2 PASS: App.RunTask stood up a key-free task container and merged a real commit.
```

## How the *sandcastle* side was verified (source citation)

The coding-CLI-in-a-box model is cited from the pinned upstream clone of **sandcastle**
(`github.com/mattpocock/sandcastle`, MIT, read at merge commit `e99f832`), which orchestrates
*coding* agents in Docker/Podman sandboxes. Its key-injection flow, read line-by-line:

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
  environment** — which is precisely what PART 1's Side A reproduces at the container-env level,
  using aisotropy's own Docker provider and the same `-e` mechanism.

This matches the honest, adjacent note already in [`../README.md`](../README.md): sandcastle is an
out-of-category tool (it orchestrates coding agents in sandboxes, not a multi-tenant conversational
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

## Honest caveats

- **This arena does not run a full sandcastle task.** A complete sandcastle run needs a real coding
  CLI *and* a real model + credential inside the box; standing that up is out of scope and would
  require a live provider key. What is provable — and what actually matters for the security
  question — is the **key-injection difference at the container-env level**, and that is what both
  parts measure directly. Side A is therefore a *faithful minimal repro* of sandcastle's injection
  (same `docker run -e` mechanism, cited above), not sandcastle's own binary end-to-end.
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

- [`broker_arena_test.go`](broker_arena_test.go) — the docker+git-gated real-code proof
  (`TestKeyBrokering_HeadToHead`) plus its probe and scripted host client.
- [`run.sh`](run.sh) — the runnable driver: PART 1 (pure-docker, language-agnostic) + PART 2 (the
  Go proof).
