// `stream` mode -- Mastra NATIVE real streaming via `agent.stream()`. Records
// `ttft_ms` (time to first streamed text chunk), a latency dimension a
// non-streaming `agent.generate()` call cannot expose. Usage is resolved from the
// stream result's `usage` promise once the stream completes.

import { Agent } from '@mastra/core/agent';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_INSTRUCTIONS = 'You are a concise, accurate assistant.';

export async function runStream(input, env) {
  const messages = buildMessages(input.messages);
  if (messages.length === 0) {
    throw new Error('stream mode requires at least one message');
  }
  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;

  const agent = new Agent({ name: 'arena-stream', instructions, model: env.chatModel });

  const startedAt = process.hrtime.bigint();
  const result = await agent.stream(messages);

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
