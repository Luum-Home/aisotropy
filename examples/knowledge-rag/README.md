# knowledge-rag — seed a knowledge base, retrieve it (Lesson 05)

Ingest documents, then let the semantic tier hydrate the most relevant chunks
into the system prompt (RAG) so the agent answers from your knowledge base
**without a tool call**. This example is library-first: it uses `App.Turn`
(the in-process runtime — no HTTP server, no SSE), so the whole ingest → retrieve
loop is a handful of function calls.

It is a separate Go module that imports only
`github.com/Luum-Home/aisotropy/aisotropy`, so Go's `internal/` rule enforces
"public surface only" — if it compiles, it never reached a private package.

## What it shows

| Concept | Where |
|---|---|
| Tenant-shared knowledge | `IngestTenantKnowledge(ctx, tenant, docs…)` — one bucket every user of the tenant retrieves (ADR-0019) |
| Per-user knowledge | `IngestKnowledge(ctx, tenant, user, docs…)` — scoped to one `(tenant, user)` |
| Chunk-count feedback | both calls return the number of chunks written — the deterministic, keyless proof the pipeline ran |
| Retrieval into the prompt | `App.Turn` hydrates the ingested chunks (tenant-shared + this user's) into the system prompt before the model answers |
| Isolation by construction | scope is derived from the `tenant`/`user` you pass — there is no way to ingest or read across tenants |

## Run it (keyless — no API key, no Docker)

```bash
cd examples/knowledge-rag
go run .
```

Output (trimmed):

```
ingested 2 tenant-shared doc(s) -> 2 chunk(s) into tenant "acme"
ingested 1 user-scoped doc(s) -> 1 chunk(s) for "acme"/"alice"

reply (tenant acme): stub-llm structured reply for "What is the return window, …"
NOTE: stub=true — … the ingest/RAG path ran (see the chunk counts above), but
meaningful retrieval needs a real embedder + model. …
```

## Honesty gate — what keyless does and doesn't do

Keyless, two defaults are in play:

- **Embedder:** with neither `WithEmbedder` nor `AISOTROPY_EMBED_URL` set, the
  default is a deterministic **hash** embedder. It chunks, embeds, stores, and
  retrieves — the full path runs — but it does **not** rank *semantically*.
- **LLM:** with no `ANTHROPIC_API_KEY`, the credential-less **stub** answers by
  echoing the prompt; it does not reason over the hydrated context.

So the honest, observable keyless payoff is the **chunk count** each ingest
returns — it proves the ingestion/RAG plumbing works end to end. Meaningful
retrieval quality only appears once you plug in a real embedder and model.

## Make retrieval real

No code change — set two environment variables and re-run:

```bash
# A real embeddings model behind an OpenAI-compatible /v1/embeddings endpoint —
# OpenAI, OpenRouter, or a fully local server (Ollama, text-embeddings-inference):
export AISOTROPY_EMBED_URL=http://localhost:11434/v1/embeddings
export AISOTROPY_EMBED_API_KEY=…            # if the endpoint needs one (never logged)

# A real LLM to reason over the retrieved context:
export ANTHROPIC_API_KEY=sk-ant-…

go run .
# now retrieval ranks by meaning and the model answers from the KB; stub=false
```

To pin the embedder in code instead of via env, pass `WithEmbedder(...)` to
`aisotropy.New`. To tune chunking, add `WithChunkSize(size, overlap)` (default
`512/64` runes).

## Where the documents live at scale

Keyless, the semantic tier is an in-process store. Point it at real networked
Qdrant (and rqlite for the facts tier) with the repo's reference
`docker-compose.yml` — the same code, no change:

```bash
docker compose up -d qdrant rqlite
QDRANT_HOST=localhost QDRANT_PORT=6334 RQLITE_URL=http://localhost:4001 go run .
```

For the HTTP arm of the same capability (ingest over `POST .../knowledge` and
retrieve on the streaming chat route), see the `helpdesk` example and
`serve-http`.
