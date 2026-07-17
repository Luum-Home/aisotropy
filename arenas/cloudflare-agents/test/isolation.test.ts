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
//     and two products collapse onto ONE object and share ONE SQLite. This is a
//     same-key collision — a developer bug, NOT a framework defect. It is
//     tautologically true of ANY name->object map (aisotropy's own Scope
//     included): build the wrong key, address the wrong store. Same failure
//     CLASS as Eino's dropped checkpoint segment or LangGraph's short prefix.
//
//   PART C — the STRONG path closes PART B: derive the DO name from VERIFIED
//     claims (must-agree bind) instead of caller-supplied path text, so a caller
//     cannot address a scope it was not authorized for. The SDK gives the seam
//     (`onBeforeConnect` on the routing path); binding it to identity is the
//     developer's middleware — the same shape as aisotropy's ADR-0024 JWT bind.

import { env } from "cloudflare:test";
import { getAgentByName } from "agents";
import { describe, expect, it } from "vitest";
import {
  orbitName,
  nameFromClaims,
  resolveVerifiedName,
  verifyOrbitToken,
  type Env,
  type OrbitAgent,
} from "../src/index";

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

  it("PART B: dropping the product axis makes two products share ONE object (same-key collision, a developer bug — not a framework defect)", async () => {
    // The "buggy" developer assembles the name WITHOUT the product axis.
    // Both productX and productY now resolve to the SAME Durable Object.
    const buggyName = (tenant: string, user: string) => `${tenant}|${user}`; // product_id forgotten

    const asProductX = await agent(buggyName("acme", "alice"));
    await asProductX.remember("secret", SECRET); // written "as productX"

    const asProductY = await agent(buggyName("acme", "alice")); // "serving productY"
    const collided = await asProductY.recall("secret");

    // productY reads productX's secret — because the wrong key addressed the same
    // object. The framework did nothing wrong; the developer built the wrong key.
    // This is true of ANY name->object map, aisotropy's Scope included.
    expect(collided).toBe(SECRET);
  });

  it("PART B: asking for the wrong object returns the wrong object (addressing, not a leak — a tautology of any name->object map)", async () => {
    const px = await agent(orbitName("acme", "alice", "productX"));
    await px.remember("secret", SECRET);

    // A handler that *intends* to serve productY but builds the name with
    // productX simply addresses productX. There is no cross-object read here —
    // "ask for X, get X" is not a leak, it is what addressing means. The real
    // fix is not in the store; it is binding the name to a verified caller
    // (PART C), so the wrong name can never be built in the first place.
    const asked = await agent(orbitName("acme", "alice", "productX")); // intended productY
    expect(await asked.recall("secret")).toBe(SECRET);
  });
});

describe("Cloudflare Agents — the STRONG path: bind the name to verified identity", () => {
  it("PART C: the DO name is derived from VERIFIED claims, not caller-supplied path text", async () => {
    const claims = verifyOrbitToken("Bearer tok:acme|alice|productX");
    expect(claims).toEqual({ tenant: "acme", user: "alice", product: "productX" });

    const a = await agent(nameFromClaims(claims!));
    await a.remember("secret", SECRET);
    expect(await a.recall("secret")).toBe(SECRET);
  });

  it("PART C: must-agree bind REJECTS a caller asking for a product its token does not authorize (403, before any object is addressed)", async () => {
    // URL says productY; the token only authorizes productX. Rejected on the
    // routing path — the DO is never woken, so PART B's mis-address is unreachable.
    const r = resolveVerifiedName(
      { tenant: "acme", user: "alice", product: "productY" },
      "Bearer tok:acme|alice|productX",
    );
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.status).toBe(403);
  });

  it("PART C: a self-consistent, verified request resolves to exactly its own object and no other", async () => {
    const r = resolveVerifiedName(
      { tenant: "acme", user: "alice", product: "productX" },
      "Bearer tok:acme|alice|productX",
    );
    expect(r.ok).toBe(true);
    if (r.ok) {
      const a = await agent(r.name);
      await a.remember("k", "v");

      // The rejected wrong-product caller can never reach this object; a
      // correctly-named sibling is still a physically separate SQLite.
      const py = await agent(orbitName("acme", "alice", "productY"));
      expect(await py.recall("k")).toBeNull();
    }
  });
});
