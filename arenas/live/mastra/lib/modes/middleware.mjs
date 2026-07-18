// `middleware` mode -- proves Mastra's model-level middleware seam.
//
// MECHANISM (be honest which, per the task): this uses the Vercel AI SDK's
// `wrapLanguageModel` from the `ai` package, NOT a Mastra `inputProcessors`/
// `outputProcessors` pipeline. Mastra ships both seams (its own Agent-level
// processors AND, because an Agent's `model` is a plain AI SDK
// `LanguageModel`, standard AI SDK model middleware) -- this mode picks
// `wrapLanguageModel` because it lets ONE middleware object implement both
// the inbound transform (`transformParams`) and the outbound counter
// (`wrapGenerate`) against the exact params/result the real model sees,
// rather than splitting inbound/outbound across two separate Mastra
// processor classes with their own message-list abstraction. This is the
// mechanism `@mastra/core`'s own `Agent` uses under the hood to run any
// model, so it is a "Mastra-native" wrapping point in the sense that it sits
// exactly where the framework hands control to the AI SDK, not a bolted-on
// side channel.
//
// INBOUND transform: `transformParams` runs before the model ever sees the
// call. It finds the `{ role: 'system', content }` message in the
// already-compiled `LanguageModelV4Prompt` (Mastra turns `instructions` into
// exactly this system message) and appends the sentinel "\n[ARENA_MW]" to
// its content.
//
// OUTBOUND count: `wrapGenerate` runs once per real `doGenerate` call
// (Mastra's Agent may call this more than once in a multi-step tool loop;
// this mode runs a single-shot `qa` turn, so it fires exactly once here) and
// increments an invocation counter before delegating to the real
// `doGenerate`.

import { Agent } from '@mastra/core/agent';
import { wrapLanguageModel } from 'ai';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const SENTINEL = '\n[ARENA_MW]';
const DEFAULT_INSTRUCTIONS = 'You are a concise, accurate assistant.';

export async function runMiddleware(input, env) {
  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;
  const messages = buildMessages(input.messages);
  if (messages.length === 0) {
    throw new Error('middleware mode requires at least one message');
  }

  let inboundFired = false;
  let invocations = 0;

  const middleware = {
    // Inbound: mutate the system prompt before the model ever sees it.
    transformParams: async ({ params }) => {
      inboundFired = true;
      if (!Array.isArray(params.prompt)) {
        return params;
      }
      const prompt = params.prompt.map((message) =>
        message && message.role === 'system'
          ? { ...message, content: `${message.content}${SENTINEL}` }
          : message
      );
      return { ...params, prompt };
    },
    // Outbound: count each real generate call, then delegate unchanged.
    wrapGenerate: async ({ doGenerate }) => {
      invocations += 1;
      console.error(`PROGRESS: middleware -- outbound wrapGenerate fired (invocation ${invocations})`);
      return doGenerate();
    },
  };

  const wrappedModel = wrapLanguageModel({ model: env.chatModel, middleware });

  const agent = new Agent({ name: 'arena-middleware', instructions, model: wrappedModel });

  console.error('PROGRESS: middleware -- running one qa turn through the wrapped model');
  const startedAt = process.hrtime.bigint();
  const result = await agent.generate(messages);
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const text = typeof result.text === 'string' ? result.text : '';
  const outboundFired = invocations > 0;

  console.error(
    `PROGRESS: middleware -- inbound_fired=${inboundFired} outbound_fired=${outboundFired} invocations=${invocations}`
  );

  return {
    reply: text,
    // Literal top-level fields the task's stdout contract asks for, additive
    // alongside the shared baseOutput envelope (framework/mode/model/error
    // etc.) every other mode also returns -- see lib/output.mjs.
    text,
    middleware: {
      mechanism: 'wrapLanguageModel',
      inbound_fired: inboundFired,
      outbound_fired: outboundFired,
      invocations,
      supported: true,
    },
    input_tokens,
    output_tokens,
    latency_ms,
  };
}
