#!/usr/bin/env node
// Runtime + DIY-memory smoke for the Vercel eve arena.
//
// Runs against a REAL, built eve server (Nitro + Workflow-SDK durable sessions +
// the real tool loop). The ONLY faked component is the AI-SDK model (offline
// deterministic mock in agent/lib/model.ts) — every runtime layer (session
// durability, tool dispatch, channel auth, streaming) is genuine eve.
//
// What this demonstrates about eve's scope (NOT a defect in eve):
//   eve is a SESSION-SCOPED durable-agent runtime. It ships session working
//   memory (`defineState`, session-scoped BY CONSTRUCTION) but has NO
//   tenant/user/product axis anywhere — multi-tenant memory is out of its scope
//   by design, exactly like Pydantic AI / Eino. So per-customer, per-product
//   memory is a store the DEVELOPER builds (see agent/tools/orbit_memory.ts),
//   keyed by a string the developer computes. Whether it partitions is a
//   property of the developer's key, not of eve:
//     * correct key `${t}:${u}:${p}` partitions products
//     * naive  key `${t}:${u}` (product segment dropped) collides them
//   This is the same-key/wrong-key developer bug that is TRUE OF ANY key-value
//   store — including aisotropy's own BadgerDB if a dev drops a scope segment.
//   It is not something eve "fails" to validate; eve has no tenancy primitive to
//   validate against. A competent dev uses the composite key (or binds identity
//   at the channel) and it partitions fine. What aisotropy adds on ITS narrow
//   axis is that `product_id` rides context.Context inside an unforgeable Scope,
//   so the naive read is unrepresentable rather than merely discouraged.
//
// Contract: prints PASS/FAIL lines; exits 0 when the demonstration completes
// (the positive control, the correct-key partition, and the naive-key collision
// are all EXPECTED outcomes — the collision is the documented DIY footgun, not a
// framework failure, so it does not exit non-zero).

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

let collisions = 0;
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

  console.log("\n## DIY memory partitioning — the (tenant,user,product) axis eve does not have\n");

  const secX = marker("checkoutSecret");
  const secB = marker("billingSecret");
  const secNaive = marker("naiveCheckout");

  // Seed two products for the SAME (tenant,user) via the correct composite key.
  await run(`MEM: REMEMBER mode=strict t=acme u=alice p=checkout secret=${secX}`);
  await run(`MEM: REMEMBER mode=strict t=acme u=alice p=billing  secret=${secB}`);

  // Positive control: correct-key recall of the SAME product returns its own secret.
  const okX = recalledSecret(await run(`MEM: RECALL mode=strict t=acme u=alice p=checkout`));
  assert("correct_key.positive_control", okX.secret === secX, `key=${okX.key} secret=${okX.secret}`);

  // Correct composite key partitions products: reading product=billing must NOT
  // return the checkout secret (and vice versa). This is the competent-dev path.
  const okB = recalledSecret(await run(`MEM: RECALL mode=strict t=acme u=alice p=billing`));
  assert("correct_key.product_partitioned", okB.secret === secB && okB.secret !== secX,
    `billing key=${okB.key} secret=${okB.secret}`);

  // THE NAIVE-KEY COLLISION: a key builder that drops the product segment. This
  // is the same-key/wrong-key developer bug of ANY string-keyed KV (incl.
  // aisotropy's BadgerDB) — not an eve defect; eve has no tenancy axis to bind.
  // Seed under product=checkout with the naive key (key becomes acme:alice)...
  await run(`MEM: REMEMBER mode=naive t=acme u=alice p=checkout secret=${secNaive}`);
  // ...then RECALL under a DIFFERENT product=billing with the same naive builder.
  const naive = recalledSecret(await run(`MEM: RECALL mode=naive t=acme u=alice p=billing`));
  const collided = naive.secret === secNaive;
  assert("naive_key.product_collision", collided,
    `recall(product=billing) hit product=checkout's slot? key=${naive.key} secret=${naive.secret}`);
  if (collided) {
    collisions++;
    console.log("      ^ Expected: the naive key `${t}:${u}` drops product, so two products share");
    console.log("        one slot. Same-key/wrong-key dev bug, true of ANY KV incl. BadgerDB — the");
    console.log("        correct composite key above partitions fine. eve has no tenancy axis by design.");
  }

  console.log("\n## Summary");
  console.log(`checks=${checks}  naive_key_collisions=${collisions}  (expected: 1 — the documented DIY footgun)`);
  console.log("verdict: eve is session-scoped by design; multi-tenant (tenant,user,product) memory is");
  console.log("         DIY / OUT-OF-SCOPE (no tenancy primitive), like Pydantic AI / Eino — NOT a defect.");
  console.log("         Session-to-session isolation IS by-construction (runtime-owned sessionId); the");
  console.log("         correct composite key partitions products. aisotropy's narrow add: product_id");
  console.log("         rides context.Context in an unforgeable Scope, so the naive read cannot compile.");
}

main().then(
  () => process.exit(0),
  (err) => {
    console.error("SMOKE ERROR:", err?.stack || err);
    process.exit(2);
  },
);
