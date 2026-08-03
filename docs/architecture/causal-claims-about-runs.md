---
type: Concept
title: "Causal Claims About Runs — falsify before you assert (a standing PREMISE)"
description: >
  Before asserting a cause for what a run did, state the observation that would disprove it and check that first.
tags: [architecture, premise, causal-claims, evidence, debugging]
timestamp: 2026-08-03T00:00:00Z
---

# Causal Claims About Runs — falsify before you assert (a standing PREMISE)

> **Origin (2026-07-28, five in one session).** Every guard in this repo enforces
> evidence for claims about CODE: `prune-guard.sh` for "this is dead",
> `arena-citation-guard.py` for "this path exists", `absence-claim-guard.py` for
> "they lack X". None covers the claim an agent makes in conversation about **what
> just happened in a run** — and that is where five confident, false statements
> shipped in a single session:
>
> | asserted | disconfirming evidence ALREADY IN HAND |
> |---|---|
> | "Qdrant and rqlite are down" | the run's own error said `connection refused` on 6334 — a definitive refusal, which is *evidence the service answered the socket*, not a timeout |
> | "my earlier diagnosis was wrong, they were up" | the same message, re-read to mean its opposite |
> | "five adapters degrade silently" | the log's ordering: the outage window contained ONLY the erroring adapter; the other five ran earlier |
> | "8/8 — the plumbing is measurable" | an echo cannot compute `1200-315-208`; the number was structurally impossible |
> | "concurrent docker builds starved rqlite" | line numbers in the log already open: 503s at 67-89, first build at 823 |
>
> Not one was caused by missing data. Every one was a conclusion formed **before
> querying evidence already in hand**. And four of the five were the reading that
> made the story tidier — services-down explains a failure, silent degradation is
> a striking finding, 8/8 is success, docker-builds explains the 503. The fifth
> was the mirror bias: over-correcting to look rigorous, which produced a
> retraction of a claim that had been right.

## The rule

**Before asserting a CAUSE for something a run did, state the observation that
would DISPROVE it, and check that first.** The disconfirming check is nearly
always one command, and it is nearly always over evidence already collected.

This is `evidencia-ejecutable` applied to a surface it did not name. That norm
requires a reproducible command behind any claim with numbers. A causal claim
about a run is such a claim — "the store was down", "they degrade silently",
"the builds caused it" are all assertions of fact about a system — and it needs
the same treatment, plus one thing more: the check that could have said no.

## The four questions, in order

1. **What would make this false?** Name it before looking. If nothing could,
   the claim is not empirical and should not be stated as a finding.
2. **Do I already have that evidence?** Logs, error text, records, timings —
   usually yes. Read the ACTUAL bytes, not the summary you formed of them.
3. **Does the probe distinguish the alternatives?** A tool that collapses
   "refused", "timed out" and "unauthorized" into one boolean cannot support a
   claim that names one of them. `curl -o /dev/null -w '%{http_code}'` discards
   the body, which is where `Must provide an API key` lives.
4. **Is this the tidy answer?** If the claim closes the story neatly, that is
   when to spend the extra command, not when to skip it.

## Specific traps, each one paid for

- **Absence has many causes; presence has one.** Refused, timed out, wrong port,
  wrong credential and slow-under-load all look alike through a boolean probe.
  A conclusion of "not there" needs a probe that could have distinguished them.
- **Totals hide windows.** "Five adapters had no errors" says nothing until you
  check whether they RAN during the window that produced the errors.
- **A structurally impossible result is a bug report, not a success.** If the
  mechanism cannot produce the number, the number came from somewhere else.
  Chase it before celebrating it.
- **Correction has its own bias.** Withdrawing a correct claim to appear rigorous
  is as wrong as defending a false one, and it is harder to spot because it
  looks like humility.
- **Ad-hoc probes test a different configuration.** `run.sh` sources `.env` with
  `set -a` AFTER inheriting the environment, so `RQLITE_URL=... bash run.sh`
  silently uses `.env`'s value. Prefer the real harness; a hand probe that
  disagrees with it is suspect until proven otherwise.

## Why this is a rule and not a guard

It cannot be mechanised the way the others were. A script can check that a cited
path resolves; nothing can check that a sentence in a conversation was reasoned
from the log rather than toward a conclusion.

What CAN be mechanised is the residue: **when reporting a diagnosis, include the
command that produced it and the check that could have refuted it.** A diagnosis
without both is a hypothesis wearing a verdict's clothes, and should be labelled
as one — "I think X, disconfirming check pending" is an honest sentence and
costs nothing.

## Contextual trigger

Always active when explaining why a run failed, why a number changed, why a
service misbehaved, or what an error means — and most of all when the
explanation arrives quickly and fits well.
