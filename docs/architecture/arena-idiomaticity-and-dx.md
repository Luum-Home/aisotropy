# Arena idiomaticity audit + a deterministic developer-experience metric

> Two linked questions about the live arena (`arenas/live/`):
> **Q1 — is each adapter's adoption of its framework's infrastructure the idiomatic,
> recommended pattern, or a hand-rolled deviation?**
> **Q2 — can developer experience / code simplicity be measured *deterministically*,
> alongside the arena's other benchmarks?**
> They are linked (see "The Q1↔Q2 bridge"): a fair DX metric requires the adapters to
> use native primitives where they exist. Grounded in a read-only research round
> (per-framework code-truth + official-doc web research) and the arena's
> deterministic-judging discipline.

## Q1 — Idiomaticity scorecard

Every adapter uses **real** infra in the real lane (see `arena-infrastructure-parity.md`).
This audit is orthogonal: is the *way* each tier is adopted the framework's
officially-recommended primitive, or hand-rolled glue around it?

Classifications: **NATIVE-IDIOMATIC** (uses the recommended primitive the recommended
way) · **DEVIATES-JUSTIFIED** (bypasses the native primitive for a real, documented
reason — a framework gap or corpus-schema mismatch) · **DEVIATES-SHOULD-FIX**
(hand-rolls something the framework ships natively, for no strong reason).

| Framework | Vector (RAG) | Embeddings | Memory | Verdict |
|---|---|---|---|---|
| **llamaindex** | `QdrantVectorStore` native | `OpenAILikeEmbedding` native | `Memory` native | ✅ fully idiomatic |
| **ai-sdk** | DIY (correct — thin SDK ships no store) | `embed()` native | DIY rqlite (SDK has no memory) | ✅ idiomatic-given-the-SDK |
| **pydantic-ai** | DIY via tool (idiomatic for PA) | ⚠️ raw `openai` SDK (native `Embedder` exists) | native message history | 🟡 nearly — embedder fix |
| **eino** | ⚠️ `qdrant-go` direct — `eino-ext` retriever hardcodes `content` payload key ≠ corpus `text` | native `eino-ext` embedder | on-disk SQLite | 🟡 DEVIATES-JUSTIFIED (verified corpus-schema mismatch) |
| **genkit-go** | 🟡 `go-client` direct — no **first-party** Go Qdrant plugin | ⚠️ raw `net/http` (`compat_oai` `DefineEmbedder` exists) | real rqlite | 🟡 justified vector + embedder fix |
| **langgraph** | `QdrantVectorStore` native | `OpenAIEmbeddings` native | ⚠️ **hand-rolled SQLite** (native `BaseStore` with namespaces + semantic search exists) | 🔴 SHOULD-FIX |
| **mastra** | ⚠️ raw `@qdrant/js-client-rest` (native `QdrantVector` exists) + `semanticRecall:false` | e5 via raw `fetch` | native libsql `Memory` | 🔴 SHOULD-FIX |

### Ranked SHOULD-FIX list (highest-value first)

