// A per-(tenant,user,product) memory tool — the honest way to give an eve agent
// cross-session memory scoped by tenant/user/product, since eve does not ship one.
//
// WHAT THIS ARENA SHOWS (not a defect in eve): eve is a session-scoped durable
// runtime. `defineState` is *session*-scoped (isolated BY CONSTRUCTION) and
// `ctx.session` exposes only { id, turn, auth, parent lineage } — there is no
// tenant_id/user_id/product_id axis anywhere. Multi-tenant memory is OUT OF
// eve's SCOPE BY DESIGN, the same posture as Pydantic AI / Eino. So a
// cross-session, per-customer, per-product store is 100% DEVELOPER-built (DIY),
// and its key is a string the developer computes. There is nothing for eve to
// validate — it has no tenancy primitive.
//
// This tool makes that explicit. The `command` carries the scope as plain args,
// and a `mode` selects the key builder:
//   * mode=strict -> key = `${tenant}:${user}:${product}`   (competent-dev path: partitions)
//   * mode=naive  -> key = `${tenant}:${user}`              (product segment dropped: collides)
// The naive builder is the classic same-key/wrong-key bug of ANY key-value store
// (including aisotropy's own BadgerDB if a dev drops a scope segment) — a
// tautology, not an eve failing. The correct composite key partitions products
// fine. aisotropy's narrow difference: `product_id` rides context.Context inside
// an unforgeable Scope, so the naive read is unrepresentable rather than merely
// discouraged.
import { defineTool } from "eve/tools";
import { z } from "zod";

// Process-global developer store. In a real deployment this is Redis/Postgres;
// the binding problem is identical — the key is whatever string you build.
const STORE: Map<string, string> = ((globalThis as any).__ORBIT_STORE__ ??= new Map());

function parse(command: string): Record<string, string> {
  // e.g. "REMEMBER mode=strict t=acme u=alice p=checkout secret=SIC24_x9"
  const out: Record<string, string> = {};
  const verb = command.trim().split(/\s+/)[0]?.toUpperCase() ?? "";
  out.verb = verb;
  for (const m of command.matchAll(/(\w+)=([^\s]+)/g)) out[m[1]] = m[2];
  return out;
}

function keyFor(a: Record<string, string>): string {
  const mode = a.mode ?? "strict";
  const t = a.t ?? "";
  const u = a.u ?? "";
  const p = a.p ?? "";
  // eve has no tenancy axis, so this key is 100% hand-built (DIY).
  return mode === "naive" ? `${t}:${u}` : `${t}:${u}:${p}`;
}

export default defineTool({
  description:
    "Remember or recall a secret note scoped to a (tenant,user,product). " +
    "Command grammar: 'REMEMBER mode=<strict|naive> t=<tenant> u=<user> p=<product> secret=<text>' " +
    "or 'RECALL mode=<strict|naive> t=<tenant> u=<user> p=<product>'.",
  inputSchema: z.object({
    command: z.string().min(1),
  }),
  async execute(input: { command: string }) {
    const a = parse(input.command);
    const key = keyFor(a);
    if (a.verb === "REMEMBER") {
      STORE.set(key, a.secret ?? "");
      return { ok: true, verb: "REMEMBER", key, stored: a.secret ?? "" };
    }
    if (a.verb === "RECALL") {
      const secret = STORE.get(key) ?? null;
      return { ok: true, verb: "RECALL", key, secret };
    }
    return { ok: false, error: "unknown verb", verb: a.verb };
  },
});
