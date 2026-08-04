---
type: Analysis
title: Positioning — is key-brokering a co-equal pillar or a flagship optional module?
description: A read-only, evidence-grounded analysis of the single strategic decision the consolidation proposal flags but does not resolve — whether key-brokering (RunTask/RunTasks + the sandbox seam) is a co-equal second pillar of the core, or a flagship OPTIONAL module demoted below one isolation+memory headline. Grounds every coupling/LOC/audience claim in the import graph and file inspection, then lands a recommendation with a stated confidence and falsifier.
tags: [architecture, positioning, key-brokering, runtask, sandbox, scope, decision, analysis]
timestamp: 2026-07-17T00:00:00Z
status: "decided: (b) flagship optional module — executed (2026-07-17): README + getting-started restructured to one isolation+memory headline with key-brokering demoted below the fold; key-brokering façade + CLI fenced behind the `keybroker` build tag so the default build excludes it. Part 1 and Part 2 both landed."
---

# Positioning — key-brokering: co-equal pillar, or flagship optional module?

> **DECISION EXECUTED (2026-07-17) — option (b), flagship optional module.** Both parts landed:
> **Part 1 (positioning):** the root `README.md` and `docs/getting-started.md` now lead with a
> single headline — *multi-tenant isolation by construction + tiered memory, in a single Go binary* —
> with a runnable above-the-fold proof (`go test -run 'TestIsolation.*NoCrossTenantLeak' ./aisotropy`,
> the black-box two-tenant leak test), and key-brokering demoted to a clearly-marked "Optional
> flagship capability" section below the fold, reframed as the best *demonstration of* the moat.
> **Part 2 (packaging):** the public key-brokering façade (`aisotropy/runtask.go`, `runtasks.go`,
> `sandbox.go`) and the `aisotropy task` CLI subcommand are fenced behind a `//go:build keybroker`
> tag, so the **default build excludes `App.RunTask`/`RunTasks`** (proven: `go doc ./aisotropy App`
> shows no `RunTask`; `go build`/`vet`/`test ./... -race` green Docker-free) while `-tags keybroker`
> restores the full surface (proven: `go build`/`vet`/`test -tags keybroker ./... -race` green, the
> broker invariant executing against real Docker). The `internal/taskagent` + `internal/sandbox`
> engine packages stay untagged (pure Go; Docker is a call-time, not build-time, dependency), so the
> default suite still unit-tests them and the promoted-set coverage floor holds (92.86% ≥ 92%). This
> is documented as an **API change** in [`docs/deployment/index.md`](../deployment/index.md#build-time-capability-key-brokering--tags-keybroker).
> The analysis below is preserved as the grounding for the decision.

> **The decision this answers.** The [consolidation proposal](consolidation-proposal.md) §2
> flags "the one genuine strategic decision" and explicitly declines to resolve it: key-brokering
> (`internal/taskagent` + `internal/sandbox`, surfaced as `App.RunTask`/`RunTasks`) is now the
> **README's lead differentiator** ([`README.md:1-20`](../../README.md)) but **requires Docker at
> runtime** and is a large, separable surface. It is either **(a)** a co-equal second pillar of the
> core, or **(b)** a flagship optional module, or **(c)** a spun-out companion. It "cannot be both
> 'the headline' and 'an optional add-on that needs Docker' without confusing the reader." This
> document resolves it.

This is **analysis only**. Nothing is deleted, demoted, or moved by writing it. Every coupling,
LOC, and dependency claim below comes from `grep`/import inspection run against the tree at HEAD
(commit `172f125`), not from estimates. Where a call is subjective it says so.

---

## 0. The answer, up front

- **Coupling verdict:** key-brokering is a **clean leaf**, not entangled. The core imports **nothing**
  from it; it imports a handful of core packages one-directionally. Splitting it behind a build tag
  or into a sub-module is nearly mechanical — **zero edits to the isolation+memory core**. (§1)
- **Audience verdict:** **overlapping but not identical buyers.** Isolation+memory is the *substrate*
  (multi-tenant SaaS platform builders); key-brokering is one *flagship application* of it (people
  who specifically need to run untrusted agentic tasks). The README itself already nests them this
  way in prose ([`README.md:76-79`](../../README.md)) — it just contradicts that nesting in its
  headline. (§2)
- **Recommendation: (b) FLAGSHIP OPTIONAL MODULE.** Lead with **one** headline —
  isolation-by-construction + tiered memory (the Docker-free, tested-by-default moat that 24 of 27
  `internal/` packages serve) — and demote key-brokering to the flagship opt-in module: the best
  *demo of* the moat, not a co-equal gate in front of it. **Confidence: high on coupling, medium-high
  on the call.** Falsifier in §4. (§3–§4)

---

## 1. Coupling — how separable is key-brokering, really?

### 1.1 The exact import graph (measured)

The key-brokering surface is three things: the internal engine `internal/taskagent`, the isolation
backend `internal/sandbox`, and the public façade files `aisotropy/runtask.go`,
`aisotropy/runtasks.go`, `aisotropy/sandbox.go`.

**What each imports from inside the module** (`grep -rhoE 'github.com/Luum-Home/aisotropy/internal/…'`,
non-test):

| Package | Imports from the module | Direction |
|---|---|---|
| `internal/sandbox` | **nothing** — only stdlib + `github.com/google/uuid` | pure leaf |
| `internal/taskagent` | `internal/agentloop`, `internal/llm`, `internal/memory`, `internal/tools`, `internal/sandbox` | depends *down* into core + sandbox |
| `aisotropy/{runtask,runtasks,sandbox}.go` | `internal/taskagent`, `internal/sandbox`, `internal/llm` | façade over the engine |

**Who imports the key-brokering packages** (`grep -rl 'internal/taskagent\|internal/sandbox'`, whole tree):

```
internal/taskagent  ← aisotropy/runtask.go, aisotropy/runtasks.go, arenas/key-brokering/broker_arena_test.go
internal/sandbox     ← aisotropy/sandbox.go, and internal/taskagent (its own engine) + that package's tests
```

**The load-bearing fact:** filtering to *other* core `internal/` packages returns **empty** —
`grep -rl 'internal/taskagent\|internal/sandbox' internal/ | grep -vE 'internal/(taskagent|sandbox)/'`
has **no matches**. No memory tier, no API layer, no LLM client, no tenancy code imports key-brokering.
The dependency arrow points strictly **one way**: `taskagent → {llm, memory, tools, agentloop, sandbox}`.
The core does not know key-brokering exists.

Corroborating this at the boot layer: `grep -n 'RunTask\|taskagent\|sandbox\|docker'` over
`aisotropy/app.go` and `aisotropy/aisotropy.go` (the composition root + the type-alias surface)
returns **nothing**. The default runtime — `App`, `Turn`, `Run`, every memory tier — compiles and
boots with **no reference to Docker, the sandbox, or the task agent**. `RunTask` is an *additive
method* on `*App` living in its own files, exactly as its own doc comment states:
"It is additive: RunTask boots no HTTP runtime and does not touch the App's Run/Turn state — it only
reads the App's llm client" ([`aisotropy/runtask.go:147-149`](../../aisotropy/runtask.go)).

### 1.2 One shared dependency to keep straight: `internal/agentloop`

`taskagent` imports `internal/agentloop` — but agentloop is **not** key-brokering-specific and must
**stay in the core**. It is the homegrown structured-output tool loop that also drives the
`/chat/agent` HTTP route (`internal/api/chat_agent.go:150-152`) and `internal/llm/tools.go`. So the
separable set is `taskagent` + `sandbox` + the three façade files; agentloop (299 src LOC) is shared
infrastructure that a split leaves untouched.

Note the distinction this exposes: **`/chat/agent` is an in-process agent loop, not key-brokering.**
It runs tools in-process via agentloop — no container, no Docker, no broker invariant. Key-brokering
(`RunTask`) has **no HTTP route at all**; its only surfaces are the library method and the
`cmd/aisotropy task` CLI subcommand (`cmd/aisotropy/task.go`). Demoting key-brokering therefore
touches **no HTTP contract** — a meaningful de-risking of options (b) and (c).

### 1.3 Measured LOC of the separable surface

`find … -name '*.go' ! -name '*_test.go' | wc -l`:

| Unit | src LOC | test LOC |
|---|---|---|
| `internal/taskagent` | 1,369 | 1,930 |
| `internal/sandbox` | 1,184 | 1,259 |
| `aisotropy/{runtask,runtasks,sandbox}.go` | 421 | — |
| **Separable total** | **2,974 src** | 3,189 test |
| `internal/agentloop` (shared — **stays in core**) | 299 | 324 |

The proposal's "~2,550 separable LOC" was a close under-estimate; the exact figure is **2,974 src
LOC** across two leaf packages and three façade files. Either way the scale claim holds: this is a
large, cleanly bounded surface.

### 1.4 What a split would and would not buy

- **External-dependency slimming: none within the same module.** The only external dep unique to the
  sandbox is `github.com/google/uuid`, and `grep` shows `internal/semantic/semantic.go` also imports
  it. So a build-tag split *inside* the current module drops **no** third-party dependency from the
  core. Only a **separate-module spin-out (option c)** would let the core's `go.mod` shed anything —
  and even then the saving is marginal (uuid stays for semantic).
- **The runtime property that matters is already true.** The core's Docker-free default boot is not a
  goal a split would *achieve* — it is a property the code **already has** (§1.1). A split only makes
  that property *legible in the packaging* instead of implicit.
- **Precedent for clean extraction already exists.** `examples/task-agent/` is already its own Go
  module with its own `go.mod`, importing key-brokering purely through the public façade. The
  separability is not theoretical; it is demonstrated by a shipped example.

**Coupling grade: A (trivially separable).** A build-tag demotion is ~5 files tagged (2 leaf packages
+ 3 façade files), zero core edits, no HTTP-contract change, no test rewrite of the core suite.

---

## 2. The two pillars — one buyer or two?

### Pillar 1 — isolation-by-construction + tiered memory (the runtime)

- **The claim:** a different `TenantID` *cannot* reach another tenant's data, and the unscoped read
  is unrepresentable — enforced per statement/key and propagated by `context.Context`, now fronted by
  a default-deny authenticator (ADR-0011/0024). Plus a memory hierarchy by access temperature
  (BadgerDB → SQLite/rqlite → cosine/Qdrant → sleep-cycle).
- **Who it's for:** engineers **building the backend/platform that runs other people's agents,
  per-tenant, embedded in a Go service, at scale** (`README.md:76-79`). The buyer is a multi-tenant
  SaaS/platform builder. Their entry need is *isolation they can trust without auditing every query*.
- **Runtime cost to adopt:** **zero external services.** `go get` + `go run .`, keyless stub, in-memory
  tiers. No Docker, Rust, Python, Qdrant, or rqlite in the default boot (proposal §2; confirmed §1.1).
- **Verification status:** tested by default `go test ./...`; 24 of 27 `internal/` packages serve it;
  the reality ledger classes it REAL and "hardest to retrofit later"
  ([reality-ledger.md "What this ledger does NOT diminish"](reality-ledger.md)).

### Pillar 2 — key-brokered untrusted execution (RunTask)

- **The claim:** "run untrusted AI agents that never touch your API keys" — the model call runs
  host-side (the key is spent in *your* process) while the agent's *actions* run in a throwaway Docker
  sandbox that never receives `ANTHROPIC_API_KEY`. Proven against real Docker
  (`TestRunTask_BrokerInvariant_KeyNeverEntersContainer`, [`README.md:12-20`](../../README.md), ADR-0036).
- **Who it's for:** someone who specifically needs to **execute untrusted agentic coding tasks against
  a git repo** and cannot leak a provider credential into the workload. That is a real, sharp need —
  but it is a *narrower, more specific* intent than "I run a multi-tenant agent platform."
- **Runtime cost to adopt:** **requires Docker (or Podman) at runtime.** The differentiator is a
  container-isolation property; there is no Docker-free path to it by definition.
- **Verification status:** the broker invariant is proven, but only under a **Docker-gated** test
  (`RunTasks` fan-out is `partial — needs Docker` per proposal §1b).

### Same audience, or two?

**Overlapping, not identical.** The README's *prose* already gets the relationship right: it frames
isolation as the substrate and key-brokering as one application riding on it — "`app.Turn` for
in-process turns, `app.RunTask` for sandboxed key-brokered tasks, and **tenant isolation by
construction under all of it**" (`README.md:78-79`). That sentence says Pillar 1 is *underneath*
Pillar 2. A platform buyer needs isolation whether or not they ever run an untrusted task; a
key-brokering buyer needs isolation *plus* Docker *plus* the agentic-task loop. The set of
key-brokering buyers is close to a **subset** of the isolation-platform buyers, distinguished by one
extra, heavier requirement (untrusted execution + a container runtime).

**Where the two-headline framing breaks:** the README's *headline* (`README.md:1-20`) inverts the
prose. It leads with Pillar 2 ("never touch your API keys") and its Docker-bound demo, then a reader
scrolls to Quickstart (`README.md:84-138`) and meets Pillar 1 as if *it* were the differentiator, then
hits a **third** "the differentiator" heading for compression (`README.md:216`). Three sections, two
of them literally titled "The differentiator." A platform buyer arrives for isolation and is first
told the product is about Docker sandboxes; a key-brokering buyer is told the runnable Quickstart
needs no Docker — which is true of the *core* but not of *their* feature. **One headline confuses both
because the two needs do not enter at the same door.**

Crucially, the code agrees with Pillar 1 being the substrate: the moat (`internal/tenancy`) is the one
thing [no competitor builds](../../arenas/README.md) (proposal §5), it is Docker-free, and it is what
makes Pillar 2's *"untrusted"* claim meaningful in the first place — untrusted execution without
tenant isolation underneath is just a sandbox, not a multi-tenant guarantee.

---

## 3. The three options, with consequences

### (a) CO-EQUAL PILLAR — README leads with both; Docker in the main pitch

- **What reorders:** keep both headline sections; make the top-of-README pitch explicitly "isolation +
  key-brokering, co-equal," with Docker named as a first-class prerequisite alongside `go get`.
- **Pros:** honors the effort already invested (2,974 LOC, ADR-0036/0037/0038, a dedicated arena);
  key-brokering is the one capability the README can honestly call *unique-in-Go, proven vs real
  Docker* (proposal §5) — co-equal keeps that spotlight brightest.
- **Cons:** puts **Docker in the 5-minute onramp**, breaking the "no Docker/Rust/Python in default
  boot" property that is the sovereignty pitch *and* the fastest path to a green `go run .`. Preserves
  exactly the "two differentiators" confusion the consolidation audit named as the headline problem
  (§0, proposal §0). Forces every reader to reason about a container runtime before they see the moat.
- **Migration cost:** low (mostly README/docs re-leveling; no code moves).
- **Risk:** high on *positioning* — it keeps the fork the audit diagnosed; low on *engineering*.
- **5-minute onramp effect:** **degraded** — Docker install/pull enters the critical path for a
  meaningful fraction of readers who will never need it.

### (b) FLAGSHIP OPTIONAL MODULE — one headline (isolation+memory), key-brokering demoted to opt-in ★

- **What reorders:**
  - **README:** collapse to **one** "the differentiator" heading — isolation-by-construction + tiered
    memory — leading with the Docker-free `Turn` + two-tenant proof (proposal §4.1). Move the
    key-brokering section *below the fold* as "Flagship module: run untrusted agents that never touch
    your key (requires Docker)." Cut or demote the compression "differentiator" per proposal M2 so
    exactly one headline survives.
  - **Adoption surface / docs:** label key-brokering an **opt-in module** in
    [adoption-surface.md](adoption-surface.md) (it is currently barely mentioned there — `grep` finds
    only an unrelated docker-compose reference at line 108); document that it needs a container runtime
    and is *not* part of the default boot.
  - **Code (optional, low-cost):** fence the ~5 files behind a build tag (e.g. `//go:build sandbox`)
    so a consumer who never calls `RunTask` compiles a Docker-unaware binary. Purely additive; §1
    shows it touches no core file.
  - **arenas/key-brokering:** keep as the *flagship demo of the moat* ("untrusted agents, isolated"),
    reframed as evidence *for* isolation rather than a separate pillar.
- **Pros:** matches the code's actual shape (core has zero key-brokering deps; §1.1); restores the
  Docker-free 5-minute onramp; resolves the "two differentiators" confusion with the least surgery;
  keeps the unique-in-Go flagship **in the repo and in the story**, just one level down; aligns the
  headline with the README's own prose (§2) and with the reality ledger's "hardest to retrofit" core.
- **Cons:** key-brokering loses top billing — if untrusted-execution is genuinely the strongest sales
  hook, this under-sells it. Requires disciplined README surgery to avoid re-introducing a second
  "differentiator."
- **Migration cost:** low–moderate. Docs re-level (README + adoption-surface + getting-started); an
  optional build tag on 5 files. **No core edits, no HTTP-contract change, no test rewrites.**
- **Risk:** low. The capability, tests, ADRs, arena, and example all stay; only the *framing* and
  optional *packaging* move.
- **5-minute onramp effect:** **restored** — first runnable payoff is `go run .` with no container.

### (c) SPIN OUT — key-brokering as its own companion repo/module importing the façade

- **What reorders:** move `internal/taskagent` + `internal/sandbox` + the façade files into a
  companion module (e.g. `aisotropy-taskagent`) that imports the public `aisotropy` façade; README
  links to it as a sister project.
- **Pros:** hardest boundary; the core `go.mod` can (marginally) slim; the "single Go binary, no
  Docker" story becomes unambiguous because the Docker-touching code physically leaves the repo. The
  precedent works — `examples/task-agent` is already a separate module consuming the façade (§1.4).
- **Cons:** the flagship becomes **invisible from the main repo** — you lose the one capability the
  README can call unique-in-Go from the front-door narrative, which is the opposite of the "keep the
  differentiator visible" goal. `taskagent` depends on four core packages (`llm`, `memory`, `tools`,
  `agentloop`); the companion can only reach them through whatever the public façade re-exports, so a
  spin-out may force **promoting more internal surface** (e.g. session-store internals, agentloop
  types) into the public contract than the project wants — a real API-stability cost. Highest churn
  for the least positioning gain.
