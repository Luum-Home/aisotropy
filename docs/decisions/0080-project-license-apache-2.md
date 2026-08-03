---
type: ADR
title: "ADR-0080 — The project's OWN license: Apache-2.0, not MIT — decided before publication, while it is still free to decide"
description: Every license decision in this repo so far pointed OUTWARD (Grafana AGPL ADR-0067, voicebox GPL ADR-0077, Asterisk GPLv2 ADR-0079, the dependency gate in license-policy.md). The license aisotropy itself grants was never argued — as of 2026-08-03 `git log -- LICENSE` returned two commits, one of them `chore: add MIT license`, and no ADR. This closes that gap by settling the project on Apache-2.0. THE MIT PERIOD WAS THEN EXPUNGED FROM GIT HISTORY by operator decision the same day (git filter-repo over all 541 commits, signature-scoped so that every TRUTHFUL statement about a THIRD PARTY's MIT license survives untouched — a blanket sweep would have falsified other projects' licenses), which makes this ADR the only remaining evidence that the MIT period existed; the rewrite is therefore recorded here in full, with its backup bundle, its old and new tips, and its collateral. Four reasons, ranked. (1) §3 express patent grant — the target adopter is an enterprise embedding a multi-tenant runtime into commercial SaaS (ADR-0031's App.Turn, ADR-0013's façade), and the differentiators are technique-shaped (isolation-by-construction, bits-per-token routing, sleep-cycle consolidation); the load-bearing half is that the grant flows from FUTURE CONTRIBUTORS, a mechanism MIT has none of, so under MIT the patent risk lands on every downstream adopter. (2) §6 trademark reservation — ADR-0000 states branding/trademark is recorded nowhere while the entire arena programme builds a brand; MIT's "deal in the Software without restriction" is broad enough to be read as covering the name. (3) Cohort match, MEASURED not assumed via the GitHub license API on 2026-08-03: every direct Go competitor (google/adk-go, firebase/genkit, cloudwego/eino) plus spring-ai, letta and vercel/ai are Apache-2.0, mastra is Apache-2.0-core + `ee/` enterprise, while the Python/TS library cohort (langgraph, langchain, llama_index, pydantic-ai, crewai, agent-framework, openai-agents) is MIT — aisotropy is infrastructure, not a scripting library. (4) §5 inbound terms replace the homemade sentence at CONTRIBUTING.md:151. The ONE real loss is stated and accepted: Apache-2.0 is FSF-incompatible with GPLv2-only (MIT is not), with no identified consumer in a Go runtime aimed at commercial SaaS; the §4 adopter obligations are already imposed by BadgerDB/wazero/Qdrant/OTel. TIMING IS THE ARGUMENT — verified, not assumed: the repo is still PRIVATE (unauthenticated GET /repos/Luum-Home/aisotropy → 404 while /orgs/Luum-Home → 200), one copyright holder over 541 commits, zero external contributors, no CLA/DCO, and `git ls-files external/` → 0 so no third-party source is redistributed; relicensing today is an edit, after the first external PR it is per-person consent forever. ALSO FIXES A DEFECT THAT STOOD UNDER EITHER LICENSE — the old LICENSE appended 506 bytes (32.3% of the file) of CC-BY-SA carve-out to the canonical MIT body, and licensee matches at 98% similarity, so GitHub and SBOM tooling would have published the repo as "Other"/NOASSERTION rather than MIT (empirically confirmed: mastra-ai/mastra and vercel/ai fail exactly this way through the same API). LICENSE is now byte-identical to apache.org's canonical text (sha256 cfc7749b…d30, verifiable) and the carve-out moved to `NOTICE`, its §4(d) home. HONEST LIMITS — Apache §5 does NOT permit relicensing contributed code under proprietary terms, so the open-core tier of ADR-0033/0034 still needs a CLA if it is ever to include community contributions (true under MIT identically); the patent grant is worth only the patents that exist, so its value to us is defensive and prospective; the CC-BY-SA obligation on the XQuAD slice is undischarged by design and is exactly why NOTICE must exist; and no legal review was performed — which is now a NAMED ARTIFACT rather than an intention: the two CLAs exist as docs/legal/cla-individual.md + cla-corporate.md, ADAPTED from Apache ICLA v2.2 / CCLA r190612 with every deviation tabled (the substantive one: Apache's reciprocal promise rests on NONPROFIT STATUS Luum does not have, replaced by an Outbound Commitment that each accepted Contribution stays available under Apache-2.0 or another OSI license, while §2's `sublicense` is preserved — we may dual-license, we may never take it away), both carrying a DO-NOT-SIGN banner, and docs/legal/counsel-review-packet.md poses 11 numbered questions to counsel, led by Q1 the exact registered entity behind "Luum" (which blocks publication) and Q5 whether copyright in a single-author repository vests in the Company at all.
tags: [decisions, adr, licensing, apache-2.0, mit, patents, trademark, contributions, open-core, governance, publication]
timestamp: 2026-08-03T00:00:00Z
---

