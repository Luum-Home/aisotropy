// `structured` mode -- Mastra NATIVE structured output: `agent.generate(messages,
// { structuredOutput: { schema } })` against a zod schema, validated by Mastra
// before this adapter ever sees it. The parsed, schema-conformant object is
// returned on `result.object`.

import { Agent } from '@mastra/core/agent';
import { z } from 'zod';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_INSTRUCTIONS =
  "Extract structured information from the user's message: a one-sentence summary, " +
  'the overall sentiment, and any concrete follow-up action items (empty array if none).';

const NoteSchema = z.object({
  summary: z.string().describe('One-sentence summary of the user message'),
  sentiment: z.enum(['positive', 'neutral', 'negative']),
  action_items: z
    .array(z.string())
    .describe('Concrete follow-up actions implied by the message; empty array if none'),
});

export async function runStructured(input, env) {
  const messages = buildMessages(input.messages);
  if (messages.length === 0) {
    throw new Error('structured mode requires at least one message');
  }
  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;

  const agent = new Agent({ name: 'arena-structured', instructions, model: env.chatModel });

  const startedAt = process.hrtime.bigint();
  const result = await agent.generate(messages, { structuredOutput: { schema: NoteSchema } });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);
  const object = result.object !== undefined ? result.object : null;

  return {
    reply: object !== null ? JSON.stringify(object) : typeof result.text === 'string' ? result.text : '',
    structured: object,
    input_tokens,
    output_tokens,
    latency_ms,
  };
}