- **Migration cost:** high — a package move across module boundaries, façade re-exports to widen,
  arena + example re-pointing, CI/release plumbing for a second module.
- **Risk:** moderate–high (API-surface promotion is a one-way door; ADR-0031 kept these internal on
  purpose).
- **5-minute onramp effect:** restored for the core, but the flagship is now a second click away in a
  different repo — worse *discoverability* than (b) for no additional onramp benefit.

---

## 4. Recommendation

**Choose (b) — FLAGSHIP OPTIONAL MODULE.** Lead with one headline (isolation-by-construction + tiered
memory) and demote key-brokering to an opt-in flagship module that needs Docker.

**Rationale (evidence-based):**

1. **The code already is this.** §1 shows the core imports nothing from key-brokering, boots with no
   Docker reference, and treats `RunTask` as an additive method. Option (b) makes the packaging tell
   the truth the import graph already tells. Options (a) and (c) fight the code — (a) by promoting a
   leaf to co-core, (c) by amputating a cleanly-attached limb at high cost.
2. **It resolves the exact confusion the audit named** — "two things both called 'the differentiator'"
   (proposal §0/§4) — with the least surgery, while (a) preserves it.
3. **It protects the onramp and the sovereignty pitch.** The Docker-free `go run .` is both the
   fastest first payoff and the core's actual moat; (a) trades that away, (c) keeps it but hides the
   flagship.
