---
type: Section Index
title: Legal — licensing, contribution agreements, counsel review
description: The project's outbound legal surface, as distinct from the inbound dependency gate. Holds the two CLA drafts required before Aisotropy accepts external code contributions (ADR-0080), the counsel review packet that turns "legal review pending" into twelve answerable questions, and the contribution workflow that switches on once a lawyer signs off. Everything here is DRAFT and NOT IN FORCE - which costs nothing today, because external code contributions are closed until it is.
tags: [legal, licensing, cla, contributions, index]
timestamp: 2026-08-03T00:00:00Z
---

# Legal

Two different things get called "licensing" in this repo. They do not live together:

| Axis | What it governs | Where it lives |
|---|---|---|
| **Inbound** | which dependencies may enter | [`license-policy.md`](../../.claude/rules/cos/license-policy.md), [`scripts/license-audit.sh`](../../scripts/license-audit.sh), the [audit](../architecture/license-audit.md) |
| **Outbound** | what the project grants, and on what terms contributions arrive | **this directory**, plus [`LICENSE`](../../LICENSE) and [`NOTICE`](../../NOTICE) |

## What is here

- **[Individual CLA](cla-individual.md)** — for a person contributing on their own behalf.
- **[Corporate CLA](cla-corporate.md)** — for an organisation authorising its employees. Does not
  replace the individual agreement; both are needed.
- **[Counsel review packet](counsel-review-packet.md)** — what was decided, the evidence, and eleven
  numbered questions for a lawyer. Written so the review is one sitting, not a discovery exercise.

## Status: DRAFT, NOT IN FORCE

Neither CLA has been reviewed by counsel. **Nobody should sign either one yet.** This blocks nothing,
because [`CONTRIBUTING.md`](../../CONTRIBUTING.md) states that external **code** contributions are not
accepted until the review lands — issues, bug reports, benchmarks and documentation fixes are open and
welcome. A temporary closed door is strictly better than accepting code under terms that may turn out
to be the wrong ones.

## The workflow this switches on (once counsel signs off)

1. Contributor opens a PR.
2. A CLA check reports whether an agreement is on file for that GitHub user. The **mechanism** —
   click-through recorded by a bot versus a signed PDF — depends on
   [packet Q3](counsel-review-packet.md), so it is not built yet. Building it before that answer
   would mean building it twice.
3. No agreement on file → the PR is not merged, and the bot says how to sign.
4. Agreements are stored by the Company. Public name and GitHub user appear in project history;
   postal address and e-mail do not.

## Why a CLA and not a DCO

Recorded in [ADR-0080](../decisions/0080-project-license-apache-2.md) §"Sub-decision". Short version:
a DCO certifies the right to *submit*, and grants the project no right to offer the code on other
terms — so it is equivalent to bare Apache §5 on the one axis that matters. The decision rests on
reversibility, not on a forecast: **CLA → DCO can be dropped any day; DCO → CLA is impossible
retroactively**, because every contribution already merged under a DCO stays non-relicensable. With
zero external contributors, a CLA costs one signature and buys back the entire option.

Note the scope, which is narrower than people assume: the Company's **own** code — all of the
repository today — is relicensable at will regardless. A CLA only ever governs code the Company does
not own.
