#!/usr/bin/env node
// Adversarial isolation + runtime smoke for the Vercel eve arena.
//
// Runs against a REAL, built eve server (Nitro + Workflow-SDK durable sessions +
// the real tool loop). The ONLY faked component is the AI-SDK model (offline
// deterministic mock in agent/lib/model.ts) — every isolation-relevant layer
// (session durability, tool dispatch, channel auth, streaming) is genuine eve.
//
// It proves eve's isolation posture for the 4th scope axis (tenant,user,PRODUCT):
//   eve binds NO tenant/user/product to its durable memory. Per-customer,
//   per-product memory is a DEVELOPER-built store keyed by a hand-concatenated
//   string (see agent/tools/orbit_memory.ts). Whether it isolates depends
//   entirely on the developer's key builder:
//     * strict key `${t}:${u}:${p}` isolates products
//     * leaky key `${t}:${u}` (product dropped — one edit away) LEAKS
//   The framework validates neither. That is CONVENTION, not construction.
//
// Contract: prints PASS/FAIL lines; exits 0 when the demonstration completes
// (both the positive control and the proven leak are expected outcomes here —
// this arena documents the leak rather than exiting non-zero on it).

import { Client } from "eve/client";
import crypto from "node:crypto";

const HOST = process.env.EVE_HOST || "http://127.0.0.1:4117";
const client = new Client({ host: HOST });

function marker(prefix) {
  return `${prefix}_${crypto.randomBytes(8).toString("hex")}`;
}

// Send one command through a FRESH durable session and return the assistant's
// final text (our mock relays the tool result as "RESULT: <json>").
async function run(command) {
  const session = client.session();
  const response = await session.send({ message: command });
  let finalText = "";
  for await (const event of response) {
    if (
      event.type === "message.completed" &&
      event.data?.finishReason !== "tool-calls"
    ) {
      // event.data.message is the finalized assistant text (a string).
      const m = event.data?.message;
      finalText = (typeof m === "string" ? m : m?.text) ?? event.data?.text ?? finalText;
    }
  }
  return finalText;
}

function recalledSecret(text) {
  // text looks like: RESULT: {"ok":true,"verb":"RECALL","key":"...","secret":"..."}
  const m = text.match(/RESULT:\s*(\{[\s\S]*\})/);
  if (!m) return { raw: text, secret: undefined };
  try {
    const obj = JSON.parse(m[1]);
    return { raw: text, secret: obj.secret ?? null, key: obj.key };
  } catch {
    return { raw: text, secret: undefined };
  }
}

let leaks = 0;
let checks = 0;
function assert(name, cond, detail) {
  checks++;
  console.log(`${cond ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + detail : ""}`);
}

async function main() {
  console.log(`# eve arena smoke — host ${HOST}\n`);

  // 0) Health + runtime liveness (proves the real server booted).
  const health = await client.health();
  assert("runtime.health", health.status === "ready", `status=${health.status} workflow=${health.workflowId}`);
  const info = await client.info();
  assert("runtime.info", info.agent?.name === "eve-arena", `model=${info.agent?.model?.id}`);

  // 1) Plain turn — proves the mock model streams a real assistant reply.
  const hello = await run("hello Orbit");
  assert("runtime.turn", /Orbit here/.test(hello), JSON.stringify(hello).slice(0, 80));

  console.log("\n## Isolation — 4th scope axis (tenant, user, PRODUCT)\n");

  const secX = marker("checkoutSecret");
  const secB = marker("billingSecret");
  const secLeak = marker("leakyCheckout");

  // Seed two products for the SAME (tenant,user) via the strict key builder.
  await run(`MEM: REMEMBER mode=strict t=acme u=alice p=checkout secret=${secX}`);
  await run(`MEM: REMEMBER mode=strict t=acme u=alice p=billing  secret=${secB}`);

  // Positive control: strict recall of the SAME product returns its own secret.
  const okX = recalledSecret(await run(`MEM: RECALL mode=strict t=acme u=alice p=checkout`));
  assert("strict.positive_control", okX.secret === secX, `key=${okX.key} secret=${okX.secret}`);

  // Product isolation under the strict key: reading product=billing must NOT
  // return the checkout secret (and vice versa).
  const okB = recalledSecret(await run(`MEM: RECALL mode=strict t=acme u=alice p=billing`));
  assert("strict.product_isolated", okB.secret === secB && okB.secret !== secX,
    `billing key=${okB.key} secret=${okB.secret}`);

  // THE LEAK: the "one edit cheaper" key builder drops the product segment.
  // Seed under product=checkout with the leaky key (key becomes acme:alice)...
  await run(`MEM: REMEMBER mode=leaky t=acme u=alice p=checkout secret=${secLeak}`);
  // ...then RECALL under a DIFFERENT product=billing with the same leaky builder.
  const leaky = recalledSecret(await run(`MEM: RECALL mode=leaky t=acme u=alice p=billing`));
  const leaked = leaky.secret === secLeak;
  assert("leaky.cross_product_read", leaked,
    `recall(product=billing) returned product=checkout secret? key=${leaky.key} secret=${leaky.secret}`);
  if (leaked) {
    leaks++;
    console.log("      ^ LEAK: product_id is unbound. Dropping `:${product}` from the developer key");
    console.log("        crosses products, and TypeScript/eve validate nothing. CONVENTION, not construction.");
  }

  console.log("\n## Summary");
  console.log(`checks=${checks}  proven_cross_product_leaks=${leaks}`);
  console.log("verdict: eve isolation for (tenant,user,product) = CONVENTION (developer-passed key).");
  console.log("         Session-to-session isolation IS by-construction (runtime-owned sessionId),");
  console.log("         but there is no tenant/user/product axis in the runtime to bind.");
}

main().then(
  () => process.exit(0),
  (err) => {
    console.error("SMOKE ERROR:", err?.stack || err);
    process.exit(2);
  },
);
