// `qa` mode -- a single-shot Mastra `Agent` turn.
//
// One `agent.generate()` call. When the arena's `tool_calling` task supplies
// `tools` (JSON-schema declarations), this mode builds them as Mastra tools with
// NO `execute` and caps the loop at `maxSteps: 1`, so the agent surfaces its
// DECIDED tool call (name + arguments) single-shot without running a full agent
// loop -- that is what the arena's tool_calling task kind grades (native tool
// *selection*). The `tools` mode handles the full multi-step executed loop.

import { Agent } from '@mastra/core/agent';
import { createTool } from '@mastra/core/tools';
import { buildMessages, extractTokenCounts, mapToolCalls, msSince } from '../output.mjs';

const DEFAULT_INSTRUCTIONS = 'You are a concise, accurate assistant.';

function buildDeclaredTools(toolSpecs) {
  const toolSet = {};
  for (const spec of toolSpecs) {
    if (!spec || typeof spec.name !== 'string' || spec.name.length === 0) {
      continue;
    }
    // Mastra's createTool accepts a raw JSON-schema object as inputSchema. No
    // `execute` is attached: with maxSteps:1 the agent decides the call but does
    // not run it, matching the single-shot tool-selection grading.
    toolSet[spec.name] = createTool({
      id: spec.name,
      description: spec.description || '',
      inputSchema: spec.parameters || { type: 'object', properties: {} },
    });
  }
  return toolSet;
}

export async function runQa(input, env) {
  const instructions =
    typeof input.system === 'string' && input.system.length > 0
      ? input.system
      : DEFAULT_INSTRUCTIONS;
  const messages = buildMessages(input.messages);

  const hasTools = Array.isArray(input.tools) && input.tools.length > 0;
  const tools = hasTools ? buildDeclaredTools(input.tools) : undefined;

  const agent = new Agent({
    name: 'arena-qa',
    instructions,
    model: env.chatModel,
    ...(tools ? { tools } : {}),
  });

  const startedAt = process.hrtime.bigint();
  const result = await agent.generate(messages, hasTools ? { maxSteps: 1 } : undefined);
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    tool_calls: mapToolCalls(result.toolCalls),
  };
}
