// Minimal client for the REAL local e5 embedding server (an OpenAI-shaped
// /v1/embeddings endpoint, no auth) built on Node's native `fetch` -- no extra
// dependency. Used by the `rag` mode to embed the query with the same
// multilingual-e5-small-query model (384-dim) that populated the Qdrant
// `arena_rag` collection, so query and corpus vectors live in the same space.

const QUERY_MODEL_ID = 'multilingual-e5-small-query';

/**
 * Embed a single string via the real e5 server and return its 384-dim vector.
 * Throws (never returns a fabricated vector) on any transport or response-shape
 * error so the failure lands in the adapter's `error` field rather than silently
 * degrading retrieval to garbage.
 */
export async function embedQuery(embedUrl, text) {
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
