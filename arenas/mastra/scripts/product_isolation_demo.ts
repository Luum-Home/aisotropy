#!/usr/bin/env tsx
/**
 * Mastra arena — MULTI-PRODUCT ISOLATION probe (the 3rd/4th scope axis). RUNS OFFLINE.
 *
 * Run:  npm run product-isolation      (from arenas/mastra)
 *   or: node_modules/.bin/tsx scripts/product_isolation_demo.ts
 *
 * No API key, no network. Uses REAL @mastra/memory (Memory + LibSQLStore + LibSQLVector,
 * file-backed temp dbs). The only "fake" is a deterministic hash embedder for the
 * semantic-recall lever in step (d); no real model is called.
 *
 * ── WHAT THIS TEST HONESTLY SHOWS ───────────────────────────────────────────
 * aisotropy scopes every memory read/write by FOUR native axes propagated through
 * context.Context: (tenant_id, user_id, session_id, product_id). This probe asks a
 * narrower, fair question: can a Mastra developer isolate a THIRD/FOURTH axis
 * (product_id) that has no dedicated name in the framework?
 *
 * The honest answer is YES — a competent developer CAN prevent the cross-product
 * read. Mastra ships FOUR documented scoping levers:
 *     1. resourceId            (owner partition; ownership-validated on threads)
 *     2. threadId              (conversation partition)
 *     3. working-memory scope  ('resource' | 'thread')
 *     4. semanticRecall.filter (MongoDB-style metadata filter over the vector store,
 *                               e.g. the docs' own `projectId: { $eq: 'project-a' }`)
 *   Docs: https://mastra.ai/docs/memory/semantic-recall
 *         https://mastra.ai/docs/memory/working-memory
 *         https://mastra.ai/blog/building-multi-user-multi-channel-agents
 *
 * So this is NOT a framework defect and NOT a "leak the framework forces on you".
 * What this script executes:
 *   (a) POSITIVE CONTROL — productX under (tenantA,userA) reads its own marker.
 *   (b) NAIVE / DEFAULT-CONVENTION PATH — a developer who puts product_id in NONE
 *       of the four levers (it rides only as message metadata / free text) gets no
 *       partitioning: recall keys on resourceId+threadId, so the product tag is a
 *       scoping no-op. This is a developer omission, not a framework leak.
 *   (c) COMPETENT PATH #1 — fold product into the resourceId ("tenant:user:product").
 *       Mastra binds each threadId to its owning resourceId and THROWS
 *       validateThreadIsOwnedByResource on a cross-product read. The read is
 *       prevented. (This is the arena's own long-standing finding, now elevated.)
 *   (d) COMPETENT PATH #2 — semanticRecall.filter. On the vector path, recall keys
 *       on the vector store's metadata; filtering to productX's resource returns
 *       ONLY productX's message, refuting any claim that "the memory layer never
 *       keys on it". (Caveat below.)
 *
 * ── THE REAL, NARROWER THESIS (convention vs construction) ───────────────────
 * All four levers are real and work. But every one of them is a CONVENTION the
 * developer must remember to apply on each call: none is a SERVER-AUTHORITATIVE,
 * unforgeable, auto-propagated axis. aisotropy's `Scope{}` value object (unexported
 * fields, built from verified identity, carried via context.Context) makes a wrong
 * or forgotten product_id UNREPRESENTABLE by construction — it filters product_id
 * server-side on every read whether or not the developer thought about it. Mastra
 * gives you the levers; it does not wire product isolation for you by default.
 *
 * ── HONEST CAVEATS TO KEEP ──────────────────────────────────────────────────
 * • On the bare message-history recall path (semanticRecall OFF), the only extra
 *   filter Mastra exposes is `dateRange` — there is no per-call metadata filter
 *   there. Metadata/product scoping lives on the semantic-recall path (lever 4) or
 *   in the composite key (lever 1). That is fine; it just means you pick the right
 *   lever for the path you are on.
 * • The default message-embedding path indexes a FIXED metadata set on the vector
 *   ({message_id, thread_id, resource_id, role, content, created_at} — verified in
 *   this arena). So the filter in (d) scopes on the indexed `resource_id` (i.e. the
 *   composite key). To filter on a bespoke `productId` field you must own the vector
 *   upsert (the RAG / knowledge-ingestion path) so that field is actually indexed.
 *   Either way, product scoping is achievable — it is just convention, not default.
 */

