// Offline stub backend -- activated when `ARENA_STUB=1` in the environment.
//
// GOAL: every mode in this adapter must be runnable with ZERO network egress
// under this toggle -- no real agnes chat call, no real e5 embed server, no
// real Qdrant, no real rqlite. This module is the single place that builds
// those offline replacements, using the AI SDK's OWN native test double
// (`MockLanguageModelV4` from `ai/test`) rather than a hand-rolled fake model
// class, so the SAME `generateText()` / `streamText()` / `generateObject()`
// code every mode handler already calls against the real agnes model also
// runs, unchanged, against this mock -- only `lib/env.mjs` decides which
// model instance gets wired in.
//
// HONEST NOTE (mirrors the "Quality NOT scored" contract): stub mode is a
// WIRING/SMOKE lane, not a quality lane. The canned replies below are only
// plausible enough to keep each mode handler's OWN logic (tool execution via
// a real, local, non-network `execute`, structured-output JSON parsing,
// streaming chunk assembly, rqlite-shaped persistence) exercised end to end.
// They prove the AI SDK integration code path runs fully offline; they do
// NOT prove the "model" is good at anything -- there is no real model here.

import { MockLanguageModelV4 } from 'ai/test';
import { simulateReadableStream } from 'ai';

export const STUB_ENABLED = process.env.ARENA_STUB === '1';
export const STUB_MODEL_ID = 'arena-stub-mock-language-model-v4';
// A non-http scheme so `lib/rqlite.mjs` can route to the in-process/on-disk
// stub table engine instead of ever calling `fetch()` against a real rqlite.
export const STUB_RQLITE_URL = 'stub:local';

function textOf(part) {
  return part && typeof part.text === 'string' ? part.text : '';
}

/** Best-effort extraction of the last user message's plain text from a V4 prompt array. */
function lastUserText(prompt) {
  if (!Array.isArray(prompt)) {
    return '';
  }
  for (let i = prompt.length - 1; i >= 0; i -= 1) {
    const msg = prompt[i];
    if (msg && msg.role === 'user' && Array.isArray(msg.content)) {
      return msg.content.map(textOf).join(' ').trim();
    }
  }
  return '';
}

/** True once the prompt already carries a tool-result turn -- i.e. this doGenerate call is the follow-up step after a stub tool call was executed. */
function hasToolResult(prompt) {
  if (!Array.isArray(prompt)) {
    return false;
  }
  return prompt.some(
    (msg) =>
      msg &&
      msg.role === 'tool' &&
      Array.isArray(msg.content) &&
      msg.content.some((part) => part && part.type === 'tool-result')
  );
}

function findToolResultValue(prompt) {
  for (const msg of prompt || []) {
    if (msg && msg.role === 'tool' && Array.isArray(msg.content)) {
      const part = msg.content.find((p) => p && p.type === 'tool-result');
      if (part && part.output) {
        return part.output.value !== undefined ? part.output.value : part.output;
      }
    }
  }
  return null;
}

function stubUsage() {
  return {
    inputTokens: { total: 8, noCache: 8, cacheRead: undefined, cacheWrite: undefined },
    outputTokens: { total: 6, text: 6, reasoning: undefined },
  };
}

/**
 * Build the ONE offline chat model every mode handler wires in (as
 * `env.chatModel`) when `ARENA_STUB=1` -- including through
 * `wrapLanguageModel()` in `middleware` mode, since a mock model is still a
 * `LanguageModelV4` and composes with middleware exactly like the real one.
 *
 * `doGenerate` inspects the actual call params (`tools`, `responseFormat`,
 * `prompt`) so the SAME mode code that drives real agnes also drives this
 * double, with no mode-specific branching anywhere else in the adapter:
 *   - `responseFormat.type === 'json'` (generateObject, `structured` mode)
 *     -> a canned object matching `structured.mjs`'s NoteSchema.
 *   - `tools` present, no tool-result in the prompt yet (`tools`/
 *     `observability`/`qa` modes) -> decide to call the first declared tool
 *     (a real, local, non-network `execute` then genuinely runs it).
 *   - `tools` present AND a tool-result is already in the prompt -> finish
 *     with a text reply that echoes the real tool output.
 *   - otherwise -> a plain canned text reply echoing the last user message.
 */
