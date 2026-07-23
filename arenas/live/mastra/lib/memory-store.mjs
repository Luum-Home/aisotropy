// Shared builder for Mastra's RESOURCE-SCOPED Memory — the primitive that gives a
// FAIR answer on the memory-extension dimensions (memory_cross_session,
// memory_semantic_recall, memory_consolidation) and the tenancy dimension
// (isolation_forgetful). Both the plain `memory` mode (when a user_id / resourceId
// is present) and the `isolation_forgetful` mode build their Memory here so they
// share ONE idiomatic Mastra configuration — the difference between them is only
// WHICH resourceId reaches the Memory API on recall (present vs deliberately
// omitted), never a different store.
//
// WHY resource scope (vs the thread-only `memory` mode, lib/modes/memory.mjs):
// the new dimensions VARY the session_id (threadId) while HOLDING the user_id
// (resourceId) across a task's invocations, which defeats transcript replay by
// construction — a fresh threadId makes a thread-keyed store return empty, so only
// genuine USER-scoped memory recalls the persisted fact. Mastra's official answer
// is `scope: 'resource'` on both working memory and semanticRecall
// (https://mastra.ai/docs/memory/working-memory — "Resource-scoped memory";
// https://mastra.ai/docs/memory/semantic-recall), which persist/retrieve a fact
// across every thread that shares a resourceId. This is the idiomaticity doc's
// SHOULD-FIX: the plain `memory` mode's `semanticRecall:false` is a justified
// deviation for THAT dimension only; here it MUST be on.
//
// Vector store (semanticRecall backend):
//   - real lane  -> `@mastra/qdrant` QdrantVector against the arena's real Qdrant
//     (the idiomaticity doc's SHOULD-FIX #2 — use the SAME vector infra the other
//     adapters use, not a bespoke store), embedded with the SAME multilingual-e5
//     server the `rag` mode uses, so query and message vectors live in one space.
//   - stub lane  -> @mastra/libsql LibSQLVector (file-backed, offline, zero
//     network) with a deterministic hash embedder. The stub proves the wiring
//     DRIVES; it cannot score recall quality (a MockLanguageModel returns fixed
//     text and a hash embedding carries no semantic meaning) — hence the memory-
//     extension stub matrix cells are `infeasible`, not `pass`.
//
// All @mastra/* packages used here are Apache-2.0 (verified in each package.json):
// @mastra/core, @mastra/memory, @mastra/libsql, @mastra/qdrant. No ee/-licensed
// code path is touched.

import { Memory } from '@mastra/memory';
import { LibSQLStore, LibSQLVector } from '@mastra/libsql';
import { QdrantVector } from '@mastra/qdrant';
import { MockEmbeddingModelV4 } from 'ai/test';
import { createOpenAI } from '@ai-sdk/openai';

// multilingual-e5-small is 384-dim; the real embed server serves it at
// /v1/embeddings (see lib/embed.mjs). The stub embedder matches the dimension so
// the offline vector store machinery has a consistent shape to exercise.
const E5_DIM = 384;
const E5_QUERY_MODEL = 'multilingual-e5-small-query';

// A single, STABLE Qdrant/libsql index name shared by every memory-extension
// task. Per-task isolation is by resourceId (the semanticRecall `scope:'resource'`
// filter runs DURING the vector search), so one index is correct AND matches the
// arena's per-framework-collection fairness rule (this name is framework-unique,
// never the shared `arena_rag` the rag mode uses). Overridable for a per-run
// namespace if an operator wants one.
export function memoryIndexName() {
  return process.env.ARENA_MASTRA_MEM_INDEX || 'arena_mem_mastra';
}

// Deterministic, offline, 384-dim pseudo-embedding — a stable hash of the input
// spread across E5_DIM floats in [-1, 1]. Carries NO semantic meaning (identical
// shape/intent to lib/embed.mjs's stubEmbedding); it exists only so the vector
// store has real-shaped vectors to store and query under ARENA_STUB=1.
function hashEmbedding(text) {
  const vector = new Array(E5_DIM);
  let seed = 0;
  const s = String(text);
  for (let i = 0; i < s.length; i += 1) {
    seed = (seed * 31 + s.charCodeAt(i)) >>> 0;
  }
  for (let i = 0; i < E5_DIM; i += 1) {
    seed = (seed * 1103515245 + 12345) >>> 0;
    vector[i] = (seed % 2000) / 1000 - 1;
  }
  return vector;
}

/** Offline deterministic embedder (ARENA_STUB=1) — no network, 384-dim. */
function buildStubEmbedder() {
  return new MockEmbeddingModelV4({
    modelId: 'arena-stub-e5',
    maxEmbeddingsPerCall: 256,
    supportsParallelCalls: true,
    doEmbed: async ({ values }) => ({
      embeddings: values.map((v) => hashEmbedding(v)),
    }),
  });
}

/**
 * Real e5 embedder: the SAME multilingual-e5 server the rag mode embeds against,
 * reached through the AI SDK's OpenAI-compatible embedding model so Mastra's
 * Memory can embed both stored messages and the recall query in one vector space.
 * The e5 server takes no auth; a throwaway key satisfies the SDK's required field
 * and is never logged.
 */
function buildRealEmbedder(embedUrl) {
  const provider = createOpenAI({
    baseURL: `${embedUrl}/v1`,
    apiKey: process.env.OPENAI_API_KEY || 'arena-e5-no-auth',
  });
  return provider.textEmbeddingModel(E5_QUERY_MODEL);
}

/**
 * Build a resource-scoped Mastra Memory. `env` is the resolveEnv() result
 * (carries libsqlUrl, qdrantUrl, embedUrl, stub). semanticRecall + workingMemory
 * are BOTH resource-scoped so a fact stated under a resourceId in thread A is
 * recallable in thread B for the same resourceId — the cross-session mechanic.
 */
export function buildResourceScopedMemory(env) {
  const storage = new LibSQLStore({ id: 'arena-mastra-memory-ext', url: env.libsqlUrl });

  let vector;
  let embedder;
  if (env.stub) {
    vector = new LibSQLVector({ id: 'arena-mastra-mem-vec', url: env.libsqlUrl });
    embedder = buildStubEmbedder();
  } else {
    vector = new QdrantVector({ id: 'arena-mastra-mem-vec', url: env.qdrantUrl });
    embedder = buildRealEmbedder(env.embedUrl);
  }

  return new Memory({
    storage,
    vector,
    embedder,
    options: {
      // Same-thread recent history (keeps the plain `memory` dimension's behavior
      // for the persist thread) PLUS resource-scoped semantic retrieval across
      // threads (the cross-session/semantic-recall mechanic).
      lastMessages: 30,
      semanticRecall: {
        topK: 8,
        messageRange: 2,
        scope: 'resource',
        indexName: memoryIndexName(),
      },
      // Resource-scoped working memory: Mastra's LLM-maintained profile that
      // persists across every thread of a resourceId. This is the "consolidation"
      // mechanism for memory_consolidation — but it runs IN THE HOT PATH, not as
      // an async background worker (Mastra ships no async consolidator). That is a
      // reported FINDING, not a defect: the consolidate directive turn is a
      // near-no-op trigger (see README + docs/architecture/arena-memory-extensions.md).
      workingMemory: { enabled: true, scope: 'resource' },
    },
    generateTitle: false,
  });
}
