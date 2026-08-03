---
type: Research Report
title: "The project's OWN license — MIT vs Apache-2.0 (and the LICENSE file bug found on the way)"
description: Exhaustive review of whether aisotropy should ship under Apache-2.0 instead of MIT. Verdict — YES, switch, and do it now while the repo is still private with a single copyright holder (verified, not assumed). Four things Apache-2.0 adds that this project specifically needs (express patent grant §3, trademark reservation §6, inbound-contribution terms §5, a standard NOTICE channel §4(d)) against one real loss (GPLv2-only combination, practically irrelevant here) and adopter friction the dependency tree already imposes. Measured ecosystem split — every direct Go competitor (adk-go, genkit, eino) plus spring-ai, letta, vercel/ai and mastra's core are Apache-2.0, while the Python/TS library cohort (langgraph, llama_index, pydantic-ai, crewai, agent-framework, openai-agents) is MIT; aisotropy sits in the first cohort. Also documents a defect that exists under EITHER license — the LICENSE file appends 506 bytes (32.3% of the file) of non-MIT text to the canonical MIT body, so license detectors will report "Other", not MIT. Honest limits stated - what Apache-2.0 does NOT solve (proprietary relicensing still needs a CLA; a patent grant is only worth the patents that exist) and no legal review was performed.
tags: [research, licensing, mit, apache-2.0, patents, trademark, open-core, contributions, governance, adoption]
timestamp: 2026-08-03T00:00:00Z
---

# The project's OWN license — MIT vs Apache-2.0

