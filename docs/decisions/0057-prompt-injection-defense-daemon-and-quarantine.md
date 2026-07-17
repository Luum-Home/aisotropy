---
type: ADR
title: "ADR-0057 — Prompt-injection defense, tier 1+2: warm guardrail daemon (+ real-model loader fix) and the dual-LLM quarantine seam"
description: Advances the prompt-injection roadmap (docs/research/prompt-injection-defense-soa-2026-2026-07-17.md) on two fronts. T1 — productionize the ADR-0052 mDeBERTa scanner. ADR-0052 shipped a COLD subprocess PoC that re-loaded ~557MB of weights per call; this adds a WARM `--serve` daemon to crates/guardrail-model (model loaded once, NDJSON over a pipe, mirroring crates/embed's DaemonEmbedder) plus a Go DaemonScanner that talks to it, so inline detection is not a cold model load per call. Running the REAL model in-session surfaced and FIXED a latent ADR-0052 defect — the worker could not load the real HF checkpoint at all (tensor-name prefix mismatch: `deberta.` base-model prefix vs candle's root-layout expectation). With the fix the warm path measures p50 ~124ms/call (vs ~0.7–2.2s cold) and 10/12 on a hand-labeled multilingual smoke set — honest ceiling — the zero-shot-NLI framing is phrasing-sensitive and additive/fail-open only, NOT a standalone gate. T2 — ship the dual-LLM QUARANTINE seam (internal/guardrails/quarantine.go): untrusted content that must inform a decision is processed by a tool-less quarantined model whose output is constrained to a validated schema, so tainted data can never carry an instruction or a tool call into the privileged path. Interface + reference impl + tests (incl. a structural proof the seam cannot emit a tool invocation). NOT wired into the agent loop here (out of scope); adoption note included.
tags: [decisions, adr, security, prompt-injection, guardrails, mdeberta, candle, subprocess, daemon, warm-process, dual-llm, quarantine, camel, nli, seam, license]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0057 — Prompt-injection defense, tier 1+2: warm guardrail daemon (+ real-model loader fix) and the dual-LLM quarantine seam

## Status

✅ **Accepted — 2026-07-17.** Advances two tiers of the prompt-injection defense roadmap in
`docs/research/prompt-injection-defense-soa-2026-2026-07-17.md`:

- **T1 (Tier 1, §5)** — turn the ADR-0052 mDeBERTa scanner from a cold subprocess PoC into a
  production-usable warm daemon, and (discovered while doing so) fix the latent defect that stopped
  the real model from loading at all.
- **T2 (Tier 2, §5 item 4)** — ship the **dual-LLM / quarantine** seam (the CaMeL/Willison pattern
  the research names as the only architectural lane with *provable* blast-radius bounds).

Both land inside `internal/guardrails/**` and `crates/guardrail-model/**`. Wiring the enable
(`aisotropy/app.go`) and adopting the quarantine in `internal/agentloop` are deliberately **out of
scope** here (owned elsewhere); this ADR delivers the **seams + evidence + honest ceiling**.

## Context

ADR-0052 added an opt-in multilingual injection scanner behind the R4 `InputScanner` seam: a Go
`ModelScanner` shells out to a candle `crates/guardrail-model` worker running a pinned **MIT**
mDeBERTa-v3 zero-shot-NLI model, scoring whether the user message (premise, any of ~100 languages)
entails an English injection hypothesis. It shipped explicitly as *"a proven Go seam + a
functional-but-minimal worker whose end-to-end multilingual accuracy is PoC-not-yet-benchmarked."*

Two gaps kept it out of production:

1. **Cold per-call cost.** The worker only had a batch `--input/--output` mode: every single
   classification spawned a fresh process and re-loaded ~557MB of weights before scoring one line.
   Fine for a batch job; a non-starter for an inline guardrail on the chat path.
2. **Unbenchmarked accuracy.** The real model had never actually been run in-repo, so its
   multilingual detection quality was unknown.