# ADR-0080 — The project's OWN license: Apache-2.0, not MIT

## Status

✅ **Accepted — 2026-08-03.** The project's license is the **Apache License 2.0**. `LICENSE` is the
verbatim canonical Apache-2.0 text, `NOTICE` is the §4(d) attribution channel, and the CC-BY-SA-4.0
carve-out for the XQuAD slice lives in `NOTICE` rather than deforming `LICENSE`. Full review, with the
counter-case and the measured ecosystem data:
[MIT vs Apache-2.0](../research/license-mit-vs-apache-2026-08-03.md).

> **Read §"Note on the repository history" before running `git log` to check any of this.** The repo
> briefly carried an **undocumented MIT license**, and on the same day this ADR was accepted the
> operator decided to **expunge it from git history**. The rewrite is recorded below, in full, because
> after it **this ADR is the only remaining evidence that the MIT period existed** — and a project that
> gates deletions on executable evidence does not get to make its own history unverifiable and stay
> silent about it.

**This does not touch the dependency gate.**
[`license-policy.md`](../../.claude/rules/cos/license-policy.md),
[`constitutional-gates.md`](../../.claude/rules/constitutional-gates.md) and
[`scripts/license-audit.sh`](../../scripts/license-audit.sh) govern what comes **in**; this ADR governs
what goes **out**. Different axis, unchanged.

## Context

This repository has argued licensing repeatedly and carefully — but always about somebody else's code:
[ADR-0067](0067-grafana-agpl-exception-observability-stack.md) (Grafana AGPL),
[ADR-0077](0077-voicebox-gpl-exception-audio-backend.md) (voicebox GPL-3.0),
[ADR-0079](0079-telephony-cpaas-first-asterisk-gpl-exception-precleared.md) (Asterisk GPLv2),
[ADR-0055](0055-redpanda-bsl-position.md) (Redpanda BSL), plus the reproducible
[dependency audit](../architecture/license-audit.md). The license aisotropy itself grants was never
argued at all. As of 2026-08-03, `git log --oneline --all -- LICENSE` returned **two commits** — one of
them literally `chore: add MIT license`, the other a pre-publication audit fix — and **no ADR, no
options considered**. For a repo whose constitution is "a claim without executable evidence is an
opinion", the outbound license was the one decision running on inertia.

(That `git log` no longer reproduces: see §"Note on the repository history". The quoted subjects are
recorded here precisely because the commits they name are gone.)

The question became live now, and not later, because of a fact worth verifying rather than assuming:

```bash
curl -s -o /dev/null -w "%{http_code}\n" https://api.github.com/repos/Luum-Home/aisotropy   # 404
curl -s -o /dev/null -w "%{http_code}\n" https://api.github.com/orgs/Luum-Home              # 200
git shortlog -sne --all | wc -l   # 1 author, 541 commits
git ls-files external/ | wc -l    # 0 — gollem and voicebox are gitignored, nothing is redistributed
```

