// Runnable usage example for the typed aisotropyd TS client.
//
// Run against a local NoAuth server:
//   AISOTROPY_ALLOW_NOAUTH=1 AISOTROPY_ADDR=127.0.0.1:8080 go run ./cmd/aisotropyd
//   npx tsx src/example.ts            # (tsx not vendored here; `npm run typecheck` is the CI gate)
//
// Env: AISOTROPY_BASE_URL (default http://127.0.0.1:8080), AISOTROPY_TOKEN (optional).

import { chat, streamChat, type ClientOptions, type ChatRequest } from "./client.js";

const opts: ClientOptions = {
  baseUrl: process.env.AISOTROPY_BASE_URL ?? "http://127.0.0.1:8080",
  token: process.env.AISOTROPY_TOKEN, // required unless server is in NoAuth mode
};

const TENANT = "acme";
const USER = "alice";

const req: ChatRequest = {
  prompt: "Give me a one-line status.",
  product_id: "analytics-v1",
  session_id: "session-42",
  // scopes gate tool access, e.g. ["write:tasks"]; omit for read-only chat.
};

async function main(): Promise<void> {
  // 1) Non-streaming, structured reply.
  const out = await chat(opts, TENANT, USER, req);
  console.log(`[chat] tenant=${out.tenant_id} stub=${out.stub}\n  reply: ${out.reply}`);

  // 2) SSE streaming — tokens arrive incrementally.
  process.stdout.write("[stream] ");
  await streamChat(
    opts,
    TENANT,
    USER,
    { ...req, prompt: "Stream a short greeting." },
    {
      onToken: (ev) => process.stdout.write(ev.text),
      onDone: (ev) => console.log(`\n[stream] done — served by tenant=${ev.tenant_id}`),
      onError: (ev) => console.error(`\n[stream] error: ${ev.message}`),
    },
  );
}

main().catch((err) => {
  console.error(err instanceof Error ? err.message : err);
  process.exitCode = 1;
});