import { Memory } from '@mastra/memory';
import { LibSQLStore, LibSQLVector } from '@mastra/libsql';
import * as os from 'node:os';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as crypto from 'node:crypto';

// ── offline plumbing ────────────────────────────────────────────────────────
const tmpDirs: string[] = [];
function makeMemory() {
  // Bare message-history memory (no vector): exercises levers 1+2 (resourceId/threadId).
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mastra-product-'));
  tmpDirs.push(dir);
  return new Memory({
    storage: new LibSQLStore({ id: 'store', url: 'file:' + path.join(dir, 'store.db') }),
    options: { lastMessages: 20, semanticRecall: false, workingMemory: { enabled: false } },
  });
}

/** Deterministic hash embedder (AI-SDK v5 EmbeddingModelV2). SYNTHETIC vectors, no network. */
function hashEmbed(text: string, dim = 64): number[] {
  const v = new Array(dim).fill(0);
  for (let i = 0; i < text.length; i++) { const c = text.charCodeAt(i); v[(c + i) % dim] += ((c % 13) - 6) / 7; }
  const n = Math.sqrt(v.reduce((s, x) => s + x * x, 0)) || 1;
  return v.map((x) => x / n);
}
const embedder: any = {
  specificationVersion: 'v2', provider: 'fake-embed', modelId: 'fake-hash-64', maxEmbeddingsPerCall: 100,
  async doEmbed({ values }: any) { return { embeddings: values.map((t: string) => hashEmbed(t)), usage: { tokens: 0 } }; },
};
function makeSemanticMemory() {
  // Vector-backed memory: exercises lever 4 (semanticRecall.filter over the vector store).
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mastra-product-sem-'));
  tmpDirs.push(dir);
  return new Memory({
    storage: new LibSQLStore({ id: 'store', url: 'file:' + path.join(dir, 'store.db') }),
    vector: new LibSQLVector({ id: 'vec', url: 'file:' + path.join(dir, 'vec.db') }),
    embedder,
    options: { lastMessages: 0, semanticRecall: { topK: 10, messageRange: 0, scope: 'resource' }, workingMemory: { enabled: false } },
  });
}
function cleanup() {
  for (const d of tmpDirs) { try { fs.rmSync(d, { recursive: true, force: true }); } catch {} }
}
function marker(prefix: string) {
  return `${prefix}${crypto.randomBytes(10).toString('hex')}`;
}
async function saveThread(memory: any, id: string, resourceId: string) {
  await memory.saveThread({ thread: { id, resourceId, title: id, createdAt: new Date(), updatedAt: new Date(), metadata: {} } });
}
function userMsg(id: string, threadId: string, resourceId: string, text: string) {
  return { id, threadId, resourceId, role: 'user', content: { format: 2, parts: [{ type: 'text', text }] }, createdAt: new Date() };
}
async function readBack(mem: any, threadId: string, resourceId: string): Promise<string> {
  const rec: any = await mem.recall({ threadId, resourceId, threadConfig: { lastMessages: 20 } });
  return (rec.messages ?? []).map((m: any) => JSON.stringify(m)).join('|');
}

const details: string[] = [];
function log(s: string) { console.log(s); details.push(s); }

