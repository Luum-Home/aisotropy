// Minimal "hello agent" app on Mastra (@mastra/core) — footprint benchmark subject.
//
// The MODEL IS HELD CONSTANT as a deterministic offline fake (the same v2
// language-model shape the Mastra arena uses): no provider, no network. This
// measures the Mastra + Node.js runtime FOOTPRINT, not model behaviour.
//
// Boot sequence, matched to the other subjects: import the framework -> build
// one Agent -> run ONE warmup turn (best-effort) -> serve GET /healthz on :8080.
import http from 'node:http';
import { Agent } from '@mastra/core/agent';

let calls = 0;
const fakeModel = {
  specificationVersion: 'v2',
  provider: 'fake-offline',
  modelId: 'fake-synthetic-model',
  supportedUrls: {},
  async doGenerate() {
    calls++;
    const reply = 'ACK: deterministic offline reply. No network was touched.';
    return {
      content: [{ type: 'text', text: reply }],
      finishReason: 'stop',
      usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      warnings: [],
    };
  },
  async doStream() {
    throw new Error('doStream unused in footprint bench');
  },
};

const agent = new Agent({
  name: 'footprint-agent',
  instructions: 'Offline stub agent for the footprint benchmark.',
  model: fakeModel,
});

// One warmup turn through the real Agent. Best-effort: the footprint (image /
// deps / RSS) is captured regardless, and the framework is already imported +
// the Agent constructed by this point, which is the tax being measured.
try {
  await agent.generate('benchmark warmup');
} catch (e) {
  console.error('warmup turn skipped:', e.message);
}

http
  .createServer((req, res) => {
    if (req.url === '/healthz') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end('{"status":"ok"}');
    } else {
      res.writeHead(404);
      res.end();
    }
  })
  .listen(8080, () => console.error('ready'));
