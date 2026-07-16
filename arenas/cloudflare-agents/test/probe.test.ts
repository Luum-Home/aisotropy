import { env } from "cloudflare:test";
import { getAgentByName } from "agents";
import { describe, expect, it } from "vitest";
import type { Env, OrbitAgent } from "../src/index";

describe("capability probes (DO SQLite)", () => {
  it("probe: FTS5 available in Durable Object SQLite?", async () => {
    const a = await getAgentByName<Env, OrbitAgent>(env.OrbitAgent, "probe|fts");
    const res = await a.probeFts();
    console.log("FTS5_PROBE:", JSON.stringify(res));
    expect(res).toBeDefined();
  });
});
