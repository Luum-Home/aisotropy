---
type: Legal
title: "Counsel review packet — licensing, contributions, and publication readiness"
description: Everything a lawyer needs to review Aisotropy's licensing position in one sitting, and nothing they should have to re-derive. Part 1 states what was decided and shipped by the engineering side (Apache-2.0 per ADR-0080, NOTICE, CLA drafts, the history rewrite) with the evidence for each. Part 2 is twelve numbered questions in priority order, each with the context, the position taken, and what turns on the answer - the load-bearing ones being Q1 the exact legal entity behind "Luum", Q2 whether replacing the Apache ICLA's nonprofit reciprocal promise with an outbound open-source commitment is sound, Q3 governing law and whether a click-through CLA binds in the relevant jurisdiction, and Q5 whether copyright in a repository authored entirely by one person actually vests in the company. Part 3 lists what engineering verified itself, with the commands, so counsel can spot-check rather than repeat. NOT legal advice - written by the engineering side to be corrected.
tags: [legal, review, counsel, licensing, cla, publication, checklist]
timestamp: 2026-08-03T00:00:00Z
---

# Counsel review packet — licensing, contributions, publication readiness

**Prepared by:** engineering, 2026-08-03. **Status:** awaiting review.
**Not legal advice.** Every position below is an engineering reading, written down so it can be
corrected rather than inherited. Where we guessed, it says so.

**The ask, in one line:** the repository is private today and intended to be published. Before it is,
confirm or correct the licensing position, and tell us what must change.

---

## Part 1 — What was decided and shipped

| # | Decision | Artifact | Evidence |
|---|---|---|---|
| 1 | Project licensed under **Apache License 2.0**, replacing an earlier, undocumented MIT | [`LICENSE`](../../LICENSE) — verbatim canonical text | `shasum -a256 LICENSE` = `cfc7749b…d30`, identical to `https://www.apache.org/licenses/LICENSE-2.0.txt` |
| 2 | Reasoning recorded | [ADR-0080](../decisions/0080-project-license-apache-2.md), [full review](../research/license-mit-vs-apache-2026-08-03.md) | Patent grant §3, trademark §6, inbound terms §5, ecosystem cohort |
| 3 | **`NOTICE`** added as the §4(d) channel, carrying the one attribution obligation the project cannot sublicense | [`NOTICE`](../../NOTICE) | `bench/rag-quality/testdata/xquad_slice.json` is CC-BY-SA-4.0 |
| 4 | Contributions to require a **CLA**, not a DCO | [ICLA draft](cla-individual.md), [CCLA draft](cla-corporate.md) | Adapted from Apache ICLA v2.2 / CCLA r190612; deviations listed in each |
| 5 | Until the CLA is in force, **external code contributions are not accepted** | [`CONTRIBUTING.md`](../../CONTRIBUTING.md) | Deliberate temporary closure |
| 6 | The MIT period was **expunged from git history** | ADR-0080 §"Note on the repository history" | 541 commits rewritten; backup bundle retained outside the repo |

**Position taken on #6, which we would like confirmed or corrected (Q9):** the rewrite is repository
hygiene with no legal effect. The repository has never been public, so MIT was never granted to any
third party; erasing the record neither revokes a grant nor creates one.

---

## Part 2 — Questions, in priority order

### Q1 — What is the legal entity, exactly? *(blocks publication)*

`LICENSE`, `NOTICE` and both CLA drafts name the copyright holder as **"Luum"**. We do not know
whether that is the registered legal name, and we did not invent one. The GitHub organisation is
`Luum-Home`.