4. **It keeps the flagship where it earns its keep.** Key-brokering stays in-repo, in the tests, in
   ADR-0036, and in the arena — reframed as *the best demonstration of* isolation (untrusted agents,
   provably isolated), which is a **stronger** argument for the moat than a competing headline. This is
   exactly the proposal's own §5 guidance: "even if it becomes a 'flagship module' rather than co-core,
   do not delete it."

**Confidence:** **High** on the coupling and audience *facts* (grounded in the import graph and the
README's own prose). **Medium-high** on the *positioning call* — this is a go-to-market judgment on
which the operator has data I do not.

**What would change my mind (the falsifier):** if the operator's demand signal shows that *"run
untrusted agents without leaking keys"* is the phrase buyers actually search for and convert on — i.e.
key-brokering is the **lead** purchase intent, not a downstream one — then **(a) co-equal** becomes
defensible, because you lead with what sells. Even then I would keep Docker out of the *first* runnable
snippet (open with the Docker-free isolation proof, then the key-brokering demo second) — so in
practice the operator's data would move me from (b) toward a "co-equal but isolation-first-in-the-boot"
variant of (a), not toward putting Docker at the very front door. I would **not** move to (c) without a
separate, stronger reason (repo weight, a real second consumer, or a licensing/packaging constraint) —
its cost/benefit is the worst of the three for this decision.

**Low-confidence flags:**
- The audience subset-relationship (§2) is a reasoned inference from the README's framing, not
  measured user data.
- Whether to actually add the `//go:build sandbox` tag (vs. a docs-only demotion) is a minor,
  reversible sub-choice; (b) works either way. I lean toward the build tag because it makes "the
  default binary is Docker-unaware" a *compiler-enforced* fact rather than a prose claim, but it is not
  load-bearing to the recommendation.
- I did **not** run `go test ./...`; tested/partial statuses are inherited from the consolidation
  proposal and reality ledger, cross-checked against the import graph I did measure.

---

## References

- [Consolidation proposal](consolidation-proposal.md) — §2 flags this decision; §4 the onramp; §5 "do
  not delete key-brokering."
- [Reality ledger](reality-ledger.md) — the REAL/DORMANT arbiter; "what this ledger does NOT diminish."
- [Adoption surface as built](adoption-surface.md) — where key-brokering would be labeled opt-in.
- [`README.md`](../../README.md) — the current entry path (headline `:1-20`; nesting prose `:76-79`;
  third differentiator `:216`).
- [`aisotropy/runtask.go`](../../aisotropy/runtask.go) / [`runtasks.go`](../../aisotropy/runtasks.go) /
  [`sandbox.go`](../../aisotropy/sandbox.go) — the façade over the engine.
- `internal/taskagent`, `internal/sandbox` — the separable engine + isolation backend (measured §1).
- [Arenas README](../../arenas/README.md) — the isolation moat evidence; `arenas/key-brokering/` the
  flagship demo.