The org is public; **the repo is not, yet**. One copyright holder, zero external contributors, no
CLA/DCO, and no third-party source in the tree. Relicensing today is an edit. After publication and
the first external PR it requires per-contributor consent, forever. The window is open and will not
reopen.

## Options considered

### Option A — stay MIT

**For:** the lowest-friction license that exists (nothing for an adopter to read), which matches the
README's "`go get`, `go run .`, done" pitch; GPLv2-compatible; the norm in the Python/TS AI-framework
cohort; already in place, zero work.

**Against:** silent on patents; silent on trademark while [ADR-0000](0000-framework-name-aisotropy.md)
records that branding is protected nowhere; leaves contribution terms to a homemade sentence in
`CONTRIBUTING.md`; and gives the CC-BY-SA carve-out no standard home, which is how it ended up
deforming the LICENSE file (§"The defect this also fixes").

### Option B — Apache-2.0 (**chosen**)

**For:** §3, §6, §5, §4(d) — argued in the Decision below.

**Against:** incompatible with GPLv2-only per the FSF; §4 imposes obligations on adopters (keep
LICENSE, propagate NOTICE, mark modified files); ~3,000 words instead of ~200.

### Option C — dual `MIT OR Apache-2.0` (the Rust convention)

**Rejected.** It buys back GPLv2 compatibility, but (a) it makes the patent grant **optional** — an
adopter electing the MIT arm is not bound by §3, which dissolves the defensive-termination property
that is the main thing being purchased; (b) it is a Rust-ecosystem idiom, essentially unknown in Go,
where it reads as indecision; (c) it doubles the compliance surface of every downstream SBOM to buy a
GPLv2 compatibility with no identified consumer. Pick one license.

## Decision

**Apache-2.0.** Ranked by what actually carries the decision:

### 1. §3 — express patent grant (the reason; the rest is reinforcement)

MIT grants copyright permissions and is silent on patents; any patent license is at best implied, and
the scope of that implication is exactly the question a corporate legal team will not answer cheaply.
Apache-2.0 §3 grants a perpetual, worldwide, royalty-free patent license from every contributor and
terminates it for anyone who sues alleging the Work infringes.

Two things make it land here rather than on a generic library:

- **The target adopter has an OSPO.** The pitch is a multi-tenant runtime *embedded in* commercial SaaS
  ([ADR-0031](0031-embeddable-in-process-turn.md)'s in-process `App.Turn`,
  [ADR-0013](0013-public-extension-surface-facade-vs-promote.md)'s façade). That reader's checklist has
  a patent-grant line on it.
- **The load-bearing half is prospective, not ours.** Luum holds no patents, so *our* §3 grant is worth
  little today. The value appears the day a company contributes code covered by *their* patents:
  Apache §3 pulls the grant in automatically. MIT has no such mechanism, so under MIT that risk lands
  on every downstream adopter, silently.

### 2. §6 — trademark reservation

[ADR-0000](0000-framework-name-aisotropy.md) chose the name and states plainly that trademark, branding
and logo are *"not recorded anywhere in this repo"*. Meanwhile the [arena programme](../../arenas/README.md)
is a sustained brand investment — a 43-row competitive matrix published under that name. MIT's "deal in
the Software without restriction" is broad enough that some readers treat it as covering the name;
Apache §6 says explicitly that no trademark rights are granted. Free protection for an asset the
project is actively creating.

### 3. Cohort — measured, not assumed

```bash
for r in google/adk-go firebase/genkit cloudwego/eino spring-projects/spring-ai letta-ai/letta \
         vercel/ai mastra-ai/mastra langchain-ai/langgraph langchain-ai/langchain run-llama/llama_index \
         pydantic/pydantic-ai crewaiinc/crewai microsoft/agent-framework openai/openai-agents-python; do
  printf "%-34s %s\n" "$r" "$(curl -s https://api.github.com/repos/$r/license | jq -r '.license.spdx_id // "N/A"')"
done
```

Run 2026-08-03: the **Go / infra / vendor-backed runtime** cohort (adk-go, genkit, eino, spring-ai,
letta, vercel/ai) is **Apache-2.0**; mastra is Apache-2.0 core plus `ee/` enterprise directories — the
exact open-core shape [ADR-0033](0033-open-core-extension-seams.md)/[ADR-0034](0034-open-core-tier2-sealed-driver.md)
point at. The **Python/TS library** cohort (langgraph, langchain, llama_index, pydantic-ai, crewai,
agent-framework, openai-agents) is **MIT**.

Aisotropy's own dependencies straddle both (Huma/gollem/rqlite MIT; BadgerDB/Qdrant/wazero/OTel/DataFusion
Apache-2.0), so the dependency tree does not decide it. What decides it is which cohort aisotropy is
in, and the repo answers unambiguously: an LLM *OS*, a sealed kernel, a multi-tenant substrate,
Dockerfile-shipped and k8s-deployed, sold on isolation guarantees. That is the Apache cohort — and it
is precisely the cohort of the three Go competitors the arena benchmarks against, so matching removes a
difference a procurement reviewer would otherwise have to reason about.

