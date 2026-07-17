// Orbit — a minimal multi-tenant support chatbot on the Cloudflare Agents SDK.
//
// The whole point of this arena: each Agent is exactly ONE Durable Object, and
// each Durable Object has its OWN embedded SQLite database (`this.sql`). Two
// Agents with different NAMES are two different Durable Objects → two physically
// separate SQLite files. There is no API to SELECT across Durable Objects.
//
// The Orbit scope is (tenant_id, user_id, product_id). In this SDK that scope is
// expressed by the *name string* the developer hands to `getAgentByName`. Getting
// the name right → hard, platform-enforced isolation. Getting the name wrong (e.g.
// forgetting product_id) → you address a different-but-still-isolated object, and
// two products collapse into one shared store. That is the honest nuance this
// arena demonstrates.

import { Agent, getAgentByName, type AgentNamespace } from "agents";

export interface Env {
  OrbitAgent: AgentNamespace<OrbitAgent>;
}

// Per-agent (per-DO) durable state kept in the Agents SDK JSON state channel.
type OrbitState = {
  identity: string; // the composite name this DO was addressed by
  turns: number;
};

type Turn = { id: number; role: string; text: string; ts: number };

/**
 * One OrbitAgent instance == one Durable Object == one embedded SQLite DB.
 * Every method below runs *inside* that single object; `this.sql` can only ever
 * see this object's own rows.
 */
export class OrbitAgent extends Agent<Env, OrbitState> {
  initialState: OrbitState = { identity: "unbound", turns: 0 };

  // Called once per DO wake. Create the per-object messages + facts tables.
  onStart() {
    this.sql`CREATE TABLE IF NOT EXISTS messages (
      id    INTEGER PRIMARY KEY AUTOINCREMENT,
      role  TEXT NOT NULL,
      text  TEXT NOT NULL,
      ts    INTEGER NOT NULL
    )`;
    this.sql`CREATE TABLE IF NOT EXISTS facts (
      key   TEXT PRIMARY KEY,
      value TEXT NOT NULL
    )`;
  }

  // Record which composite name addressed this object (for observability/replies).
  async bind(identity: string): Promise<void> {
    if (this.state.identity !== identity) {
      this.setState({ identity, turns: this.state.turns });
    }
  }

  // Working memory: the last N turns from THIS object's SQLite.
  private recent(limit = 12): Turn[] {
    return this.sql<Turn>`SELECT id, role, text, ts FROM messages
      ORDER BY id DESC LIMIT ${limit}`.reverse();
  }

  /**
   * The Orbit "chat" turn. No API key needed: a deterministic stub "LLM" that
   * echoes how much of THIS object's memory it can see, proving per-object
   * memory hydration. A real model would slot in via env + fetch here.
   */
  async chat(prompt: string): Promise<{ reply: string; hydrated: number; identity: string }> {
    const now = Date.now();
    const history = this.recent();
    this.sql`INSERT INTO messages (role, text, ts) VALUES ('user', ${prompt}, ${now})`;

    // Stub reply: surface the count of prior turns this object remembers.
    const reply = `[orbit:${this.state.identity}] I have ${history.length} prior turns for you. You said: ${prompt}`;
    this.sql`INSERT INTO messages (role, text, ts) VALUES ('assistant', ${reply}, ${now + 1})`;
    this.setState({ identity: this.state.identity, turns: this.state.turns + 1 });
    return { reply, hydrated: history.length, identity: this.state.identity };
  }

  // Store a private fact (a "secret") in THIS object's SQLite.
  async remember(key: string, value: string): Promise<void> {
    this.sql`INSERT OR REPLACE INTO facts (key, value) VALUES (${key}, ${value})`;
  }

  // Read a fact from THIS object's SQLite. Cannot reach any other object.
  async recall(key: string): Promise<string | null> {
    const rows = this.sql<{ value: string }>`SELECT value FROM facts WHERE key = ${key}`;
    return rows.length ? rows[0].value : null;
  }

  // Full transcript of THIS object only.
  async history(): Promise<Turn[]> {
    return this.sql<Turn>`SELECT id, role, text, ts FROM messages ORDER BY id ASC`;
  }

  // Capability probe: is SQLite FTS5 available inside a Durable Object? Decides
  // whether the "facts journal / keyword search" tier is a real built-in or DIY.
  async probeFts(): Promise<{ ok: boolean; hit?: string; error?: string }> {
    try {
      this.sql`CREATE VIRTUAL TABLE IF NOT EXISTS fts_probe USING fts5(body)`;
      this.sql`INSERT INTO fts_probe (body) VALUES ('production uses aws lambda')`;
      const rows = this.sql<{ body: string }>`SELECT body FROM fts_probe WHERE fts_probe MATCH ${"aws"}`;
      return { ok: rows.length > 0, hit: rows[0]?.body };
    } catch (e) {
      return { ok: false, error: String(e) };
    }
  }
}

