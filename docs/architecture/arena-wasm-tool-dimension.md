---
type: Concept
title: The wasm_tool dimension measures a technology choice, not a capability
date: 2026-07-28
status: finding — dimension retained, claim retracted, restatement proposed
---

# `wasm_tool`: what the ecosystem sweep found

## The claim that started it

`arenas/live/aisotropy/main.go` described the dimension as:

> `wasm_tool` — aisotropy's DIFFERENTIATOR: a tool body executed inside a wazero
> WASM sandbox **(the capability no other framework here has)**

That parenthetical is a capability-absence claim about eight other products, which
`.claude/rules/capability-absence-claims.md` requires a five-point ecosystem sweep to
support — core, official extensions, platform offerings, official third-party
integrations, vendor docs — with a maturity label and last-release date per finding.

The sweep had never been run. It was run on 2026-07-28, while writing the eight
`infeasible:<reason>` cells the other adapters were missing. **The claim did not
survive it.**

## What the sweep found

| framework | sandboxed tool execution? | WASM guest? | maturity |
|---|---|---|---|
| **aisotropy** | yes — wazero WASI guest, in-process | **yes** | NATIVE |
| **pydantic-ai** | `pydantic/mcp-run-python`, Pyodide (CPython→WASM) under Deno | **yes** | OFFICIAL-EXTENSION — **ARCHIVED 2026-01-30** |
| **langgraph** | `langchain-ai/langchain-sandbox`, `PyodideSandboxTool` wired into LangGraph ReAct/CodeAct agents | **yes** | OFFICIAL-EXTENSION — **ARCHIVED 2026-01-14** |
| **ai-sdk** | AI SDK 7 `SandboxSession` + Vercel Sandbox | no — Node/microVM | NATIVE, current |
| **mastra** | Workspaces remote sandboxes (Daytona, E2B, Blaxel) | no — container/microVM | NATIVE/OFFICIAL, current |
| **eino** | CommandLine tool → Docker; ADK Agentkit Sandbox → remote Volcengine | no — container/remote VM | OFFICIAL-EXTENSION, current |
| **genkit-go** | filesystem middleware (path scoping); server-side execution in Managed Agents | no | NATIVE scoping + PLATFORM |
| **spring-ai** | `spring-ai-community/agent-sandbox` over local process / Docker / E2B microVM | no | COMMUNITY, current |
| **llamaindex** | official `code_interpreter` calls `subprocess.run` on the host | no | **no isolation at all**; WASM only via third-party `llm-sandbox` (COMMUNITY-ONLY) |

## Why the claim was false — two separate ways

**1. Two competitors shipped WASM-sandboxed tool execution.** Not a community fork:
Pydantic's own repo and LangChain's own repo, both running tool code inside Pyodide
(CPython compiled to WebAssembly) under Deno, both wired into their agent loops.
"No other framework here has it" was never true.

Both were **archived in January 2026**, two weeks apart, and the stated reasons matter
more than the dates. Pydantic: *"no safe way to run Python within pyodide safely with
reasonable latency"* — Python inside Pyodide can run arbitrary JavaScript, taint the
runtime, read reachable files, and exhaust memory. LangChain: *"no longer maintained …
we do not recommend using `langchain-sandbox` for any production use cases"*, with the
strategy moving to provider-backed containers (Modal, Daytona, Runloop).

Under our own rule an abandoned extension does not count as "has" — so *today* neither
ships one. But the honest sentence is **"two tried it with a different technology and
retreated"**, not "nobody has it". The difference matters: the first invites the
question of whether they retreated for reasons that also apply to us.

**2. Six of the eight sandbox tool execution today — just not in WebAssembly.** Only
llamaindex has no isolation boundary in its official tool (and it says so itself).
Stated as a capability — *is the tool body isolated from the host at all?* — this
dimension is one most competitors pass.

## What this means for the dimension

`wasm_tool`'s pass criterion is **"the tool body executes inside a WASM guest"**. That
is a statement about *mechanism*, not about *capability*, and it is a mechanism only
our implementation uses. A clean sweep here is therefore not evidence that we sandbox
tools and they do not — it is evidence that we sandbox them **the way we chose to**.

This is the same defect the arena found in `memory_cross_session` the same day
(`arena-fairness-protocol.md` §4a), in a purer form. There, the outcome turned on which
adapter author decided to build something. Here, the criterion itself is shaped around
our implementation, so the outcome is settled before any adapter runs.

It is also the sharpest instance of the asymmetry `capability-absence-claims.md` warns
about: **a gap that flatters the house is the one nobody else is motivated to find.**
Eight competitors were absent from this dimension entirely — not failing it, not
declaring it infeasible, simply not present — while our single ✓ sat there reading like
a comparison had happened.

## Decision taken, and the one left open

**Taken (2026-07-28):**
- The "no other framework here has" claim is **retracted** in
  `arenas/live/aisotropy/main.go`, with the sweep result recorded inline.
- All eight competitors now declare `wasm_tool` with a real `infeasible:<reason>` that
  names **what they do have** and why it does not satisfy this criterion — never a bare
  absence, which would be false for six of them.
- The runner warns on every run when an adapter does not declare a dimension its peers
  do, including when we are the sole declarant (`undeclaredDimensions`,
  `arenas/live/runner/matrix.go`).

**Open — operator's call.** Either rename the dimension to its real subject
(`wasm_tool` → it already is honestly named; the *framing* was the problem), or restate
it as **`sandboxed_tool`** with the isolation mechanism as a `pass:<provenance>` label —
exactly the machinery built for `memory_cross_session` the same day:

```
pass:WASM guest (wazero, in-process)     aisotropy
pass:microVM (Vercel Sandbox)            ai-sdk
pass:container (Daytona/E2B/Blaxel)      mastra
pass:container (Docker CommandLine)      eino
fail                                     llamaindex (subprocess.run, no boundary)
```

That version compares a capability every one of them competes on, discriminates on real
differences (in-process µs-latency WASM vs a remote container round-trip), and does not
decide the outcome by naming the criterion after our own build.

Until that decision is made, **`wasm_tool` is descriptive, not a scoreboard win**, and
must not be cited as one in `RESULTS.md`, marketing-adjacent docs, or any comparison.

## Sources

- [pydantic/mcp-run-python](https://github.com/pydantic/mcp-run-python) — archived 2026-01-30
- [langchain-ai/langchain-sandbox](https://github.com/langchain-ai/langchain-sandbox) — archived 2026-01-14
- [Vercel AI SDK 7 — SandboxSession](https://vercel.com/blog/ai-sdk-7)
- [Mastra — remote sandboxes](https://mastra.ai/blog/introducing-remote-sandboxes)
- [LlamaIndex — code interpreter tool](https://developers.llamaindex.ai/python/framework-api-reference/tools/code_interpreter/)
- [Eino — CommandLine tool](https://www.cloudwego.io/zh/docs/eino/ecosystem_integration/tool/tool_commandline/)
- [spring-ai-community/agent-sandbox](https://github.com/spring-ai-community/agent-sandbox)