### 4. §5 — inbound contribution terms

Replaces `CONTRIBUTING.md:151`'s homemade *"By contributing, you agree…"* with a term of the license
itself. With zero external contributors today this costs one paragraph; after the first external PR it
costs consent-gathering.

### 5. §4(d) — a standard home for the carve-out

`NOTICE` is the canonical channel for attribution that must survive into derivative works. The repo
already operates the consumer-side twin ([`THIRD-PARTY-NOTICES`](../../THIRD-PARTY-NOTICES), which the
[`Dockerfile`](../../Dockerfile) ships next to the binary and whose comment already cites §4(d) by
name). This makes it symmetric.

## The one real loss, accepted

**Apache-2.0 is incompatible with GPLv2-only** (FSF position: the patent-termination and indemnification
terms are additional restrictions). MIT is not. Apache-2.0 → GPLv3/AGPLv3 remains fine. For a Go agent
runtime aimed at commercial multi-tenant SaaS there is no identified consumer of GPLv2-only
combination — but the loss is genuine, irreversible for anyone who needed it, and is recorded here
rather than argued away. The FSF position is a widely-followed legal opinion, never litigated to a
holding; it is treated as settled practice, which is how every distributor treats it.

**Adopter friction (§4)** is the second cost, and it is close to nil in practice: every adopter already
links BadgerDB, wazero, the Qdrant client and OpenTelemetry — all Apache-2.0 — so the exact obligations
being added are ones their compliance process already discharges.

## The defect this also fixes (it stood under either license)

The pre-ADR `LICENSE` was the canonical MIT text **plus** a 506-byte appended `EXCEPTION` block for the
CC-BY-SA-4.0 XQuAD slice — measured before the change, on the file as it then stood:

```
wc -c LICENSE                     1567 bytes total
canonical MIT body (lines 1-21)   1061 bytes
appended EXCEPTION block           506 bytes   → 32.3% foreign text
```

