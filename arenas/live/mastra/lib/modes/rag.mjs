// `rag` mode -- REAL retrieval-augmented generation against REAL infrastructure.
//
//   1. Embed the last user message via the real e5 embed server (the same
//      multilingual-e5-small-query model, 384-dim, that populated the corpus).
//   2. Search the real Qdrant `arena_rag` collection via @qdrant/js-client-rest.
//   3. A Mastra `Agent` answers STRICTLY from the retrieved context -- "answer
//      only from context, else say you don't know" -- so a trap question (whose
//      answer is NOT in the corpus) surfaces as an honest refusal rather than a
//      hallucination. Retrieved passages are injected into the agent's
//      instructions, the Mastra-native equivalent of a grounding system prompt.

import { Agent } from '@mastra/core/agent';
import { QdrantClient } from '@qdrant/js-client-rest';
import { embedQuery } from '../embed.mjs';
import { buildMessages, extractTokenCounts, lastUserContent, msSince } from '../output.mjs';

const RETRIEVAL_LIMIT = 4;

const DEFAULT_SYSTEM =
  'You are a precise assistant that answers strictly from the CONTEXT provided below. ' +
  'If -- and only if -- the CONTEXT does not contain the answer, reply exactly: ' +
  '"I don\'t know." Never use outside knowledge and never guess.';

export async function runRag(input, env) {
  const messages = buildMessages(input.messages);
  const query = lastUserContent(messages);
  if (!query) {
    throw new Error('rag mode requires at least one message with role "user"');
  }

  const vector = await embedQuery(env.embedUrl, query);

  // checkCompatibility: false -- the local Qdrant server may run an older minor
  // version than the installed client; that mismatch is cosmetic for the plain
  // REST `search` call this adapter makes, so skip the noisy startup warning.
  const qdrant = new QdrantClient({ url: env.qdrantUrl, checkCompatibility: false });
  const searchResult = await qdrant.search(env.qdrantCollection, {
    vector,
    limit: RETRIEVAL_LIMIT,
    with_payload: true,
  });

  const retrieved = searchResult.map((point) => ({
    doc_id: (point.payload && point.payload.doc_id) || null,
    title: (point.payload && point.payload.title) || null,
    text: (point.payload && point.payload.text) || null,
    score: point.score,
  }));

  const contextBlock = retrieved.length
    ? retrieved.map((r, i) => `[${i + 1}] ${r.title}\n${r.text}`).join('\n\n')
    : '(no passages retrieved)';

  const baseSystem =
    typeof input.system === 'string' && input.system.length > 0 ? input.system : DEFAULT_SYSTEM;
  const instructions = `${baseSystem}\n\nCONTEXT:\n${contextBlock}`;

  const agent = new Agent({ name: 'arena-rag', instructions, model: env.chatModel });

  const startedAt = process.hrtime.bigint();
  const result = await agent.generate(messages);
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    retrieved,
  };
}
