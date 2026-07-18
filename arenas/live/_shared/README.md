# Live arena — shared real-RAG foundation (`_shared/`)

This directory is the **shared, real-infrastructure foundation** every live-arena
framework adapter builds on to do **real** retrieval-augmented generation. Nothing
here is stubbed or mocked:

- a **real** OpenAI-compatible `/embeddings` server backed by the actual
  `intfloat/multilingual-e5-small` model (384-dim) — the same embedder aisotropy
  uses (see `internal/semantic`, ADR-0049/0054);
- a **real** RAG corpus about a fictional company (**Nimbus Rail**) the base chat
  model cannot already know, so a framework only answers correctly if its retrieval
  actually works;
- a **real** Qdrant collection per framework (`arena_rag_<framework>`) populated
  with those docs' embeddings — one collection per adapter, so frameworks never
  bleed into each other's vectors (see §3);
- a **real** question set including trap questions to catch hallucination.

The shared **chat** model for the arena is `agnes-2.0-flash` (OpenAI-compatible,
reached via the repo-root `OPENAI_*` env — see `../SPEC.md`). agnes has **no
embeddings endpoint**, which is exactly why this embed server exists.

---

## 1. The embed server

### Contract

OpenAI-compatible embeddings. Start it on `127.0.0.1:8899`:

```bash
./start-embed-server.sh          # foreground (add & for background)
```

It creates an isolated venv under `embed-server/.venv` (prefers Python 3.13 for
stable torch wheels), installs deps, downloads the e5 weights once (~120 MB), and
serves:

| Method & path | Purpose |
|---|---|
| `POST /embeddings` (also `/v1/embeddings`) | OpenAI-shape embeddings |
| `GET /health` (also `/`) | `{"status":"ok","model":...,"dim":384}` |

Request:

```json
{ "model": "multilingual-e5-small-query", "input": "How late before a refund?" }
```

Response (OpenAI shape):

```json
{
  "object": "list",
  "data": [{ "object": "embedding", "index": 0, "embedding": [/* 384 floats */] }],
  "model": "multilingual-e5-small-query",
  "usage": { "prompt_tokens": 8, "total_tokens": 8 }
}
```

`input` may be a string or a list of strings. Vectors are L2-normalized (unit
length), so Qdrant `Cosine` and dot product agree.

### e5 prefixes — the part you must get right

e5 is trained with instruction prefixes and degrades without them. A search only
works when **both** sides used their correct prefix:

| Role | Prefix | Use it for |
|---|---|---|
| query | `query: ` | the search text you embed to search **with** |
| passage | `passage: ` | the corpus documents you **index** |

The server picks the prefix in this precedence order:

1. explicit request field `"input_type": "query" | "passage" | "document"`;
2. model-id suffix — `...-query` → query, `...-passage` → passage;
3. default → `query:` (and it logs a warning to STDERR).

So an adapter has two equivalent ways to choose the role:

```jsonc
// index side (documents)
{ "model": "multilingual-e5-small-passage", "input": ["passage text ..."] }
// search side (queries)
{ "model": "multilingual-e5-small", "input": "user question", "input_type": "query" }
```

The corpus in Qdrant was indexed with **`passage:`**; adapters MUST embed their
search queries with **`query:`**. Mixing them up silently wrecks recall.

---

## 2. The RAG corpus (`corpus/`)

22 short, titled markdown passages about **Nimbus Rail**, a fictional maglev
transit operator in the fictional city of Vellmoor. Every fact (products like the
Vireo T7 trainset and Cascade signaling, policies like the Blue Guarantee refund
and Frostbrake winter protocol, people like CEO Dr. Imara Sowande, and specific
numbers) is invented and internally consistent. The base model cannot know any of
it, so a question about Nimbus Rail is answerable **only** from the corpus — a
framework without working retrieval must hallucinate or refuse, and one with real
retrieval answers correctly. Each file's first line is a `# Title`; the whole file
is the passage.

## 3. The Qdrant collection(s) (`arena_rag_<framework>`, one per adapter)

| Property | Value |
|---|---|
| Name | `arena_rag_<framework>` (e.g. `arena_rag_eino`, `arena_rag_langgraph`) — see below |
| Vector size | 384 |
| Distance | Cosine |
| Points | one per corpus doc (22) |
| Payload | `{ "doc_id": "NN-slug.md", "title": "...", "text": "full passage" }` |

**Collision fix (fairness).** The live-arena runner (`arenas/live/runner/main.go`
`frameworkCollectionEnv`) sets a UNIQUE `QDRANT_COLLECTION` per framework —
`arena_rag_<framework>` — so multiple adapters sharing one real Qdrant instance
never read/write each other's vectors (6 adapters previously defaulted to the
SAME literal `arena_rag` collection). The aisotropy reference adapter is exempt:
it self-ingests the corpus into its own tiered memory at runtime rather than
reading a pre-populated collection (see `arenas/live/aisotropy/README.md`).