`licensee` (GitHub's detector) matches at a **98% similarity threshold**, so the repo would have
published with its license shown as **"Other"** and its SBOMs emitting `NOASSERTION`. Not a theory —
the same failure is observable through the API on `mastra-ai/mastra` and `vercel/ai`, both of which are
ordinary permissive licenses with prepended text and both of which come back `NOASSERTION`. For a
README that sells a *"100% permissively-licensed stack"* in its second paragraph, shipping with a
license badge reading "Other" is a self-inflicted wound. The block's *content* was correct — it was
added by the pre-publication audit commit that closed a real gap — only its location was wrong.

Fixed structurally: `LICENSE` is now byte-identical to apache.org's canonical text, and the carve-out
lives in `NOTICE`.

```bash
shasum -a256 LICENSE
# cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30
curl -s https://www.apache.org/licenses/LICENSE-2.0.txt | shasum -a256   # same digest
```

## What this does NOT do (symmetric rigor)

- **It grants no patents Luum does not hold.** §3's value to us is defensive and prospective.
- **It does not enable proprietary relicensing of contributed code.** §5 licenses contributions *under
  Apache-2.0*, full stop. If the open-core tier of ADR-0033/0034 is ever to include community
  contributions under different terms, that needs a **CLA with a relicensing grant** — equally true
  under MIT. This is the most commonly overstated argument for Apache-2.0 and it is recorded here as a
  limit, not a benefit. **Decided in the same pass (see below): CLA, not DCO.**

### Sub-decision — CLA, not DCO (2026-08-03)

A DCO (`Signed-off-by`) certifies only that the contributor had the right to submit the work under the
project's license. It grants the project **no** right to offer that code under any other terms. So DCO
and bare Apache §5 are equivalent on the one axis that matters here, and both foreclose a future
commercial tier that includes community code.

The decision rests on an **asymmetry of reversibility**, not on a forecast:

- **CLA → DCO is free**, any day, by dropping the requirement.
- **DCO → CLA is impossible retroactively.** Every contribution already merged under DCO stays
  non-relicensable; the only remedies are per-contributor consent or excising their code.

With **zero external contributors today**, a CLA costs one signature per future contributor and buys
back the entire option. Being wrong the other way costs archaeology. Note the scope: **Luum's own code —
100% of the repo today — is relicensable at will regardless**, because Luum owns it; the CLA only
governs code the project does not own.

**The CLA text now exists** (2026-08-03, same day): [`docs/legal/cla-individual.md`](../legal/cla-individual.md)
and [`docs/legal/cla-corporate.md`](../legal/cla-corporate.md), **adapted** from the Apache ICLA v2.2
and CCLA r190612 rather than drafted — as this ADR required. Each ends with a table of **every**
deviation from its source. Three matter:

1. **Recipient** is Luum, not a foundation.
2. **The substituted consideration.** The Apache ICLA's reciprocal promise is anchored in ASF's
   *nonprofit status* — a clause Luum cannot truthfully make. Copying it would be misleading; deleting
   it outright would leave the contributor with a bare grant and nothing in return, which is precisely
   what makes company CLAs unpopular and fairly so. It is replaced by an **Outbound Commitment**
   (ICLA §9): every accepted Contribution stays available under Apache-2.0, a later version, or another
   OSI-approved license, surviving change of control — while §2's `sublicense` right is expressly
   preserved so a commercial tier may *also* include it. The bargain is "we may dual-license, we may
   never take it away". **This is a choice, not a derivation**, and the alternative (drop §9: more
   permissive for the Company, materially less attractive to contributors) is recorded in the draft so
   it can be taken instead.
3. **`sublicense` is untouched** in §2 — flagged explicitly in both drafts, because it is the operative
   word for this whole sub-decision and nobody should later assume it was softened.

**Still open, and now a single named artifact rather than an intention:** counsel review. The
[review packet](../legal/counsel-review-packet.md) states what was decided with its evidence, then asks
**eleven numbered questions**. The load-bearing ones: **Q1** the exact registered entity behind "Luum"
(today `LICENSE`, `NOTICE` and both CLAs say only "Luum" — nothing was invented, and this blocks
publication), **Q2** whether §9 is sound consideration and whether §2 actually delivers the commercial
option this sub-decision was made for, **Q3** governing law plus whether a click-through CLA binds
(which decides whether the contribution workflow is a bot or a signed PDF — hence it is not built yet),
and **Q5** whether copyright in a repository authored entirely by one person in fact vests in the Company, which no CLA
addresses.

Until that review lands, both drafts carry a **DO NOT SIGN** banner and `CONTRIBUTING.md` keeps
**external code contributions closed** (issues, reports, benchmarks and docs are open). A deliberate,
temporary closed door — strictly better than accepting code under terms that later turn out to be the
wrong ones, and it costs nothing while the contributor count is zero.
- **It does not discharge the CC-BY-SA obligation** on the XQuAD slice — that travels with the data file
  regardless, which is exactly why `NOTICE` must exist rather than `LICENSE` simply being reverted.
