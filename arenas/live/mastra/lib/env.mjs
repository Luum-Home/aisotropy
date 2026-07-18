// Environment + provider-model resolution shared by every Mastra mode handler.
//
// Every credential is read from process.env ONLY -- never hardcoded, never logged.
// Host URLs for the local real infra (embed server, Qdrant, rqlite, on-disk libsql)
// are env-configurable so the same adapter works unchanged from a Docker container
// (via host.docker.internal) or natively on the host.

import { createOpenAI } from '@ai-sdk/openai';
import { buildStubChatModel } from './stub-model.mjs';

const DEFAULT_EMBED_URL = 'http://127.0.0.1:8899';
const DEFAULT_QDRANT_URL = 'http://localhost:6333';
const DEFAULT_QDRANT_COLLECTION = 'arena_rag';
const DEFAULT_RQLITE_URL = 'http://localhost:4001';
// Mastra's native Memory (memory mode) persists to an on-disk libsql/SQLite file.
// A stable default path lets a SECOND `node index.mjs` process reopen the same
// store and recall history written by the first (proving real cross-process
// persistence, not an in-process Map). Override via AISOTROPY_MASTRA_LIBSQL.
const DEFAULT_LIBSQL_URL = 'file:/tmp/mastra_arena_memory.db';

/**
 * Resolve every piece of configuration + the Mastra-compatible language model this
 * adapter needs, once, from environment variables. Throws a descriptive error
 * (never crashes silently) when a required chat credential is missing -- the caller
 * wraps this in the top-level try/catch so the error lands in the contract's
 * `error` field.
 *
 * ARENA_STUB=1 short-circuits every real-infra decision below: no
 * OPENAI_API_KEY/OPENAI_MODEL check, an offline `MockLanguageModelV4` chat
 * model instead of a real provider call (lib/stub-model.mjs), and `stub:
 * true` so mode handlers that talk to Qdrant directly (lib/modes/rag.mjs)
 * know to use the offline substitute in lib/stub-qdrant.mjs. rqlite and the
 * e5 embed server are handled transparently one layer down (lib/rqlite.mjs,
 * lib/embed.mjs each check ARENA_STUB themselves), so every other mode needs
 * no change at all to run fully offline.
 */
export function resolveEnv() {
  const embedUrl = process.env.EMBED_URL || DEFAULT_EMBED_URL;
  const qdrantUrl = process.env.QDRANT_URL || DEFAULT_QDRANT_URL;
  const qdrantCollection = process.env.QDRANT_COLLECTION || DEFAULT_QDRANT_COLLECTION;
  const rqliteUrl = process.env.RQLITE_URL || DEFAULT_RQLITE_URL;
  const libsqlUrl = process.env.AISOTROPY_MASTRA_LIBSQL || DEFAULT_LIBSQL_URL;

  if (process.env.ARENA_STUB === '1') {
    return {
      modelName: 'arena-stub-mock',
      chatModel: buildStubChatModel(),
      embedUrl,
      qdrantUrl,
      qdrantCollection,
      rqliteUrl,
      libsqlUrl,
      stub: true,
    };
  }

  const apiKey = process.env.OPENAI_API_KEY;
  const baseURL = process.env.OPENAI_BASE_URL;
  const modelName = process.env.OPENAI_MODEL || '';

  if (!apiKey) {
    throw new Error('OPENAI_API_KEY is not set in the environment');
  }
  if (!modelName) {
    throw new Error('OPENAI_MODEL is not set in the environment');
  }

  const chatProvider = createOpenAI({ baseURL, apiKey });
  // `.chat(modelName)` forces the classic /chat/completions wire format. Mastra
  // (like the Vercel AI SDK it wraps) would otherwise default to the newer
  // Responses API shape (/responses, function_call_output items), which agnes's
  // OpenAI-compatible proxy does not fully support: it 500s ("'role'") on the
  // Responses-style multi-step tool-loop follow-up request. agnes (like aisotropy's
  // own internal/llm/openai_compat.go client) speaks classic /chat/completions,
  // so every mode uses that explicitly.
  const chatModel = chatProvider.chat(modelName);

  return {
    modelName,
    chatModel,
    embedUrl,
    qdrantUrl,
    qdrantCollection,
    rqliteUrl,
    libsqlUrl,
    stub: false,
  };
}
