// Minimal client for the REAL local e5 embedding server (an OpenAI-shaped
// /v1/embeddings endpoint, no auth) built on Node's native `fetch` -- no extra
// dependency. Used by the `rag` mode to embed the query with the same
// multilingual-e5-small-query model (384-dim) that populated the Qdrant
// `arena_rag` collection, so query and corpus vectors live in the same space.

const QUERY_MODEL_ID = 'multilingual-e5-small-query';
const STUB_DIM = 384;

function isStub() {
  return process.env.ARENA_STUB === '1';
}

/**
 * Deterministic, offline, 384-dim pseudo-embedding used ONLY when
 * ARENA_STUB=1. It is NOT a real embedding -- it carries no semantic meaning,
 * it is a stable hash of `text` spread across STUB_DIM floats in [-1, 1] so
 * the same input always yields the same vector (useful for wiring checks),
 * nothing more. `rag` mode does not rely on this vector for retrieval quality
 * under the stub -- see lib/stub-qdrant.mjs, which searches by keyword
 * overlap instead, precisely because a hash has no cosine-similarity meaning.
 */
function stubEmbedding(text) {
  const vector = new Array(STUB_DIM);
  let seed = 0;
  const s = String(text);
  for (let i = 0; i < s.length; i += 1) {
    seed = (seed * 31 + s.charCodeAt(i)) >>> 0;
  }
  for (let i = 0; i < STUB_DIM; i += 1) {
    seed = (seed * 1103515245 + 12345) >>> 0;
    vector[i] = (seed % 2000) / 1000 - 1; // in [-1, 1)
  }
  return vector;
}

/**
 * Embed a single string via the real e5 server and return its 384-dim vector.
 * Throws (never returns a fabricated vector) on any transport or response-shape
 * error so the failure lands in the adapter's `error` field rather than silently
 * degrading retrieval to garbage. Under ARENA_STUB=1, returns the deterministic
 * offline pseudo-embedding above instead of making any network call.
 */
export async function embedQuery(embedUrl, text) {
  if (isStub()) {
    return stubEmbedding(text);
  }
  const res = await fetch(`${embedUrl}/v1/embeddings`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ model: QUERY_MODEL_ID, input: text }),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(`embed server failed: HTTP ${res.status} ${JSON.stringify(data)}`);
  }
  const vector = data && data.data && data.data[0] && data.data[0].embedding;
  if (!Array.isArray(vector) || vector.length === 0) {
    throw new Error(`embed server returned no embedding: ${JSON.stringify(data).slice(0, 200)}`);
  }
  return vector;
}
