#!/usr/bin/env node
/**
 * Mastra arena — CONTAINERIZED minimal HTTP server (mock model, no API key).
 *
 * This is the smallest real Mastra app that the arena's isolation smoke can be
 * driven against over HTTP. It boots two REAL framework primitives:
 *
 *   1. A Mastra `Agent` (@mastra/core/agent) wired to a hand-written AI-SDK v5
 *      mock LanguageModelV2 — the same offline stub the native demos use
 *      (scripts/feature_parity_demo.ts). No ANTHROPIC/OPENAI key is required;
 *      `/chat` proves the minimal app serves a model response deterministically.
 *
 *   2. Real `@mastra/memory` (Memory + LibSQLStore) — the authoritative isolation
 *      primitive exercised by scripts/product_isolation_demo.ts. Memory is keyed by
 *      `resourceId` (the tenant/owner partition) and `threadId`. Mastra binds each
 *      thread to its owning resourceId and validates ownership on read, so a
 *      cross-tenant recall of another tenant's thread is refused — that is the
 *      assertion docker/run.sh drives against THIS container.
 *
 * Endpoints (plain node:http, zero web deps so the image stays minimal):
 *   GET  /healthz                      -> {status:"ok"} once memory+agent are ready
 *   POST /chat        {prompt}         -> {text}          (mock model, no key)
 *   POST /remember    {tenant,thread,secret}
 *                                      -> {ok:true, resourceId}
 *   GET  /recall?tenant=&thread=       -> {messages:[...], secrets:[...], isolated?:true}
 *
 * The isolation contract, HTTP-observable:
 *   - recall for the OWNING tenant returns its stored secret (positive control).
 *   - recall for a DIFFERENT tenant on the same threadId returns NO messages
 *     (Mastra's thread<->resource ownership guard throws; the server reports it as
 *     {isolated:true}). The cross-tenant read cannot see tenant-A's secret.
 */

import { createServer } from 'node:http';
import { Agent } from '@mastra/core/agent';
import { Memory } from '@mastra/memory';
import { LibSQLStore } from '@mastra/libsql';
import * as os from 'node:os';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as crypto from 'node:crypto';

const PORT = Number(process.env.PORT || 4111);

// ── AI-SDK v5 mock model (no network, no key) ────────────────────────────────
// Mirrors the offline stub in scripts/feature_parity_demo.ts:makeTextModel.
function makeMockModel(reply = 'ok (deterministic offline reply)') {
  const m = {
    specificationVersion: 'v2',
    provider: 'fake',
    modelId: 'fake-text',
    supportedUrls: {},
    async doGenerate(opts) {
      const promptStr = JSON.stringify(opts?.prompt ?? '');
      const inputTokens = Math.max(1, Math.floor(promptStr.length / 4));
      const outputTokens = Math.max(1, Math.floor(reply.length / 4));
      return {
        content: [{ type: 'text', text: reply }],
        finishReason: 'stop',
        usage: { inputTokens, outputTokens, totalTokens: inputTokens + outputTokens },
        warnings: [],
      };
    },
    async doStream() { throw new Error('doStream not used by this arena server'); },
  };
  return m;
}

const agent = new Agent({
  name: 'arena-mock',
  instructions: 'You are the containerized Mastra arena agent. Reply deterministically.',
  model: makeMockModel(),
});

// ── Real @mastra/memory (LibSQLStore) — the isolation primitive ──────────────
const dataDir = process.env.AISOTROPY_DATA_DIR
  || fs.mkdtempSync(path.join(os.tmpdir(), 'mastra-arena-'));
fs.mkdirSync(dataDir, { recursive: true });

const memory = new Memory({
  storage: new LibSQLStore({ id: 'store', url: 'file:' + path.join(dataDir, 'store.db') }),
  options: { lastMessages: 20, semanticRecall: false, workingMemory: { enabled: false } },
});

function userMsg(id, threadId, resourceId, text) {
  return {
    id, threadId, resourceId, role: 'user',
    content: { format: 2, parts: [{ type: 'text', text }] },
    createdAt: new Date(),
  };
}

function extractText(msg) {
  // Mastra returns messages in a couple of shapes across paths; be lenient.
  if (typeof msg?.content === 'string') return msg.content;
  const parts = msg?.content?.parts ?? msg?.parts;
  if (Array.isArray(parts)) return parts.map((p) => p?.text ?? '').join(' ');
  return JSON.stringify(msg);
}

// ── HTTP plumbing ────────────────────────────────────────────────────────────
function send(res, status, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(body);
}

function readBody(req) {
  return new Promise((resolve) => {
    let data = '';
    req.on('data', (c) => { data += c; });
    req.on('end', () => {
      if (!data) return resolve({});
      try { resolve(JSON.parse(data)); } catch { resolve({}); }
    });
  });
}

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, `http://localhost:${PORT}`);

    if (req.method === 'GET' && url.pathname === '/healthz') {
      return send(res, 200, { status: 'ok', model: 'fake-text', memory: 'libsql' });
    }

    if (req.method === 'POST' && url.pathname === '/chat') {
      const { prompt } = await readBody(req);
      const r = await agent.generate(String(prompt ?? 'hello'));
      return send(res, 200, { text: r?.text ?? '' });
    }

    if (req.method === 'POST' && url.pathname === '/remember') {
      const { tenant, thread, secret } = await readBody(req);
      if (!tenant || !thread || !secret) {
        return send(res, 400, { error: 'tenant, thread, secret required' });
      }
      // resourceId IS the tenant/owner partition (Mastra's authoritative key).
      const resourceId = String(tenant);
      const threadId = String(thread);
      await memory.saveThread({
        thread: {
          id: threadId, resourceId, title: threadId,
          createdAt: new Date(), updatedAt: new Date(), metadata: {},
        },
      });
      await memory.saveMessages({
        messages: [userMsg(crypto.randomUUID(), threadId, resourceId, `secret: ${secret}`)],
      });
      return send(res, 200, { ok: true, resourceId, threadId });
    }

    if (req.method === 'GET' && url.pathname === '/recall') {
      const tenant = url.searchParams.get('tenant');
      const thread = url.searchParams.get('thread');
      if (!tenant || !thread) {
        return send(res, 400, { error: 'tenant and thread query params required' });
      }
      try {
        const rec = await memory.recall({
          threadId: String(thread), resourceId: String(tenant),
          threadConfig: { lastMessages: 20 },
        });
        const messages = rec?.messages ?? [];
        const texts = messages.map(extractText);
        const secrets = texts
          .map((t) => (t.match(/secret:\s*(\S+)/) || [])[1])
          .filter(Boolean);
        return send(res, 200, { messages: texts, secrets });
      } catch (e) {
        // Mastra's validateThreadIsOwnedByResource fires on a cross-tenant read:
        // the guard REFUSED the read. Surface it as an explicit isolation signal.
        return send(res, 200, {
          messages: [], secrets: [], isolated: true,
          guard: String(e?.message ?? e).split('\n')[0],
        });
      }
    }

    return send(res, 404, { error: 'not found', path: url.pathname });
  } catch (e) {
    return send(res, 500, { error: String(e?.message ?? e) });
  }
});

server.listen(PORT, () => {
  console.log(`[mastra-arena] containerized server listening on :${PORT} (mock model, no key)`);
  console.log(`[mastra-arena] data dir: ${dataDir}`);
});
