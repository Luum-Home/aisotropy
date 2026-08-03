---
type: Research
title: Inspect AI — evaluated, not adopted, and what to steal instead
date: 2026-07-28
status: verdict — DO NOT ADOPT as the harness; steal two things
---

# Inspect AI as our trajectory layer: evaluated

The prior-art sweep flagged this as the biggest gap against the state of the
art: maintained eval harnesses model agent **trajectories** as first-class —
reasoning chains, tool calls, intermediate steps — while our scorer grades the
final text plus a side-channel `tool_calls` array. Inspect AI (UK AI Security
Institute + Meridian Labs) is the most serious of them.

The instruction was to evaluate it, not adopt it blind. Verdict: **do not adopt
it as the harness.** The mismatch is architectural, not a matter of taste. Two
specific things are worth stealing.

## What Inspect actually is

- Python framework, `pip install`, Python ≥ 3.10.
- Primitives: **dataset → Task → Solver → Scorer**.
- Multi-turn/agent workflows with tools; sandboxed execution (Docker built in,
  optional Kubernetes/Proxmox).
- One provider interface over OpenAI, Anthropic, Google, Groq, Mistral, xAI,
  Bedrock, Azure, Together, Cloudflare, plus local vLLM/Ollama/llama-cpp.
- VS Code log viewer and a web Inspect View.
- Custom solvers and scorers package as ordinary Python packages.

Sources: [inspect.aisi.org.uk](https://inspect.aisi.org.uk/),
[UKGovernmentBEIS/inspect_ai](https://github.com/UKGovernmentBEIS/inspect_ai),
[practitioner review](https://hamel.dev/notes/llm/evals/inspect.html).

## Why it does not fit — three reasons, in order of severity

**1. The abstraction is inverted relative to ours.** Inspect's provider layer
sits exactly where we put the FRAMEWORK. Its model is "one solver, many models";
ours is "one model held constant, many frameworks". A Solver in Inspect is
Python code that calls a provider. Our solver is a **subprocess in five
languages** — Go, Python, Node, Java — speaking a JSON contract over
stdin/stdout, precisely so that no adapter can be advantaged by living in the
harness's native language. Expressing that in Inspect means every adapter
becomes a Python shim around a subprocess, which is the harness we already have
plus a layer.

**2. Its scorers are model-based by default; ours are forbidden to be.**
Inspect's own guidance is that sophisticated evals "often require custom scorers
that use models to evaluate output". `.claude/rules/arena-deterministic-judging.md`
makes the PRIMARY score a pure function by construction — enforced by
`scripts/arena-determinism-guard.sh`, which proves purity and byte-identical
reproducibility across fresh processes. That rule exists because an LLM judge
rated our own family ~5.0 flat across frameworks that behave very differently.
Adopting a harness whose grain runs the other way would put continuous pressure
on the one rule we are most sure about.

**3. It would not have caught today's defects.** Every real finding of this
session came from provenance and isolation questions — was this capability the
framework's or the adapter's, was this cell measured this run or inherited, does
the store answer or replay. Those are properties of a multi-tenant integration,
not of a trajectory. A better trajectory model would have improved none of them.

## What to steal

**1. Solver/Scorer as a NAMED separation.** We have `scoring/` as a pure
package, which is the Scorer half done well. We have no first-class "Solver" —
the adapter-invocation logic is spread through `main.go`'s passes. Naming it
would make it obvious which code decides WHAT to ask and which decides WHETHER
the answer is right, a boundary we currently keep by convention.

**2. The transcript/log viewer, as a format not a dependency.** This is the
genuinely missing thing. `RESULTS.records.jsonl` holds one envelope per cell and
throws away the trajectory: we cannot answer "what exactly did the framework
send the model on turn 2" without re-running. Today that cost real time — the
`input_tokens: 8` clue that finally explained the ai-sdk failure was recovered by
hand from a reply string. Inspect's answer is a structured transcript per sample
plus a viewer over it. We should record the transcript; we do not need their
viewer to do it.

## Recommendation

Do not adopt. Add a `transcript` field to the durable record — the messages
actually sent and received per invocation — and keep everything else. That
closes the one gap that cost us today, at a fraction of the cost of an inverted
harness, and leaves the deterministic-scoring rule untouched.

Revisit if the arena ever grows a dimension where the trajectory IS the subject
(multi-step planning, self-correction loops), because that is where Inspect's
model genuinely beats ours.

## Related

- `docs/research/arena-prior-art-sweep-2026-07-28.md` — the sweep that raised it
- `.claude/rules/arena-deterministic-judging.md` — the rule reason #2 protects
- `docs/architecture/arena-integration-lane-ceiling.md`
