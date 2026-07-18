// `middleware` mode -- the REFERENCE demonstration of the AI SDK's NATIVE
// `wrapLanguageModel({ model, middleware })` mechanism (`ai` package, see
// https://ai-sdk.dev/docs/ai-sdk-core/middleware). This is exactly the
// pattern the AI SDK documents for intercepting a language model call: a
// `LanguageModelMiddleware` object with `transformParams` (runs BEFORE the
// underlying model ever sees the call) and `wrapGenerate`/`wrapStream` (runs
// AROUND the underlying model call).
//
// Two OBSERVABLE transforms, wired around ONE real generateText() turn:
//   INBOUND  (transformParams) -- appends the sentinel "\n[ARENA_MW]" to the
//            system prompt before the model call is made. Provable because
//            the transform mutates `params.prompt`'s `role: 'system'` entry
//            -- exactly the shape `LanguageModelV4CallOptions.prompt` uses
//            (an array of `{role, content}` messages; the AI SDK folds the
//            high-level `system` string into that array's first entry).
//   OUTBOUND (wrapGenerate) -- intercepts the real `doGenerate` call and
//            counts that the middleware actually fired around it. `qa` mode
//            (unchanged) makes the same call with NO middleware at all, so
//            comparing the two is the honest A/B for "does wrapLanguageModel
//            actually run" rather than a no-op wrapper that always claims
//            success.
//
// `wrapStream` is also wired (fires if a caller drives this same wrapped
// model through streamText() elsewhere), but this mode's own turn uses
// generateText(), so `wrapGenerate` is what's expected to fire here.

import { generateText, wrapLanguageModel } from 'ai';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const SENTINEL = '\n[ARENA_MW]';

function appendSentinelToSystem(prompt) {
  if (!Array.isArray(prompt)) {
    return prompt;
  }
  return prompt.map((message) => {
    if (message && message.role === 'system' && typeof message.content === 'string') {
      return { ...message, content: `${message.content}${SENTINEL}` };
    }
    return message;
  });
}

function buildArenaMiddleware(counters) {
  return {
    // INBOUND: runs before the underlying model sees the call.
    transformParams: async ({ type, params, model }) => {
      counters.inboundFired = true;
      console.error(
        `PROGRESS: middleware -- transformParams (type=${type}, model=${model.modelId}) appending sentinel to system prompt`
      );
      return { ...params, prompt: appendSentinelToSystem(params.prompt) };
    },
    // OUTBOUND: wraps the real (transformed-params) doGenerate call.
    wrapGenerate: async ({ doGenerate }) => {
      counters.outboundFired = true;
      counters.invocations += 1;
      console.error(`PROGRESS: middleware -- wrapGenerate fired (invocation #${counters.invocations})`);
      return doGenerate();
    },
    // OUTBOUND (stream path): wired for completeness -- this mode's own turn
    // uses generateText(), so wrapGenerate is what's expected to fire.
    wrapStream: async ({ doStream }) => {
      counters.outboundFired = true;
      counters.invocations += 1;
      console.error(`PROGRESS: middleware -- wrapStream fired (invocation #${counters.invocations})`);
      return doStream();
    },
  };
}

export async function runMiddleware(input, env) {
  const messages = buildMessages(input.messages);
  if (messages.length === 0) {
    throw new Error('middleware mode requires at least one message');
  }
  const system = typeof input.system === 'string' ? input.system : undefined;

  const counters = { inboundFired: false, outboundFired: false, invocations: 0 };

  console.error('PROGRESS: middleware -- wrapping chat model via wrapLanguageModel()');
  const wrappedModel = wrapLanguageModel({
    model: env.chatModel,
    middleware: buildArenaMiddleware(counters),
  });

  const startedAt = process.hrtime.bigint();
  const result = await generateText({ model: wrappedModel, system, messages });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const text = typeof result.text === 'string' ? result.text : '';

  console.error(
    `PROGRESS: middleware -- inbound_fired=${counters.inboundFired} outbound_fired=${counters.outboundFired} invocations=${counters.invocations}`
  );

  return {
    text,
    reply: text,
    input_tokens,
    output_tokens,
    latency_ms,
    middleware: {
      mechanism: 'wrapLanguageModel',
      inbound_fired: counters.inboundFired,
      outbound_fired: counters.outboundFired,
      invocations: counters.invocations,
      supported: true,
    },
  };
}
