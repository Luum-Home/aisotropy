---
type: ADR
title: "ADR-0079 — Telephony for `examples/voice-pipecat`: CPaaS-first, with the Asterisk GPLv2 exception analysed and pre-cleared but not granted"
description: >
  Closes the "telephony deferred" item from the voice-pipeline landscape. Asterisk's licence was
  verified at the source and the landscape row is CORRECTED: Asterisk is GPLv2, but its own LICENSE
  carries an explicit AMI/AGI/ARI protocol grant from Sangoma US Inc. (the exclusive licensor)
  stating that applications controlling Asterisk over those protocols need not be GPL — which makes
  this the cleanest of the three GPL cases this repo has faced, cleaner than ADR-0067 (Grafana) and
  ADR-0077 (voicebox), both of which rested on our own reading of the process boundary rather than
  on a written grant. Every license-policy §Exceptions condition is answered individually and all
  five are satisfiable, so the exception is PRE-CLEARED. It is nevertheless NOT granted today,
  because §Exceptions requires approval before an integration and there is no integration: the
  design concludes the licence was never the cost. Telephony goes CPaaS-first on code-truth grounds
  — pipecat 1.6.0 ships six CPaaS serializers and zero Asterisk support, ARI carries no media, and
  Asterisk does not remove the carrier. No telephony leg is built in this change. Scope is
  examples/ only; the core still has zero audio code.
tags: [decisions, adr, license-policy, gpl, asterisk, telephony, cpaas, twilio, sip, ari, voice, pipecat, constitutional-gates, examples, sovereignty]
timestamp: 2026-08-03T00:00:00Z
supersedes: []
revisits: [0066, 0069, 0077, 0078]
---

# ADR-0079 — Telephony for `examples/voice-pipecat`: CPaaS-first, with the Asterisk GPLv2 exception analysed and pre-cleared but not granted

## Status

**✅ Accepted (2026-08-03)** — as a **decision-first** ADR with three parts, of which the third is a
deliberate non-build:

1. **When a telephony leg is built for `examples/voice-pipecat`, it is CPaaS-first** (Twilio or
   Telnyx, through pipecat's shipped serializers over its shipped FastAPI WebSocket transport).
2. **The Asterisk GPLv2 exception is PRE-CLEARED, not granted.** All five
   `.claude/rules/cos/license-policy.md` §Exceptions conditions are answered individually below and
   all five are satisfiable. The exception is nevertheless withheld because §Exceptions requires
   operator approval *"before integration"* and there is **no integration to approve**. It converts
   to a granted exception the moment a concrete Asterisk integration is proposed, on the conditions
   enumerated here and nothing wider.
3. **No telephony leg is built in this change** — no Asterisk container, no compose overlay, no ARI
   smoke check, and no CPaaS leg either. The reasoning is in *Decision 3*.

The operator's approval of 2026-07-23 (Engram #31266) authorised the exception **if the design
concluded Asterisk**. The design concludes otherwise. That approval is therefore **banked, not
spent** — it is recorded here so that a future Asterisk integration inherits it rather than
re-litigating it.

## Context

### What was deferred, and by whom

`docs/research/voice-pipeline-landscape-2026-07-23.md` ranked the telephony leg **#4** in its
adoption path (*"Telephony leg: Asterisk (ARI) or a CPaaS in front of #2 … M, needs ADR (GPLv2
exception)"*) and closed with *"**Defer telephony** … when it lands, Asterisk needs the
license-exception ADR, or a CPaaS sidesteps GPL entirely."* Moves #1 and #2 have since landed
(ADR-0066 the shim, ADR-0069 the WER lane, ADR-0077 the audio backend, ADR-0078 the pipecat 1.x
migration). This ADR closes #4 as a decision.

### The gate this ADR satisfies

`.claude/rules/constitutional-gates.md` → **License policy (repo-wide, hard gate)**:

> Do not introduce a new dependency with a copyleft (GPL/AGPL) or source-available/BUSL license
> **without an explicit ADR**.

GPLv2 is copyleft and is **not** on `license-policy.md`'s BLOCKER table (AGPL/SSPL/BSL/ELv2/Commons
Clause/FSL). So Asterisk is **ADR-gated, not blocked** — the same posture ADR-0067 and ADR-0077
occupied. This is that ADR.

### Why the licence was re-verified rather than inherited