// ── the probe ───────────────────────────────────────────────────────────────
async function run(): Promise<boolean> {
  const resourceId = 'tenantA:userA';   // (tenant_id, user_id) — the 2 axes Mastra names directly
  const threadId   = 'session-42';      // session_id
  const mk = marker('prod');

  console.log('='.repeat(72));
  console.log('Mastra — Multi-PRODUCT isolation probe (naive path vs the 4 real levers)');
  console.log('='.repeat(72));
  console.log();
  log(`[setup] Mastra names two scoping keys directly (resourceId, threadId) and`);
  log(`        exposes two more levers (working-memory scope, semanticRecall.filter).`);
  log(`        product_id has no dedicated NAME, but it can ride any of the four.`);
  log(`        resourceId="${resourceId}", threadId="${threadId}".`);
  console.log();

  // ── (a) POSITIVE CONTROL ──────────────────────────────────────────────────
  const mem = makeMemory();
  await saveThread(mem, threadId, resourceId);
  await mem.saveMessages({ messages: [
    userMsg('m1', threadId, resourceId, `[product=productX] secret: ${mk}`),
  ]});

  const asProductX = await readBack(mem, threadId, resourceId);
  const posOk = asProductX.includes(mk);
  if (posOk) log(`  (a) ✓ POSITIVE CONTROL: productX (tenantA/userA/session-42) read its own marker ${mk}`);
  else { log(`  (a) ✗ POSITIVE CONTROL FAILED — aborting`); return false; }
  console.log();

  // ── (b) NAIVE / DEFAULT-CONVENTION PATH (product in NONE of the levers) ────
  // The developer tags product only in message text/metadata and reuses the same
  // resourceId+threadId. recall keys on resourceId+threadId, so the product tag is
  // a scoping no-op: the SAME data comes back regardless of the product_id value.
  // This is a DEVELOPER OMISSION (no lever used), not a framework leak.
  const wrongProduct = 'productY';
  const asProductY = await readBack(mem, threadId, resourceId); // same keys → same data, by definition
  const naiveNoop = asProductY.includes(mk);
  if (naiveNoop) {
    log(`  (b) ⚠ NAIVE PATH (no lever used): a request tagged product_id="${wrongProduct}" as`);
    log(`      metadata — but reusing the same resourceId+threadId — still reads productX's`);
    log(`      marker ${mk}. recall keys on resourceId+threadId, so a product tag placed in`);
    log(`      NONE of the four levers does nothing. This is the developer failing to scope,`);
    log(`      not a framework defect: steps (c)/(d) show the read IS preventable.`);
  } else {
    log(`  (b) (unexpected) metadata-only product tag changed the read`);
  }
  console.log();

  // ── (c) COMPETENT PATH #1 — composite resourceId ("tenant:user:product") ──
  // Fold product into the resourceId. Mastra binds each threadId to its owning
  // resourceId and THROWS on a cross-product read. The read is PREVENTED.
  const mem2 = makeMemory();
  const ridX = 'tenantA:userA:productX';
  const ridWrong = 'tenantA:userA:productZZZ-typo';   // a different product folded into resourceId
  const mk2 = marker('cmp');
  await saveThread(mem2, threadId, ridX);
  await mem2.saveMessages({ messages: [ userMsg('c1', threadId, ridX, `compound secret: ${mk2}`) ]});

  const readX = await readBack(mem2, threadId, ridX);
  const xOk = readX.includes(mk2);

  let guardFired = false;
  let guardMsg = '';
  try {
    await readBack(mem2, threadId, ridWrong);   // productX's thread, wrong product resourceId
  } catch (e: any) {
    guardFired = true;
    guardMsg = String(e?.message ?? e).split('\n')[0];
  }
  log(`  (c) COMPETENT PATH #1 — product folded into resourceId: productX store holds marker=${xOk}`);
  if (guardFired) {
    log(`      reading the SAME thread under wrong product resourceId "${ridWrong}" → Mastra THREW:`);
    log(`        "${guardMsg}"`);
    log(`      → the cross-product read is PREVENTED. This is thread↔resourceId ownership`);
    log(`        validation, applied to the product folded into the resourceId.`);
  } else {
    log(`      reading under wrong product resourceId did NOT throw — no ownership guard fired.`);
  }
  console.log();

  // ── (d) COMPETENT PATH #2 — semanticRecall.filter over the vector store ───
  // The docs' own example is filter:{ projectId:{ $eq:'project-a' } }. The default
  // message-embedding path indexes {message_id, thread_id, resource_id, role,
  // content, created_at}, so here we filter on the indexed resource_id (the product
  // folded into the composite key). Recall returns ONLY productX's row — proving the
  // metadata-filter lever really keys on the axis, refuting "never keys on it".
  const sem = makeSemanticMemory();
  const semTid = 'sem-session';
  const semRidX = 'tenantA:userA:productX';
  const semRidY = 'tenantA:userA:productY';
  const mkX = marker('semX');
  const mkY = marker('semY');
  for (const [rid, mkv] of [[semRidX, mkX], [semRidY, mkY]] as const) {
    await saveThread(sem, `${rid}:${semTid}`, rid);
    await sem.saveMessages({ messages: [ userMsg(`${rid}-1`, `${rid}:${semTid}`, rid, `the secret color code is ${mkv}`) ]});
  }
  const recFiltered: any = await sem.recall({
    threadId: `${semRidX}:${semTid}`, resourceId: semRidX, vectorSearchString: 'what is the secret color code',
    threadConfig: { lastMessages: 0, semanticRecall: { topK: 10, messageRange: 0, scope: 'resource', filter: { resource_id: { $eq: semRidX } } } as any },
  });
  const filteredTxt = (recFiltered.messages ?? []).map((m: any) => JSON.stringify(m)).join('|');
  const filterScoped = filteredTxt.includes(mkX) && !filteredTxt.includes(mkY);
  log(`  (d) COMPETENT PATH #2 — semanticRecall.filter { resource_id: { $eq: "productX" } }:`);
  if (filterScoped) {
    log(`      recall returned productX's marker ${mkX} and NOT productY's ${mkY}.`);
    log(`      → the metadata filter (docs' own projectId:{$eq} lever) really keys on the axis.`);
  } else {
    log(`      filter did NOT scope as expected (hasX=${filteredTxt.includes(mkX)}, hasY=${filteredTxt.includes(mkY)}).`);
  }
  console.log();

  // ── absence proof, honestly framed ────────────────────────────────────────
  // There is no primitive NAMED product_id; product rides one of the four generic
  // levers above. That is the "convention, not a named axis" point — not "no scoping".
  const grepHits = grepFrameworkForProduct();
  log(`  • naming note: a primitive NAMED product_id in @mastra/{core,memory} = ${grepHits} hits.`);
  log(`    Product isolation is achieved by putting product on one of the four generic levers`);
  log(`    (resourceId / threadId / working-memory scope / semanticRecall.filter), not via a`);
  log(`    dedicated product axis. Real, but convention the developer must apply per call.`);

  return posOk && naiveNoop && xOk && guardFired && filterScoped;
}

