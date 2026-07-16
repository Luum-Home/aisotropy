// A per-(tenant,user,product) memory tool — the honest way to give an eve agent
// cross-session memory scoped by tenant/user/product.
//
// THE POINT OF THE ARENA: eve binds NO tenant/user/product identity to its
// durable memory. `defineState` is *session*-scoped and `ctx.session` exposes
// only { id, turn, auth, parent lineage } — there is no tenant_id/user_id/
// product_id axis anywhere in the runtime. So a cross-session, per-customer,
// per-product memory store is something the DEVELOPER builds, and the key is a
// string the developer concatenates by hand. The framework validates nothing.
//
// This tool makes that explicit. The `command` carries the scope as plain args,
// and a `mode` selects the key builder:
//   * mode=strict -> key = `${tenant}:${user}:${product}`   (product bound)
//   * mode=leaky  -> key = `${tenant}:${user}`              (product dropped)
// The leaky builder is one edit — one dropped `:${product}` — away from the
// strict one, and TypeScript is equally happy with both. That is the whole
// isolation story: CONVENTION, not construction.
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
  // The framework does NOT do this for you. It is hand-built here.
  return mode === "leaky" ? `${t}:${u}` : `${t}:${u}:${p}`;
}

export default defineTool({
  description:
    "Remember or recall a secret note scoped to a (tenant,user,product). " +
    "Command grammar: 'REMEMBER mode=<strict|leaky> t=<tenant> u=<user> p=<product> secret=<text>' " +
    "or 'RECALL mode=<strict|leaky> t=<tenant> u=<user> p=<product>'.",
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