**Needed:** the exact registered name and jurisdiction to appear in `NOTICE` ("Copyright 2026
\_\_\_\_"), in both CLAs as "the Company", and in any future trademark filing. **Nothing else in this
packet can be finalised before this.**

### Q2 — Is the CLA's substituted consideration sound? *(the drafting question)*

The Apache ICLA gives the contributor a reciprocal promise anchored in ASF's nonprofit status
(*"…shall not use Your Contributions in a way that is contrary to the public benefit or inconsistent
with its nonprofit status and bylaws…"*). Luum is a company; the clause is inapplicable.

**What we did:** replaced it with an **Outbound Commitment** (ICLA §9) — the Company undertakes that
every accepted Contribution will remain available under Apache-2.0, a later version, or another
OSI-approved license, surviving change of control, while §2's `sublicense` right is expressly
preserved so a commercial tier may also include the Contribution.

**Please confirm or correct:**
1. Is §9 enforceable as drafted, and is it adequate consideration?
2. Does §2's `sublicense` in fact permit offering a contributor's code under proprietary terms — the
   entire reason a CLA was chosen over a DCO? If not, the choice is wrong and we need to know now,
   while there are zero external contributions.
3. Does §9 create any obligation that would obstruct a future acquisition or a source-available tier?
4. Alternative on the table if §9 is a problem: delete it. More permissive for the Company,
   materially less attractive to contributors. **Operator's call, informed by yours.**

### Q2b — One deviation was found late, and is flagged rather than buried

The ICLA's deviation table claims no deviation is silent. An independent re-read against the source
PDF (2026-08-03, after the table was written) found one that was: the preamble extends the ASF's *"It
does not change your rights to use your own Contributions for any other purpose"* with a plain-language
gloss (*"you keep your copyright, and you may license the same work to anyone else…"*). It is now
listed as deviation 6b.

**Please confirm** the gloss accurately states what §2 leaves the contributor — or tell us to strike
it, since the ASF sentence alone is sufficient. Raised separately because it sits in the **operative
text**, not in a marked non-binding note.

### Q3 — Governing law, venue, and how a CLA gets signed

Neither Apache CLA carries a governing-law clause, and we did not add one.

- Which law and venue should govern? (Company's jurisdiction? A neutral one?)
- Is a **click-through / bot-recorded** acceptance (e.g. CLA-assistant recording a GitHub comment)
  binding in that jurisdiction, or is a signed PDF required? This determines the whole contribution
  workflow, so we would rather build it once.
- Is a wet or electronic signature required for the corporate CCLA?

### Q4 — Is anything else needed to make the Apache-2.0 switch effective?

The repository was never published under MIT. Our position: no formal act is required, the license is
whatever `LICENSE` says at publication. Confirm — and say whether the internal record (ADR-0080) is
sufficient evidence of the decision, or whether a board/officer resolution is wanted given the entity
is a company.

### Q5 — Does copyright in the existing code actually vest in the Company?

`git shortlog -sne --all` reports **a single author across every commit in the repository** (544 as of 2026-08-03). That author is the operator. The
CLA governs *external* contributions and does nothing for internal ones.

**Needed:** confirm that employment or contractor agreements vest copyright in the Company for all
work to date. If any part was written before an agreement was in place, by a contractor without an
assignment clause, or outside working time under a jurisdiction that matters, we would like to know
before publication rather than before a financing due-diligence.

### Q6 — Personal data in signed CLAs

Both drafts collect full name, postal address, e-mail and GitHub user, and ICLA §10 states retention
("as long as the Project distributes the Contribution") and non-publication of address and e-mail.
Confirm this satisfies applicable data-protection law (Argentina; GDPR if EU contributors sign), and
tell us whether a privacy notice or a lawful-basis statement must accompany the form.

### Q7 — The CC-BY-SA-4.0 dataset in the repository

`bench/rag-quality/testdata/xquad_slice.json` is a derived slice of XQuAD, redistributed under
CC-BY-SA-4.0 and carved out of the Apache-2.0 grant in `NOTICE` and
[`bench/rag-quality/LICENSE-DATA.md`](../../bench/rag-quality/LICENSE-DATA.md).

- Is the carve-out drafted adequately, and is `NOTICE` the right place for it?
- **ShareAlike scope:** does the ShareAlike obligation reach only that file and adaptations of it, as
  we assume, or could it reach benchmark outputs derived from it? Our position: outputs are not
  adaptations. If that is wrong, the file should be removed and re-derived from a permissive corpus —
  cheap to do now, expensive later.

### Q8 — Trademark

ADR-0000 records that "Aisotropy" has **no** trademark, branding or logo protection. Apache-2.0 §6
reserves trademark rights but creates none. Given the name is the vehicle for the public comparative
programme, advise on whether to file, in which classes and jurisdictions, and whether publication
before filing prejudices anything.

### Q9 — The history rewrite

Confirm or correct the position stated in Part 1. Also: the pre-rewrite history is preserved in a
backup bundle held outside the repository. Is there any reason to **retain** it (evidentiary), or any
reason to **destroy** it, once you have reviewed?

### Q10 — Cryptography and export

The runtime implements JWT verification (HS256/RS256), at-rest encryption for two memory tiers
([ADR-0044](../decisions/0044-working-memory-at-rest-encryption.md),
[ADR-0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md)) and a WASM sandbox. All use
standard library and permissive third-party primitives; no cryptography is implemented from scratch.
Flag anything that would trigger export-control or notification obligations on publication.

### Q11 — Anything in the outbound posture that you would change

Open question, deliberately. If the intended commercial shape (permissive core, paid tier, possible
source-available components) is better served by a different structure than "Apache-2.0 + CLA", we
would rather hear it now than after publication. Context: [ADR-0033](../decisions/0033-open-core-extension-seams.md),
[ADR-0034](../decisions/0034-open-core-tier2-sealed-driver.md).

---

## Part 3 — What engineering already verified (so it need not be repeated)

Each of these is reproducible; spot-check rather than redo.

| Claim | How to check |
|---|---|
| `LICENSE` is the unmodified canonical Apache-2.0 | `shasum -a256 LICENSE` vs `curl -s https://www.apache.org/licenses/LICENSE-2.0.txt \| shasum -a256` |
| No first-party file still declares MIT | `git grep -nIE '"license": *"MIT"\|^license = "MIT"' -- '*package.json' '*Cargo.toml' ':!*package-lock.json'` → 0, and the same for `MIT](LICENSE)` over `README.md CONTRIBUTING.md` → 0. Scoped to files that *declare* a license: a repo-wide grep also matches ADR-0080 quoting the strings it replaced. |
| No third-party source is redistributed in the tree | `git ls-files external/` → 0; `git ls-files \| grep -c '^vendor/'` → 0 |
| Every dependency is permissively licensed | `scripts/license-audit.sh` → exit 0; [audit](../architecture/license-audit.md), [rust](../architecture/rust-dependency-licenses.md) |
| Copyleft is present only by explicit, documented exception | [ADR-0067](../decisions/0067-grafana-agpl-exception-observability-stack.md) Grafana AGPL (image only), [ADR-0077](../decisions/0077-voicebox-gpl-exception-audio-backend.md) voicebox GPL-3.0 (separate service), [ADR-0079](../decisions/0079-telephony-cpaas-first-asterisk-gpl-exception-precleared.md) Asterisk GPLv2 (pre-cleared, not granted) |
| The repository is not public today | `curl -s -o /dev/null -w "%{http_code}" https://api.github.com/repos/Luum-Home/aisotropy` → 404 (unauthenticated); the org → 200 |
| One author, no external contributions | `git shortlog -sne --all` |

## What we are NOT asking

Not asking for a review of code quality, security posture, or the arena's competitive claims. The
comparative statements about other frameworks are governed by an internal evidence rule
([capability-absence-claims](../../.claude/rules/capability-absence-claims.md)) — if you believe any
public comparative claim carries legal risk of its own, say so under Q11, but auditing them is not
part of this ask.