function grepFrameworkForProduct(): number {
  // Count references to a *product-scoping* primitive in the shipped framework.
  // We look for "productId" as an API concept (not the npm word "product").
  const roots = ['node_modules/@mastra/core/dist', 'node_modules/@mastra/memory/dist'];
  let hits = 0;
  for (const r of roots) {
    const abs = path.resolve(r);
    if (!fs.existsSync(abs)) continue;
    const stack = [abs];
    while (stack.length) {
      const cur = stack.pop()!;
      let st: fs.Stats;
      try { st = fs.statSync(cur); } catch { continue; }
      if (st.isDirectory()) { for (const f of fs.readdirSync(cur)) stack.push(path.join(cur, f)); continue; }
      if (!/\.(js|d\.ts|mjs|cjs)$/.test(cur)) continue;
      let txt = '';
      try { txt = fs.readFileSync(cur, 'utf8'); } catch { continue; }
      const m = txt.match(/productId|product_id|productScope/g);
      if (m) hits += m.length;
    }
  }
  return hits;
}

// ── main ──────────────────────────────────────────────────────────────────
run().then((passed) => {
  cleanup();
  console.log();
  console.log('-'.repeat(72));
  console.log('VERDICT');
  console.log('-'.repeat(72));
  if (passed) {
    console.log('PASS — positive control verified; the NAIVE path (product in no lever) is a');
    console.log('no-op; and BOTH competent paths PREVENT the cross-product read:');
    console.log('  (c) composite resourceId → thread↔resource ownership guard throws;');
    console.log('  (d) semanticRecall.filter → recall keys on the axis and returns only productX.');
    console.log();
    console.log('HONEST VERDICT — convention, not construction. Mastra gives a competent developer');
    console.log('four real levers to isolate product_id, but none is a server-authoritative,');
    console.log('unforgeable, auto-propagated axis: each is a convention applied per call, and the');
    console.log('default path (product in none of them) silently does not partition. product_id has');
    console.log('no dedicated NAME in the framework — it must be smuggled onto a generic lever.');
    console.log('Contrast: aisotropy makes product_id a native context.Context axis of the Scope{}');
    console.log('value object, filtered server-side on every read whether or not the developer');
    console.log('remembered it — a wrong/forgotten product reads nothing, by construction.');
  } else {
    console.log('FAIL — could not reproduce the expected findings; see details above.');
  }
  process.exit(passed ? 0 : 1);
}).catch((e) => {
  cleanup();
  console.error('ERROR:', e);
  process.exit(2);
});
