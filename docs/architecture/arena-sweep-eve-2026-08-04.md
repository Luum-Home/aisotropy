---
type: Process
title: "Arena sweep — eve (Vercel): the five-point ecosystem sweep the 17 absence claims never had"
description: >-
  The eve arena shipped 17 assertions that Vercel's eve "has no tenancy axis", and none was ever
  swept — the guard could not see eve until its name was added to COMPETITORS. The 17 collapse into
  4 distinct assertions. Three are false as stated: eve's runtime carries a verified caller identity
  on ctx.session.auth, and eve ships three first-party multi-tenant pattern guides, all of which sat
  on disk inside this checkout's own node_modules while the claims were written. The fourth (no
  official Docker guidance) survives only in its narrowest form. Symmetric rigor turned up a fifth
  error pointing the other way — the arena's claim that aisotropy's product_id rides an unforgeable
  Scope is false in our own favour.
tags: [architecture, arena, eve, vercel, multi-tenancy, tenancy, absence-claims, code-truth, research]
timestamp: 2026-08-04T00:00:00Z
---

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

# Arena sweep — eve (Vercel)

> **Why this document exists.** `scripts/absence-claim-guard.py` keeps a ledger of assertions that
> somebody else's product lacks something, made without a recorded ecosystem sweep. eve contributed
> **17** of them and had **never** been swept — not because nobody got around to it, but because
> until 2026-08-04 the guard could not *see* eve: its name was missing from `COMPETITORS`, so its
> absence claims were invisible and the ledger showed it with a single entry. Seventeen published
> assertions about another company's product, zero verified.
>
> This is the five-point sweep required by `.claude/rules/capability-absence-claims.md`, with the
> L0–L3 ladder from `docs/architecture/arena-tenancy-levels.md`, applied to eve **and to aisotropy
> with the same severity in the same document**.

## 1. What eve is (with evidence)

**eve is a genuine third-party framework, not one of ours.** Every check agrees:

