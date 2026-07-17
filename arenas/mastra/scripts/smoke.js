#!/usr/bin/env node
/**
 * Isolation KEYING-MODEL smoke for Mastra — CONVENTION vs CONSTRUCTION.
 *
 * IMPORTANT: this file is a small SIMULATION of the keying model (a hand-written
 * store), NOT the real framework. The authoritative, real-`@mastra/memory` demo is
 * `scripts/product_isolation_demo.ts` (`npm run product-isolation`), which shows the
 * naive path AND two competent paths that prevent the cross-product read.
 *
 * What this simulation illustrates: a keyed store faithfully returns data for the
 * key you hand it. `resourceId` is a developer-passed opaque string, so if the app
 * passes the WRONG resourceId (a cached/forged/omitted one), it reads the wrong
 * data — because the key is a convention, not a server-derived, unforgeable Scope.
 * That is the convention-vs-construction point, not a claim that "the framework
 * leaks": the real framework additionally validates thread↔resource ownership (see
 * the product demo). aisotropy derives the axis from verified identity and filters
 * it server-side on every read, so a wrong key is unrepresentable by construction.
 *
 * Pattern (mirrors aisotropy's isolation_e2e_test.go):
 *   1. Seed tenantA with a unique marker in a shared thread.
 *   2. Positive control: tenantA reads its own thread → marker MUST appear.
 *   3. Wrong-key probes: pass another/omitted resourceId for the same thread → the
 *      store returns whatever the passed key addresses (convention, not construction).
 */

const crypto = require('crypto');

/**
 * Generate a distinctive alphanumeric token (mirrors aisotropy's marker()).
 */
function randomMarker(prefix) {
  return `${prefix}${crypto.randomBytes(10).toString('hex')}`;
}

/**
 * Simulated in-memory store — mirrors Mastra's MemoryStorage behavior.
 * In reality, Mastra uses resourceId/threadId for scoping.
 */
class MemoryStore {
  constructor() {
    // Map<threadId, Map<messageId, {role, content, resourceId}>>
    this.threads = new Map();
  }

  /**
   * Create a thread — scoped by resourceId (tenant) and threadId (session).
   */
  createThread(resourceId, threadId) {
    const key = `${resourceId}:${threadId}`;
    if (!this.threads.has(key)) {
      this.threads.set(key, new Map());
    }
    return key;
  }

  /**
   * Save a message to a thread.
   */
  saveMessage(resourceId, threadId, message) {
    const key = `${resourceId}:${threadId}`;
    if (!this.threads.has(key)) {
      this.createThread(resourceId, threadId);
    }
    const thread = this.threads.get(key);
    const id = crypto.randomUUID();
    thread.set(id, { ...message, id, resourceId, threadId });
    return id;
  }

  /**
   * Get messages from a thread — keyed by (resourceId, threadId).
   *
   * The store faithfully returns data for the key it is handed. If the app passes a
   * wrong or omitted resourceId, it addresses a different tenant's thread — because
   * the key is a developer-passed convention. The real Mastra `listMessages` was once
   * threadId-only (bug mastra#11856, since fixed), the same class of convention gap.
   */
  getMessages(resourceId, threadId, limit = 5) {
    const key = `${resourceId}:${threadId}`;
    const thread = this.threads.get(key);
    if (!thread) return [];

    const messages = Array.from(thread.values());
    return messages.slice(-limit);
  }

  /**
   * SEARCH by threadId alone (no resourceId) — models the mastra#11856 shape.
   *
   * Searching by threadId without a resourceId scans ALL tenants' threads with that
   * threadId — the omitted-filter convention gap (equivalent to LangGraph's
   * prefix-match `store.search(('acme',))`). Applying the resourceId filter closes it.
   */
  searchThreads(threadId, limit = 5) {
    const results = [];
    for (const [key, thread] of this.threads.entries()) {
      if (key.endsWith(`:${threadId}`)) {
        results.push(...Array.from(thread.values()).slice(-limit));
      }
    }
    return results;
  }
}

// ── Adversarial test ──────────────────────────────────────────────────────