- **No legal review was performed.** This is an engineering reading of two license texts, the FSF
  compatibility list and the measured ecosystem. One hour of counsel before publication is
  proportionate for a company-owned project.
- **Per-file `SPDX-License-Identifier` headers are still absent** (`git grep -n "SPDX-License-Identifier"
  -- internal aisotropy cmd crates` → 0). Apache recommends them in its appendix; they are not a
  condition of the license. Deliberately out of scope here — a mechanical sweep over ~500 files would
  bury the decision it is attached to.

## Note on the repository history (operator decision, 2026-08-03)

Together with this ADR, the operator decided to **expunge the MIT period from git history** rather than
leave it as a normal superseded state. It was carried out and is recorded here in full.

**What was done.** `git filter-repo` over all **541 commits**, replacing — at blob level, by content
signature — only the project's **own** license declarations:

| Signature rewritten | Where it lived |
|---|---|
| the MIT `LICENSE` body containing `Copyright (c) 2026 Luum` | `LICENSE` (both historical variants) → verbatim Apache-2.0 |
| `[MIT](LICENSE) © 2026 Luum.` | `README.md` |
| `project's [MIT License](LICENSE).` | `CONTRIBUTING.md` |
| `the project's own LICENSE (MIT).` | `THIRD-PARTY-NOTICES` |
| `license = "MIT"` | the 4 `crates/*/Cargo.toml` (verified: no third-party `Cargo.toml` was ever tracked) |
| `"license": "MIT"` **only in files without `"lockfileVersion"`** | the 3 first-party `package.json` |

Plus one commit-message replacement: `chore: add MIT license` → `chore: add Apache-2.0 license`.
Old tip `3ec36fb…` → new tip `9e71927…`. No commit was dropped (541 in, 541 out; zero entries map to
the null SHA).

**What was deliberately NOT touched, and why it matters more than what was.** Every *truthful*
statement about somebody else's MIT license survives untouched: the 30 `"license": "MIT"` entries in
`examples/clients/typescript/package-lock.json`, the dependency scans under `arenas/*/scripts/license_scan.txt`,
and `README.md`'s stack line (`Huma (Go/MIT)`, `100% permissive: MIT/BSD/Apache-2.0`). A blanket
`MIT → Apache-2.0` sweep would have been one command and would have **falsified other projects'
licenses** — turning a cosmetic cleanup into a documented lie about third parties. That is the reason
the rewrite is signature-scoped rather than global.

**Backup.** A full `--all` bundle of the pre-rewrite history was taken and `git bundle verify`-ed
before the rewrite ran (`aisotropy-pre-apache-rewrite-2026-08-03.bundle`, kept **outside** the repo).
Restoring it reproduces `3ec36fb…` exactly.

**Collateral, stated rather than discovered later.** Every commit SHA changed, so every citation of one
in the docs broke. **31 citations across 15 files** were remapped by chaining the commit-maps of this
rewrite and of an **earlier one (2026-07-27)** that had already run on this repo. **33 hex citations
remain unresolvable** — verified against the backup bundle to have been **already broken before this
pass**, i.e. pre-existing rot, not collateral of this change.

That verification corrected a number worth keeping: measured in the working repo, 20 of 51 citations
appeared valid before the rewrite; measured against a **clean clone of the backup bundle**, only **3**
actually were. The other 17 resolved only because unreachable objects from the July rewrite had not
been garbage-collected locally — they were already dead for anyone who cloned. After this pass, **18**
resolve. Rewriting history did not cause the docs' SHA rot; it exposed it, and repaired most of it.

**What the rewrite does NOT achieve, plainly.** It grants nobody anything and revokes nothing. The
repository was never public (§Context), so MIT was never granted to any third party, and erasing the
record would not have revoked such a grant if one had existed. This is repository hygiene, not a legal
act — and it is the reason the *decision* had to stay written down even though the *commits* did not.