| Question | Answer | Evidence |
|---|---|---|
| Publisher | Vercel | `arenas/eve/node_modules/eve/package.json` → `"repository": "git+https://github.com/vercel/eve.git"` |
| License | **Apache-2.0** | same file, `"license": "Apache-2.0"` — permissive, inside the repo's license policy |
| What it does | "Filesystem-first framework for durable backend AI agents that run anywhere" | same file, `"description"` |
| Version in the arena | **0.24.6** | `arenas/eve/package.json` `"eve": "^0.24.6"`; installed tree confirms |
| Current version | **0.30.6** | `https://registry.npmjs.org/eve` → `dist-tags.latest`, read 2026-08-04 |
| Project health | **very active, in public beta** | announced June 2026 (<https://vercel.com/blog/introducing-eve>); six minor versions in ~7 weeks; Vercel states it runs 100+ internal production agents |
| Docs | <https://vercel.com/docs/eve> and <https://eve.dev/docs> — **also shipped inside the npm package** | `package.json` `"files": ["bin","dist","docs",…]`; `node_modules/eve/docs/` is a full docs tree |

So the sweep is warranted: eve is a comparable third-party agent framework, actively developed, and
the 17 entries are genuine competitor-absence claims. **Not** a demo of ours, and not stale — if
anything the opposite, since the arena pinned a version six releases behind current.

**The single most important fact for everything below:** eve ships its documentation *inside the
npm tarball*. The three files that refute the claims —
`node_modules/eve/docs/patterns/multi-tenant-{memory,auth,approvals}.md` — have a filesystem mtime
of **2026-07-17 10:23**, i.e. they were on disk in this very checkout, hours before the arena README
was last edited (12:44) and on the same day the smoke run was recorded. Every claim below was
refutable by one `grep -rn tenant arenas/eve/node_modules/eve/docs/` at authoring time. This is the
`prune-evidence-gate.md` failure mode exactly: a fact asserted from assumption when the source was
already in hand.

## 2. The 17 claims group into 4 distinct assertions

`grep "arenas/eve" arenas/live/absence-claims-baseline.txt` returns 17 lines across 5 files. Grouped
by what they actually assert:

| # | Assertion | Instances | Where |
|---|---|---|---|
| **A1** | eve has no tenancy axis / "the (tenant,user,product) axis eve does not have" / "no `tenant_id`/`user_id`/`product_id` anywhere in the runtime" | **11** | README ×4, FEATURES ×3, smoke.mjs ×3, orbit_memory.ts ×1 |
| **A2** | "There is nothing for eve to validate — it has no tenancy primitive to validate against" | **3** | README ×1, smoke.mjs ×2 |
| **A3** | eve ships no cross-session memory store scoped by tenant/user/product | **1** | orbit_memory.ts:2 |
| **A4** | eve publishes no official Docker guidance | **1** | docker/README.md:11 |
| *(n/a)* | "not a verdict that eve is insecure" — a **disclaimer**, not an absence claim; the guard flagged it on `insecure`-word proximity | 1 | README:189 |

Seventeen entries, **four** real assertions and one guard false-positive.

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

## 3. The five-point sweep

### Point 1 — Core

The decisive reads, all from the installed 0.24.6 tree.

**eve's runtime owns a caller-identity object.** `ctx.session.auth` is `{ current, initiator }`
(`node_modules/eve/dist/src/context/keys.d.ts:20-22`), each a `SessionAuthContext`:

```ts
// node_modules/eve/dist/src/channel/types.d.ts:60-67
export interface SessionAuthContext {
    readonly attributes: Readonly<Record<string, string | readonly string[]>>;
    readonly authenticator: string;
    readonly issuer?: string;
    readonly principalId: string;
    readonly principalType: string;
    readonly subject?: string;
}
```

The `attributes` bag is typed at `dist/src/runtime/sessions/auth.d.ts:13`
(`RuntimeSessionAuthAttributes`) and is zod-validated on the serialization boundary
(`runtimeSessionAuthContextSchema`, same file, lines 21-33). It is populated **only** by a channel's
`AuthFn` — the model has no write path to it.

**A shipped channel already uses it as a tenant axis, unprompted.** eve's Microsoft Teams channel
emits `attributes.tenant_id`, `issuer: "teams:<tenantId>"` and
`principalId: "teams:<tenantId>:<userId>"` (`dist/src/public/channels/teams/defaults.js`,
`defaultTeamsAuth`). The whole Teams inbound/continuation path threads `tenantId`
(`dist/src/public/channels/teams/{api,inbound,teamsChannel}.d.ts`).

**Per-tenant credentials are a documented runtime capability.** `CHANGELOG.md:382`: "MCP and OpenAPI
connections can now resolve `auth` providers and headers from the active session context, enabling
per-caller and **per-tenant credentials**." Type-level echo at
`dist/src/runtime/connections/types.d.ts:164`: "…`ctx.session`, such as selecting a **tenant-specific**
Vercel Connect client."

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md -->
**What is genuinely absent:** a framework-owned data plane. eve has no memory tier, no store
abstraction that persists across sessions, nothing to scope. `defineState` is session-scoped and
eve's own docs say not to use it for long-term memory.

### Point 2 — Official extensions

eve ships a first-party extension mechanism: `defineExtension` (`docs/extensions.md`), scaffolded via
`npx eve@latest extension init`, mounted under `agent/extensions/`. It packages tools, connections,
skills, instructions and hooks.

**No first-party Vercel-authored memory extension was found.** Searched the shipped docs tree
(`grep -rln -i "mem0\|zep\|letta\|langmem" node_modules/eve/docs/` → zero hits in 0.24.6) and the
package's own export surface. The extension mechanism is the *vehicle* third parties use (see
Point 4), not a Vercel memory offering. **Label: NONE (first-party memory extension).**

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### Point 3 — Platform (Vercel hosted tier)