Qdrant runs locally at `http://localhost:6333` (REST) / `6334` (gRPC). Populate
it (idempotent — only ever touches the collection(s) it targets, never others):

```bash
python3 populate-qdrant.py        # needs the embed server running
# default (no override): seeds arena_rag_<fw> for EVERY discovered adapter
# under arenas/live/*/run.sh (matching what the runner actually points each
# adapter at) — ai-sdk, eino, genkit-go, langgraph, llamaindex, mastra,
# pydantic-ai. The corpus is embedded ONCE and reused across collections.

ARENA_COLLECTION=arena_rag python3 populate-qdrant.py   # legacy/manual/smoke-test
# mode: seeds exactly the ONE named collection (also honors QDRANT_COLLECTION).
```

Verify (per collection, e.g. eino):

```bash
curl -s localhost:6333/collections/arena_rag_eino | python3 -m json.tool
# result.points_count == 22
```

## 4. The question set (`rag_questions.jsonl`)

One JSON object per line:

```json
{"id":"q1","question":"...","reference":"...","supporting_docs":["08-blue-guarantee.md"],"trap":false}
```

9 questions total: 7 answerable **only** from the corpus (each names its supporting
doc(s)), plus **2 trap questions** (`"trap": true`, empty `supporting_docs`) whose
answers are **not** in the corpus. Correct behaviour on a trap is to say the
information isn't available — a framework that invents an answer is hallucinating,
which is exactly what the traps exist to expose.

## 5. How a framework adapter should do RAG

Given the arena's uniform STDIN→STDOUT contract (see `../SPEC.md`), an adapter that
wants real RAG does this per user question:

1. **Embed the query** — `POST 127.0.0.1:8899/embeddings` with
   `{"model":"multilingual-e5-small-query","input": <question>}` → 384-dim vector.
2. **Retrieve** — `POST localhost:6333/collections/arena_rag/points/search` with
   that vector, `limit: 3–5`, `with_payload: true` → top-k passages.
3. **Augment** — stuff the retrieved `payload.text` passages into the prompt as
   grounding context, with an instruction to answer only from that context and to
   say "I don't know" if the context doesn't contain the answer (this is what makes
   the traps catch hallucination).
4. **Answer** — call the shared chat model `agnes-2.0-flash` via the `OPENAI_*` env
   with the augmented prompt; return its text as the adapter's `reply`.

The end-to-end retrieval half is demonstrated by:

```bash
python3 smoke-rag.py              # embed query -> Qdrant search -> top-1 title per question
```

## 6. Memory-persistence & tool-execution conventions

Per-framework agents in this arena follow two conventions so the comparison is
honest:

- **Real, persisted memory — never in-memory only.** A framework's conversational
  / long-term memory must live in a real store that survives process restart: the
  served **Qdrant** at `localhost:6333` (semantic memory / RAG), the networked
  **rqlite** at `localhost:4001` (facts/journal, SQLite+FTS5), or a real on-disk
  file the framework owns. An adapter that keeps state only in a process variable
  and loses it on exit does **not** satisfy the memory dimension — the whole point
  is to measure real persistence, not a dictionary in RAM.
- **Real, executable tools — never a fake that returns a canned value.** When an
  adapter exercises tool-calling, the tool must actually run and produce a real
  result: e.g. a **calculator** that truly evaluates the expression, or a
  **corpus-lookup** tool that performs the real Qdrant search above and returns the
  retrieved passage. A stubbed tool that returns a hardcoded string is not a tool
  under test. (The arena's single-shot `tool_calling` task in `../SPEC.md` captures
  the model's *native* `tool_calls` without executing them — that is a separate,
  deliberate measurement; the convention here governs adapters that go further and
  actually execute tools for a RAG/agent loop.)

---

## Files

| Path | Role |
|---|---|
| `embed-server/app.py` | FastAPI OpenAI-compatible e5 embeddings server |
| `embed-server/requirements.txt` | Server deps (all permissive) |
| `start-embed-server.sh` | Create venv + run server on 127.0.0.1:8899 |
| `corpus/*.md` | 22 titled Nimbus Rail passages |
| `populate-qdrant.py` | Embed corpus (passage:) → create/fill `arena_rag` |
| `smoke-rag.py` | End-to-end retrieval smoke (embed query → Qdrant search) |
| `rag_questions.jsonl` | 9 questions (7 grounded + 2 traps) |
| `README.md` | This document |

## Dependencies & licenses (all MIT / BSD / Apache-2.0)

| Dependency | License |
|---|---|
| sentence-transformers | Apache-2.0 |
| transformers (transitive) | Apache-2.0 |
| torch (transitive) | BSD-3 |
| fastapi | MIT |
| uvicorn | BSD-3 |
| pydantic | MIT |
| Qdrant (server) | Apache-2.0 |
| rqlite (server) | MIT |

All pass the repo's constitutional license gate (no GPL/AGPL/SSPL/BSL). The
`populate-qdrant.py` and `smoke-rag.py` scripts use only the Python standard
library (`urllib`), so they run with any Python 3 without extra installs.
