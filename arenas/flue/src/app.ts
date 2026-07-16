// Orbit's Flue application entrypoint (Hono). Two jobs:
//   1. Register an OFFLINE fake model provider so the whole arena runs with no
//      API key. Flue's documented custom-provider path (guide/models §"Custom
//      providers") is exactly this: a provider id speaking the `openai-completions`
//      wire protocol at a baseUrl. Our fake server (scripts/fake-model.mjs) speaks
//      that protocol and echoes deterministic replies.
//   2. Mount flue(). The per-instance authorization guard lives in the agent's
//      `route` export (src/agents/orbit.ts) — Flue calls it before every prompt.
import { registerProvider } from '@flue/runtime';
import { flue } from '@flue/runtime/routing';
import { Hono } from 'hono';

registerProvider('orbit-fake', {
	api: 'openai-completions',
	baseUrl: process.env.ORBIT_FAKE_MODEL_URL || 'http://127.0.0.1:4117/v1',
	apiKey: 'offline-no-key',
	models: { 'echo-1': { contextWindow: 8192, maxTokens: 1024 } },
});

const app = new Hono();
app.get('/health', (c) => c.json({ ok: true, arena: 'flue' }));
app.route('/', flue());

export default app;
