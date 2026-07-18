// `tools` mode -- a REAL multi-step agentic tool loop, not single-shot capture.
//
// The `calculate` tool has a real executing `execute` (lib/calculate-tool.mjs ->
// lib/calculator.mjs, no eval), and `maxSteps > 1` lets Mastra actually run the
// tool and continue the conversation with the tool's real output to produce a
// final natural-language answer -- exercising Mastra's native multi-step agent
// loop instead of just capturing a decided-but-unexecuted tool call (that is what
// `qa` mode's tool_calling task kind measures; see lib/modes/qa.mjs).

import { Agent } from '@mastra/core/agent';
import { buildCalculateTool, CALCULATE_SYSTEM } from '../calculate-tool.mjs';
import { buildMessages, extractTokenCounts, mapToolCalls, msSince } from '../output.mjs';

const MAX_STEPS = 5;

export async function runTools(input, env) {
  const messages = buildMessages(input.messages);
  const instructions =
    typeof input.system === 'string' && input.system.length > 0 ? input.system : CALCULATE_SYSTEM;

  const agent = new Agent({
    name: 'arena-tools',
    instructions,
    model: env.chatModel,
    tools: { calculate: buildCalculateTool() },
  });

  const startedAt = process.hrtime.bigint();
  const result = await agent.generate(messages, { maxSteps: MAX_STEPS });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    tool_calls: mapToolCalls(result.toolCalls, result.toolResults),
  };
}
