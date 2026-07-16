// Adversarial isolation smoke for the Cloudflare Agents SDK.
//
// The Orbit scope is (tenant_id, user_id, product_id). In this SDK the scope is
// expressed as the Durable Object NAME string. This test proves two things at
// once:
//
//   PART A — per-object isolation is a HARD platform boundary (by-construction):
//     two different names => two different Durable Objects => two physically
//     separate SQLite databases. There is NO API to read across objects. A
//     cross-object read is unrepresentable, not merely discouraged. This is
//     STRONGER than the string-keyed shared stores in the other 7 arenas.
//
//   PART B — the tenant/user/product -> object MAPPING is developer convention:
//     the name is a string the developer assembles. Drop the product_id segment
//     and two products collapse onto ONE object and share ONE SQLite -> leak.
//     The framework binds the name to nothing. Same failure class as the others.

import { env } from "cloudflare:test";
import { getAgentByName } from "agents";
import { describe, expect, it } from "vitest";
import { orbitName, type Env, type OrbitAgent } from "../src/index";

const ns = () => env.OrbitAgent;
const SECRET = "ORBIT-SECRET-acme-alice-productX-42";

async function agent(name: string) {
  const stub = await getAgentByName<Env, OrbitAgent>(ns(), name);
  await stub.bind(name);
  return stub;
}

describe("Cloudflare Agents — per-Durable-Object SQLite isolation", () => {
  it("PART A: correct name reads its own object (positive control)", async () => {
    const a = await agent(orbitName("acme", "alice", "productX"));
    await a.remember("secret", SECRET);
    expect(await a.recall("secret")).toBe(SECRET);
  });

  it("PART A: a DIFFERENT tenant is a DIFFERENT object — physically separate SQLite (NO leak)", async () => {
    // Seed acme/alice/productX with the secret.
    const acme = await agent(orbitName("acme", "alice", "productX"));
    await acme.remember("secret", SECRET);

    // globex/alice/productX is a different NAME => different Durable Object =>
    // its own empty SQLite. It cannot see acme's row. There is no cross-object
    // SELECT to even attempt.
    const globex = await agent(orbitName("globex", "alice", "productX"));
    expect(await globex.recall("secret")).toBeNull();
  });

  it("PART A: a DIFFERENT product (correct name) is a DIFFERENT object (NO leak)", async () => {
    const px = await agent(orbitName("acme", "alice", "productX"));
    await px.remember("secret", SECRET);

    // Same tenant + user, DIFFERENT product, name assembled CORRECTLY => a third
    // distinct object. Product isolation holds *because the name was right*.
    const py = await agent(orbitName("acme", "alice", "productY"));
    expect(await py.recall("secret")).toBeNull();
  });

  it("PART A: cross-object memory hydration stays private", async () => {
    const px = await agent(orbitName("acme", "alice", "productX"));
    await px.chat("hi from productX");
    await px.chat("second turn productX");

    // A different object starts with zero hydrated history.
    const py = await agent(orbitName("acme", "alice", "productY"));
    const out = await py.chat("hi from productY");
    expect(out.hydrated).toBe(0); // saw none of productX's turns
    const hist = await py.history();
    expect(hist.every((t) => !t.text.includes("productX"))).toBe(true);
  });

  it("PART B: DROPPING product_id from the name collapses two products onto ONE object (LEAK)", async () => {
    // The "buggy" developer assembles the name WITHOUT the product axis.
    // Both productX and productY now resolve to the SAME Durable Object.
    const buggyName = (tenant: string, user: string) => `${tenant}|${user}`; // product_id forgotten

    const asProductX = await agent(buggyName("acme", "alice"));
    await asProductX.remember("secret", SECRET); // written "as productX"

    const asProductY = await agent(buggyName("acme", "alice")); // "serving productY"
    const leaked = await asProductY.recall("secret");

    // productY reads productX's secret — because they share one object/SQLite.
    // The framework did nothing wrong; the developer-assembled key was wrong.
    expect(leaked).toBe(SECRET);
  });

  it("PART B: WRONG product substitution addresses the wrong (but still isolated) object", async () => {
    const px = await agent(orbitName("acme", "alice", "productX"));
    await px.remember("secret", SECRET);

    // A handler that is *supposed* to serve productY but builds the name with
    // productX reads productX's data. Nothing validates the product segment
    // against the authenticated caller — product_id is an unbound string.
    const wrong = await agent(orbitName("acme", "alice", "productX")); // should have been productY
    expect(await wrong.recall("secret")).toBe(SECRET);
  });
});