/**
 * Compose the Durable Object name from the Orbit scope. In the Agents SDK the
 * name is the isolation surface: a plain string the framework binds to nothing.
 * The `|` separator and the inclusion of every axis are the developer's
 * discipline. This is the NAIVE path — fine when the caller is already trusted,
 * unsafe when the segments come straight off an untrusted request (see the
 * strong path below).
 */
export function orbitName(tenant: string, user: string, product: string): string {
  return `${tenant}|${user}|${product}`;
}

// ---------------------------------------------------------------------------
// The STRONG path: bind the object name to VERIFIED identity.
//
// The naive path takes the scope straight off the URL, so the name is only as
// trustworthy as the caller. The SDK gives a first-class seam to fix this: the
// routing entry points (`routeAgentRequest` / a custom `fetch`) can run an
// `onBeforeConnect` / `onBeforeRequest` check that verifies a credential BEFORE
// the Durable Object wakes. (Source-verified in the installed `.d.ts`:
// `getAgentByName` itself does NOT run `onBeforeConnect` — "the caller is
// assumed to have performed whatever access checks are needed" — so the check
// belongs on the routing path, not on the RPC stub.)
//
// The competent-dev pattern:
//   1. verify a bearer token  -> trusted claims
//   2. build the DO name FROM the claims, never from caller-supplied path text
//   3. must-agree: if the URL scope disagrees with the claims, reject (403)
// This mirrors aisotropy's must-agree JWT bind (ADR-0024). The name is still a
// bare string — Cloudflare offers no value-object to make a dropped axis a
// compile error — but it is now minted from verified identity, so a caller
// cannot address a scope it was not authorized for.
// ---------------------------------------------------------------------------

export type OrbitClaims = { tenant: string; user: string; product: string };

/**
 * Stub verifier standing in for HS256/JWKS signature verification. Returns the
 * trusted claims or null. A real implementation checks a signature; the SHAPE of
 * the result (a trusted claims object) is what matters for the binding pattern.
 * Arena token format: "tok:tenant|user|product" (a signed JWT in production).
 */
export function verifyOrbitToken(token: string | null): OrbitClaims | null {
  if (!token) return null;
  const bearer = token.replace(/^Bearer\s+/i, "");
  const m = bearer.match(/^tok:([^|]+)\|([^|]+)\|([^|]+)$/);
  if (!m) return null;
  return { tenant: m[1], user: m[2], product: m[3] };
}

/** Derive the DO name from VERIFIED claims — the identity-bound naming path. */
export function nameFromClaims(c: OrbitClaims): string {
  return orbitName(c.tenant, c.user, c.product);
}

/**
 * Must-agree bind: the scope the client asked for (URL path) must match the
 * verified identity (token claims), or the request is rejected before any object
 * is addressed. Returns the identity-bound DO name on success.
 */
export function resolveVerifiedName(
  pathScope: OrbitClaims,
  token: string | null,
): { ok: true; name: string } | { ok: false; status: number } {
  const claims = verifyOrbitToken(token);
  if (!claims) return { ok: false, status: 401 }; // no/invalid credential
  if (
    claims.tenant !== pathScope.tenant ||
    claims.user !== pathScope.user ||
    claims.product !== pathScope.product
  ) {
    return { ok: false, status: 403 }; // path scope disagrees with verified identity
  }
  return { ok: true, name: nameFromClaims(claims) };
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    // Orbit contract: POST /v1/tenant/{tenant}/user/{user}/product/{product}/chat
    const m = url.pathname.match(
      /^\/v1\/tenant\/([^/]+)\/user\/([^/]+)\/product\/([^/]+)\/chat$/,
    );
    if (request.method === "POST" && m) {
      const [, tenant, user, product] = m;
      const body = (await request.json().catch(() => ({}))) as { prompt?: string };

      // Strong path: if the caller presents a credential, enforce the must-agree
      // bind and derive the name from VERIFIED claims. This is the routing-side
      // `onBeforeConnect`-style check the SDK expects before waking the DO.
      const auth = request.headers.get("Authorization");
      let name: string;
      if (auth) {
        const r = resolveVerifiedName({ tenant, user, product }, auth);
        if (!r.ok) return new Response("Forbidden", { status: r.status });
        name = r.name;
      } else {
        // Naive path (no credential): the URL is trusted as-is. Documented and
        // demonstrated so the trade-off is visible, not hidden.
        name = orbitName(tenant, user, product);
      }

      const agent = await getAgentByName(env.OrbitAgent, name);
      await agent.bind(name);
      const out = await agent.chat(body.prompt ?? "");
      return Response.json(out);
    }
    if (url.pathname === "/healthz") return Response.json({ status: "ok" });
    return new Response("Not found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