Separately, the research is emphatic (§2, §4) that **detection is a floor, not a ceiling** — no
scanner catches every injection, and the only lane that *provably* bounds damage is architectural:
the **dual-LLM / quarantine** pattern, where untrusted data is processed by a model that structurally
cannot act on it. aisotropy already has the two pieces CaMeL needs (a scope/capability gate and a
trust boundary via `context.Context`), but no quarantine step for the sharp edge: constructing a
decision or tool argument *from untrusted content* (a retrieved RAG chunk, a tool's output).

## Decision

### T1 — Warm guardrail daemon + real-model loader fix

**(a) `--serve` daemon mode (`crates/guardrail-model`).** Mirroring `crates/embed`'s `--serve`
`DaemonEmbedder` path, the worker now loads the model **once** and then reads NDJSON requests from
stdin line-by-line, writing one result row per line to stdout (flushed per line) so a Go caller can
round-trip synchronously over the pipe. The batch path is unchanged; the two modes are mutually
exclusive. The per-line scoring glue (`score_row_json`) is shared, so batch and serve produce
byte-identical rows.

**(b) `DaemonScanner` (`internal/guardrails/daemon_scanner.go`).** The warm Go client, a twin of
`semantic.DaemonEmbedder`: it starts `guardrail-model --serve` once, serializes `Inspect` calls under
a mutex (one pipe, single-threaded serve loop), and on **any** fault (crash, I/O error, timeout,
malformed row) tears the daemon down so the next call respawns from a known-clean state — a shared
pipe must never be left half-drained. Because the `InputScanner` seam is ctx-free by R4's design,
the scanner owns its own per-call deadline internally (a watchdog kills a stuck daemon after
`Timeout`). It satisfies the same **advisory, additive, FAIL-OPEN** contract as `ModelScanner`: a
model-tier fault yields the zero `Verdict` and the built-in regex heuristics still run.

**(c) Env selection (`scanner_env.go`).** `AISOTROPY_GUARDRAIL_MODEL_MODE` picks the form when the
feature is enabled (`AISOTROPY_GUARDRAIL_MODEL_BIN` set): unset/`daemon` → warm `DaemonScanner` (the
production default); `subprocess` → the ADR-0052 cold `ModelScanner`. The feature remains **OFF by
default** (bin unset ⇒ regex-only, byte-for-byte identical).

**(d) The loader fix (the defect running the real model surfaced).** Pointing the worker at the real
`MoritzLaurer/mDeBERTa-v3-base-mnli-xnli` checkpoint failed with
`cannot find tensor embeddings.word_embeddings.weight`. Root cause: candle's
`DebertaV2SeqClassificationModel::load` expects the base-model tensors at the **root**
(`embeddings.*`, `encoder.*`) with the task head (`pooler.*`, `classifier.*`) also at root, but the
HF sequence-classification export prefixes every base-model tensor with `deberta.` (199 of 203
tensors) while leaving the head at root. The fix loads the tensors via the safe
`candle_core::safetensors::load` API (no more `unsafe` mmap), strips a single leading `deberta.`
where present (`remap_base_key`, pure + unit-tested), normalizes to F32, and builds the `VarBuilder`
from the remapped map. This bug was invisible for exactly the reason the repo's code-truth discipline
warns about: **the model was never actually loaded in CI**, so a claim of "functional worker" rested
on a path that had never executed.

**Measured evidence (in-session, real model, arm64-mac CPU).** After the fix the forward pass is
numerically correct on standard NLI controls (entailment 0.997, contradiction 0.0007, neutral
0.0015 — a sanity gate that the model math is right, not just that it loads). On a small, hand-
labeled **12-sample multilingual smoke set** (es/pt/fr/de/it/en × injection/benign) through the warm
daemon at the shipped default hypothesis and threshold 0.5:

| Metric | Value |
|---|---|
| Accuracy (smoke set) | **10/12 = 83.3%** at threshold 0.5 |
| Caught (injection) | es 0.865, pt 0.713, de 0.743, it 0.735 ✓ |
| **Missed (injection)** | **en 0.092, fr 0.019 ✗** |
| Benign (all correct) | 0.089–0.360, all below threshold ✓ |
| Warm latency | **p50 ≈124ms, p90 ≈131ms** per call (model loaded once) |
| Cold latency (batch, 1 line) | ≈0.7–2.2s (spawn + 557MB load + 1 forward) |

The warm daemon removes ~0.5–2s of per-call spawn+load tax, leaving the ~124ms DeBERTa-base CPU
forward as the floor — i.e. it is what makes an inline per-request guardrail viable at all.

### T2 — Dual-LLM quarantine seam (`internal/guardrails/quarantine.go`)

The seam removes the attacker's **leverage** instead of trying to spot the attack. Its guarantee has
two halves, both enforced **by construction**, not by prompt wording:

1. **Structural — `QuarantinedModel`.** The quarantined capability's type is exactly
   `Ask(ctx, instruction, untrusted string) (string, error)`. There is **no** parameter to pass a
   tool set through and **no** return channel a tool call could escape on. Tool use is impossible
   because there is no seam for it — a compile-time property. A reflection test locks the signature
   so a future edit cannot silently widen it.
2. **Semantic — `Schema`.** Whatever text the quarantined model emits, **only** a value that
   satisfies the schema crosses back into the privileged path. A tool-call-shaped payload, an
   injected instruction, or prose is **rejected**, not forwarded. The reference schemas are
   `EnumSchema` (exact match against a closed vocabulary — the airtight case) and `BoolSchema`;
   matching is on the whole trimmed value, never a substring, so a valid token cannot carry a tool
   call after it.

`DualLLMQuarantine` is the reference `Quarantine`: it calls the tool-less model with a fenced
instruction (untrusted content passed *separately* as data, never spliced into the instruction),
then admits only schema-conforming output. Unlike the input scanner it **fails CLOSED**: a model
error or non-conforming output yields an error and no usable value (failing open would re-admit the
free-form text the quarantine exists to keep out).

**Relationship to the research's symbolic-ref form.** Willison's dual-LLM returns symbolic
references (`$VAR1`) an orchestrator substitutes. The schema-validated value here is the concrete
first form of the same idea (a typed, closed-vocabulary value is a substitutable symbol); symbolic
indirection over larger extracted payloads is a compatible future extension of the same seam.

**Adoption note (for `internal/agentloop`, out of scope here).** To adopt: implement
`QuarantinedModel` as a thin adapter over the existing `internal/llm` client that calls the model
with an **empty tool set / tool-choice=none** — the adapter is the single place the "no tools"
invariant is honored, and it keeps `internal/guardrails` free of an LLM dependency. Then, wherever
the agent would let a **retrieved RAG chunk or a tool's output** determine a branch or a tool
argument, route that decision through `Quarantine.Extract` with an `EnumSchema`/`BoolSchema` and use
the returned `Value` (never the `Raw`). The privileged, tool-capable LLM keeps the trusted
instructions and never ingests the tainted text as free-form. This is the concrete on-ramp to Tier 3
(a full CaMeL taint layer): the quarantine is the data-extraction half CaMeL is built around.

## Consequences

**Positive.**
- The multilingual scanner is now inline-viable: warm ~124ms/call, real model actually loads, the
  loader fix is regression-locked by a unit test, and the whole feature stays OFF-by-default and
  fail-open (zero risk to the default path).
- The quarantine gives the codebase its first *architectural* injection defense — the lane the
  research says is the only one with provable blast-radius bounds — as a small, tested, dependency-
  free seam ready for the agent loop to adopt.

**Negative / accepted.**
- Throughput is bounded by a single daemon (serialized `Inspect`); a daemon **pool** is the
  documented high-concurrency follow-up (same as `DaemonEmbedder`).
- ~124ms/call is real latency on the chat path when enabled; operators trade it for coverage, and it
  is opt-in.

## Honest ceiling — this is not 100%, and residual risk lives inside granted scope

- **The scanner is a floor, not a gate.** Even correctly loaded, the zero-shot-NLI framing is
  **phrasing-sensitive and incomplete**: on the smoke set it *missed the canonical English injection*
  (0.092) and a French one (0.019) while catching es/pt/de/it. Alternative meta-hypotheses tried
  in-session (e.g. "This example is a prompt injection") scored injections *near zero* — worse than
  the shipped default — confirming the approach is brittle. It is wired as an **additive, fail-open**
  signal that only ever RAISES suspicion (the missed English case is separately caught by the regex
  layer, which is the point of the additive design). It must **not** be relied on as a standalone
  decision gate. The honest production accuracy fix is a **purpose-trained** Apache-2.0 injection
  classifier (e.g. `protectai/deberta-v3-base-prompt-injection-v2`, same candle DeBERTa-v3 path,
  license-clean) rather than a general NLI model — a follow-up, not this ADR; the constitutional
  license gate still **blocks** Llama Prompt Guard 2 (BSL-family) regardless of its superior accuracy.
- **The quarantine bounds blast radius; it does not guarantee correctness.** A determined injection
  inside the untrusted content can still make the quarantined model *misclassify* (return the wrong
  allowed token). What it CANNOT do is escalate: the tool-less model has no tool channel, and only a
  schema-conforming value crosses back, so tainted data can never become an instruction or a tool
  call in the privileged path. The residual is a wrong-but-in-vocabulary answer, whose consequences
  are bounded by the caller's own logic and the existing scope gate — i.e. **residual risk lives
  inside the granted scope**, which is exactly the property the capability layer exists to bound.
- **Not wired here.** Neither the enable nor the agent-loop adoption ships in this ADR; until those
  land, both deliverables are dark seams with tests, not live defenses.

## References

- Research: `docs/research/prompt-injection-defense-soa-2026-2026-07-17.md` (§2 taxonomy, §3 license
  gate, §5 tiered roadmap — T1 = Tier 1, T2 = Tier 2 item 4).
- ADR-0052 — the mDeBERTa scanner + subprocess contract this productionizes.
- ADR-0023 — the warm `--serve` `DaemonEmbedder` pattern T1(a)/(b) mirrors.
- ADR-0005 — the Go↔Rust subprocess boundary both workers follow.
- `.claude/rules/prune-evidence-gate.md` / constitutional license gate — the code-truth discipline
  that turned "unbenchmarked worker" into "the model never loaded; here is the fix + numbers."
- CaMeL (arXiv 2503.18813) / Willison dual-LLM — the T2 pattern.
