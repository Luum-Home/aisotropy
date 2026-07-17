// `structured` mode -- AI SDK NATIVE structured output: generateObject() against a
// zod schema, validated by the SDK before this adapter ever sees it (a schema
// violation throws NoObjectGeneratedError rather than returning malformed JSON).

import { generateObject } from 'ai';
import { z } from 'zod';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

const DEFAULT_SYSTEM =
  'Extract structured information from the user\'s message: a one-sentence summary, ' +
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
  const system = typeof input.system === 'string' && input.system.length > 0
    ? input.system
    : DEFAULT_SYSTEM;

  const startedAt = process.hrtime.bigint();
  const result = await generateObject({
    model: env.chatModel,
    system,
    messages,
    schema: NoteSchema,
  });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  return {
    reply: JSON.stringify(result.object),
    structured: result.object,
    input_tokens,
    output_tokens,
    latency_ms,
  };
}
