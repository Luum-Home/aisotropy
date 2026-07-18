// The `calculate` Mastra tool, shared by `tools` and `observability` modes.
//
// A REAL, executable arithmetic tool: its `execute` runs the recursive-descent
// evaluator in `lib/calculator.mjs` (NOT eval()) over the model-supplied
// expression and returns the exact numeric result, which Mastra folds back into
// the conversation for a final natural-language answer -- a genuine multi-step
// agentic loop, not a captured-but-unexecuted tool call.

import { createTool } from '@mastra/core/tools';
import { z } from 'zod';
import { evaluateArithmetic } from './calculator.mjs';

export const CALCULATE_SYSTEM =
  'You are a precise assistant. For ANY arithmetic, call the "calculate" tool instead ' +
  "of computing it yourself -- never guess a numeric result. After you see the tool's " +
  'result, give the final answer in one short sentence.';

/** Build a fresh `calculate` Mastra tool with a real executing implementation. */
export function buildCalculateTool() {
  return createTool({
    id: 'calculate',
    description:
      'Evaluate a real arithmetic expression (+, -, *, /, parentheses, unary +/-) and ' +
      'return its exact numeric result.',
    inputSchema: z.object({
      expression: z
        .string()
        .describe('An arithmetic expression, e.g. "(23847 * 9182) / 17"'),
    }),
    outputSchema: z.object({
      expression: z.string(),
      result: z.number(),
    }),
    execute: async ({ expression }) => {
      const value = evaluateArithmetic(expression);
      return { expression, result: value };
    },
  });
}
