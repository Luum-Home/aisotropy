#!/usr/bin/env tsx
/**
 * Mastra arena — STEELMAN: what Mastra is GENUINELY GREAT at. RUNS OFFLINE, exit 0.
 *
 * Run:  npm run strengths       (from arenas/mastra)
 *   or: node_modules/.bin/tsx scripts/strengths_demo.ts
 *
 * No API key, no network. Every capability below is executed against the REAL pinned
 * framework (@mastra/core 1.51.0 · @mastra/memory 1.23.0 · @mastra/libsql), grounded in
 * a named exported symbol from the installed dist. Nothing here is asserted-but-unrun.
 *
 * ── WHY THIS FILE EXISTS ─────────────────────────────────────────────────────
 * The isolation probes (smoke.js, product_isolation_demo.ts) draw ONE honest, narrow
 * distinction: aisotropy isolates every scope axis by construction (unforgeable Scope{}
 * wired from context.Context), while Mastra isolates by convention (real levers, applied
 * per call). That is a fair critique on a single axis — and it would be a strawman to stop
 * there. On the axes Mastra is BUILT for, it is excellent, and aisotropy — a Go runtime —
 * is not even a competitor: it is not a TypeScript-native agent framework at all. This
 * file demonstrates two of Mastra's strongest, genuinely TS-native capabilities:
 *
 *   STRENGTH 1 — Durable, typed WORKFLOWS (createWorkflow/createStep):
 *     .then / .branch control flow + suspend/resume (human-in-the-loop) with a persisted
 *     run snapshot. Steps carry Zod input/output/resume/suspend schemas end to end, so the
 *     graph is type-checked at the boundary. Symbols: `createWorkflow`, `createStep`
 *     (@mastra/core/workflows); `Mastra` (@mastra/core); `LibSQLStore` (@mastra/libsql).
 *
 *   STRENGTH 2 — First-class EVALS / SCORERS (createScorer):
 *     A composable preprocess → generateScore → generateReason pipeline, code-based (no LLM
 *     needed), returning a numeric score + human-readable reason. Symbol: `createScorer`
 *     (@mastra/core/evals).
 *
 * Both are TS-native DX aisotropy does not try to offer. This is where Mastra wins.
 */

import { Mastra } from '@mastra/core';
import { createWorkflow, createStep } from '@mastra/core/workflows';
import { createScorer } from '@mastra/core/evals';
import { LibSQLStore } from '@mastra/libsql';
import { z } from 'zod';
import * as os from 'node:os';
import * as fs from 'node:fs';
import * as path from 'node:path';

const tmpDirs: string[] = [];
function tmp(prefix: string) { const d = fs.mkdtempSync(path.join(os.tmpdir(), prefix)); tmpDirs.push(d); return d; }
function cleanup() { for (const d of tmpDirs) { try { fs.rmSync(d, { recursive: true, force: true }); } catch {} } }

const checks: Array<[string, boolean]> = [];
function check(label: string, ok: boolean) { checks.push([label, ok]); console.log(`  ${ok ? '✓' : '✗'} ${label}`); }

// ─────────────────────────────────────────────────────────────────────────────
// STRENGTH 1 — Durable, typed workflows: .then / .branch + suspend/resume
// ─────────────────────────────────────────────────────────────────────────────
async function workflowsDemo(): Promise<void> {
  console.log('='.repeat(72));
  console.log('STRENGTH 1 — Typed workflows (.then / .branch + suspend/resume)');
  console.log('='.repeat(72));

  // Typed steps: each carries Zod schemas — the graph is type-checked end to end.
  const classify = createStep({
    id: 'classify',
    inputSchema: z.object({ amount: z.number() }),
    outputSchema: z.object({ amount: z.number(), tier: z.enum(['auto', 'review']) }),
    execute: async ({ inputData }) => ({ amount: inputData.amount, tier: inputData.amount > 1000 ? ('review' as const) : ('auto' as const) }),
  });
  const autoApprove = createStep({
    id: 'autoApprove',
    inputSchema: z.object({ amount: z.number(), tier: z.string() }),
    outputSchema: z.object({ decision: z.string() }),
    execute: async ({ inputData }) => ({ decision: `auto-approved $${inputData.amount}` }),
  });
  // Human-in-the-loop step: suspends, persists, and resumes with typed resumeData.
  const humanReview = createStep({
    id: 'humanReview',
    inputSchema: z.object({ amount: z.number(), tier: z.string() }),
    outputSchema: z.object({ decision: z.string() }),
    resumeSchema: z.object({ approved: z.boolean(), reviewer: z.string() }),
    suspendSchema: z.object({ reason: z.string() }),
    execute: async ({ inputData, resumeData, suspend }) => {
      if (!resumeData) { await suspend({ reason: `manual review required for $${inputData.amount}` }); return { decision: 'suspended' }; }
      return { decision: resumeData.approved ? `approved by ${resumeData.reviewer}` : `rejected by ${resumeData.reviewer}` };
    },
  });

  const wf = createWorkflow({
    id: 'approval',
    inputSchema: z.object({ amount: z.number() }),
    outputSchema: z.object({ decision: z.string() }),
  })
    .then(classify)
    .branch([
      [async ({ inputData }) => inputData.tier === 'auto', autoApprove],
      [async ({ inputData }) => inputData.tier === 'review', humanReview],
    ])
    .commit();

  // A storage-backed Mastra persists run snapshots — the prerequisite for durable resume.
  const mastra = new Mastra({
    storage: new LibSQLStore({ id: 'wf', url: 'file:' + path.join(tmp('mastra-wf-'), 'wf.db') }),
    workflows: { approval: wf },
  });
  const bound = mastra.getWorkflow('approval');

  // (1a) BRANCH — a small amount takes the auto-approve branch and completes.
  const runSmall = await bound.createRun();
  const small: any = await runSmall.start({ inputData: { amount: 200 } });
  const autoDecision = small?.result?.decision ?? small?.steps?.autoApprove?.output?.decision;
  check(`.branch routed $200 → auto branch (status=${small.status}, "${autoDecision}")`,
    small.status === 'success' && String(autoDecision).includes('auto-approved'));

  // (1b) SUSPEND — a large amount takes the review branch and SUSPENDS (persisted).
  const runLarge = await bound.createRun();
  const suspended: any = await runLarge.start({ inputData: { amount: 5000 } });
  const didSuspend = suspended.status === 'suspended' && JSON.stringify(suspended.suspended ?? []).includes('humanReview');
  check(`.branch routed $5000 → review branch and SUSPENDED at humanReview (status=${suspended.status})`, didSuspend);

  // (1c) RESUME — resume the persisted run with typed reviewer input; it completes.
  const resumed: any = await runLarge.resume({ step: 'humanReview', resumeData: { approved: true, reviewer: 'alice' } });
  const reviewDecision = resumed?.result?.decision ?? resumed?.steps?.humanReview?.output?.decision;
  check(`resume() completed the suspended run with typed resumeData (status=${resumed.status}, "${reviewDecision}")`,
    resumed.status === 'success' && String(reviewDecision).includes('approved by alice'));

  // (1d) TYPE-SAFE DX — a malformed input is rejected at the Zod boundary, not silently run.
  let zodRejected = false;
  try {
    const bad = await bound.createRun();
    await bad.start({ inputData: { amount: 'not-a-number' } as any });
  } catch { zodRejected = true; }
  check(`Zod inputSchema rejected a malformed workflow input at the boundary`, zodRejected);
  console.log();
}