The dispatch that produced this ADR carried an explicit instruction not to take the landscape doc's
word for Asterisk's licence, on the grounds that the same document's licence rows had collapsed on
inspection three separate times (its own §*"The recurring trap"* table records Kokoro-via-`kokoro-onnx`,
voicebox's "purely permissive image" recipe, and Inflect-Micro-v2 — each advertised permissive, each
carrying a GPL grapheme-to-phoneme dependency in the inference path). That instruction was correct
and it paid: the row **was** wrong, though in the *opposite* direction from every prior case.

## The licence, verified at the source (and the landscape row corrected)

| Question | Answer | Evidence |
|---|---|---|
| Licence | **GPL version 2** | `LICENSE` @ `github.com/asterisk/asterisk`: *"Asterisk is distributed under the GNU General Public License version 2"* |
| Copyright holder / licensor | **Sangoma US Inc.**, holding *"copyright and/or sufficient licenses to all components of the Asterisk package"* | same file |
| Dual / commercial option | **Yes.** Sangoma is the worldwide exclusive licensor; a commercial licence grants the right *"both to modify Asterisk and to incorporate it into a product, without the obligation of providing the resulting code under the GPLv2"* | `asterisk.org/products/software/licensing/` |
| Linking exceptions | **Yes** — *"Specific permission is also granted to link Asterisk with OpenSSL, OpenH323 UniMRCP, and/or the UW IMAP Toolkit and distribute the resulting binary files."* | `LICENSE` |
| **AMI / AGI / ARI protocol grant** | **Yes — and this is the finding that changes the analysis.** | `LICENSE`, `docs.asterisk.org/About-the-Project/License-Information/` |

The protocol grant, quoted because it carries the whole decision:

> "it is our belief that applications using these protocols to manage or control an Asterisk instance
> **do not have to be licensed under the GPL or a compatible license**, as we believe these protocols
> do not create a 'derivative work' as referred to in the GPL. However, should any court or other
> judiciary body find that these protocols do fall under the terms of the GPL, then **we hereby grant
> you a license to use these protocols in combination with Asterisk in external applications licensed
> under any license you wish**."

That is a belt-and-suspenders grant: an assertion of non-derivation **plus** a fallback licence grant
that operates precisely in the case where the assertion fails. It comes from the party with standing
to give it (the exclusive licensor), it is published in the canonical `LICENSE` file rather than in
marketing copy, and it names ARI — the exact integration shape a voice bot would use.

### How this compares to the two GPL exceptions this repo has already accepted

This is the point worth recording, because it inverts the intuition that Asterisk is the *hard* case:

| | ADR-0067 Grafana (AGPL-3.0) | ADR-0077 voicebox (GPL-3.0 transitively) | **Asterisk (GPLv2)** |
|---|---|---|---|
| What keeps the copyleft off us | Our reading of the process/network boundary | Our reading of the process/network boundary | **The licensor's written grant, plus the boundary** |
| Does upstream address our integration shape? | No | No | **Yes, by name (AMI/AGI/ARI)** |
| Fallback if a court disagrees | None — we would be arguing the boundary | None — we would be arguing the boundary | **An explicit licence grant that activates on that finding** |

**Correction to `docs/research/voice-pipeline-landscape-2026-07-23.md`.** Its Asterisk row
(*"⚠️ GPLv2 … Allowed only under the license-policy exception"*) is true but materially incomplete:
it omits the ARI grant, and therefore reads as though Asterisk were the riskiest GPL item in the
voice stack when it is the least risky. The row should be read alongside this ADR. The landscape
doc's closing framing — *"a CPaaS sidesteps GPL entirely"* — is accurate but is **no longer a reason
to prefer a CPaaS**, because the GPL question does not survive contact with the licence file.

### chan_pjsip / pjproject — a second licence, and where it does and does not matter

Modern Asterisk's default SIP channel driver is `chan_pjsip`, backed by **pjproject (PJSIP)**, which
is separately dual-licensed: **GPLv2-or-later, or a proprietary licence arranged with Teluu**
(`pjsip.org/licensing.htm`, `docs.pjsip.org/en/latest/overview/license.html`).

This changes nothing for the integration shape decided here and it is worth being precise about why:
pjproject is linked **into the Asterisk process**, not into ours. An external ARI client never links
it, never ships it, and never distributes it. It becomes our problem only on **absorb** — vendoring,
embedding, or redistributing — which is already a tripwire below. Recorded so that a future reader
does not discover a second licence and assume it was missed.