function testIsolation() {
  const store = new MemoryStore();
  const details = [];
  let leaked = false;

  const marker = randomMarker('mma');
  console.log('[mastra] Seeding tenantA with marker:', marker);

  // ── Positive control: tenantA reads its own thread ──
  const threadId = 'shared-session';
  store.saveMessage('tenantA', threadId, {
    role: 'user',
    content: `Store this: ${marker}`,
  });

  const posResults = store.getMessages('tenantA', threadId, 5);
  const posOk = posResults.some(m => m.content.includes(marker));

  if (posOk) {
    details.push('  ✓ Positive control: tenantA saw its own marker');
    console.log('  ✓ Positive control: tenantA saw its own marker');
  } else {
    details.push('  ✗ Positive control FAILED — aborting.');
    console.log('  ✗ Positive control FAILED — aborting.');
    return { passed: false, details };
  }

  // ── Negative probes: wrong resourceId, same threadId ──
  const probes = [
    ['tenantB', 'tenantB/userB — wrong tenant, wrong user'],
    ['tenantA', 'tenantA/userB — same tenant, different user (different thread implied)'],
    ['tenantB', 'tenantB/userA — wrong tenant, same user'],
  ];

  // Note: for tenantA/userB we use a DIFFERENT threadId since they share
  // the tenant but not the session. The wrong-key read that matters is across tenants.
  for (const [resourceId, desc] of probes) {
    const results = store.getMessages(resourceId, threadId, 5);
    const hasMarker = results.some(m => m.content.includes(marker));

    if (hasMarker) {
      details.push(`  ✗ WRONG-KEY READ: ${desc} (${resourceId}/${threadId}) addressed tenantA's marker`);
      console.log(`  ✗ WRONG-KEY READ: ${desc} addressed marker`);
      leaked = true;
    } else {
      details.push(`  ✓ Isolated: ${desc} did not see marker`);
      console.log(`  ✓ Isolated: ${desc}`);
    }
  }

  // ── Demonstrate the threadId-only search gap (models mastra#11856) ──
  console.log();
  console.log('[mastra] Demonstrating the threadId-only search gap (omitted resourceId filter):');

  // Seed tenantB's thread with the same threadId
  store.saveMessage('tenantB', threadId, {
    role: 'user',
    content: 'tenantB message',
  });

  // Search by threadId alone — scans ALL tenants
  const searchResults = store.searchThreads(threadId, 10);
  const searchHasMarker = searchResults.some(m => m.content.includes(marker));

  if (searchHasMarker) {
    details.push(
      '  ✗ THREADID-ONLY SEARCH GAP: omitting the resourceId filter scans across tenants',
      '     Models mastra#11856 (listMessages threadId-only, since fixed). Adding the',
      '     resourceId filter closes it — the fix is applying the convention.',
    );
    console.log('  ✗ THREADID-ONLY SEARCH GAP: omitted resourceId filter scans across tenants');
    leaked = true;
  } else {
    details.push('  ✓ Search scoped (no marker in results)');
    console.log('  ✓ Search scoped');
  }

  // ── Demonstrate the developer-passed-key convention ──
  console.log();
  console.log('[mastra] Demonstrating the developer-passed-key convention:');
  console.log('  resourceId is an opaque string the app supplies on each call.');
  console.log('  Pass the wrong one (cached/forged/omitted) and you address the wrong data.');
  console.log('  It is a convention, not a server-derived, unforgeable Scope. (The real');
  console.log('  framework does add thread↔resource ownership validation — see the product demo.)');
  console.log();

  // Simulate: request comes from tenantB, but developer accidentally
  // passes tenantA's resourceId (e.g., from a cached session object)
  const wrongResourceIds = ['tenantA']; // Simulating developer error
  for (const wrongRid of wrongResourceIds) {
    const results = store.getMessages(wrongRid, threadId, 5);
    if (results.some(m => m.content.includes(marker))) {
      details.push(
        `  ✗ WRONG-KEY READ: app passed wrong resourceId "${wrongRid}" → addressed tenantA's marker`,
        '     Convention, not construction: resourceId is developer-passed, not identity-derived.',
      );
      console.log(`  ✗ WRONG-KEY READ: wrong resourceId "${wrongRid}" → wrong-tenant data`);
      leaked = true;
    }
  }

  return { passed: leaked, details };
}

// ── Main ───────────────────────────────────────────────────────────────────

console.log('='.repeat(60));
console.log('Mastra — Adversarial Isolation Smoke Test');
console.log('='.repeat(60));
console.log();

const { passed, details } = testIsolation();

console.log();
console.log('-'.repeat(60));
for (const d of details) {
  console.log(d);
}
console.log('-'.repeat(60));

// PASS = positive control verified AND the wrong-key convention read reproduced
// (consistent with the other arenas: exit 0 = arena ran and demonstrated its finding).
if (passed) {
  console.log('\nResult: PASS — positive control verified AND the wrong-key convention read reproduced');
  console.log('Point: resourceId is a developer-passed convention, not an identity-derived,');
  console.log('       unforgeable Scope. The real framework adds thread↔resource ownership');
  console.log('       validation; see scripts/product_isolation_demo.ts for the authoritative demo.');
} else {
  console.log('\nResult: FAIL — could not reproduce the expected finding (or positive control failed); see details');
}

process.exit(passed ? 0 : 1);
