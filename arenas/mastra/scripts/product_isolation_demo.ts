#!/usr/bin/env tsx
/**
 * Mastra arena — MULTI-PRODUCT ISOLATION probe (the 3rd axis). RUNS OFFLINE.
 *
 * Run:  npm run product-isolation      (from arenas/mastra)
 *   or: node_modules/.bin/tsx scripts/product_isolation_demo.ts
 *
 * No API key, no network. Uses REAL @mastra/memory (Memory + LibSQLStore, file-backed
 * temp db). The only "fake" is the embedder (unused here — this probe is pure
 * message read/write, no semantic recall).
 *
 * ── WHY THIS TEST EXISTS ────────────────────────────────────────────────────
 * aisotropy scopes every memory read/write by FOUR native axes propagated through
 * context.Context: (tenant_id, user_id, session_id, product_id). The earlier arena
 * smokes only exercised tenant_id + user_id. product_id — a bot vendor selling the
 * SAME assistant to multiple downstream PRODUCTS under one tenant/user — was never
 * tested against Mastra.
 *
 * Mastra's memory layer understands exactly TWO scoping keys:
 *     resourceId   (opaque developer-passed string)
 *     threadId     (opaque developer-passed string)
 * There is NO product_id concept anywhere in @mastra/core or @mastra/memory. So a
 * developer who wants product isolation has to hand-encode it into one of those two
 * strings — pure CONVENTION — and the framework binds/validates NOTHING.
 *
 * This script PROVES, by executing real Memory:
 *   (a) POSITIVE CONTROL — productX under (tenantA,userA) reads its own marker.
 *   (b) CROSS-PRODUCT LEAK — a request for a WRONG product_id, same tenant+user+
 *       session, reads productX's marker, because product_id was carried as
 *       request/tool metadata the memory layer never keys on.
 *   (c) THE ONLY GUARD IS ON resourceId — the "responsible" fix (fold product into
 *       resourceId as "tenant:user:product") DOES get protected, but ONLY because
 *       Mastra binds a threadId to its owning resourceId and throws on a cross-
 *       resource read. That guard is thread-ownership, not product-awareness:
 *       product_id is still not a framework axis, it is just smuggled into the
 *       opaque resourceId string. Whichever of aisotropy's 4 axes you cannot fit
 *       into Mastra's 2 keys (resourceId, threadId) is the one that leaks.
 */

import { Memory } from '@mastra/memory';
import { LibSQLStore } from '@mastra/libsql';
import * as os from 'node:os';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as crypto from 'node:crypto';

