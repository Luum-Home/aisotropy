// `qa` mode -- the ORIGINAL single-shot adapter behavior, unchanged.
//
// One generateText() call, no multi-step tool-execution loop. Tools (if provided)
// are declared without an `execute` function so the AI SDK surfaces a decided tool
// call in the result instead of trying to run it -- this is the arena's
// `tool_calling` task kind (arenas/live/SPEC.md), which grades the model's native
// tool *selection*, single-shot, not a full agent loop (see `tools` mode for that).

import { generateText, tool, jsonSchema } from 'ai';
import { buildMessages, extractTokenCounts, msSince } from '../output.mjs';

function buildToolSet(toolSpecs) {
  const toolSet = {};
  for (const spec of toolSpecs) {
    if (!spec || typeof spec.name !== 'string' || spec.name.length === 0) {
      continue;
    }
    toolSet[spec.name] = tool({
      description: spec.description || '',
      inputSchema: jsonSchema(spec.parameters || { type: 'object', properties: {} }),
    });
  }
  return toolSet;
}

export async function runQa(input, env) {
  const system = typeof input.system === 'string' ? input.system : undefined;
  const messages = buildMessages(input.messages);

  const hasTools = Array.isArray(input.tools) && input.tools.length > 0;
  const toolSet = hasTools ? buildToolSet(input.tools) : undefined;

  const callOptions = { model: env.chatModel, system, messages };
  if (toolSet) {
    callOptions.tools = toolSet;
  }

  const startedAt = process.hrtime.bigint();
  const result = await generateText(callOptions);
  const latency_ms = msSince(startedAt);

  const { input_tokens, output_tokens } = extractTokenCounts(result.usage);

  const toolCalls = Array.isArray(result.toolCalls) ? result.toolCalls : [];
  const tool_calls = toolCalls.map((call) => ({
    name: call.toolName,
    arguments: call.input !== undefined ? call.input : {},
  }));

  return {
    reply: typeof result.text === 'string' ? result.text : '',
    input_tokens,
    output_tokens,
    latency_ms,
    tool_calls,
  };
}