## Decision 1 — the §Exceptions conditions, answered individually (the pre-clearance)

Answered in the form ADR-0077 established, for the integration shape that would be proposed: a
**separate, unmodified Asterisk container, controlled exclusively over ARI**.

**1. "It runs as a completely separate service, such as its own container or externally hosted
endpoint." — ✅ SATISFIABLE.** Asterisk is a standalone daemon; it is packaged as an official
container image and would run as its own compose project with loopback-only publication, exactly as
`docker-compose.voicebox.yml` does for the audio backend. No Asterisk code enters the Go module, the
example's virtualenv, or any aisotropy artifact. `aisotropyd` would have no awareness Asterisk
exists — the voice gateway remains an ordinary SSE client of
`/v1/tenant/{tenant_id}/user/{user_id}/chat/stream`.

**2. "Its source code is not modified." — ✅ SATISFIABLE, with one named trap.** Asterisk is
configured through text files (`ari.conf`, `pjsip.conf`, `extensions.conf`) and controlled at
runtime over ARI. Neither is modification. **The trap is that Asterisk's extensibility model invites
modification in a way voicebox's did not**: the natural way to extend a PBX is to write a loadable
module (`res_*`, `chan_*`, `app_*`), and such a module **is linked into the Asterisk process** — it
is outside the ARI grant, it is a derivative work under any reading, and it would void this
condition. Any Asterisk integration must therefore be constrained to *configuration + ARI*, never a
module. This is listed as a tripwire below rather than as advice.

**3. "The project communicates with it only through a documented public API." — ✅ SATISFIABLE, and
this is the condition Asterisk satisfies better than any prior case.** ARI is a documented, versioned
public REST + WebSocket API (`docs.asterisk.org/.../Asterisk_REST_Interface/`), and — uniquely among
the three GPL cases — it is an API whose licensor has **explicitly pre-cleared external, non-GPL
consumers**. Where ADR-0077 had to argue that four undocumented-by-contract HTTP routes constituted a
public API, here the API *is* the licence grant's subject.

**4. "The justification, residual risk, and operational boundary are documented." — ✅ SATISFIED by
this document.** Justification: this section and *Decision 2*. Residual risk and boundary: *Residual
risk* and *What VOIDS the exception*, below, stated without softening.

