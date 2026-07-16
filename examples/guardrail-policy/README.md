# guardrail-policy — plug policy at the seam, not around the client

A runnable, **keyless** demo of the right place to attach answer-time policy in an
Aisotropy runtime: the [`WithGuardrailCheck`](../../aisotropy/extend.go) output
seam (ADR-0033) — instead of wrapping the LLM client via `WithLLM` and
re-implementing streaming, usage accounting and cancellation just to get an
inspection hook.

> **Dev/demo only — not for production.** The checks here are deliberately crude
> heuristics (a regex intent guess, a map-backed claim lookup). They exist to make
> the *seam and its wiring* concrete, not to be a real policy engine.

## What it shows

The program registers **two output checks in one `WithGuardrailCheck` call**. Each
`OutputCheck` receives `{Input, Output, SystemPrompt}`, so a single seam carries
both input-facing and output-facing policy:

| Check | Reads | Role |
|---|---|---|
| `intent-classifier` | `in.Input` | Input-facing: classifies the request and **flags** an input that carries a secret-looking token. |
| `grounded-output` | `in.Output` | Output-facing: the **grounded-output** pattern — flags a claim the tenant's ingested corpus does not support (the generic core of "approved-claims validation"). |

A flagged verdict, with `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT=true` (the program sets
it), makes the non-streaming `App.Turn` path substitute a safe refusal — the
visible effect of a check firing.

## Run it

```sh
cd examples/guardrail-policy
go run .
```

No API key and no Docker required: with `ANTHROPIC_API_KEY` unset the App uses the
built-in stub LLM. Expected shape:

- **turn 1** — `The Eiffel Tower is in Paris.` → both checks pass, reply returned unchanged.
- **turn 2** — `The Eiffel Tower is in Berlin.` → `grounded-output` flags `berlin` → safe refusal.
- **turn 3** — `Please store my api key: sk-demo-abc123` → `intent-classifier` flags the input secret → safe refusal.

The `[intent-classifier]` / `[grounded-output]` log lines are the checks running
*inside* the runtime's output inspector — reached through the seam, with no client
wrapper anywhere in this program.

## Honesty notes

- **Keyless stub.** The stub's structured reply deterministically embeds your
  prompt text, which is what lets `grounded-output` inspect controllable output
  without a real model. A real provider changes nothing about where the seam fires.
- **This is the OUTPUT, advisory stage.** The seam flags/refuses *after*
  generation; it does not mutate the model's input. Input **redaction** and
  prompt-injection detection are separate, **built-in** stages (the runtime's
  `Redactor` + injection `Detector`) — you do not wrap the client for those either.
- **Public façade only — no `internal/` import.** The `Check` signature uses
  `aisotropy.GuardrailCheckInput` / `aisotropy.GuardrailVerdict` (re-exported on the
  public façade), so this example implements the seam against
  `github.com/Luum-Home/aisotropy/aisotropy` alone — exactly what a fully external
  module would do. That is the proof the seam is a real public extension point, not
  an in-tree-only one.

## See also

- [docs/guides/extending-the-pipeline.md](../../docs/guides/extending-the-pipeline.md) — the "plug policy at the seam, don't wrap the client" guide.
- [aisotropy/extend.go](../../aisotropy/extend.go) — every `WithX` extension seam and its wiring status.