// ─────────────────────────────────────────────────────────────────────────────
// STRENGTH 2 — First-class evals / scorers: createScorer pipeline
// ─────────────────────────────────────────────────────────────────────────────
async function evalsDemo(): Promise<void> {
  console.log('='.repeat(72));
  console.log('STRENGTH 2 — Evals / scorers (createScorer: preprocess → score → reason)');
  console.log('='.repeat(72));

  // A code-based scorer (no LLM): fraction of required keywords present in the answer.
  // Composable steps: preprocess extracts, generateScore reduces to a number,
  // generateReason produces a human-readable explanation.
  const keywordCoverage = createScorer({
    id: 'keyword-coverage',
    name: 'keyword-coverage',
    description: 'Fraction of required keywords present in the output text.',
  })
    .preprocess(({ run }: any) => {
      const required: string[] = run.input?.required ?? [];
      const text = String(run.output?.text ?? '').toLowerCase();
      const present = required.filter((k) => text.includes(k.toLowerCase()));
      return { required, present };
    })
    .generateScore(({ results }: any) => {
      const { required, present } = results.preprocessStepResult;
      return required.length === 0 ? 1 : present.length / required.length;
    })
    .generateReason(({ results, score }: any) => {
      const { required, present } = results.preprocessStepResult;
      const missing = required.filter((k: string) => !present.includes(k));
      return `covered ${present.length}/${required.length}; missing: ${missing.join(', ') || 'none'}`;
    });

  // Run the scorer over a tiny eval suite — the framework drives the pipeline.
  const required = ['refund', 'policy', '30 days'];
  const good: any = await keywordCoverage.run({
    input: { required },
    output: { text: 'Our refund policy allows returns within 30 days of purchase.' },
  });
  const bad: any = await keywordCoverage.run({
    input: { required },
    output: { text: 'You can send it back sometime.' },
  });

  console.log(`  good answer → score ${good.score.toFixed(2)} (${good.reason})`);
  console.log(`  bad  answer → score ${bad.score.toFixed(2)} (${bad.reason})`);
  check(`createScorer pipeline scored a complete answer 1.00`, good.score === 1);
  check(`createScorer pipeline scored an off-topic answer 0.00 with a reason`, bad.score === 0 && typeof bad.reason === 'string');
  console.log();
}

// ── main ─────────────────────────────────────────────────────────────────────
(async () => {
  await workflowsDemo();
  await evalsDemo();
  cleanup();

  const passed = checks.every(([, ok]) => ok);
  console.log('-'.repeat(72));
  console.log('VERDICT');
  console.log('-'.repeat(72));
  console.log(`  ${checks.filter(([, ok]) => ok).length}/${checks.length} checks passed.`);
  console.log();
  if (passed) {
    console.log('PASS — Mastra\'s TS-native strengths are real and executed here:');
    console.log('  • durable typed workflows: .then / .branch control flow + suspend/resume');
    console.log('    with a persisted run snapshot and Zod-checked boundaries;');
    console.log('  • first-class evals: a composable createScorer pipeline returning a numeric');
    console.log('    score + human-readable reason, no LLM required.');
    console.log();
    console.log('This is the honest other half of the arena. On isolation-by-DEFAULT (an');
    console.log('unforgeable Scope{} axis wired from ctx on every read) aisotropy differs; but on');
    console.log('TypeScript-native workflow/eval/memory DX, Mastra is genuinely excellent and');
    console.log('aisotropy — a Go runtime — is not a TS framework at all. Right tool, right job.');
  } else {
    console.log('FAIL — a strength check did not pass; see the ✗ lines above.');
  }
  process.exit(passed ? 0 : 1);
})().catch((e) => {
  cleanup();
  console.error('ERROR:', e);
  process.exit(2);
});
