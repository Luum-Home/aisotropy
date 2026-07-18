// Environment + client resolution shared by every mode handler.
//
// Every credential is read from process.env ONLY -- never hardcoded, never logged.
// Host URLs for the local real infra (embed server, Qdrant, rqlite) are
// env-configurable so the same adapter works unchanged from a Docker container
// (via host.docker.internal) or natively on the host.

import { createOpenAI } from '@ai-sdk/openai';
import { STUB_ENABLED, STUB_MODEL_ID, STUB_RQLITE_URL, createStubChatModel } from './stub.mjs';

const DEFAULT_EMBED_URL = 'http://127.0.0.1:8899';
const DEFAULT_QDRANT_URL = 'http://localhost:6333';
const DEFAULT_QDRANT_COLLECTION = 'arena_rag';
const DEFAULT_RQLITE_URL = 'http://localhost:4001';

/**
 * Resolve every piece of configuration + provider client this adapter needs, once,
 * from environment variables. Throws a descriptive error (never crashes silently)
 * when a required chat credential is missing -- the caller wraps this in the
 * top-level try/catch so the error lands in the contract's `error` field.
 *
 * STUB TOGGLE (ARENA_STUB=1): skips every real-credential/network requirement
 * below and returns an env built entirely from offline doubles (see
 * `lib/stub.mjs`) -- `env.chatModel` is a `MockLanguageModelV4`, `env.stub`
 * is `true` (checked by `lib/modes/rag.mjs` to skip the real embed+Qdrant
 * round trip), and `env.rqliteUrl` is the `stub:local` sentinel `lib/rqlite.mjs`
 * routes to its local table engine instead of `fetch()`. No `OPENAI_API_KEY`,
 * `OPENAI_MODEL`, `EMBED_URL`, `QDRANT_URL`, or `RQLITE_URL` is read in this
 * branch, so the adapter runs the full mode suite with zero network egress.
 */
export function resolveEnv() {
  if (STUB_ENABLED) {
    return {
      modelName: STUB_MODEL_ID,
      chatModel: createStubChatModel(),
      embedProvider: null,
      embedUrl: '',
      qdrantUrl: '',
      qdrantCollection: '',
      rqliteUrl: STUB_RQLITE_URL,
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
  // `.chat(modelName)` forces the classic /chat/completions wire format.
  // @ai-sdk/openai's default call signature (`chatProvider(modelName)`) uses the
  // newer Responses API shape (/responses, function_call_output items), which
  // agnes's OpenAI-compatible proxy does not fully support: it 500s ("'role'")
  // on the Responses-style multi-step tool-loop follow-up request. agnes (like
  // aisotropy's own internal/llm/openai_compat.go client) speaks classic
  // /chat/completions, so every mode uses that explicitly.
  const chatModel = chatProvider.chat(modelName);

  const embedUrl = process.env.EMBED_URL || DEFAULT_EMBED_URL;
  // The local e5 embed server (arenas/live/_shared/embed-server) is OpenAI-shaped
  // but requires no auth. @ai-sdk/openai still wants a non-empty apiKey string to
  // construct the client -- this placeholder is never a real credential and is
  // never sent anywhere that checks it.
  const embedProvider = createOpenAI({
    baseURL: embedUrl,
    apiKey: 'local-embed-server-no-auth-required',
  });

  const qdrantUrl = process.env.QDRANT_URL || DEFAULT_QDRANT_URL;
  const qdrantCollection = process.env.QDRANT_COLLECTION || DEFAULT_QDRANT_COLLECTION;
  const rqliteUrl = process.env.RQLITE_URL || DEFAULT_RQLITE_URL;

  return {
    modelName,
    chatModel,
    embedProvider,
    embedUrl,
    qdrantUrl,
    qdrantCollection,
    rqliteUrl,
    stub: false,
  };
}
