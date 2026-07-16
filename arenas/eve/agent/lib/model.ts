// Offline, deterministic AI-SDK-v4 mock model so the whole eve runtime
// (Nitro routes + Workflow-SDK durable session + tool loop) runs with NO
// API key and NO network. This is the ONLY faked component; every other layer
// (session durability, tool dispatch, streaming, channel auth) is the real eve.
//
// Behaviour, decided purely from the prompt eve hands us:
//   * If a tool result for `orbit_memory` is already in the prompt  -> relay it
//     verbatim as the assistant's final text (so the HTTP caller can assert on it).
//   * Else if the latest user text starts with "MEM:"               -> emit a
//     tool-call to `orbit_memory` with the remainder as its `command`.
//   * Else                                                          -> greet,
//     echoing the user's text (proves memory hydration on multi-turn).
import { MockLanguageModelV4 } from "ai/test";

function lastUserText(prompt: any[]): string {
  let text = "";
  for (const msg of prompt) {
    if (msg?.role === "user" && Array.isArray(msg.content)) {
      for (const part of msg.content) {
        if (part?.type === "text" && typeof part.text === "string") text = part.text;
      }
    }
  }
  return text;
}

// Relay a tool result only when it is *more recent* than the last user message,
// i.e. we are on the second model step of the current turn. This keeps the mock
// correct even if prior turns left tool-results in the history.
function pendingToolOutput(prompt: any[]): unknown | undefined {
  let lastUser = -1;
  let lastTool = -1;
  let out: unknown | undefined;
  prompt.forEach((msg, i) => {
    if (msg?.role === "user") lastUser = i;
    if (msg?.role === "tool" && Array.isArray(msg.content)) {
      for (const part of msg.content) {
        if (part?.type === "tool-result") {
          lastTool = i;
          out = part.output?.value ?? part.output ?? part.result;
        }
      }
    }
  });
  return lastTool > lastUser ? out : undefined;
}

function usage() {
  return {
    inputTokens: { total: 11 },
    outputTokens: { total: 7 },
    totalTokens: { total: 18 },
  } as any;
}

function textStream(text: string): ReadableStream<any> {
  const id = "t1";
  return new ReadableStream({
    start(c) {
      c.enqueue({ type: "text-start", id });
      c.enqueue({ type: "text-delta", id, delta: text });
      c.enqueue({ type: "text-end", id });
      c.enqueue({ type: "finish", finishReason: "stop", usage: usage() });
      c.close();
    },
  });
}

function toolCallStream(command: string): ReadableStream<any> {
  const toolCallId = "call_" + Math.random().toString(36).slice(2, 10);
  const input = JSON.stringify({ command });
  return new ReadableStream({
    start(c) {
      c.enqueue({ type: "tool-input-start", id: toolCallId, toolName: "orbit_memory" });
      c.enqueue({ type: "tool-input-delta", id: toolCallId, delta: input });
      c.enqueue({ type: "tool-input-end", id: toolCallId });
      c.enqueue({ type: "tool-call", toolCallId, toolName: "orbit_memory", input });
      c.enqueue({ type: "finish", finishReason: "tool-calls", usage: usage() });
      c.close();
    },
  });
}

export const orbitMockModel = new MockLanguageModelV4({
  modelId: "orbit-mock/deterministic",
  doStream: async (options: any) => {
    const prompt = options.prompt as any[];
    const toolOut = pendingToolOutput(prompt);
    if (toolOut !== undefined) {
      return { stream: textStream("RESULT: " + JSON.stringify(toolOut)) };
    }
    const user = lastUserText(prompt).trim();
    if (user.startsWith("MEM:")) {
      return { stream: toolCallStream(user.slice(4).trim()) };
    }
    return { stream: textStream("Orbit here. You said: " + user) };
  },
}) as any;
