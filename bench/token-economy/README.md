# bench/token-economy

A reproducible, fully offline benchmark comparing **input-token consumption**
between two context-assembly strategies over the same synthetic multi-turn
conversation:

- **Strategy A — "aisotropy surgical context"**: drives the REAL
  `internal/memory.WorkingMemory` (bounded active window + condensed
  summary), exactly as `internal/api/chat_stream.go`'s `hydrateSystemPrompt`
  assembles it.
- **Strategy B — "classic LangChain-style flow"**: a full-history
  `ConversationBufferMemory`-style baseline — every prior turn is re-sent
  verbatim on every request, plus a representative default agent
  prompt-template overhead.

This harness exists because `docs/architecture/overview.md` claimed
"~70% fewer input tokens vs. a classic LangChain flow" with no benchmark
behind it. This is the first measurement against that claim. **No API key is
available in this environment (or required)** — everything here is
deterministic and runs offline.

## How to re-run

```sh
go run ./bench/token-economy                       # default: 50 turns, seed 42
go run ./bench/token-economy -turns 100 -seed 7     # custom depth/seed
go test ./bench/...                                  # correctness + determinism tests
go test -race ./bench/...                            # same, with the race detector
go test -bench . ./bench/token-economy/...           # timing benchmarks (not token counts)
```

Two consecutive runs with the same flags always print byte-identical output
(`TestRunDeterministicOutput` in `bench_test.go` asserts this).

## Methodology

### Synthetic conversation generator (`conversation.go`)

`GenerateConversation(cfg Config)` deterministically synthesizes `cfg.Turns`
user/assistant exchange pairs using `math/rand` seeded with `cfg.Seed` — no
wall-clock time, no global RNG state, no network or filesystem access.
Message text is built from a small fixed vocabulary of ~130 common
chat-support/product-question words (not lorem-ipsum), so word-count and
average-word-length statistics resemble real chat text. Default message-size
distribution (`DefaultConfig()`):

- User messages: 8–40 words (uniform)
- Assistant replies: 20–150 words (uniform, deliberately longer than user
  prompts, matching typical chat-agent transcripts)

Both turn count and word-count ranges are configurable via `Config`; turn
count and seed are also exposed as `-turns`/`-seed` CLI flags on
`go run ./bench/token-economy`.

### Strategy A: what we replicate vs. import

We **import and drive the real `internal/memory` package** — this is not a
re-implementation. Per turn, `RunStrategyA` (in `strategies.go`):

1. Calls the real `WorkingMemory.Summary()` and `WorkingMemory.Recent(12)`
   (12 = `recentMessagesWindow` in `internal/api/chat_stream.go`) — reading
   the actual condensed summary and actual bounded active-buffer window,
   BEFORE the new turn's user message is appended, matching the real
   handler's ordering.
2. Assembles the system prompt via `hydrateSystemPromptText`, which
   reproduces the literal algorithm of `internal/api/chat_stream.go`'s
   unexported `hydrateSystemPrompt` function (base prompt + optional summary
   block + optional recent-messages block, in that order, with the same
   literal separator text). We reproduce this ~15-line formatting function
   rather than the memory/condensation logic itself, because
   `hydrateSystemPrompt` is unexported and this package cannot import it
   directly without modifying `internal/api` (forbidden by this task's
   constraints). The condensation/window logic that actually matters for
   token economy — `WorkingMemory.Append`, `.Summary`, `.Recent`, and the
   `Summarizer` — is the real, unmodified code.
3. The measured "input to the LLM" for that request is
   `tokens(system prompt) + tokens(new user message)`, mirroring
   `client.StreamChat(ctx, system, in.Body.Prompt)` in the real handler.
4. Appends the user message, then (after `wm.Sync()`) the assistant's reply,
   to the real `WorkingMemory` — exactly as the real handler does once a
   stream completes successfully.

We use `memory.NoopSummarizer` (deterministic, no LLM call), not
`memory.LLMSummarizer`, because there is no API key in this environment and
a real LLM call would make the benchmark non-deterministic and
non-reproducible offline. `internal/memory`'s own test suite makes the same
choice for the same reason. **This is the single most consequential modeling
choice in this benchmark and is explicitly a limitation — see below.**