Read from `docs/guides/deployment.md` (shipped in-package) plus the hosted docs. On Vercel, eve
gains: the **AI Gateway** (keyless model auth via project OIDC), **Vercel Workflow** as the durable
world, **Vercel Sandbox** with build-time template prewarm, **Vercel Cron** for schedules,
Deployment-Protection bypass, and an **Agent Runs** observability tab (explicitly gated: "Your
Vercel team needs the feature enabled before it appears").

**The platform adds no tenant-scoped memory tier.** Nothing in the hosted surface supplies what the
OSS core lacks on this axis — unlike, say, LangGraph Platform, whose commercial tier is precisely
where its L2 auth lives. **This is the one place where the arena's instinct was directionally right,
and it is worth stating positively rather than as a vague absence.**

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### Point 4 — Official third-party integrations

This is where the claim ages worst. eve runs a **vendor-operated integration gallery**
(<https://eve.dev/integrations>, ~90+ entries), and the current hosted revision of the very doc that
says eve has no memory subsystem now *opens* by pointing at it: "You can add long-term memory from
the integration gallery using the Memory filter, **or** build tenant-aware memory from your own
application store…" (<https://eve.dev/docs/patterns/multi-tenant-memory>, read 2026-08-04).

| Integration | What it provides | License | Self-hostable? | Last release |
|---|---|---|---|---|
| **Mem0** | "Persistent memory for AI agents and assistants" | **Apache-2.0** (`api.github.com/repos/mem0ai/mem0` → `spdx_id`) | Yes (OSS) + managed cloud | repo pushed **2026-08-04** |
| **Hindsight** (`@vectorize-io/hindsight-eve`) | Memory injected before every turn, retained after, no model tool-call | **MIT** (npm registry) | Yes — "point at your own base URL… pass `apiKey: null`" | **v0.2.1** |
| **Upstash AgentKit** | Long-term memory, Redis Search, durable chat history | not verified in this sweep | cloud-first | not verified |

**Label: OFFICIAL-THIRD-PARTY-INTEGRATION (not COMMUNITY-ONLY, not NONE)** — these are listed in the
vendor's own gallery, and two of the three are permissively licensed and self-hostable. A statement
that eve users have no memory option is not defensible.

Honest caveat on scoping quality: Hindsight scopes by `HINDSIGHT_BANK_ID` with "one bank per user"
as *guidance*; wiring the bank id to the verified caller is still the developer's job. So these
integrations move eve from "build it yourself" to "install it and wire the scope yourself" — a real
improvement, not a tenancy guarantee.

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### Point 5 — Vendor docs

The heaviest evidence. eve ships **three** first-party `patterns/multi-tenant-*.md` guides:

- **`docs/patterns/multi-tenant-memory.md`** — line 6, verbatim: *"eve does not have a tenant-aware
  memory subsystem. You can build one today by composing three existing primitives: 1. route auth
  puts the tenant and user on `ctx.session.auth`; …"*. It supplies a `requireTenantCaller(ctx)` that
  **throws** unless `auth.current.principalType === "user"` and `attributes.tenantId` is a string;
  a `MemoryStore` contract where "tenant and user are mandatory inputs to every read and write"; and
  at line 26 the rule the arena's own demo breaks: **"Never accept a tenant or user id from the
  model. Read both from verified session context."** It even pre-empts the semantic-search variant:
  "The tenant-and-user scope must remain part of the query, not a filter applied afterward" — the
  same in-scan-vs-post-filter distinction `.claude/rules/constitutional-gates.md` makes for Qdrant.
- **`docs/patterns/multi-tenant-auth.md`** — tenant-scoped *outbound* credential selection for tools,
  OpenAPI and MCP connections, driven off the same `ctx.session.auth.current.attributes.tenantId`.
- **`docs/patterns/multi-tenant-approvals.md`** — the HITL axis.
- Plus `docs/guides/auth-and-route-protection.md` (ordered auth walk, fail-closed guarantee) and
  `docs/concepts/security-model.md`.

**Label per the rule's taxonomy: prescribed-pattern (vendor docs), NATIVE for the identity primitive
they compose, NONE for the memory tier itself.**

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

## 4. Maturity table

| Capability | Maturity | Evidence | Currency |
|---|---|---|---|
| Verified caller identity on the session (`principalId`, `principalType`, `attributes`) | **NATIVE** | `dist/src/channel/types.d.ts:60-67`; `dist/src/context/keys.d.ts:20-22` | in 0.24.6; still in 0.30.6 docs |
| Fail-closed route auth (`httpBasic`, `jwtHmac`, `jwtEcdsa`, `oidc`, `vercelOidc`, custom `AuthFn`) | **NATIVE** | `dist/src/runtime/governance/auth/types.d.ts`; `docs/guides/auth-and-route-protection.md` | 0.24.6 |
| `tenantId` as a first-class attribute convention | **NATIVE (open bag) + prescribed by vendor docs** | `docs/patterns/multi-tenant-auth.md:17`; Teams channel emits it already | 0.24.6 |
| Tenant-scoped **memory subsystem** in core | **NONE** — vendor states it verbatim | `docs/patterns/multi-tenant-memory.md:6` | 0.24.6 & current hosted docs |
| Multi-tenant memory **pattern** | **VENDOR DOCS (prescribed-pattern)** | three `patterns/multi-tenant-*.md` files | shipped in-package 2026-07-17 |
| Long-term memory product | **OFFICIAL-THIRD-PARTY-INTEGRATION** | eve.dev integration gallery: Mem0 (Apache-2.0, pushed 2026-08-04), Hindsight (MIT, v0.2.1), Upstash AgentKit | current |
| First-party memory extension | **NONE** | no hit in shipped docs or export surface | 0.24.6 |
| Platform-tier tenant memory | **NONE** | `docs/guides/deployment.md` §7 + hosted docs — platform adds gateway/sandbox/cron/observability, not memory | current |
| Official **server** Docker image / Dockerfile | **NONE** | no server Dockerfile in the repo; `ghcr.io/vercel/eve:latest` is the **sandbox** image per `docs/sandbox.mdx:150` | current |
| Official **self-host** deployment guidance | **NATIVE** | `docs/guides/deployment.md` §8 "Deploy without Vercel" | 0.24.6 |

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

## 5. L0–L3 placement

Using the ladder from `docs/architecture/arena-tenancy-levels.md`.

**eve: L2 on identity / L0 on the memory data plane.**

- *L2 on identity* because the binding happens once, at the channel boundary, in framework-owned
  code: the `AuthFn` runs before the run starts, the resulting `SessionAuthContext` is runtime-owned
  and reaches every tool/hook/instruction as `ctx.session.auth.current`, and neither the model nor a
  tool argument can forge it. Default posture is fail-closed — the framework default and the
  scaffolded `placeholderAuth()` both reject production browser traffic. It is not L3: the
  `attributes` bag is an untyped `Record<string, string | string[]>`, so `tenantId` is a convention
  the developer must place *and* read, and nothing rejects a handler that ignores it.
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md -->
- *L0 on the data plane* because there is no framework-owned store to scope. `requireTenantCaller`
  is prescribed, not enforced — a developer who never calls it gets no error.

This is structurally the **same split the ladder already records for Genkit Go** ("L0 data plane /
L2 identity"), which is a useful consistency check: the classification was not invented for eve.

**What this means for the original claim:** "no tenancy axis" placed eve at **L0 across the board**.
It is L0 on *one* of two layers. Per the rule's own words, "conflating L1 with L0 overstates the
gap"; conflating **L2** with L0 overstates it by two rungs.

**aisotropy on the same ladder, same severity** (carried forward from `arena-tenancy-levels.md`,
re-verified in this sweep):

- **Storage: L3.** `internal/memory/key.go:87-91` — `Key{Tenant, User, Session}` with a `Validate()`
  every operation calls; `internal/tenancy/scope.go:72-76` — `Scope{tenant, user}` with **unexported
  fields** and `FromContext` as the only constructor, so a bare `tenancy.Scope{}` cannot silently
  widen a query.
- **HTTP edge: L2 with auth configured, L1 in NoAuth mode.** NoAuth needs two explicit opt-ins
  (`AISOTROPY_ALLOW_NOAUTH`, plus `AISOTROPY_ALLOW_NOAUTH_NONLOCAL` for a non-loopback bind), but
  once on, identity provenance is whatever the caller puts in the URL — the same parameter-trust
  failure mode we criticise elsewhere.
- **Library facade: mandatory-but-trusted.** `App.Turn` requires a `(tenant, user, session)` triple
  and validates it, so it cannot be *omitted* — but the embedder supplies the tenant **string**, and
  aisotropy does not prove the caller is who they claim.
- **Product axis: no primitive at all — see §7.**

Both systems therefore depend on a correctly-supplied identity at their outermost layer. aisotropy's
advantage is real but narrower than the arena claimed: it is that once identity is in `ctx`, the
storage key is *unconstructable* without it, whereas eve's equivalent discipline is documented and
unenforced.

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

## 6. Verdict per assertion, with replacement sentences

### A1 — "eve has no tenancy axis" (11 instances) → **FALSE. Corrected.**

Refuted by `dist/src/channel/types.d.ts:60-67`, `dist/src/context/keys.d.ts:20-22`, the Teams
channel's `tenant_id` emission, and three vendor pattern docs — all present in the checkout when the
claim was written.

> **Replacement:** *"the tenant-aware **memory subsystem** eve does not have (its own docs' words) —
> note the identity axis itself is native: `ctx.session.auth.current` carries a channel-verified
> principal plus an `attributes` bag eve's docs tell you to put `tenantId` in."*

Applied at `arenas/eve/README.md` (heading, intro line, isolation bullets), `arenas/eve/FEATURES.md`
(heading, gap row), `arenas/eve/scripts/smoke.mjs` (header comment, section heading, collision
comment, verdict block), `arenas/eve/agent/tools/orbit_memory.ts` (header, `keyFor` comment).

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### A2 — "there is nothing for eve to validate — it has no tenancy primitive" (3) → **FALSE. Corrected.**

There is a primitive *and* a vendor-supplied validator over it:
`requireTenantCaller(ctx)` throws unless `auth.current.principalType === "user"` and
`attributes.tenantId` is a string (`docs/patterns/multi-tenant-memory.md`,
`docs/patterns/multi-tenant-auth.md:17-34`).

> **Replacement:** *"eve does have something to validate against — `ctx.session.auth.current`, with
> the vendor's own `requireTenantCaller(ctx)` throwing on an unauthenticated tenant. What eve does
> not do is **enforce** it: skip the helper and nothing complains. The gap is enforcement, not
> concept."*

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### A3 — "eve does not ship a cross-session memory store" (1) → **TRUE for core, incomplete. Qualified.**

The vendor confirms it verbatim. But the sentence, standing alone, reads as "eve users have no
memory option", which Point 4 refutes.

> **Replacement:** *"…since eve ships no memory tier of its own — long-term memory comes either from
> eve's integration gallery (Mem0, Apache-2.0; Hindsight, MIT; Upstash AgentKit) or from a store you
> build, per eve's own `patterns/multi-tenant-memory.md`."*

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### A4 — "eve has NO official Docker guidance" (1) → **narrowly TRUE, supporting sentences FALSE. Corrected.**

Survives: there is no server `Dockerfile` in the eve repo, and the one published image
(`ghcr.io/vercel/eve:latest`) is eve's **sandbox** runtime image (`docs/sandbox.mdx:150`), not a
server image. The arena's caveat on that point was right.

Does not survive:

1. *"Its documented deployment target is Vercel."* `docs/guides/deployment.md` **§8 is literally
   titled "Deploy without Vercel"** and gives the commands, plus a self-host checklist (persist
   `.eve/.workflow-data`, forward **both** `/eve/` and `/.well-known/workflow/`, replace
   `vercelOidc()`). It names "your own process manager, **container platform**, or reverse proxy".
2. *"eve agents compile to Vercel Functions."* Same doc, "How portability works": Nitro is the HTTP
   host layer; Vercel Build Output is emitted **only when `VERCEL` is set**.
3. *"Docker appears in eve's docs only as a local sandbox backend."* `deployment.md:148` lists Docker
   as a backend choice *inside the self-deployment guidance*.

> **Replacement:** *"eve publishes no official Dockerfile and no official **server** image — the one
> published image, `ghcr.io/vercel/eve:latest`, is its sandbox runtime. It does ship first-party
> **self-host** guidance (`docs/guides/deployment.md` §8, 'Deploy without Vercel'), so absent a
> Dockerfile this image boots eve's own official build artifact exactly as `eve start` would."*

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

### The guard false-positive

`README.md:189` — "That is one axis of difference, **not a verdict that eve is insecure**" — is a
*disclaimer*, caught because `insecure` sits near a competitor name. It needed no correction, only a
marker. Worth noting for whoever tunes the guard: the DEFECT regex has no polarity check, so
sentences that explicitly *withdraw* a defect claim are flagged like ones that make it.

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

## 7. Symmetric rigor — the error pointing the other way

The rule demands the same severity on our own claims. Applying it produced the most serious finding
in this sweep, and it is about **aisotropy**.

Three places in the eve arena assert: *"aisotropy's narrow difference: `product_id` rides
`context.Context` inside an unforgeable value-object `Scope`, so the naive read is **unrepresentable**
rather than merely discouraged."* (`README.md:187-188`, `orbit_memory.ts:21-23`, `smoke.mjs:137`.)

**That is false, and false in our own favour.**

| What the claim says | What the code says |
|---|---|
| `product_id` is in an unforgeable `Scope` | `tenancy.Scope` has exactly three unexported fields — `tenant`, `user`, `hasUser` (`internal/tenancy/scope.go:72-76`). No product. |
| `product_id` scopes memory | The working-memory key is `Key{Tenant, User, Session}` → `tenant:%s:user:%s:session:%s` (`internal/memory/key.go:87-91`, `:117`). No product. |
| `product_id` rides `context.Context` | It rides the **request body**. `ChatInput.ProductID` selects a per-product **persona base prompt** (`aisotropy/turn.go:40-42`, `aisotropy/config.go:52-56`, `aisotropy/aisotropy.go:93`). |
| — | `grep -rn "Product" internal/{tenancy,memory,semantic,facts}/` returns **zero** non-test hits. `internal/responsecache`'s `Scope` is `{Tenant, User}` too (`exact.go:25-28`). |

The live arena adapter says so out loud in its own error message: *"aisotropy's working memory is
keyed `tenant:{t}:user:{u}:session:{s}`"* (`arenas/live/aisotropy/mode_scoped.go:67`).

Two consequences worth stating plainly:

1. **The comparison axis was chosen such that neither system has it.** The eve arena built its whole
   demo around the `(tenant, user, product)` triple and framed product as "the 4th axis eve does not
   have". aisotropy does not have it as a memory scope either. The genuine aisotropy difference is
   on **tenant/user**, and it is narrower than "unrepresentable": strong downstream of the identity,
   caller-trusted at the boundary that supplies it.
2. **The arena's demo does what eve's docs forbid.** `orbit_memory` takes tenant/user/product from
   **tool arguments** parsed out of a model-authored command string — precisely the thing
   `docs/patterns/multi-tenant-memory.md:26` prohibits ("Never accept a tenant or user id from the
   model"). It also happens to be what our own `.claude/rules/constitutional-gates.md` prohibits for
   aisotropy ("Never accept `tenant_id` from body/header/query as the source of truth"). The
   "documented DIY footgun" the arena demonstrates is a footgun eve's documentation already closes;
   the demo had to depart from eve's prescribed pattern to build it. The arena's parenthetical
   "(or, better, derives the key from the channel-verified identity rather than from tool args)" is
   correct — but presents as *our* suggestion what is in fact **eve's own mandate**.

All three instances are corrected in place.

## 8. What was changed

13 of the 17 baseline lines had their text rewritten; the remaining 4 sit inside **verbatim pasted
run output** from the 2026-07-17 arena run (`README.md` ×2, `FEATURES.md` ×2) and were **deliberately
left unedited** — rewriting recorded program output would fabricate evidence. Each paste now carries
an annotation, immediately after the fence, naming the two sentences inside it that are now known to
be wrong and pointing here.

| File | Change |
|---|---|
| `arenas/eve/README.md` | Intro line, isolation section fully rewritten (identity NATIVE / memory tier absent), naive-key paragraph rewritten with both corrections, L0–L3 placement added, run-paste annotated |
| `arenas/eve/FEATURES.md` | Section heading corrected, gap row corrected, run-paste annotated |
| `arenas/eve/agent/tools/orbit_memory.ts` | Header block rewritten with file:line evidence + the anti-pattern disclosure; `keyFor` comment corrected; symmetric-rigor correction added. **Comments only — no executable line touched.** |
| `arenas/eve/scripts/smoke.mjs` | Header comment, section heading, collision comment and verdict block corrected. `node --check` passes. **No assertion or control flow touched** — the assertions never depended on these strings. |
| `arenas/eve/docker/README.md` | Narrow claim kept, three supporting sentences corrected with `deployment.md` citations |

`python3 scripts/absence-claim-guard.py` → **exit 0**, 174 swept (was 140), 394 grandfathered
(was 417). `--write-baseline` was **not** run.

**Known divergence, stated rather than hidden:** `scripts/smoke.mjs` now prints different text than
the pastes in `README.md`/`FEATURES.md`, which are the preserved 2026-07-17 run. Re-running the arena
(needs Node ≥ 24 and a build) reconciles them. Both pastes say so inline.

**Baseline lines that can now be deleted** — all 17 `arenas/eve/…` rows: 13 because their text no
longer exists (dead keys), 4 because they now carry current SWEPT markers. This sweep did **not**
edit `arenas/live/absence-claims-baseline.txt`: concurrent sessions were sweeping dify, letta,
adk-go and cloudflare against the same file, and a shared-file write under concurrency is exactly
what `.claude/rules/verify-before-execute.md` says to coordinate rather than race.

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md (this document IS the sweep; every eve absence statement below is one of its findings, evidenced in-line) -->

## 9. What was NOT verified

Stated so nobody reads this document as broader than it is.

- **Upstash AgentKit's license and self-hostability** — listed in the gallery, not verified at the
  source. Left blank in the table rather than guessed.
- **eve 0.30.6's source.** Everything core-level here is read from the **0.24.6** tree the arena
  pinned. Six releases have landed since. The hosted docs for 0.30.x still carry the multi-tenant
  patterns (checked), and the current revision *adds* the gallery pointer, so the direction of
  travel strengthens the refutation rather than weakening it — but I did not install 0.30.6 and
  diff it. **A claim about 0.30.6's core is not supported by this document.**
- **Whether the gallery integrations actually scope correctly.** I read what they claim; I ran none
  of them. Hindsight's "one bank per user" is guidance, and I did not verify that its eve hooks read
  `ctx.session.auth`.
- **The arena was not re-run.** Node 24 + `eve build` + a container smoke is heavy, and the
  corrections are prose/comment-level. The 2026-07-17 results stand as recorded.
- **Other frameworks' claims.** Only the 17 eve rows were swept. dify, letta, adk-go and cloudflare
  were being swept concurrently by other sessions; nothing here speaks for them.
- **eve's non-memory absence claims elsewhere in the repo** — `arenas/ARENA-SPEC-v2.md:536` mentions
  eve in a "lean durable-execution" line. It is a different assertion (about execution model, not
  tenancy), it is outside this sweep's territory, and it remains grandfathered.