1. **langgraph — scoped/facts memory → native `BaseStore`.** LangGraph's flagship
   long-term-memory primitive (namespaced `(tenant,user,product)`, semantic-search-capable)
   is exactly what the adapter hand-rolls in raw SQLite. This is the single highest-value
   fix. (The accompanying false "no multi-tenant primitive" doc claim was already corrected
   — P1, see `arena-infrastructure-parity.md` action items.)
   Docs: [Long-term memory / Store](https://docs.langchain.com/oss/python/langchain/long-term-memory),
   [Semantic search for LangGraph memory](https://www.langchain.com/blog/semantic-search-for-langgraph-memory).
2. **mastra — vector → native `QdrantVector` (`@mastra/qdrant`).** Official first-party
   vector store bypassed for the raw client; the swap restores Mastra's real RAG DX +
   telemetry and pairs naturally with `embedMany`. Docs:
   [Mastra Qdrant reference](https://mastra.ai/reference/vectors/qdrant),
   [Mastra vector DBs](https://mastra.ai/docs/rag/vector-databases).
3. **genkit-go & pydantic-ai — embeddings → native embedder.** genkit-go: register e5 via
   `compat_oai` `DefineEmbedder(WithBaseURL)` instead of raw `net/http` (verify the known
   base-url rough edges first, [genkit#3583](https://github.com/firebase/genkit/issues/3583)).
   pydantic-ai: swap the raw `openai` SDK for native `pydantic_ai.Embedder`
   ([PA embeddings](https://pydantic.dev/docs/ai/guides/embeddings/)) — but confirm the pinned
   PA version ships it (the API is recent; if it postdated the adapter, the raw choice was
   justified *then*).

**Justified deviations (do NOT "fix"):** eino Qdrant (the `eino-ext` retriever hardcodes
`content`/`metadata` payload keys, verified in source, mismatching the corpus's `text` key —
using it would silently return empty content); mastra `semanticRecall:false` (the `memory`
task grades durable last-N recall, not semantic recall — enabling it would wire an untested
dimension); genkit-go vector (no first-party Go plugin; the third-party `qdrant/qdrant-genkit`
Go retriever exists but carries maintenance/version risk and needs an `ai.Embedder`); all
hand-rolled tenant/user/product scoped stores (no framework ships a tri-scope isolation
primitive); every ai-sdk DIY tier (thin SDK by design).

### The Q1↔Q2 bridge

A fair DX metric **requires** the adapters to use native primitives where they exist. If you
measure "lines of code to implement RAG" but langgraph hand-rolled a store instead of using
its native `BaseStore`, you penalize langgraph for an *adapter* decision, not a *framework*
property. So **landing the Q1 SHOULD-FIX items is a prerequisite for an honest Q2 measurement**
— otherwise the DX numbers measure adapter authorship, not framework DX.

## Q2 — A deterministic DX / simplicity dimension (`ScoreDX`)

Yes — DX/simplicity can be measured deterministically, and it fits the arena's
deterministic-judging rule (`.claude/rules/arena-deterministic-judging.md`) exactly: pure
functions over static files (source, manifests), byte-identical across runs, no LLM / network
/ randomness / wall-clock.

### The arena's fairness asset

Every adapter implements the **same stdin/stdout contract** across the same modes. So *"how
much code / config / dependencies each framework needs to do the identical job"* is a fair,
deterministic DX proxy — the same methodology as the RealWorld/Conduit and TodoMVC
"same-spec, N-implementations" framework comparisons (which rank on LOC-to-implement-the-spec,
dependency count, and bundle size, with an idiomatic-review clause as the anti-gaming guard —
exactly our Q1 audit).

### Established deterministic metrics (research survey)

- **Composite:** Maintainability Index (Halstead volume + cyclomatic complexity + LOC).
  **Deliberately NOT used as a cross-language primary** — its regression constants were fit on
  1990s C/Pascal and don't transfer across languages ([van Deursen, "Think Twice Before Using
  the Maintainability Index"](https://avandeursen.com/2014/08/29/think-twice-before-using-the-maintainability-index/)).
- **Complexity:** cyclomatic (McCabe, 1976) and cognitive complexity ([SonarSource
  whitepaper](https://www.sonarsource.com/docs/CognitiveComplexity.pdf)) — both need a
  per-language AST parser; usable **within a language only**.
- **Size:** SLOC (physical non-blank/non-comment). Tools: `cloc`, `tokei`, `scc` (pure-Go, also
  emits a branch-keyword complexity estimate). Documented pitfalls: gameable (duplication
  inflates, refactor-deletes deflate), verbosity ≠ complexity, cross-language SLOC needs
  adjustment factors ([boyter/scc](https://github.com/boyter/scc),
  [GetDX — LOC falls short](https://getdx.com/blog/lines-of-code/)).
- **Surface:** direct-dependency count and config/env-var count — the **most cross-language-fair**
  signals (a dep is a dep, an env var is an env var; parsed from declarative manifests).

**Critical environment finding:** none of `cloc/tokei/scc/gocyclo/radon/eslint` is installed,
and pinning an external counter's version forever is infeasible (a version bump silently breaks
byte-identical reproducibility). **So every sub-metric is computed in pure Go stdlib**, reusing
the runner-does-I/O → pure-scorer seam (`eval.go`'s `scoreDeterministic` pattern).

### The proposed sub-metrics (each a pure function over static files)

Following the arena convention (see `ScoreStream`), **`Pass` means "was it measured," never an
arbitrary threshold** — the comparative signal is the raw value + within-language rank +
ratio-to-baseline rendered in RESULTS.md, never a hand-picked cutoff.

1. **Contract-SLOC** — implementation source only (`*.go` excl. `_test.go`, `*.py`,
   `*.mjs/js`, `*.java`), excluding `node_modules/`, `testdata/`, `.venv/`, manifests, READMEs,
   `run*.sh`, `Dockerfile`, compiled binaries. Pure per-language comment/blank classifier.
   Reported as raw **and** `SLOC / dimensions-implemented` (per-capability normalization).
   *Proxy for:* verbosity to implement the identical contract.
2. **Direct-dependency count** — `go.mod` `require` minus `// indirect`; `requirements.txt`
   non-comment lines; `package.json` `dependencies` (excl. `devDependencies`). Language-neutral.
   *Proxy for:* third-party surface / maintenance burden.
3. **Config / env-var surface** — a declared `env: [...]` list in `matrix.json` (additive
   field), cross-checked by a deterministic regex count of `os.Getenv`/`process.env`/`os.environ`
   call sites; a mismatch is surfaced, not silently reconciled. Language-neutral.
   *Proxy for:* configuration/onboarding simplicity.
4. **Native-primitive coverage** — `native / (native + hand-rolled)` tiers, declared per-dimension
   in `matrix.json` (`native: bool` + mandatory `native_note` reason when false), transcribing
   the READMEs' already-reviewed native-vs-hand-rolled facts. **The richest DX signal** (quality,
   not quantity) and the direct nexus with Q1 — and the metric where aisotropy honestly looks
   good (native tiered memory + native isolation).
5. **Setup-SLOC** — `run.sh` + `Dockerfile` non-comment lines. *Proxy for:* time-to-first-run.
6. *(optional, SECONDARY, clearly caveated)* branch-keyword complexity (scc-style, cross-language
   uniform, tokenizer-free) + a Go-only true-AST cyclomatic/cognitive column via stdlib `go/ast`
   — never ranked across languages.

### Cross-language fairness (explicit)

The composite view is **grouped by language**; the headline is **never a raw cross-language LOC
rank**. Per sub-metric, report: raw value (with its language), **within-language rank** (the
primary comparative signal), and **ratio-to-language-baseline** (value ÷ the simplest
same-language adapter). Dependency-count and env-var count are the **only** two shown in a single
cross-language table without a caveat (genuinely language-neutral). aisotropy additionally gets
**per-capability (LOC-per-dimension)** normalization because it implements strictly more
dimensions (wasm sandbox, crash-recovery, isolation, observability, middleware) than the
networked frameworks.

### Mandatory caveats (rendered at the top of the RESULTS.md section)

1. These are **PROXIES** for DX, not DX — no static metric captures how it *feels* to build on a
   framework.
2. The arena adapter is **not a real app** — it implements a narrow contract; the numbers don't
   generalize.
3. Hand-rolling inflates LOC — and unevenly: aisotropy also hand-rolls where doing so adds a
   thesis capability. Sub-metric #4 exists so "more code because you built a real capability"
   isn't conflated with "more code because the framework made you."
4. **aisotropy does more** (self-ingest + OS pieces) → its totals are normalized per-capability
   and its extra dimensions listed; raw totals are never its headline.
5. LOC is gameable; verbosity ≠ complexity; cross-language totals need adjustment → within-language
   ranking is primary.
6. No per-item judgment, no threshold-picking — `Pass` means "measured," like `ScoreStream`.

### How it slots into the three enforced pieces

- **Scoring (`arenas/live/runner/scoring/dx.go`)** — pure `ScoreDX*` functions returning
  `Score{Value,Pass,Detail}`, importing only the already-allowed stdlib set. The extractor
  (file walking, I/O) lives in the runner (`runner/dx.go`), NOT in the pure package —
  mirroring `eval.go`. No new forbidden imports → the purity guard stays green untouched.
- **Determinism guard** — add one fixture per new scorer to `TestDeterministic_Reproducible`
  (so `scripts/arena-determinism-guard.sh` proves byte-identical at 100×/`-count=5`), plus a
  separate `runner/dx_test.go` that runs the extractor twice over a frozen `testdata/` adapter
  tree and asserts identical `DXInputs`.
- **RESULTS.md** — a new PRIMARY-but-explicitly-PROXY "Developer experience / simplicity"
  section beside the correctness table, opening with the six caveats, then a language-neutral
  table (deps, env-vars), within-language tables (contract-SLOC ± per-dimension + ratio,
  setup-SLOC), and the native-coverage table with each hand-rolled tier's reason inline.
  Secondary AST/branch-keyword columns stay in a labeled subsection, never merged into primary.
- **matrix.json** — additive fields `native: bool` + `native_note: string` per dimension, and an
  adapter-level `env: [...]` list; documented next to the existing `pass|fail|infeasible|pending`
  vocabulary.

### Honest uncertainties

- The pure-Go SLOC classifier will disagree slightly with `cloc`/`scc` on edge cases (block
  strings, heredocs) — it is pinned by its own fixture and described as "the arena's SLOC
  definition," not "canonical SLOC."
- The `native: bool` flag is a human judgment (encoded like `passQAThreshold`) and needs the same
  adversarial re-verification the prune-evidence gate demands before shipping.
- Transitive-dependency count is left out of the primary set — lockfile resolution is
  environment-sensitive and not cleanly reproducible across all three ecosystems.

## Sources

Idiomaticity per-framework docs (LangGraph Store/semantic-search, Mastra Qdrant/vector,
Genkit `compat_oai`/qdrant-genkit-go, Pydantic-AI embeddings, eino-ext retriever, LlamaIndex
Qdrant, Vercel AI SDK embeddings) and DX-metric literature (RealWorld/Conduit, TodoMVC,
Maintainability Index + van Deursen critique, SonarSource cognitive complexity, boyter/scc,
GetDX) are cited inline above and captured in the session research transcript.