## Consequences

**As shipped:**

| File | Change |
|---|---|
| [`LICENSE`](../../LICENSE) | MIT + appended exception → **verbatim** Apache-2.0 (sha256 matches apache.org) |
| [`NOTICE`](../../NOTICE) | **New** — copyright, the CC-BY-SA-4.0 carve-out, pointer to `THIRD-PARTY-NOTICES` |
| [`README.md`](../../README.md) | License section → Apache-2.0, links `NOTICE` and this ADR |
| [`CONTRIBUTING.md`](../../CONTRIBUTING.md) | Homemade inbound clause → Apache §5, plus the NOTICE/CC-BY-SA obligations a contributor must respect |
| `crates/{bitrate-scorer,embed,guardrail-model,sleep-cycle}/Cargo.toml` | `license = "MIT"` → `"Apache-2.0"` (4 files) |
| `arenas/live/{ai-sdk,mastra}/package.json` | `"license": "MIT"` → `"Apache-2.0"` (both `private: true`) |
| [`Dockerfile`](../../Dockerfile) | `COPY` now includes `NOTICE`; comment updated |
| [`docs/research/license-mit-vs-apache-2026-08-03.md`](../research/license-mit-vs-apache-2026-08-03.md) | The full review this ADR decides |
| [`docs/legal/cla-individual.md`](../legal/cla-individual.md), [`cla-corporate.md`](../legal/cla-corporate.md) | The two CLA drafts, Apache-adapted, every deviation tabled — **DRAFT, do not sign** |
| [`docs/legal/counsel-review-packet.md`](../legal/counsel-review-packet.md) | What was decided + 11 questions for counsel, so the review is one sitting |
| [`docs/legal/README.md`](../legal/README.md), [`docs/index.md`](../index.md) | Outbound-vs-inbound licensing split, and the contribution workflow that switches on after review |

**Deliberately unchanged:** [`README.md`](../../README.md) §"The stack",
[`docs/index.md`](../index.md), [`docs/vision/glossary.md`](../vision/glossary.md) — those describe the
**dependency** posture ("100% permissive: MIT/BSD/Apache-2.0"), which is still exactly true and is a
different axis from the project's own grant. Likewise
[`license-policy.md`](../../.claude/rules/cos/license-policy.md) and
[`scripts/license-audit.sh`](../../scripts/license-audit.sh): inbound gate, untouched.

**Verification** (all run on the change):

```bash
git grep -nIE '"license": *"MIT"|^license = "MIT"|MIT\]\(LICENSE\)' -- . \
  ':!*package-lock.json' ':!arenas/eve' ':!bench' | wc -l   # 0
head -2 LICENSE | grep -q "Apache License" && test -s NOTICE  # exit 0
scripts/license-audit.sh --root-only                          # exit 0 — inbound gate unaffected
go build ./... && go vet ./...                                # exit 0 — no code touched
```

## Revisit trigger

- **A GPLv2-only consumer appears** and the combination genuinely matters → re-open Option C, knowing
  what it costs the patent grant.
- **A dual-license / source-available enterprise tier becomes real** → the CLA question in
  §"What this does NOT do" stops being a follow-up and becomes a blocker, *before* the first external
  contribution lands.
- **The repo goes public** → the window described in §Context closes; any further license change becomes
  a consent-gathering exercise rather than an edit.

## Sources

- Apache License 2.0, canonical text — https://www.apache.org/licenses/LICENSE-2.0.txt
- FSF license list, Apache-2.0 / GPLv2 incompatibility — https://www.gnu.org/licenses/license-list.html#apache2
- GitHub license detection (`licensee`), 98% content-match threshold — https://github.com/licensee/licensee
- Competitor licenses, GitHub license API, fetched 2026-08-03 (command inline in §3 above)
- [MIT vs Apache-2.0 — the project's OWN license](../research/license-mit-vs-apache-2026-08-03.md) (the full review)