**5. "A human operator explicitly approves the exception before integration." — ⏸️ BANKED, NOT
SPENT.** The operator approved the exception on **2026-07-23** (Engram #31266) *conditionally* — the
ask was to produce the exception ADR **"vs CPaaS alternative analysis"** and to build the Asterisk
leg **"if the exception is approved in design"**. The design does not approve it, because there is
nothing to integrate. Granting an exception with no subject would violate the narrowness this repo's
own precedent insists on (ADR-0077: *"The exception covers voicebox only … it does not extend to a
different integration shape"*). The approval is recorded here so a future Asterisk integration
inherits conditions 1–4 pre-answered and needs only to confirm that its shape matches.

`license-policy.md` adds that *"even when these conditions are met, the preferred decision is to
choose a permissively licensed alternative."* Here that preference and the engineering answer point
the same way for once — see *Decision 2*.

## Decision 2 — CPaaS goes first anyway, because the licence was never the cost

With the GPL objection dissolved, the choice is decided on engineering grounds. Four findings, three
of them code-truth from this repo's own installed dependency.

### (a) pipecat 1.6.0 ships six CPaaS serializers and **zero** Asterisk support

Verified against the example's own virtualenv, not against documentation:

```bash
cd examples/voice-pipecat
V=.venv/lib/python3.11/site-packages/pipecat
ls "$V"/serializers/*.py | grep -vE "__init__|base_serializer|protobuf" | wc -l   # -> 6
grep -rin -E "asterisk|audiosocket|freeswitch" "$V" \
  | grep -vi "markdown_text_filter|dtmf/types" | wc -l                            # -> 0
```

The six are `twilio.py`, `telnyx.py`, `plivo.py`, `exotel.py`, `vonage.py`, `genesys.py`; the
transport they pair with (`pipecat/transports/websocket/fastapi.py`) is also already installed. The
two greps that matched Asterisk-adjacent strings are false positives — a Markdown `*` filter and a
DTMF type module — and are excluded above.

**So the CPaaS leg is configuration of code we have already installed and already licence-cleared
(BSD-2-Clause, ADR-0078). The Asterisk leg is net-new code we would write and own.**

### (b) ARI carries control, not media — the audio bridge is ours to build

ARI is signalling: REST for commands, a WebSocket for Stasis events. Getting *audio* out of Asterisk
requires `POST /channels/externalMedia`, which offers three channel drivers, each with a cost we
would absorb:

| Driver | What we would write |
|---|---|
| `UnicastRTP` (`chan_rtp`) | Raw RTP: binary packet parsing/serialisation **and** our own send-pacing |
| `AudioSocket` | A TCP framing protocol client, still with send-timing on us |
| `chan_websocket` | Newer and the least painful — Asterisk added it, in its own documentation's words, *"to ease the burden on ARI application developers with getting media in and out of Asterisk"* because the other two *"require binary packet manipulation (RTP especially)"* and *"require that the app developer handle the timing of sending packets to asterisk"* |

Whichever driver, the result is a media bridge **plus** a pipecat serializer for it — both net-new,
both maintained by us, both living in `examples/`. That is precisely the shape ADR-0077 spent 40
lines of explicit HTTP to *avoid* taking on, and it is an order of magnitude larger.

### (c) Asterisk does not remove the vendor — it removes a different vendor

This is the honest correction to the "self-hosted telephony" framing. Asterisk is a PBX, not a
carrier. A self-hosted deployment still needs, from a third party: a **SIP trunk** (origination and
termination), **DIDs**, **E911/emergency-services registration**, and **STIR/SHAKEN attestation** for
outbound. What Asterisk changes is *which* vendor and *what layer* — the carrier becomes a bit-pipe
instead of an application platform. That is a real difference (see *The sovereignty tension*), but it
is not the difference between "a vendor" and "no vendor", and the ADR should not pretend otherwise.

### (d) The two legs share a substrate, so CPaaS-first is not a detour

Both paths terminate in the same place: a FastAPI WebSocket endpoint handing PCM frames to the
pipecat pipeline that already exists in `bot.py`. Building the CPaaS leg first builds the shared
substrate — the WebSocket app, the serializer seam, session/call lifecycle, per-call tenant
identity, barge-in over a network transport. A later Asterisk leg swaps the serializer and adds the
media bridge; it does not restart. **CPaaS-first is therefore the cheapest path to the Asterisk leg
as well as the cheapest path to a working phone call.**

## The CPaaS alternative, analysed

Prices are **Twilio US list, fetched from `twilio.com/en-us/voice/pricing/us` on 2026-08-03**. They
are list prices, US-only, and volume/negotiation-dependent; Telnyx and Vonage are comparable in shape
and were not price-verified.

| Dimension | CPaaS (Twilio / Telnyx) | Self-hosted Asterisk |
|---|---|---|
| **Licence question** | None (commercial SaaS ToS) | GPLv2 — **resolved**: ARI grant + §Exceptions, pre-cleared here |
| **Per-minute, inbound** | $0.0085/min + **$0.0044/min Media Streams** = **$0.0129/min** (~$0.77/hr of call). Media Streams is not optional for an AI voice agent — it is how audio reaches our pipeline | Wholesale SIP origination, **estimated** ~$0.003–0.007/min (**not first-party verified**) |
| **Per-minute, outbound US** | $0.0140/min + Media Streams | Termination at wholesale rates |
| **Number rental** | $1.15/mo per US local number | DID from the ITSP, typically under $1/mo |
| **Fixed cost** | $0 | A host, plus SIP-facing hardening, plus on-call |
| **What it removes** | The GPL question; carrier relationships; DID provisioning; E911/regulatory; STIR/SHAKEN; SIP-scanning and toll-fraud exposure | Nothing on this list — all of it stays, minus the GPL question |
| **Code we write** | ~0 net-new in this repo: pipecat's `TwilioFrameSerializer` + `FastAPIWebsocketTransport`, both installed | A media bridge (RTP / AudioSocket / `chan_websocket`) **and** a pipecat serializer for it |
| **What it adds** | Audio terminates in the vendor's application layer; vendor account and jurisdiction; per-minute opex scales linearly | Operational surface: a public SIP endpoint is a permanent security target; codec/timing/jitter become ours |
| **Lock-in** | Real but shallow — pipecat ships six serializers, so switching CPaaS is a config change, not a rewrite | None at the app layer; full ownership at the ops layer |
| **Locally testable without a carrier?** | **No** — needs an account and a public URL | **No** — a local container proves ARI answers, not that a call works |

**Where the crossover actually is.** The per-minute delta is roughly **$0.0129 − ~$0.005 ≈
$0.008/min**. At that delta, **100,000 call-minutes (~1,670 hours of conversation) saves about
$800** — less than a week of engineering, before any on-call, security hardening, or carrier
contracting. The "CPaaS gets expensive at scale" argument is real but it lives at a volume this PoC
is nowhere near, and the fixed engineering cost dominates by orders of magnitude until then. This is
arithmetic on a list price and an estimated trunk rate, not a quote; it is offered to show the
*shape* of the crossover, and the shape is not close.

## The sovereignty tension, stated honestly

This is the argument most likely to be overstated in this repo's favour, so it is stated with its
limits attached.

**The overstatement to avoid: "a CPaaS breaks our sovereignty story."** PSTN audio is **never**
sovereign. It crosses a carrier by definition, in both architectures. There is no configuration of
Asterisk that makes a phone call private from the phone network.

**The real difference, which is narrower and still matters:** *where the audio terminates in
application space.*

- **Asterisk + SIP trunk** — the carrier is a bit-pipe; media terminates on infrastructure we
  control; recording, retention and transcription are our policy, under our jurisdiction.
- **CPaaS** — media terminates in the vendor's application layer. Twilio Media Streams does forward
  PCM to our WebSocket, so we do get the audio; but Twilio is *in* the media path and holds call
  metadata, and its retention, jurisdiction and feature surface are its own.

**Where it does and does not bite for aisotropy specifically.** The isolation thesis
(`.claude/rules/constitutional-gates.md` → multi-tenant data isolation) is about tenant data in the
memory tiers, and it is **unaffected either way**: the voice gateway is another SSE client, identity
still arrives as a path param and travels by `context.Context`, and neither Asterisk nor Twilio
touches BadgerDB, rqlite or Qdrant. The sovereignty difference bites at exactly one point —
**deploying for a tenant with data-residency or BAA obligations that a CPaaS cannot meet.** That
point is not `examples/`, and it is registered below as the first revisit trigger.

Recording this asymmetry is the symmetric-rigor obligation from
`.claude/rules/capability-absence-claims.md`: the honest claim is *"self-hosting moves the
termination point and the retention policy"*, not *"self-hosting makes the call private."*

## Decision 3 — why no leg was built, including the Asterisk leg the dispatch offered

The dispatch authorised a minimal local-testable Asterisk leg (compose overlay + gated ARI smoke)
**conditional on the design concluding Asterisk**. It does not, so the leg is not built. Three
independent reasons, any one sufficient:

1. **It would test the plane that carries no risk.** The proposed smoke check — does the ARI endpoint
   answer, can a Stasis app register — exercises the *control* plane. Every hard part of the Asterisk
   path (media bridging, codec negotiation, RTP send-pacing, barge-in over a network transport,
   toll-fraud exposure) is in the *media* plane and in the carrier leg, both explicitly out of scope.
   The leg's evidence-to-cost ratio is poor, and shipping it would create the appearance of a
   validated telephony path where none exists — the failure class
   `.claude/rules/arena-full-matrix.md` calls a silently-missing cell.
2. **It is infrastructure for the path not taken.** Building the Asterisk container before the shared
   WebSocket substrate exists inverts the build order established in *Decision 2 (d)*.
3. **The machine could not honour it.** `scripts/resource-guard.sh` at the time of this work:
   **DEGRADED (tight) — pressure L2, sys-free 41%, swap 96%, free RAM 845 MB, load/core 1.48**, exit
   **1**, with concurrent agent sessions active. Per ADR-0071's "capacity is not permission" and
   `.claude/rules/verify-before-execute.md`, a heavy lane is refused before it is attempted. **No
   Asterisk image was pulled and no container was started; this is reported as not-run with the
   reason, never as a pass.**

**The CPaaS leg is also not built here**, for the reason that makes it honest: it cannot be tested
locally either. A Twilio leg needs an account, a purchased DID, and a publicly reachable URL — none
of which exist, and none of which this ADR should pretend to have exercised. Recommending it and
building it are separate changes, and a licence decision does not ride in the same commit as a
carrier integration.

## Residual risk (stated plainly)

- **The pre-clearance is a licence analysis, not a legal opinion.** It reads the licensor's own
  written grant and applies this repo's policy to it. It is not advice, and a commercial deployment
  at scale should have counsel confirm it.
- **The ARI grant covers the protocol, not the binary.** It says our external application need not be
  GPL. It says nothing about *redistributing Asterisk*. Shipping an Asterisk image or binary in an
  aisotropy artifact is plain GPLv2 distribution with full source-offer obligations, and the grant
  does not soften it.
- **Asterisk's extensibility model points at the tripwire.** Unlike voicebox, the idiomatic way to
  extend Asterisk (a loadable module) is exactly the thing that would void condition 2 and fall
  outside the ARI grant. The constraint "configuration + ARI, never a module" has to be actively
  held, not assumed.
- **A CPaaS-first decision is a decision to accept a vendor in the media path.** Named here rather
  than buried: for the PoC this is the right trade; for a residency-bound tenant it is not, and the
  revisit trigger exists because of it.
- **`chan_pjsip`'s pjproject carries its own GPLv2-or-later/commercial dual licence.** Irrelevant
  while Asterisk runs it in its own process; a live concern the moment anything is absorbed.
- **This ADR clears Asterisk only.** It pre-clears no other telephony stack (FreeSWITCH is
  MPL-1.1-ish and would need its own analysis; Kamailio is GPLv2 with its own posture) and no other
  integration shape of Asterisk itself.

### What VOIDS the exception, if and when it is granted

Any one of these ends it; the exception does not stretch:

1. **Vendoring** Asterisk source, configuration-as-code that embeds its source, or any module of it
   into the repo.
2. **Shipping its image or binary** as part of an aisotropy artifact, release, Helm chart, or product
   deployment. (This is redistribution of GPLv2 software; the ARI grant does not reach it.)
3. **Writing an Asterisk module** — any `res_*`, `chan_*`, or `app_*` loaded into the Asterisk
   process. That is linking, it is outside the AMI/AGI/ARI grant, and it requires either a GPL
   release of the module or a Sangoma commercial licence.
4. **Patching Asterisk in place** — editing its source or building from a modified tree.
5. **Absorbing pjproject** — linking, vendoring, or embedding the PJSIP stack into our own code,
   which moves its GPLv2-or-later terms out of the Asterisk process and into our dependency graph.
6. **Communicating by anything other than the documented public APIs** — reaching into Asterisk's
   internals, its database, or its sockets outside ARI/AMI/AGI.

## Revisit trigger

Re-open this decision — and convert the pre-clearance into a granted exception — when **any** of
these becomes true:

1. **A tenant with data-residency, sovereignty, or BAA obligations a CPaaS cannot meet.** This is the
   trigger the sovereignty analysis exists for, and it is the strongest one.
2. **Pipecat (or a maintained third party) ships an Asterisk / AudioSocket / `chan_websocket`
   transport or serializer.** That deletes finding (a) and most of finding (b) at once — the
   "net-new code we own" argument is the load-bearing one, and it evaporates the day someone else
   maintains it. Re-run the two greps in *Decision 2 (a)*; they are the trigger's test.
3. **Sustained call volume where the per-minute delta genuinely dominates.** The arithmetic above puts
   that far past PoC scale; revisit when the measured monthly minutes make it arguable, not before.
4. **A requirement only a self-hosted PBX satisfies** — in-house queues, conferencing, or call
   recording under our own retention policy.
5. **Twilio's Media Streams pricing or availability changes materially**, or the chosen CPaaS
   introduces terms incompatible with the tenant-isolation posture.

## What was NOT verified (stated rather than implied)

- **No Asterisk was run.** No image pulled, no container started, no ARI endpoint contacted, no
  Stasis app registered. The resource guard was DEGRADED (exit 1) and the leg was out of scope once
  the design concluded CPaaS. Every Asterisk claim here is from its published licence and
  documentation, not from execution.
- **No CPaaS was exercised.** No Twilio account, no DID, no Media Streams session. The pipecat
  serializer claim **is** executed (the two greps above, against this repo's own venv); the claim that
  those serializers *work end-to-end against Twilio* is upstream's, not ours.
- **`asterisk.org/asterisk-licensing/` returned HTTP 403** to automated fetch. The licence facts here
  come from the canonical `LICENSE` file in `github.com/asterisk/asterisk`, from
  `docs.asterisk.org/About-the-Project/License-Information/`, and from
  `asterisk.org/products/software/licensing/`.
- **Whether Asterisk's GPLv2 is "version 2 only" or "version 2 or later" was not exhaustively
  confirmed** — the licence text read names "version 2" without an "or later" clause, but the full
  file was read via summarisation, not line by line. It does not affect this decision, which links to
  nothing.
- **The wholesale SIP trunk rate (~$0.003–0.007/min) is an estimate, not a quote**, and is the one
  number in the cost table without a first-party source. The Twilio figures are first-party list
  prices for the US only.
- **`docs.asterisk.org` did not address pjproject licensing**; the PJSIP terms come from
  `pjsip.org/licensing.htm` and `docs.pjsip.org`.
- **No legal review.** This is a policy application, not counsel.

## Consequences

- **The "telephony deferred" item is closed as a decision**, not as a deferral. The landscape doc's
  move #4 now has an answer and a build order.
- **The constitutional licence gate is satisfied for Asterisk** to the extent an un-integrated
  dependency can satisfy it: the ADR exists, the conditions are answered, and the grant is documented
  for whoever integrates it.
- **The landscape doc's Asterisk row is corrected** — GPLv2 is right, "the riskiest voice-stack
  licence" is wrong, and the ARI grant is the missing fact.
- **The core is untouched.** aisotropy still contains **zero audio code** and zero telephony code;
  nothing in `internal/`, `aisotropy/`, `cmd/`, or `deploy/` references Asterisk, Twilio, SIP, or ARI.
  This ADR changes no running configuration and adds no dependency.
- **No new dependency, no new container, no new lane.** `examples/voice-pipecat`'s requirements,
  compose files, and test lane are unchanged; ADR-0077's `voice-audio-gpl-stays-in-the-container`
  claim is untouched and still green.
- **The operator's 2026-07-23 approval is preserved, not consumed** — a future Asterisk integration
  starts from conditions 1–4 already answered.

## Sources

- Asterisk `LICENSE` — `github.com/asterisk/asterisk/blob/master/LICENSE` (GPLv2; the AMI/AGI/ARI
  protocol grant; the OpenSSL/OpenH323/UniMRCP/UW-IMAP linking permission; Sangoma US Inc. as
  copyright holder)
- `docs.asterisk.org/About-the-Project/License-Information/` — the same protocol grant in the
  project documentation
- `asterisk.org/products/software/licensing/` — the dual-licence model and Sangoma as worldwide
  exclusive licensor
- `docs.asterisk.org/.../Asterisk_REST_Interface/` and
  `docs.asterisk.org/Development/Reference-Information/.../External-Media-and-ARI/` — ARI as control
  plane, `externalMedia`, and the `UnicastRTP` / `AudioSocket` / `chan_websocket` drivers
- `docs.asterisk.org/Configuration/Channel-Drivers/WebSocket/` — upstream's own statement of why
  `chan_websocket` exists (RTP binary handling and send-timing burden on the app developer)
- `pjsip.org/licensing.htm`, `docs.pjsip.org/en/latest/overview/license.html` — pjproject GPLv2+ /
  Teluu commercial
- `twilio.com/en-us/voice/pricing/us` (fetched 2026-08-03) — $0.0085/min inbound local,
  $0.0140/min outbound US, $0.0044/min Media Streams, $1.15/mo US local number
- `examples/voice-pipecat/.venv/.../pipecat/` @ **1.6.0** — six CPaaS serializers, zero Asterisk
  hits, `transports/websocket/fastapi.py` (the two greps in *Decision 2 (a)* are the executable form)
- `.claude/rules/cos/license-policy.md` §Exceptions; `.claude/rules/constitutional-gates.md`
  §License policy; `.claude/rules/capability-absence-claims.md` (symmetric rigor);
  `.claude/rules/verify-before-execute.md`
- ADR-0067 (Grafana AGPL exception) and ADR-0077 (voicebox GPL-3.0 exception) — the two exceptions
  this one is measured against, condition for condition
- ADR-0066 (the shim), ADR-0069 (the WER lane), ADR-0078 (pipecat 1.x) — the voice stack a telephony
  leg would sit in front of
- ADR-0071 ("capacity is not permission"), ADR-0075 (`scripts/dormant-lanes.sh`, the lane shape a
  built leg would have followed)
- `docs/research/voice-pipeline-landscape-2026-07-23.md` — the deferral this ADR closes, and the row
  it corrects
