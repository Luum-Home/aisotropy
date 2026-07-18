// `rag` mode -- REAL retrieval-augmented generation against REAL infrastructure.
//
//   1. Embed the last user message via the real e5 embed server, using the AI SDK
//      NATIVE embedding path: createOpenAI({baseURL: EMBED_URL}).embedding(...) + embed().
//   2. Search the real Qdrant `arena_rag` collection via @qdrant/js-client-rest.
//   3. generateText() with agnes answering strictly from the retrieved context --
//      "answer only from context, else say you don't know" -- so trap questions
//      (whose answer is NOT in the corpus) surface as an honest refusal rather than
//      a hallucination.

import { embed, generateText } from 'ai';
import { QdrantClient } from '@qdrant/js-client-rest';
import { buildMessages, extractTokenCounts, lastUserContent, msSince } from '../output.mjs';
import { stubSearchCorpus } from '../stub.mjs';

const EMBEDDING_MODEL_ID = 'multilingual-e5-small-query';
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

  let retrieved;
  if (env.stub) {
    // ARENA_STUB=1 -- skip the real e5 embed server + Qdrant search round
    // trip entirely; see `lib/stub.mjs` for the honest caveat (keyword
    // overlap over a tiny fixed corpus, not a real similarity search).
    console.error('PROGRESS: rag -- ARENA_STUB=1, using local stub corpus instead of embed()+Qdrant');
    retrieved = stubSearchCorpus(query, RETRIEVAL_LIMIT);
  } else {
    const embeddingModel = env.embedProvider.embedding(EMBEDDING_MODEL_ID);
    const embedResult = await embed({ model: embeddingModel, value: query });
    const vector = embedResult.embedding;

    // checkCompatibility: false -- the local Qdrant server may run an older minor
    // version than the installed client; that mismatch is cosmetic for the plain
    // REST `search` call this adapter makes, so skip the noisy startup warning.
    const qdrant = new QdrantClient({ url: env.qdrantUrl, checkCompatibility: false });
    const searchResult = await qdrant.search(env.qdrantCollection, {
      vector,
      limit: RETRIEVAL_LIMIT,
      with_payload: true,
    });

    retrieved = searchResult.map((point) => ({
      doc_id: (point.payload && point.payload.doc_id) || null,
      title: (point.payload && point.payload.title) || null,
      text: (point.payload && point.payload.text) || null,
      score: point.score,
    }));
  }

  const contextBlock = retrieved.length
    ? retrieved.map((r, i) => `[${i + 1}] ${r.title}\n${r.text}`).join('\n\n')
    : '(no passages retrieved)';

  const system = [
    typeof input.system === 'string' && input.system.length > 0 ? input.system : DEFAULT_SYSTEM,
    `CONTEXT:\n${contextBlock}`,
  ].join('\n\n');

  const startedAt = process.hrtime.bigint();
  const result = await generateText({
    model: env.chatModel,
    system,
    messages,
  });
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
