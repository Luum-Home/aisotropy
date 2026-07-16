---
type: Tutorial
title: "06 — Sandboxed tasks"
description: Run an agentic coding task with App.RunTask — the key-brokering primitive. The model runs host-side; its hands run in a throwaway sandbox that never gets the key.
tags: [tutorial, runtask, key-brokering, sandbox, docker]
timestamp: 2026-07-16T00:00:00Z
---

# 06 — Sandboxed tasks

**Goal:** let the agent *act* on a repository — safely. This is the
[key-brokering](../concepts.md#4-key-brokering-the-agent-gets-hands-never-the-wallet) primitive.

`App.RunTask` opens an isolated git worktree on a fresh branch, stands up a sandbox container over it,
and drives the **host-side** agent loop over the sandboxed hands (`run_shell` / `write_file` /
`read_file` / `git_commit`). If the run produced commits, the task branch is merged back into `HEAD`.

```go
app := aisotropy.New()
defer func() { _ = app.Close() }()

res, err := app.RunTask(ctx, aisotropy.TaskRequest{
	RepoDir: "./my-repo",
	Prompt:  "Create greeting.txt containing 'hola', then commit it.",
	Scopes:  aisotropy.SandboxToolScopes(), // full hands set; pass a subset to restrict
	OnLine:  func(line string) { fmt.Println("[sandbox]", line) }, // live stdout, optional
})
if err != nil {
	log.Fatal(err) // this step needs Docker — is the daemon running?
}
fmt.Printf("made %d commit(s) on %q — and never saw your API key\n", len(res.Commits), res.Branch)
```

The invariant is **structural**: there is *no field on `TaskRequest` for a provider key*. The one
privileged operation that spends the key (the model call) runs in *this* process; only the tool
*executions* the model chose are dispatched into the container. The container gets the hands, never
the wallet — the exact inverse of a coding-CLI-in-a-box.

`Scopes` gate the hands just like tool scopes gate a chat tool: `SandboxToolScopes()` grants the full
set; pass a subset to let the model, say, read and write files but never run a shell. `BranchStrategy`
decides the endgame — the zero value `MergeToHead` auto-merges; `ReturnBranch` leaves the commits on
the task branch for you to review or open a PR from.

**Requirements & honesty note.** This lesson needs **Docker** (the default provider shells out to the
`docker` CLI; it works with OrbStack too). Under the keyless stub the model produces no actionable
tool call, so a stub run completes gracefully with **zero commits** — it proves the full pipeline
(worktree → container → host-side loop → discard) and that the container booted **without** a key, but
makes no edits. A real provider key makes it edit and commit — still key-free in the box.

**Runnable example:** [`examples/task-agent`](../../examples/task-agent) — builds a throwaway repo and
runs exactly this, so it never touches a real checkout.

**Depth:** [ADR-0036 — sandboxed task agent key-brokering](../decisions/0036-sandboxed-task-agent-key-brokering.md)
and the [threat model](../architecture/threat-model.md).

Next: [07 — Concurrent tasks](07-concurrent-tasks.md).
