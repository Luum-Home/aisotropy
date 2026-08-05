// SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md
//
// A per-(tenant,user,product) memory tool — the honest way to give an eve agent
// a cross-session memory STORE, since eve ships no memory tier of its own.
//
// WHAT THIS ARENA SHOWS (not a defect in eve): eve is a session-scoped durable
// runtime. `defineState` is *session*-scoped (isolated BY CONSTRUCTION), and
// `ctx.session.auth.current` carries a runtime-owned, channel-verified caller
// — principalId/principalType/issuer/subject plus an open `attributes` bag
// (node_modules/eve/dist/src/channel/types.d.ts:60-67). eve's OWN docs tell you
// to put a string `tenantId` in that bag and read it back with a
// `requireTenantCaller(ctx)` helper that THROWS when it is absent
// (node_modules/eve/docs/patterns/multi-tenant-memory.md, .../multi-tenant-auth.md).
//
// CORRECTION (2026-08-04 sweep). The three sentences that used to stand here —
// "there is no tenant_id/user_id/product_id axis anywhere", "multi-tenant memory
// is OUT OF eve's SCOPE BY DESIGN", "there is nothing for eve to validate — it
// has no tenancy primitive" — are FALSE. All three were refutable by grepping
// `node_modules/eve/docs/` in this very checkout, where the three
// `patterns/multi-tenant-*.md` guides have sat since 2026-07-17 10:23. What eve
// genuinely lacks is a tenant-aware MEMORY SUBSYSTEM — its own words
// (`docs/patterns/multi-tenant-memory.md:6`) — i.e. a data plane, not a concept.
//
// SECOND CORRECTION, about this very file: taking the tenant/user/product from
// TOOL ARGUMENTS (the `t=`/`u=`/`p=` grammar below) is exactly what eve's docs
// forbid — "Never accept a tenant or user id from the model. Read both from
// verified session context" (multi-tenant-memory.md:26). This tool is therefore
// a demonstration of the ANTI-pattern eve documents against, kept as-is because
// the run output is a historical artifact; it is NOT the path eve prescribes.
//
// This tool makes that explicit. The `command` carries the scope as plain args,
// and a `mode` selects the key builder:
//   * mode=strict -> key = `${tenant}:${user}:${product}`   (competent-dev path: partitions)
//   * mode=naive  -> key = `${tenant}:${user}`              (product segment dropped: collides)
// The naive builder is the classic same-key/wrong-key bug of ANY key-value store
// (including aisotropy's own BadgerDB if a dev drops a scope segment) — a
// tautology, not an eve failing. The correct composite key partitions products
// fine.
//
// SYMMETRIC-RIGOR CORRECTION (2026-08-04 sweep). This block used to claim
// "aisotropy's narrow difference: `product_id` rides context.Context inside an
// unforgeable Scope, so the naive read is unrepresentable". That was FALSE, and
// false in aisotropy's own favour. `tenancy.Scope` holds {tenant, user} only
// (internal/tenancy/scope.go:72-76) and the working-memory key is
// tenant:user:session (internal/memory/key.go:87-91). `product_id` is a
// request-BODY persona selector (aisotropy/turn.go:40-42,
// aisotropy/config.go:52-56) that never reaches a storage key. On the PRODUCT
// axis neither system has a primitive. The true difference is on tenant/user:
// identity is bound once at aisotropy's HTTP edge and `tenancy.Scope` is
// unconstructable outside `FromContext` — construction vs. eve's prescribed
// (documented, unenforced) `requireTenantCaller(ctx)` discipline.
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
  // SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md
  // eve ships no memory tier, so this key is 100% hand-built (DIY). It is hand-built
  // from TOOL ARGS here, which eve's own docs forbid; the prescribed key source is
  // `ctx.session.auth.current` (see the header block).
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