export function createStubChatModel() {
  return new MockLanguageModelV4({
    modelId: STUB_MODEL_ID,
    provider: 'arena-stub',
    doGenerate: async (params) => {
      const prompt = params.prompt;
      const tools = Array.isArray(params.tools)
        ? params.tools.filter((t) => t && t.type === 'function')
        : [];

      if (params.responseFormat && params.responseFormat.type === 'json') {
        const object = {
          summary: 'stub summary (ARENA_STUB=1, no real model called)',
          sentiment: 'neutral',
          action_items: [],
        };
        return {
          content: [{ type: 'text', text: JSON.stringify(object) }],
          finishReason: { unified: 'stop', raw: undefined },
          usage: stubUsage(),
          warnings: [],
        };
      }

      if (tools.length > 0 && !hasToolResult(prompt)) {
        const target = tools.find((t) => t.name === 'calculate') || tools[0];
        const input = target.name === 'calculate' ? { expression: '2 + 2' } : {};
        return {
          content: [
            {
              type: 'tool-call',
              toolCallId: 'stub-call-1',
              toolName: target.name,
              input: JSON.stringify(input),
            },
          ],
          finishReason: { unified: 'tool-calls', raw: undefined },
          usage: stubUsage(),
          warnings: [],
        };
      }

      if (tools.length > 0 && hasToolResult(prompt)) {
        const value = findToolResultValue(prompt);
        return {
          content: [{ type: 'text', text: `[stub] tool result: ${JSON.stringify(value)}` }],
          finishReason: { unified: 'stop', raw: undefined },
          usage: stubUsage(),
          warnings: [],
        };
      }

      const query = lastUserText(prompt);
      const text = query
        ? `[stub reply, ARENA_STUB=1] ${query}`
        : '[stub reply, ARENA_STUB=1] ok';
      return {
        content: [{ type: 'text', text }],
        finishReason: { unified: 'stop', raw: undefined },
        usage: stubUsage(),
        warnings: [],
      };
    },
    doStream: async () => ({
      stream: simulateReadableStream({
        chunks: [
          { type: 'text-start', id: 'stub-text-1' },
          { type: 'text-delta', id: 'stub-text-1', delta: '[stub ' },
          { type: 'text-delta', id: 'stub-text-1', delta: 'streamed ' },
          { type: 'text-delta', id: 'stub-text-1', delta: 'reply, ARENA_STUB=1]' },
          { type: 'text-end', id: 'stub-text-1' },
          {
            type: 'finish',
            finishReason: { unified: 'stop', raw: undefined },
            usage: stubUsage(),
          },
        ],
        // No artificial delay -- this is an offline smoke lane, not a
        // real-latency simulation.
        initialDelayInMs: null,
        chunkDelayInMs: null,
      }),
    }),
  });
}

// A tiny fixed local corpus for `rag` mode under ARENA_STUB=1, standing in
// for the real e5-embed + Qdrant-search round trip. Retrieval here is a
// naive case-insensitive keyword-overlap score, NOT a real embedding
// similarity search -- honest per the "Quality NOT scored in stub" contract;
// this only proves the rag mode handler's retrieval-then-generate wiring
// runs without the embed server or Qdrant.
const STUB_CORPUS = [
  {
    doc_id: 'stub-doc-1',
    title: 'Aisotropy memory tiers',
    text: 'Aisotropy layers working memory (BadgerDB), facts memory (rqlite/FTS5), and semantic memory (Qdrant) by access temperature.',
  },
  {
    doc_id: 'stub-doc-2',
    title: 'Arena adapters',
    text: 'Each arena adapter under arenas/live/ drives the same real agnes model through a different framework to compare native features.',
  },
  {
    doc_id: 'stub-doc-3',
    title: 'ARENA_STUB toggle',
    text: 'Setting ARENA_STUB=1 swaps every adapter dependency for an offline double so the whole mode suite runs with zero network egress.',
  },
];

/** Keyword-overlap "search" over `STUB_CORPUS`, offline stand-in for the real Qdrant search in `rag` mode. */
export function stubSearchCorpus(query, limit) {
  const queryTerms = new Set(
    String(query || '')
      .toLowerCase()
      .split(/\W+/)
      .filter(Boolean)
  );
  const scored = STUB_CORPUS.map((doc) => {
    const docTerms = `${doc.title} ${doc.text}`.toLowerCase().split(/\W+/);
    const overlap = docTerms.filter((t) => queryTerms.has(t)).length;
    return { ...doc, score: overlap };
  });
  scored.sort((a, b) => b.score - a.score);
  return scored.slice(0, limit);
}
