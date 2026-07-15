#!/usr/bin/env tsx
/**
 * Mastra arena — Cost + Pre-spend Gate + Observability demo (RUNS OFFLINE).
 *
 * Run:  npm run cost-obs           (from arenas/mastra)
 *   or: node_modules/.bin/tsx scripts/cost_obs_demo.ts
 *
 * No API key needed. The "LLM" is a hand-rolled AI-SDK v5 LanguageModelV2 that
 * returns a DETERMINISTIC SYNTHETIC token count (len(text)//4). Token numbers are
 * fake-but-labeled; the point is whether the FRAMEWORK gives you the machinery, not
 * exact counts.
 *
 * Proves, by executing, three things — each with a [BUILT-IN]/[DIY]/[PAID]/[NOT-POSSIBLE]
 * verdict grounded in what Mastra 1.51.0 / @mastra/observability 1.16.1 / ai 5.x actually do:
 *
 *   1. PER-TENANT COST TRACKING
 *   2. PRE-SPEND BUDGET GATE
 *   3. OBSERVABILITY (native Mastra tracing + in-memory exporter)
 */

import { Agent } from '@mastra/core/agent';
import { Mastra } from '@mastra/core';
import { Observability, TestExporter } from '@mastra/observability';

// ── Synthetic pricing (clearly fake, illustrative only) ─────────────────────
const PRICE_PER_1K_INPUT = 0.003; // $/1K input tokens (synthetic)
const PRICE_PER_1K_OUTPUT = 0.015; // $/1K output tokens (synthetic)

function synthCostUSD(inputTokens: number, outputTokens: number): number {
  return (inputTokens / 1000) * PRICE_PER_1K_INPUT + (outputTokens / 1000) * PRICE_PER_1K_OUTPUT;
}

// ── Fake model: deterministic, offline, with a REAL call counter ────────────
// A real AI-SDK model would call a provider. This one never touches the network.
// modelCallCount lets us PROVE the model is not invoked on a gated turn.
let modelCallCount = 0;

const fakeModel: any = {
  specificationVersion: 'v2',
  provider: 'fake-offline',
  modelId: 'fake-synthetic-model',
  supportedUrls: {},
  async doGenerate(opts: any) {
    modelCallCount++;
    // Deterministic synthetic reply.
    const promptText = JSON.stringify(opts?.prompt ?? '');
    const reply = 'ACK: deterministic offline reply for the demo. No network was touched.';
    // SYNTHETIC token accounting: len(text)//4 (clearly labeled fake).
    const inputTokens = Math.max(1, Math.floor(promptText.length / 4));
    const outputTokens = Math.max(1, Math.floor(reply.length / 4));
    return {
      content: [{ type: 'text', text: reply }],
      finishReason: 'stop',
      usage: { inputTokens, outputTokens, totalTokens: inputTokens + outputTokens },
      warnings: [],
    };
  },
  async doStream() {
    modelCallCount++;
    throw new Error('doStream not used in this demo');
  },
};

// ── Observability: native Mastra tracing wired to an in-memory exporter ─────
// TestExporter is a BUILT-IN @mastra/observability exporter that captures every
// span in-memory and lets you query it. A production OTLP/console exporter is a
// one-line swap (new OtelExporter({...}) / new ConsoleExporter()).
const spanExporter = new TestExporter({ logMetricsOnFlush: false });

const mastra = new Mastra({
  observability: new Observability({
    configs: {
      default: {
        serviceName: 'mastra-arena-cost-obs',
        exporters: [spanExporter],
      },
    },
  }),
});

const agent = new Agent({
  name: 'cost-obs-agent',
  instructions: 'You are a concise support agent for the cost/observability demo.',
  model: fakeModel,
  mastra,
});

// ── DIY per-tenant cost ledger (Mastra returns per-CALL usage; aggregation is ours) ──
interface TenantLedger {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  costUSD: number;
  turns: number;
}
const ledger = new Map<string, TenantLedger>();

function ledgerFor(tenantId: string): TenantLedger {
  if (!ledger.has(tenantId)) {
    ledger.set(tenantId, { inputTokens: 0, outputTokens: 0, totalTokens: 0, costUSD: 0, turns: 0 });
  }
  return ledger.get(tenantId)!;
}

