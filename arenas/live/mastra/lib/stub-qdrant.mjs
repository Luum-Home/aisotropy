// Offline substitute for the real Qdrant `arena_rag` collection, used ONLY
// when ARENA_STUB=1 (see lib/env.mjs, lib/modes/rag.mjs). A small, hardcoded
// corpus describing THIS adapter -- NOT the real seeded corpus that lives in
// Qdrant, which the stub never touches -- searched by naive keyword overlap,
// NOT real vector similarity. That is a deliberate choice, not a shortcut:
// lib/embed.mjs's ARENA_STUB=1 vector is a deterministic hash with no
// semantic meaning, so running real cosine similarity over it would produce
// meaningless rankings dressed up as "search". Keyword overlap over plain
// text is honest about what it is.

const STUB_CORPUS = [
  {
    doc_id: 'stub-1',
    title: 'Arena Mastra Adapter',
    text: 'The Mastra live arena adapter drives a chat model through a Mastra Agent for modes ' +
      'including qa, rag, tools, memory, memory_scoped, isolation_leak, structured, stream, ' +
      'observability, and crash_recovery.',
  },
  {
    doc_id: 'stub-2',
    title: 'ARENA_STUB offline mode',
    text: 'Setting ARENA_STUB=1 replaces the real agnes chat model with an in-process AI SDK ' +
      'MockLanguageModelV4 and replaces rqlite and Qdrant with offline, file-backed or ' +
      'in-process substitutes, so every mode runs fully offline with no network access.',
  },
  {
    doc_id: 'stub-3',
    title: 'Middleware mode',
    text: 'The middleware mode wraps the chat model with an AI SDK wrapLanguageModel middleware ' +
      'that appends a sentinel to the system prompt inbound via transformParams and counts model ' +
      'invocations outbound via wrapGenerate.',
  },
];

function tokenize(text) {
  return new Set(String(text).toLowerCase().split(/[^a-z0-9]+/).filter(Boolean));
}

/**
 * Naive keyword-overlap "search" over the stub corpus. Returns the same shape
 * `lib/modes/rag.mjs` expects from `qdrant.search(...)`: an array of
 * `{ payload: { doc_id, title, text }, score }`, sorted by score descending.
 */
export function stubQdrantSearch(query, limit) {
  const queryTokens = tokenize(query);
  const scored = STUB_CORPUS.map((doc) => {
    const docTokens = tokenize(`${doc.title} ${doc.text}`);
    let overlap = 0;
    for (const t of queryTokens) {
      if (docTokens.has(t)) overlap += 1;
    }
    const score = queryTokens.size > 0 ? overlap / queryTokens.size : 0;
    return { doc, score };
  });
  scored.sort((a, b) => b.score - a.score);
  return scored.slice(0, limit).map(({ doc, score }) => ({
    payload: { doc_id: doc.doc_id, title: doc.title, text: doc.text },
    score,
  }));
}