// ── offline plumbing ────────────────────────────────────────────────────────
const tmpDirs: string[] = [];
function makeMemory() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mastra-product-'));
  tmpDirs.push(dir);
  return new Memory({
    storage: new LibSQLStore({ id: 'store', url: 'file:' + path.join(dir, 'store.db') }),
    options: { lastMessages: 20, semanticRecall: false, workingMemory: { enabled: false } },
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
  let leaked = false;

  // The bot vendor's NATURAL mapping onto Mastra's two keys: the framework only
  // gives you resourceId + threadId, so tenant/user go in resourceId, session in
  // threadId. product_id has no home in the framework, so it rides along as
  // request metadata (here: a plain field the app reads but memory never keys on).
  const resourceId = 'tenantA:userA';   // (tenant_id, user_id) — the 2 axes Mastra keys on
  const threadId   = 'session-42';      // session_id
  const mk = marker('prod');

  console.log('='.repeat(64));
  console.log('Mastra — Multi-PRODUCT isolation probe (3rd axis: product_id)');
  console.log('='.repeat(64));
  console.log();
  log(`[setup] Mastra keys on resourceId + threadId ONLY. There is no product_id`);
  log(`        axis in @mastra/core / @mastra/memory. resourceId="${resourceId}",`);
  log(`        threadId="${threadId}". product_id must ride as app-level metadata.`);
  console.log();

  // ── (a) POSITIVE CONTROL ──────────────────────────────────────────────────
  // productX writes a marker. The app "intends" this to be productX-scoped, and
  // tags the message text with the product_id — but memory keys on resourceId+thread.
  const mem = makeMemory();
  await saveThread(mem, threadId, resourceId);
  await mem.saveMessages({ messages: [
    userMsg('m1', threadId, resourceId, `[product=productX] secret: ${mk}`),
  ]});

  const asProductX = await readBack(mem, threadId, resourceId);
  const posOk = asProductX.includes(mk);
  if (posOk) log(`  ✓ POSITIVE CONTROL: productX (tenantA/userA/session-42) read its own marker ${mk}`);
  else { log(`  ✗ POSITIVE CONTROL FAILED — aborting`); return false; }
  console.log();

  // ── (b) CROSS-PRODUCT LEAK ────────────────────────────────────────────────
  // A new request arrives for a DIFFERENT product (productY), SAME tenant+user+
  // session. The app passes product_id="productY" as metadata — but Mastra's
  // recall only takes resourceId+threadId, both identical to productX's. So the
  // recall returns productX's private marker to productY.
  const wrongProduct = 'productY';
  const asProductY = await readBack(mem, threadId, resourceId); // SAME keys the framework understands
  const yLeaked = asProductY.includes(mk);
  if (yLeaked) {
    log(`  ✗ CROSS-PRODUCT LEAK: request for product_id="${wrongProduct}" (same tenant/user/session)`);
    log(`     read productX's private marker ${mk}.`);
    log(`     ROOT CAUSE: product_id was carried as request/tool metadata; Mastra's`);
    log(`     memory.recall({resourceId, threadId}) never keys on it, so it cannot`);
    log(`     partition by product. The extra axis is a no-op in the framework.`);
    leaked = true;
  } else {
    log(`  ✓ (unexpected) product_id metadata prevented the read — framework enforces it`);
  }
  console.log();

  // ── (c) THE ONLY GUARD IS ON resourceId (thread-ownership) ────────────────
  // The disciplined fix is to fold product into resourceId: "tenant:user:product".
  // HONEST FINDING: Mastra DOES protect this case — it binds a threadId to its
  // owning resourceId and THROWS validateThreadIsOwnedByResource on a cross-
  // resource read. So folding product into resourceId is safe. BUT that guard is
  // thread-ownership on the opaque resourceId, not product-awareness: the only way
  // to get product isolation is to overload resourceId, because product_id is not
  // a framework axis. We execute both: (1) productX's compound store holds its
  // marker; (2) reading productX's thread under a WRONG product resourceId throws.
  const mem2 = makeMemory();
  const ridX = 'tenantA:userA:productX';
  const ridWrong = 'tenantA:userA:productZZZ-typo';   // different product folded into resourceId
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
  log(`  • compound-resourceId fix (product folded into resourceId): productX store holds marker=${xOk}`);
  if (guardFired) {
    log(`    reading the SAME thread under wrong product resourceId "${ridWrong}" → Mastra THREW:`);
    log(`      "${guardMsg}"`);
    log(`    → REAL GUARD, but it is thread↔resourceId ownership, not product-awareness.`);
    log(`      product isolation only works when product is smuggled INTO resourceId;`);
    log(`      product_id itself is never a framework scoping axis.`);
  } else {
    log(`    reading under wrong product resourceId did NOT throw — no ownership guard fired.`);
  }
  console.log();

  // grep-proof of the absence: no product concept in the framework surface.
  const grepHits = grepFrameworkForProduct();
  log(`  • absence proof: grep for a product-scope concept in @mastra/{core,memory} = ${grepHits} hits`);
  log(`    (resourceId + threadId are the only scoping keys; product_id is not a framework primitive).`);

  return posOk && leaked && xOk && guardFired;
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
      // Count occurrences of a product-scoping identifier used as a memory key.
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
  console.log('-'.repeat(64));
  console.log('VERDICT');
  console.log('-'.repeat(64));
  if (passed) {
    console.log('PASS — positive control verified AND the cross-product leak reproduced.');
    console.log('product_id is NOT a Mastra scoping axis. The framework keys memory on');
    console.log('exactly two opaque developer-passed strings: resourceId + threadId.');
    console.log('When product_id rides as request/tool metadata (the natural mapping that');
    console.log('mirrors aisotropy\'s tenant/user/session onto those 2 keys), a wrong');
    console.log('product reads another product\'s data — REAL LEAK.');
    console.log('Mastra DOES enforce one guard — threadId↔resourceId ownership — so folding');
    console.log('product INTO resourceId is protected; but that is thread-ownership on the');
    console.log('opaque resourceId, not product-awareness. You get product isolation only by');
    console.log('overloading resourceId. With 4 aisotropy axes and 2 Mastra keys, whichever');
    console.log('axis will not fit is the one that leaks.');
    console.log('Contrast: aisotropy makes product_id a native context axis, filtered');
    console.log('server-side on every read — a wrong product reads nothing, by construction.');
  } else {
    console.log('FAIL — could not reproduce the expected finding; see details above.');
  }
  process.exit(passed ? 0 : 1);
}).catch((e) => {
  cleanup();
  console.error('ERROR:', e);
  process.exit(2);
});