> **Outcome addendum (same day, 2026-08-03).** This report's recommendation was executed the day it
> was written: the project relicensed to Apache-2.0 as
> [**ADR-0080**](../decisions/0080-project-license-apache-2.md). `LICENSE` is now byte-identical to
> apache.org's canonical text (sha256 `cfc7749b…d30`), the §7 detection defect is fixed by moving the
> CC-BY-SA carve-out into the new `NOTICE`, and §8's change set landed in full. Of the two open items
> this report raised, one is now **decided**: §5's DCO-vs-CLA question resolved to **CLA**, on the
> asymmetry that CLA→DCO can be dropped any day while DCO→CLA is impossible retroactively (see the
> ADR's sub-decision). Its *text* — adopt Apache ICLA/CCLA rather than draft one — and **legal review**
> (§9) stay open, both before publication.
>
> **One thing this report did not anticipate:** the operator also decided to **expunge the MIT period
> from git history**. That was executed the same day and is documented in the ADR — including the two
> facts that matter for reading this report: the `git log` evidence in §1 no longer reproduces, and the
> §8 "cost of switching" table understated the collateral, because rewriting history invalidates every
> commit SHA cited anywhere in the docs (31 citations remapped, 33 found already broken from an earlier
> rewrite).
>
> **Holder settled the same day.** The review wrote "© 2026 Luum" throughout because that is what
> `LICENSE` then said. Counsel-packet Q1 asked which registered entity that named; the operator
> answered that Aisotropy is a **personal project** and the holder is the natural person
> **Matías Nahuel Amendola** — "Luum" was only ever the GitHub organisation. `NOTICE`, `README` and
> both CLAs now say so, the CLAs' defined term moved from "the Company" to "the Maintainer", and that
> **unblocks publication**: a natural person needs no registry lookup. It does not unblock the CLAs,
> which are still waiting on counsel.

Every license review in this repo so far has pointed **outward** — at dependencies
([license-audit](../architecture/license-audit.md), [rust-dependency-licenses](../architecture/rust-dependency-licenses.md)),
at images ([ADR-0067](../decisions/0067-grafana-agpl-exception-observability-stack.md)),
at audio backends ([ADR-0077](../decisions/0077-voicebox-gpl-exception-audio-backend.md)) and
telephony ([ADR-0079](../decisions/0079-telephony-cpaas-first-asterisk-gpl-exception-precleared.md)).
This one points **inward**: the license aisotropy itself grants.

**Verdict: switch to Apache-2.0, now.** The window is open and cheap — verified below, not assumed.

---

## 1. What the repo declared when this was written (evidence, not memory)

> All rows below were measured on 2026-08-03, **before** the change. The first two no longer
> reproduce: the MIT period was expunged from git history the same day
> ([ADR-0080](../decisions/0080-project-license-apache-2.md) §"Note on the repository history").
> They are kept verbatim because they are the evidence the decision rested on.

| Fact | Evidence |
|---|---|
| License was MIT, © 2026 Luum | `LICENSE` as it then stood — canonical MIT body plus an appended exception block (§7) |
| **No ADR ever decided it** | `git log --oneline --all -- LICENSE` → 2 commits, one of them literally `chore: add MIT license`, the other a pre-publication audit fix. Nothing in [`docs/decisions/index.md`](../decisions/index.md). The stack's *dependency* policy is constitutional; the project's *own* license was never argued. |
| Single copyright holder, zero external contributors | `git shortlog -sne --all` → 1 author, 541 commits |
| No CLA, no DCO | `grep -rniE "\b(CLA\|DCO\|Developer Certificate\|Contributor License Agreement\|sign-off)\b" CONTRIBUTING.md README.md .github/` → 0 hits. [`CONTRIBUTING.md:151`](../../CONTRIBUTING.md) carries a homemade one-liner instead. |
| **Repo is still PRIVATE** | `curl -s -o /dev/null -w "%{http_code}" https://api.github.com/repos/Luum-Home/aisotropy` → **404** unauthenticated, while `…/orgs/Luum-Home` → **200**. `git ls-remote origin` succeeds over SSH. Org public, repo not. (Note: [ADR-0072](../decisions/0072-runtime-config-refuses-secrets.md) reasons from "the repo is PUBLIC" — correct as *intent*, not yet as *state*.) |
| No third-party source is redistributed | `git ls-files external/` → **0** (both `external/gollem` and `external/voicebox` are gitignored); `git ls-files \| grep -c '^vendor/'` → **0**. Nothing in the tree carries someone else's copyleft or attribution obligation, so the license choice is unconstrained. |
| The repo already thinks in Apache terms | [`Dockerfile:38-41`](../../Dockerfile) cites **Apache-2.0 §4(d)** by name and ships [`THIRD-PARTY-NOTICES`](../../THIRD-PARTY-NOTICES) next to the binary. The NOTICE mechanism is already operational here — as a *consumer* of it. |

The last two rows are what make this a cheap decision rather than an expensive one.

---

## 2. The four things Apache-2.0 adds that THIS project needs

### 2.1 Express patent grant (§3) — the one that actually matters

MIT grants copyright permissions ("use, copy, modify, merge, publish, distribute, sublicense, sell").
It says nothing about patents; a patent license is at best implied, and the scope of that implication
is exactly the kind of question a corporate legal team refuses to answer cheaply. Apache-2.0 §3 grants
a **perpetual, worldwide, royalty-free patent license** from every contributor, and terminates it for
anyone who files a patent suit alleging the Work infringes.

Why it lands here and not on a generic library:

- **The target adopter is an enterprise embedding a runtime into a commercial multi-tenant SaaS** —
  the README's own pitch, and the whole point of [ADR-0031](../decisions/0031-embeddable-in-process-turn.md)'s
  `App.Turn` and [ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)'s façade.
  That reader has an OSPO, and the OSPO's checklist has a patent-grant line on it.
- **The differentiators are technique-shaped, not just code-shaped** — isolation-by-construction,
  bits-per-token routing ([bet 4](bet-04-compressibility-routing.md), [bet 6](bet-06-bits-per-token-metric.md)),
  sleep-cycle consolidation. Technique-shaped work is what patent claims are drafted around, by us or
  by someone else.
- **The grant flows from contributors, not just from the holder.** This is the part usually missed: today
  the holder owns no patents, so *our* grant is worth little. The value is prospective and defensive — the
  day a company contributes code covered by *their* patents, Apache §3 pulls the grant in
  automatically. Under MIT there is no such mechanism, and the risk lands on every downstream adopter.

**Honest limit:** a patent grant is only as valuable as the patents that exist. If nobody ever holds a
relevant patent, §3 costs nothing and buys nothing. It is insurance, priced at zero.

### 2.2 Trademark reservation (§6)

[ADR-0000](../decisions/0000-framework-name-aisotropy.md) picked the name and says explicitly that
trademark/branding is *"not recorded anywhere in this repo"*. Meanwhile the entire
[arena programme](../../arenas/README.md) is brand-building: a 43-row competitive matrix published
under a name. MIT's "deal in the Software without restriction" is broad enough that some readers treat
it as covering the name; Apache-2.0 §6 says plainly that the license grants **no** trademark rights.
Free protection for an asset the project is actively creating.

### 2.3 Inbound contribution terms (§5)

Today: `CONTRIBUTING.md:151` — *"By contributing, you agree that your contributions are licensed under
the project's MIT License."* A homemade clause in a file a drive-by contributor may never open.
Apache-2.0 §5 makes it a term of the license itself: any Contribution intentionally submitted is under
the License unless explicitly stated otherwise. With **zero external contributors today**, this is a
one-line fix now and a per-person consent problem later.

### 2.4 A standard home for the attribution carve-out (§4(d))

The repo has one real attribution obligation it cannot sublicense away: the XQuAD slice under
CC-BY-SA-4.0 ([`bench/rag-quality/LICENSE-DATA.md`](../../bench/rag-quality/LICENSE-DATA.md)). Today it
is bolted onto the bottom of the LICENSE file — which breaks license detection (§7 below). Apache-2.0
ships a canonical channel for exactly this: a `NOTICE` file that §4(d) requires derivative works to
propagate. The repo already generates the consumer-side version of that artifact
(`THIRD-PARTY-NOTICES`); this makes it symmetric.

Two secondary points, worth listing but not deciding anything: §4(b)'s "mark your modified files"
gives attribution that survives a fork-and-rebrand, and §§7–8's warranty/liability language is drafted
with more care than MIT's single ALL-CAPS paragraph — a marginal advantage in non-US jurisdictions.

---

## 3. What MIT keeps — the honest counter-case

1. **GPLv2-only compatibility.** This is the single real freedom lost. The FSF holds Apache-2.0
   **incompatible with GPLv2** (the patent-termination and indemnification terms count as added
   restrictions); MIT combines into GPLv2 code fine. Apache-2.0 → GPLv3/AGPLv3 is fine. Practical
   impact for a Go agent runtime aimed at commercial SaaS: close to zero. But it is a genuine loss, and
   it is irreversible for any adopter who needed it.
2. **Adopter friction.** Apache §4 puts obligations on *downstream*: keep LICENSE, propagate NOTICE,
   mark modified files. MIT is the lowest-friction license that exists — nothing to read. That cuts
   against the "`go get`, `go run .`, done" adoption story the README leads with. **Counter:** every
   adopter already links BadgerDB, wazero, the Qdrant client and OpenTelemetry — all Apache-2.0 — so
   their compliance process already handles exactly these obligations. Marginal friction ≈ 0.
3. **Perception.** In the Python/TS AI-framework world MIT is the default and Apache can read as
   "corporate". In the Go/infra world the opposite is true (§4).
4. **Length.** ~200 words vs ~3,000. Nobody has ever adopted or rejected a runtime for this.

---

## 4. Ecosystem norm — measured, not asserted

```bash
for r in google/adk-go firebase/genkit cloudwego/eino spring-projects/spring-ai letta-ai/letta \
         vercel/ai mastra-ai/mastra langchain-ai/langgraph langchain-ai/langchain run-llama/llama_index \
         pydantic/pydantic-ai crewaiinc/crewai microsoft/agent-framework openai/openai-agents-python; do
  printf "%-34s %s\n" "$r" "$(curl -s https://api.github.com/repos/$r/license | jq -r '.license.spdx_id // "N/A"')"
done
```

Run 2026-08-03:

| Cohort | Frameworks | License |
|---|---|---|
| **Go / infra / vendor-backed runtimes** | `google/adk-go`, `firebase/genkit`, `cloudwego/eino`, `spring-projects/spring-ai`, `letta-ai/letta`, `vercel/ai` | **Apache-2.0** |
| **Open-core, Apache core + paid tier** | `mastra-ai/mastra` | Apache-2.0 core, `ee/` dirs under a separate enterprise license |
| **Python/TS "library" frameworks** | `langgraph`, `langchain`, `llama_index`, `pydantic-ai`, `crewai`, `microsoft/agent-framework`, `openai-agents-python` | **MIT** |

Two API answers needed a raw fetch because GitHub's detector failed on them (`N/A` / `NOASSERTION`) —
`firebase/genkit` and `vercel/ai` are both plain Apache-2.0 (`curl -s
https://raw.githubusercontent.com/vercel/ai/main/LICENSE | head -5`), and mastra's `LICENSE.md` is
Apache-2.0 with a prepended enterprise carve-out. **That failure mode is itself the finding in §7.**

Aisotropy's own dependencies split across both cohorts: Huma, gollem and rqlite are MIT; BadgerDB,
Qdrant, wazero, OpenTelemetry and DataFusion are Apache-2.0. So the dependency tree does not decide it.
What decides it is **which cohort aisotropy is in**, and the repo answers that unambiguously: an LLM
*OS*, a sealed kernel, a multi-tenant substrate, `Dockerfile`-shipped, k8s-deployed, sold on isolation
guarantees to whoever runs the SaaS. That is the Apache cohort. It is also, precisely, the cohort of
[every direct Go competitor in the arena](competitive-landscape.md) — adk-go, genkit, eino — where
matching the license removes one difference a procurement reviewer would otherwise have to think about.

---

## 5. Open-core: does the license choice constrain it?

[ADR-0033](../decisions/0033-open-core-extension-seams.md) and
[ADR-0034](../decisions/0034-open-core-tier2-sealed-driver.md) declare an open-core direction, and
mastra is the live precedent for the exact shape (permissive core + enterprise directories).

- **Neither license blocks a paid tier.** Both are permissive; a proprietary component alongside, or on
  top of, either core is fine.
- **Neither license blocks relicensing *by the copyright holder*.** The holder owns 100% of the copyright
  today, so the holder can ship the same code under any terms they want, whenever they want.
- **But Apache §5 does NOT give the holder the right to relicense a *contributor's* code under proprietary
  terms.** §5 licenses contributions **under Apache-2.0**, full stop. If dual-licensing or a
  source-available tier that includes community contributions is genuinely on the roadmap, that
  requires a **CLA with a relicensing grant** — under MIT *and* under Apache-2.0 alike. This is the
  most commonly overstated argument in favour of Apache-2.0, and it is stated here as a limit, not a
  benefit.

**Recommendation:** decide the CLA question in the same pass as the license, not after the first
external PR. The cheap answer for a project at this stage is a DCO (`Signed-off-by`) plus Apache §5 if
no proprietary relicensing is planned; a CLA only if it is.

---

## 6. Option C — dual `MIT OR Apache-2.0` (the Rust convention)

Rust itself, and the four first-party crates' dependency tree (`serde`, `candle`, `ryu`), use the
`MIT OR Apache-2.0` dual grant: the adopter picks. It buys back GPLv2 compatibility (via the MIT arm)
while offering the patent grant (via the Apache arm).

**Not recommended here**, for three reasons: (a) the patent grant becomes *optional* — an adopter who
picks the MIT arm is not bound by §3, so the defensive-termination property largely evaporates, which
is the main thing being bought; (b) it is a Rust-ecosystem convention, essentially unknown in Go, where
it will read as indecision; (c) it doubles the compliance surface of every downstream SBOM for a
GPLv2-compatibility benefit this project has no identified consumer for. Pick one license.

---

## 7. A defect that exists under EITHER license — the LICENSE file breaks detection

[`LICENSE`](../../LICENSE) is the canonical MIT text **plus** a 506-byte appended `EXCEPTION` block for
the CC-BY-SA-4.0 XQuAD slice.

```bash
total=$(wc -c < LICENSE); mit=$(sed -n '1,21p' LICENSE | wc -c)
echo "total=$total mit-body=$mit appended=$((total-mit))"
# total=1567 mit-body=1061 appended=506  → 32.3% of the file is not MIT text
```

GitHub's detector (`licensee`) matches license files at a **98% similarity threshold**. A file that is
32.3% foreign text does not match, so the repo will display **"Other"**, and SBOM/SPDX tooling will
emit `NOASSERTION` instead of `MIT`. That is not a theory: §4 above hit exactly this failure twice via
the API — `mastra-ai/mastra` (prepended carve-out) and `vercel/ai` (prepended copyright line) both come
back `NOASSERTION` / `"Other"` from GitHub while being, on inspection, ordinary Apache-2.0.

For a project whose README sells a *"100% permissively-licensed stack"* in its second paragraph,
publishing with a license badge that reads "Other" is a self-inflicted wound — and the publication
audit that added this block was fixing a real gap, so the content is right; only its
location is wrong.

**Fix, under either license:** keep `LICENSE` byte-identical to the canonical text, and move the
carve-out to a file that is *meant* to carry it — `NOTICE` under Apache-2.0 (the §4(d) channel), or a
`LICENSE-EXCEPTIONS` / `THIRD-PARTY-DATA` file referenced from `README.md` and
`bench/rag-quality/LICENSE-DATA.md` under MIT. Apache-2.0 has a standard home for it; MIT does not.

**Verification** (not run here — `licensee` is not installed on this machine; the mastra/vercel API
results above are the empirical stand-in):

```bash
docker run --rm -v "$PWD":/repo licensee/licensee detect /repo   # expect: MIT / Apache-2.0, confidence 100%
```

---

## 8. Cost of switching — now vs. later

Now: the repo is private, one copyright holder, no external contributions, no CLA. Relicensing is an
edit. After publication and the first external PR it needs per-contributor consent, forever.

Exact first-party change set (`git grep -nIE '"license": *"MIT"|^license = "MIT"|MIT\]\(LICENSE\)|MIT License'`,
excluding dependency lockfiles):

| File | Change |
|---|---|
| [`LICENSE`](../../LICENSE) | Replace with the **verbatim, unmodified** Apache-2.0 text (Apache's own guidance: do not edit the license body) |
| `NOTICE` *(new)* | `Aisotropy — Copyright 2026 Matías Nahuel Amendola` + the XQuAD CC-BY-SA-4.0 carve-out, pointing at `bench/rag-quality/LICENSE-DATA.md` |
| [`README.md:306`](../../README.md) | `[MIT](LICENSE) © 2026 Luum.` → Apache-2.0 |
| [`CONTRIBUTING.md:152`](../../CONTRIBUTING.md) | Inbound clause → Apache-2.0 §5 (plus the DCO/CLA decision from §5 above) |
| `crates/{bitrate-scorer,embed,guardrail-model,sleep-cycle}/Cargo.toml:6` | `license = "MIT"` → `"Apache-2.0"` (4 files) |
| `arenas/live/{ai-sdk,mastra}/package.json` | `"license": "MIT"` → `"Apache-2.0"` (both `private: true`, cosmetic) |
| [`Dockerfile:38-41`](../../Dockerfile) | Also `COPY` the new `NOTICE` into `/licenses/` |
| [`docs/index.md`](../index.md), [`docs/vision/glossary.md`](../vision/glossary.md), [`README.md:9,202-204`](../../README.md) | These describe the **dependency stack** ("100% permissive: MIT/BSD/Apache-2.0") and stay **unchanged** — do not confuse them with the project's own license |
| [`.claude/rules/cos/license-policy.md`](../../.claude/rules/cos/license-policy.md), [`scripts/license-audit.sh`](../../scripts/license-audit.sh) | **Unchanged** — they gate *inbound* dependencies, a different axis |

Not required, worth considering separately: per-file `SPDX-License-Identifier: Apache-2.0` headers
(`git grep -rn "SPDX-License-Identifier" -- internal aisotropy cmd crates` → **0 today**). Apache
recommends them in its appendix; they are not a condition of the license.

**Acceptance criteria for the switch** (if executed):

```bash
git grep -nIE '"license": *"MIT"|^license = "MIT"|MIT\]\(LICENSE\)' -- . \
  ':!*package-lock.json' ':!arenas/eve' ':!bench' | wc -l    # expect 0
head -2 LICENSE | grep -q "Apache License"                    # expect exit 0
test -s NOTICE                                                # expect exit 0
scripts/license-audit.sh --root-only                          # expect exit 0 (unaffected — inbound gate)
go build ./... && go test ./...                               # expect exit 0 (no code touched)
```

---

## 9. What this does NOT solve (symmetric rigor)

- **It grants no patents the holder does not own.** §3's value to *us* is defensive and prospective.
- **It does not enable proprietary relicensing of contributed code** — that needs a CLA (§5 above).
- **It does not discharge the CC-BY-SA obligation** on the XQuAD slice; that travels with the data file
  regardless, and is the reason `NOTICE` must exist rather than the LICENSE being simply reverted.
- **No legal review was performed.** This is an engineering reading of two license texts, the FSF
  compatibility list, and the measured ecosystem. For a company-owned project heading to publication,
  one hour of counsel before the switch is proportionate.
- **The FSF GPLv2-incompatibility position is a legal opinion**, widely followed but never litigated to
  a holding. It is treated here as settled practice, which is how every distributor treats it.

---

## 10. Recommendation

**Switch to Apache-2.0, before publication**, ranked by why:

1. **§3 patent grant** — the target adopter is an enterprise SaaS builder, and the differentiators are
   technique-shaped. This is the reason; the rest are reinforcement.
2. **§6 trademark reservation** — the arena programme is building a brand the current license arguably
   gives away.
3. **Cohort match** — every direct Go competitor and every vendor-backed runtime in this space is
   Apache-2.0. Aisotropy is infrastructure, not a scripting library.
4. **§5 inbound terms** — replaces a homemade sentence in `CONTRIBUTING.md`, and the cost of fixing it
   is zero today and non-zero the day after the first external PR.
5. **§4(d) NOTICE** — a standard home for the carve-out that currently breaks license detection.

Against: one real loss (GPLv2-only combination) with no identified consumer, and adopter obligations
their dependency tree already imposes.

**Do it in one pass, together with:** the LICENSE-detection fix (§7 — mandatory under either outcome),
the DCO-vs-CLA decision (§5), and an ADR recording the choice — because the absence of one is why this
question was open at all. Next free ADR number is `0080` as of 2026-08-03; **re-enumerate at write
time** ([verify-before-execute](../../.claude/rules/verify-before-execute.md)).

**If the decision is to stay MIT**, §7 still has to be fixed, and the ADR should still be written — a
license the project can defend is worth more than the license itself.
