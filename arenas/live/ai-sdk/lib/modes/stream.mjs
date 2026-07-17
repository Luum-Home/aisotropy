// `stream` mode -- AI SDK NATIVE real streaming via streamText(). Records
// `ttft_ms` (time to first streamed text chunk), a latency dimension a
// non-streaming generateText() call cannot expose.

import { streamText } from 'ai';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

export async function runStream(input, env) {
  const messages = buildMessages(input.messages);
  if (messages.length === 0) {
    throw new Error('stream mode requires at least one message');
  }
  const system = typeof input.system === 'string' ? input.system : undefined;

  const startedAt = process.hrtime.bigint();
  const result = streamText({
    model: env.chatModel,
    system,
    messages,
  });

  let ttft_ms = -1;
  let text = '';
  for await (const chunk of result.textStream) {
    if (ttft_ms === -1) {
      ttft_ms = msSince(startedAt);
    }
    text += chunk;
  }
  const latency_ms = msSince(startedAt);

  const usage = await result.usage;
  const { input_tokens, output_tokens } = extractTokenCounts(usage);

  return {
    reply: text,
    input_tokens,
    output_tokens,
    latency_ms,
    ttft_ms,
  };
}