/**
 * runTurn — calls the agent, reads the BUILT-IN result.usage, and folds it into
 * our DIY per-tenant ledger. tenantId is stamped onto the trace via the BUILT-IN
 * tracingOptions.metadata mechanism.
 */
async function runTurn(tenantId: string, prompt: string): Promise<void> {
  const res: any = await agent.generate(prompt, {
    tracingOptions: { metadata: { tenantId } }, // built-in: custom metadata onto the trace
  } as any);

  const usage = res.usage ?? {};
  const inTok = usage.inputTokens ?? 0;
  const outTok = usage.outputTokens ?? 0;
  const total = usage.totalTokens ?? inTok + outTok;

  const l = ledgerFor(tenantId);
  l.inputTokens += inTok;
  l.outputTokens += outTok;
  l.totalTokens += total;
  l.costUSD += synthCostUSD(inTok, outTok);
  l.turns += 1;

  console.log(
    `    [${tenantId}] turn ok — usage(in=${inTok}, out=${outTok}, total=${total})  ` +
      `[BUILT-IN result.usage]`,
  );
}

// ═══════════════════════════════════════════════════════════════════════════
async function main() {
  console.log('='.repeat(72));
  console.log('Mastra — Cost + Pre-spend Gate + Observability demo (OFFLINE)');
  console.log('Mastra core 1.51.0 · @mastra/observability 1.16.1 · ai 5.x');
  console.log('Token counts are SYNTHETIC (len//4). Prices are SYNTHETIC.');
  console.log('='.repeat(72));

  // ── 1. PER-TENANT COST TRACKING ──────────────────────────────────────────
  console.log('\n[1] PER-TENANT COST TRACKING');
  console.log('    Running 2 turns for tenantA and 2 for tenantB...');
  await runTurn('tenantA', 'What is my invoice status?');
  await runTurn('tenantA', 'And when is it due?');
  await runTurn('tenantB', 'Reset my password please.');
  await runTurn('tenantB', 'Also update my billing email.');

  console.log('\n    Per-tenant totals (DIY aggregation over BUILT-IN per-call usage):');
  for (const [tenantId, l] of ledger) {
    console.log(
      `      ${tenantId}: turns=${l.turns}  in=${l.inputTokens}  out=${l.outputTokens}  ` +
        `total=${l.totalTokens}  cost=$${l.costUSD.toFixed(6)} (synthetic)`,
    );
  }
  console.log('    VERDICT #1:');
  console.log('      per-call token usage ....... [BUILT-IN]  agent.generate() → result.usage {inputTokens,outputTokens,totalTokens}');
  console.log('      per-tenant aggregation ..... [DIY]       framework returns per-call usage only; the tenant ledger Map is ours');
  console.log('      dollar cost ................ [DIY]       framework returns tokens, not $; pricing table + math is ours');

  // ── 2. PRE-SPEND BUDGET GATE ──────────────────────────────────────────────
  console.log('\n[2] PRE-SPEND BUDGET GATE');
  // Tiny per-tenant budget. tenantA has already spent tokens in section 1.
  const BUDGET_TOKENS = 30;
  console.log(`    Per-tenant budget: ${BUDGET_TOKENS} tokens (synthetic). tenantA already spent above.`);

  // Mastra has NO built-in pre-spend/usage-limit check (no equivalent of
  // Pydantic AI UsageLimits.check_before_request). Usage is only known AFTER
  // the call. So the gate is DIY: check the ledger BEFORE calling the agent.
  async function gatedTurn(tenantId: string, prompt: string): Promise<'allowed' | 'rejected'> {
    const spent = ledgerFor(tenantId).totalTokens;
    if (spent >= BUDGET_TOKENS) {
      console.log(
        `    [${tenantId}] REJECTED before model — spent=${spent} >= budget=${BUDGET_TOKENS}  ` +
          `[DIY pre-spend gate]`,
      );
      return 'rejected';
    }
    console.log(`    [${tenantId}] under budget (spent=${spent}) → allowing call...`);
    await runTurn(tenantId, prompt);
    return 'allowed';
  }

  const callsBefore = modelCallCount;
  console.log(`    modelCallCount before gate section: ${callsBefore}`);

  // Fresh tenant, under budget → ALLOWED (model runs).
  const r1 = await gatedTurn('tenantC', 'First question, should be allowed.');
  const callsAfterAllowed = modelCallCount;

  // tenantA is over budget → REJECTED (model must NOT run).
  const r2 = await gatedTurn('tenantA', 'This must be rejected before spending.');
  const callsAfterRejected = modelCallCount;

  console.log(`    modelCallCount after ALLOWED turn (tenantC): ${callsAfterAllowed} (Δ=${callsAfterAllowed - callsBefore}, expected 1)`);
  console.log(`    modelCallCount after REJECTED turn (tenantA): ${callsAfterRejected} (Δ=${callsAfterRejected - callsAfterAllowed}, expected 0)`);

  const gateProven =
    r1 === 'allowed' &&
    r2 === 'rejected' &&
    callsAfterAllowed - callsBefore === 1 &&
    callsAfterRejected - callsAfterAllowed === 0;

  console.log('    VERDICT #2:');
  console.log(
    `      pre-spend gate ............. [DIY]  Mastra has NO built-in pre-spend/usage-limit check; ` +
      `usage is post-call only. The gate (ledger check before generate) is ours.`,
  );
  console.log(
    `      model-not-called proof ..... ${gateProven ? 'CONFIRMED' : 'FAILED'} — ` +
      `rejected turn added 0 model calls (counter unchanged).`,
  );
  if (!gateProven) {
    throw new Error('Pre-spend gate proof FAILED — see counters above.');
  }

  // ── 3. OBSERVABILITY ──────────────────────────────────────────────────────
  console.log('\n[3] OBSERVABILITY (native Mastra tracing → in-memory exporter)');
  // Flush + let async span export settle.
  await new Promise((r) => setTimeout(r, 200));
  await spanExporter.flush();

  const spans = spanExporter.getAllSpans();
  const stats = spanExporter.getStatistics();
  console.log(`    Captured ${spans.length} spans across ${stats.totalTraces} traces (in-memory, offline).`);
  console.log('    Span types seen:', JSON.stringify(stats.bySpanType));

  // Show one full model_generation span (gen_ai-style usage attributes are built-in).
  const genSpans = spans.filter((s: any) => s.type === 'model_generation');
  const sample: any = genSpans[genSpans.length - 1];
  if (sample) {
    console.log('    Sample model_generation span:');
    console.log('      name    :', sample.name);
    console.log('      attrs   :', JSON.stringify(sample.attributes?.usage ?? sample.attributes));
    console.log('      metadata:', JSON.stringify(sample.metadata));
  }

  // Does tenant_id ride on the spans? (We stamped it via built-in tracingOptions.metadata.)
  const withTenant = spans.filter((s: any) => s?.metadata?.tenantId);
  const tenantIdsOnSpans = Array.from(new Set(withTenant.map((s: any) => s.metadata.tenantId)));
  console.log(
    `    Spans carrying metadata.tenantId: ${withTenant.length}/${spans.length}  ` +
      `→ tenants=${JSON.stringify(tenantIdsOnSpans)}`,
  );

  console.log('    VERDICT #3:');
  console.log('      native tracing/spans ....... [BUILT-IN]  @mastra/observability captures agent_run / model_generation / model_step / model_inference spans');
  console.log('      in-memory span capture ..... [BUILT-IN]  TestExporter (offline). OTLP/Console exporter = one-line swap');
  console.log('      gen_ai usage on spans ...... [BUILT-IN]  model_generation span carries usage.inputTokens/outputTokens (GenAI semconv)');
  console.log('      tenant_id on spans ......... [DIY]        built-in metadata mechanism, but YOU must pass tracingOptions.metadata={tenantId}; not auto');
  console.log('      hosted trace UI (Cloud) .... [PAID]       Mastra Cloud / Langfuse / Braintrust exporters are optional paid backends; not needed here');

  // ── Summary ───────────────────────────────────────────────────────────────
  console.log('\n' + '='.repeat(72));
  console.log('SUMMARY');
  console.log('  #1 per-tenant cost .... usage=[BUILT-IN], aggregation=[DIY], dollars=[DIY]');
  console.log('  #2 pre-spend gate ..... [DIY] (no built-in; model-not-called on reject: CONFIRMED)');
  console.log('  #3 observability ...... tracing/spans/usage=[BUILT-IN], tenant_id=[DIY], hosted UI=[PAID]');
  console.log('  total model calls this run:', modelCallCount);
  console.log('='.repeat(72));
}

main()
  .then(() => process.exit(0))
  .catch((e) => {
    console.error('DEMO FAILED:', e);
    process.exit(1);
  });
