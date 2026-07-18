// Offline chat model used ONLY when ARENA_STUB=1 (see lib/env.mjs).
//
// RESEARCH FINDING (checked against the installed packages, not assumed):
// this adapter's real chat model -- `createOpenAI({...}).chat(modelName)`
// from the pinned `@ai-sdk/openai@4.0.16` -- reports
// `model.specificationVersion === 'v4'` at runtime (verified with a one-off
// `node -e` probe against node_modules). That `v4` generation is backed by
// `@ai-sdk/provider@4.0.3`, which is exactly the version `ai@7.0.31` (the
// version installed here as a new direct dependency, matching
// `@mastra/core@1.51.0`'s internal "provider-v7" alias) depends on. So the
// correct offline stand-in at THIS pin is `MockLanguageModelV4` from
// `ai/test` -- NOT `MockLanguageModelV2`. `ai/test` at this version does not
// even export a `MockLanguageModelV2` (only V3/V4 mock classes exist); the
// V2 name in earlier drafts of this task referred to a type re-exported
// under `@ai-sdk/provider` for backward compatibility, not a runtime mock
// class. Being precise about this here so the mismatch is documented, not
// silently "fixed" by guessing.
//
// Mastra's `Agent` accepts any object implementing the AI SDK's
// `LanguageModel` interface (checked in `@mastra/core`'s agent option types),
// and `MockLanguageModelV4` implements exactly that interface, so it can be
// passed straight into `new Agent({ model: buildStubChatModel() })` with no
// adapter shim.

import { MockLanguageModelV4 } from 'ai/test';

const STUB_TEXT =
  'This is an ARENA_STUB=1 offline reply from MockLanguageModelV4 -- no real model was called.';

/**
 * Fabricate a value conforming (loosely) to a JSON-Schema-7 fragment.
 * Used to synthesize plausible tool-call arguments and structured-output
 * objects offline. Values are deterministic placeholder data shaped to the
 * schema, never the result of any reasoning.
 */
function fabricate(schema) {
  if (!schema || typeof schema !== 'object') {
    return null;
  }
  if (Array.isArray(schema.enum) && schema.enum.length > 0) {
    return schema.enum[0];
  }
  const type = schema.type;
  if (type === 'object' || schema.properties) {
    const obj = {};
    for (const [key, sub] of Object.entries(schema.properties || {})) {
      obj[key] = fabricate(sub);
    }
    return obj;
  }
  if (type === 'array') {
    return [];
  }
  if (type === 'number' || type === 'integer') {
    return 0;
  }
  if (type === 'boolean') {
    return true;
  }
  return 'stub';
}

function hasToolResult(prompt) {
  return Array.isArray(prompt) && prompt.some((m) => m && m.role === 'tool');
}

function stubUsage() {
  return {
    inputTokens: { total: 8, noCache: 8, cacheRead: 0, cacheWrite: 0 },
    outputTokens: { total: 6, text: 6, reasoning: 0 },
  };
}

/**
 * Decide the mock's response for one `doGenerate`/`doStream` call, given the
 * real `LanguageModelV4CallOptions` Mastra/AI-SDK built for this turn.
 *
 *  - Tools declared AND no prior tool-result message in the prompt yet =>
 *    emit a tool-call (the "decide to call a tool" step). The `calculate`
 *    tool specifically gets a syntactically valid arithmetic expression as
 *    its argument, because `tools`/`observability` modes attach a REAL
 *    executing `execute()` (lib/calculate-tool.mjs) that Mastra actually
 *    runs against this argument even under the stub -- a schema-fabricated
 *    non-numeric string would make that real execution throw.
 *  - `responseFormat.type === 'json'` => fabricate an object from the
 *    supplied JSON-schema and return it as the text content (the shape
 *    `structured` mode's `agent.generate(..., { structuredOutput })` expects
 *    back from the model).
 *  - Otherwise => a fixed plain-text reply.
 */
function respond({ prompt, tools, responseFormat }) {
  if (Array.isArray(tools) && tools.length > 0 && !hasToolResult(prompt)) {
    const tool = tools.find((t) => t.name === 'calculate') || tools[0];
    const input = tool.name === 'calculate' ? { expression: '2 + 2' } : fabricate(tool.inputSchema);
    return {
      content: [
        {
          type: 'tool-call',
          toolCallId: 'arena-stub-call-1',
          toolName: tool.name,
          input: JSON.stringify(input),
        },
      ],
      finishReason: 'tool-calls',
      usage: stubUsage(),
      warnings: [],
    };
  }

  if (responseFormat && responseFormat.type === 'json') {
    const object = fabricate(responseFormat.schema);
    return {
      content: [{ type: 'text', text: JSON.stringify(object) }],
      finishReason: 'stop',
      usage: stubUsage(),
      warnings: [],
    };
  }

  return {
    content: [{ type: 'text', text: STUB_TEXT }],
    finishReason: 'stop',
    usage: stubUsage(),
    warnings: [],
  };
}

async function* toStreamParts(result) {
  yield { type: 'stream-start', warnings: result.warnings };
  for (const part of result.content) {
    if (part.type === 'text') {
      yield { type: 'text-start', id: 'arena-stub-text' };
      yield { type: 'text-delta', id: 'arena-stub-text', delta: part.text };
      yield { type: 'text-end', id: 'arena-stub-text' };
    } else if (part.type === 'tool-call') {
      yield part;
    }
  }
  yield { type: 'finish', finishReason: result.finishReason, usage: result.usage };
}

/** Build a fresh offline `MockLanguageModelV4` for one adapter invocation. */
export function buildStubChatModel() {
  return new MockLanguageModelV4({
    modelId: 'arena-stub-mock',
    doGenerate: async (options) => respond(options),
    doStream: async (options) => {
      const result = respond(options);
      return {
        stream: new ReadableStream({
          async start(controller) {
            for await (const part of toStreamParts(result)) {
              controller.enqueue(part);
            }
            controller.close();
          },
        }),
      };
    },
  });
}
