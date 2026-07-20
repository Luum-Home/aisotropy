// `tools` mode -- a REAL multi-step agentic tool loop, not single-shot capture.
//
// `calculate` has a real `execute` function (lib/calculator.mjs, no eval), and
// `stopWhen: stepCountIs(N)` (N > 1) lets the AI SDK actually run the tool and
// continue the conversation with the tool's real output to produce a final
// natural-language answer -- exercising the SDK's native multi-step loop instead
// of just capturing a decided-but-unexecuted tool call (that's what `qa` mode's
// tool_calling task kind measures instead; see lib/modes/qa.mjs).

import { generateText, tool, stepCountIs } from 'ai';
import { z } from 'zod';
import { evaluateArithmetic } from '../calculator.mjs';
import { buildMessages, encodeToolResult, extractTokenCounts, msSince } from '../output.mjs';

const MAX_STEPS = 5;

const DEFAULT_SYSTEM =
  'You are a precise assistant. For ANY arithmetic, call the "calculate" tool instead ' +
  'of computing it yourself -- never guess a numeric result. After you see the tool\'s ' +
  'result, give the final answer in one short sentence.';

function buildCalculateTool() {
  return tool({
    description:
      'Evaluate a real arithmetic expression (+, -, *, /, parentheses, unary +/-) and ' +
      'return its exact numeric result.',
    inputSchema: z.object({
      expression: z
        .string()
        .describe('An arithmetic expression, e.g. "(23847 * 9182) / 17"'),
    }),
    execute: async ({ expression }) => {
      const value = evaluateArithmetic(expression);
      return { expression, result: value };
    },
  });
}

export async function runTools(input, env) {
  const messages = buildMessages(input.messages);
  const system = typeof input.system === 'string' && input.system.length > 0
    ? input.system
    : DEFAULT_SYSTEM;

  const startedAt = process.hrtime.bigint();
  const result = await generateText({
    model: env.chatModel,
    system,
    messages,
    tools: { calculate: buildCalculateTool() },
    stopWhen: stepCountIs(MAX_STEPS),
  });
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  const resultsByCallId = new Map(
    (result.toolResults || []).map((r) => [r.toolCallId, r])
  );
  // The arena contract's EXECUTED-tool signal is `tool_calls[].result`, a
  // non-empty STRING (arenas/live/runner/types.go `toolCall.Result`); the AI SDK
  // names the executed value `toolResults[].output`, so encode it here.
  const tool_calls = (result.toolCalls || []).map((call) => ({
    name: call.toolName,
    arguments: call.input !== undefined ? call.input : {},
    result: resultsByCallId.has(call.toolCallId)
      ? encodeToolResult(resultsByCallId.get(call.toolCallId).output)
      : '',
  }));

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    tool_calls,
  };
}