We call `wm.Sync()` after every `Append` to wait for any background
condensation goroutine it triggers, before the next turn reads
`Summary()`/`Recent()`. Production code (`internal/api/chat_stream.go`) does
**not** do this — condensation is deliberately asynchronous there so a
request is never blocked on it. We wait anyway purely so this benchmark's
output is deterministic: without it, whether a given turn observes a
condensation pass that happened to finish first would depend on goroutine
scheduling. This models **steady-state** memory-tier behavior, not exact
request-by-request production timing.

### Strategy B: template

`langchainTemplateOverhead` (`strategies.go`) is prepended, unchanged, to
every Strategy B request, ahead of the (verbatim, growing) resent history and
the new turn. It combines two well-known LangChain defaults:

- The preamble of LangChain's `ConversationChain` default prompt
  (`langchain.chains.conversation.prompt.DEFAULT_TEMPLATE`) — the canonical
  `ConversationBufferMemory`-style prompt this task asked us to baseline
  against.
- The `Thought`/`Action`/`Action Input`/`Observation` reasoning-format
  section of LangChain's classic `hwchase17/react` hub prompt (the
  best-known default ReAct agent template), **with its `{tools}` /
  `{tool_names}` sections removed**.

The tool-listing sections are deliberately omitted: this benchmark's
synthetic scenario has no tools on either side, and including a tool-listing
block (which Strategy A also does not pay for) would strawman Strategy B
with overhead neither system actually carries — the task explicitly asked us
not to do that. What remains — the reasoning-format scaffolding — is
overhead a production LangChain agent commonly carries even without tools
registered, so it is kept. This lands on the conservative-for-B (i.e.
harder-on-A's-percentage) end of "how much fixed overhead a real deployment
pays," per the task's instruction to favor B, not A, whenever a modeling
choice is ambiguous.

Measured cost of this template text alone (turn 1, before any history
exists): **211 tokens (bytes/4 heuristic) / 177 tokens (words×1.33
heuristic)** — squarely inside the requested ~200–400 token range.

Per turn, `RunStrategyB` (`strategies.go`) resends `langchainTemplateOverhead`
+ every prior turn verbatim as `"Human: ...\nAssistant: ...\n"` lines + the
new turn's `"Human: {user}\nAssistant:"` continuation — a direct,
unmodified `ConversationBufferMemory` transcript, with nothing removed or
paraphrased.

### Token counting methodology

There is no API access in this environment, so there is no real tokenizer to
call. Every token count in this benchmark is reported under **both** of two
independent, widely cited heuristics, side by side, so a reader can judge
sensitivity to the estimation method rather than being given one number that
looks more precise than it is:

- **`bytes/4`** (`EstimateTokensByBytes`): `ceil(len(text) / 4)`. "~4 bytes
  per token" is a commonly cited rule of thumb for English text under modern
  BPE tokenizers.
- **`words×1.33`** (`EstimateTokensByWords`): `ceil(word_count * 1.33)`.
  "~0.75 words per token" (i.e. ~1.33 tokens per word) is the other commonly
  cited rule of thumb for the same tokenizer family.

No new Go dependency was added for tokenization (zero new `go.mod` entries)
per the task's constraint to prefer the dual-heuristic approach over adding
a dependency.

## Measured results (50 turns, seed 42 — the tool's defaults)

Full per-turn table: run `go run ./bench/token-economy`. Headline numbers
from that run:

**A vs B input-token reduction, per-request cost at that turn:**

| Turn | bytes/4 heuristic | words×1.33 heuristic |
|-----:|-------------------:|----------------------:|
| 10   | 21.0%               | 22.3%                 |
| 25   | 46.9%               | 47.4%                 |
| 50   | 54.0%               | 54.7%                 |

**A vs B input-token reduction, cumulative total through that turn:**

| Turn | bytes/4 heuristic | words×1.33 heuristic |
|-----:|-------------------:|----------------------:|
| 10   | 21.6%               | 23.5%                 |
| 25   | 35.9%               | 36.7%                 |
| 50   | 47.7%               | 48.4%                 |

**Final per-request reduction at turn 50: bytes/4 = 54.0%, words×1.33 = 54.7%.**

### Is the ~70% claim supported?

**Partially supported, and this measurement lands meaningfully below it.**

- The *direction* of the claim is correct and grows stronger the longer the
  conversation runs: Strategy A is cheaper than Strategy B at every turn
  from turn 2 onward, and the gap widens monotonically from ~13-23% (turns
  6-11) to ~54% (turn 50) as Strategy B's unbounded full-history resend
  keeps compounding.
- The *magnitude* at turn 50 (54.0-54.7% per-request reduction) is
  substantially below the claimed ~70%, and there is no sign it is about to
  cross 70% soon — the reduction curve is flattening around turn 30-50
  (49-55%), not still accelerating.
- Two concrete reasons this measurement likely understates what a
  production deployment could achieve, both stemming from the same root
  cause (see Limitations): **`NoopSummarizer` never actually compresses.**
  It appends a new snippet to the summary on every condensation round
  instead of re-condensing to a bounded size, so Strategy A's own context
  grows roughly linearly too (see `TestNoopSummarizerSummaryGrowsUnbounded`)
  — just ~2-3x more slowly than Strategy B's, not flat. A real
  `LLMSummarizer` that re-compresses to a roughly constant-size paragraph
  each round would very plausibly close much more of the remaining gap,
  since Strategy A's context would then asymptote to a flat size instead of
  growing at all.
- At very short conversations (turns 1-7), Strategy A is sometimes **only
  marginally cheaper** than B (turn 7: 12.8%/14.3%) — the fixed system-prompt
  and window overhead dominate before B's history has grown large enough to
  differentiate. The "~70% fewer tokens" framing, if kept, should be
  explicit about being an asymptotic/long-conversation claim, not a
  per-request-at-any-depth one.

**Bottom line: the qualitative claim ("surgical context uses meaningfully
fewer input tokens than resending full history, and the advantage grows with
conversation length") is supported. The specific "~70%" figure is not
reproduced by this first measurement — this run tops out around 54-55% at
turn 50 under both heuristics — and should not be repeated as a validated
number until re-measured with a real (or better-modeled) summarizer.**

## Limitations

1. **Deterministic `NoopSummarizer`, not a real LLM summarizer.** This is
   the largest source of uncertainty in the headline numbers (see above). We
   measured it directly: `NoopSummarizer`'s summary block grows from 0
   tokens (turn 1) to ~3,338 tokens (bytes/4 heuristic, turn 50) — see
   `TestNoopSummarizerSummaryGrowsUnbounded`'s logged output. A real
   `LLMSummarizer` re-condensing to a bounded paragraph each round would
   likely keep Strategy A's context much flatter, which this benchmark
   cannot measure without an API key.
2. **Approximate tokenization.** Both heuristics (`bytes/4`, `words×1.33`)
   are rules of thumb, not the real BPE vocabulary of any specific model.
   The two heuristics agree with each other within ~1-2 percentage points
   throughout this run, which is some evidence the reduction percentages
   aren't wildly sensitive to the choice — but neither is validated against
   a real tokenizer.
3. **Single synthetic message-size distribution.** One seed, one word-count
   range, one vocabulary. Real conversations vary far more (code blocks,
   very long or very short turns, non-English text, structured data). The
   `Config` struct and `-seed`/`-turns` flags make it easy to explore other
   points, but this README reports only the tool's own defaults.
4. **No system-level overhead beyond the prompt text itself.** Neither
   strategy models tool-call schemas, structured-output instructions, or
   multi-modal content — both would add roughly comparable fixed overhead to
   both strategies and are out of scope here.
5. **`wm.Sync()` after every `Append`** models steady-state behavior, not
   exact production request timing (see "Strategy A" above) — a deliberate,
   documented simplification for determinism, not an attempt to make
   Strategy A look better or worse.
6. **Turn-count depths (10/25/50) are per-request snapshots**, not an
   average or percentile over many conversations — this is one deterministic
   run, not a distribution.

## Files

| File | Purpose |
|---|---|
| `conversation.go` | Deterministic synthetic-conversation generator |
| `tokens.go` | Dual-heuristic token estimation |
| `strategies.go` | Strategy A (real `internal/memory`) and Strategy B (LangChain-style baseline) |
| `main.go` | `go run ./bench/token-economy` CLI: table + summary output |
| `bench_test.go` | Determinism tests, characterization tests, `go test -bench` benchmarks |
