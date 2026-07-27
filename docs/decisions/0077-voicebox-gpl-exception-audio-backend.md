---
type: ADR
title: "ADR-0077 — voicebox as the audio backend of examples/voice-pipecat, under a GPL-3.0 separate-service exception"
description: >
  examples/voice-pipecat swaps its two audio legs (Kokoro-FastAPI container for TTS +
  in-process faster-whisper for STT) for a single voicebox 0.2.12 container serving both over the
  OpenAI audio API. voicebox is MIT, but BOTH of its TTS engines pull GPL-3.0 code —
  phonemizer-fork via kokoro-onnx, and piper-tts — which upstream's "purely permissive image"
  recipe does not remove. The adoption therefore runs through license-policy §Exceptions as a
  separate, unmodified, HTTP-only service, each condition answered individually here, with the
  operator approval recorded. Scope is examples/ only: nothing is vendored, no aisotropy artifact
  ships the image, and the core still contains zero audio code.
tags: [decisions, adr, license-policy, gpl, voice, tts, stt, voicebox, kokoro, pipecat, constitutional-gates, examples]
timestamp: 2026-07-27T00:00:00Z
supersedes: []
revisits: [0066, 0069]
---

# ADR-0077 — voicebox as the audio backend of `examples/voice-pipecat`, under a GPL-3.0 separate-service exception

## Status

**✅ Accepted (2026-07-27).** voicebox `v0.2.12` (commit `539325c`, MIT) becomes the default audio
backend of `examples/voice-pipecat`, serving **both** audio legs — STT via
`POST /v1/audio/transcriptions` and TTS via `POST /v1/audio/speech` — from one container defined by
`examples/voice-pipecat/docker-compose.voicebox.yml`. It is adopted under
`.claude/rules/cos/license-policy.md` §Exceptions as a **separate, unmodified, HTTP-only service**;
the five conditions are answered one by one in **Decision** below. Operator approval for that path
(explicitly *this* path, and explicitly **not** the misaki-fork alternative) was given 2026-07-27.

The previous stack stays reachable through **one environment variable**:
`VOICE_AUDIO_BACKEND=kokoro-fastapi`. Reverting a v0.2.x dependency must not require a code edit.

## Context

### The input

`docs/06-Daily/reports/voicebox-spike-2026-07-27.md` — an executable spike that built voicebox,
measured it against this repo's own fixtures and harnesses, and found a **license defect upstream's
README does not know about**. That report *recommended* option **B** (cherry-pick the patterns, do
not adopt the backend), on the grounds that the GPL question was unresolved. This ADR resolves that
question rather than avoiding it, which is exactly what the report's own sequencing left open
("**Keep A open** … (2) decide the GPL question deliberately via ADR"). The report remains the
evidence; this document is the decision.

### The gate this ADR satisfies

`.claude/rules/constitutional-gates.md` → **License policy (repo-wide, hard gate)**:

> Do not introduce a new dependency with a copyleft (GPL/AGPL) or source-available/BUSL license
> **without an explicit ADR**.

GPL-3.0 is copyleft. Note it is **not** on `license-policy.md`'s BLOCKER table (which lists
AGPL/SSPL/BSL/ELv2/Commons Clause/FSL) — so this is not an automatic block, it is an **ADR-gated**
decision. This is the ADR.

### The GPL-3.0 exposure, named exactly

voicebox's own source is **MIT** (`external/voicebox/LICENSE`). Its GPL exposure is entirely in the
speech engines it depends on, and it exists on **both** engine paths:

| Path | The GPL-3.0 component | How it enters |
|---|---|---|
| `VOICEBOX_TTS_ENGINE=kokoro` (our configuration) | **`phonemizer-fork` 3.3.2** — `License :: OSI Approved :: GNU General Public License v3 or later (GPLv3+)` | A hard requirement of `kokoro-onnx` 0.5.0. Not optional and not lazy: `kokoro_onnx/tokenizer.py:8` does `from phonemizer.backend.espeak.wrapper import EspeakWrapper` **at module import**, and that `phonemizer` package *is* the `phonemizer-fork` distribution. Every Kokoro synthesis call goes through it. |
| `VOICEBOX_TTS_ENGINE=piper` (upstream default) | **`piper-tts`** — GPL-3.0 **code** (the amy/bryce/lessac *voices* are public-domain/MIT; it is the code that is GPL) | A direct dependency in voicebox's `pyproject.toml`, bundled in upstream's prebuilt image. |
| Both | **espeak-ng** — GPL-3.0 | `Dockerfile:9-10` installs it via apt; the `espeakng-loader` wheel additionally bundles `libespeak-ng.{so,dylib}` and declares **no license metadata at all**. |

**Why upstream's escape hatch does not remove it.** `external/voicebox/README.md:262-275` says: *"If
you need a purely permissive image, set `VOICEBOX_TTS_ENGINE=kokoro` and drop `piper-tts` from
`pyproject.toml` and the Dockerfile."* The spike **built exactly that recipe and swept the result**:
it is incomplete. Dropping `piper-tts` removes the Piper GPL code (cleanly — `piper` is a lazy
import, `tts_piper.py:40,87`, so no source patch is needed), but the Kokoro path it hands you
carries `phonemizer-fork` and still apt-installs espeak-ng. **`VOICEBOX_TTS_ENGINE=kokoro` alone
does not produce a permissive image.** A genuinely permissive TTS path requires replacing the
**phonemizer**, not the engine.

Because both engines are GPL-exposed, the engine choice is a *quality/latency* decision, not a
license one. We configure **kokoro** — matching the voice (`af_heart`), the model revisions and the
audio characteristics behind every number in ADR-0069 and the spike — and note that
`VOICEBOX_TTS_ENGINE=piper` is equally covered by this exception.

### Why this is the same shape the repo has already accepted

ADR-0067 accepted **Grafana (AGPL-3.0)** on exactly this reasoning: separate container, unmodified
image, HTTP-only, optional, documented, operator-approved. The landscape report reached the same
verdict for **Asterisk**. The distinction that carries all the weight — stated plainly in the spike
— is: *MIT source over GPL dependencies is safe to **run** as a separate service and unsafe to
**absorb***. This ADR keeps us strictly on the "run" side and makes the "absorb" side an explicit
tripwire (see Consequences).

## Decision

Accept voicebox as a **GPL-3.0 exception** under `license-policy.md` §Exceptions. Each condition,
answered individually:

**1. "It runs as a completely separate service, such as its own container." — ✅ SATISFIED.**
`examples/voice-pipecat/docker-compose.voicebox.yml` runs it as its own container, its own compose
project (`aisotropy-voice-pipecat`), published to **loopback only**
(`127.0.0.1:8790:8790`), with no volumes (models are baked at build time, `Dockerfile:20-26`,
`HF_HUB_OFFLINE=1`). No voicebox code enters the Go module, and none enters the example's
virtualenv: `requirements.txt`/`requirements-test.txt` carry **no** `kokoro-onnx`, no
`pipecat-ai[kokoro]`, no `piper-tts`. `aisotropyd` has no awareness voicebox exists.

**2. "Its source code is not modified." — ✅ SATISFIED, and this is why the fork path was rejected.**
The image is built from the **unmodified** upstream clone at `external/voicebox` (git tag `v0.2.12`,
commit `539325c`) with upstream's **own** `Dockerfile`, no patched file, no dropped dependency, no
vendored line. `external/` is **gitignored** (`.gitignore:71`) — the clone is an operator-provided
checkout, not repo content. Configuration is applied exclusively through documented `VOICEBOX_*`
environment variables, which is configuration, not modification.

> This condition is precisely what ruled out the alternative considered and rejected below: forking
> voicebox to swap its Kokoro binding to `misaki` would have *broken the very condition that makes
> the exception defensible*, in order to remove a GPL dependency the exception already handles.

*Honest sub-finding.* Upstream's README recommends pulling `ghcr.io/agjs/voicebox:0.2.12`, which
would be the cleanest possible "unmodified artifact". **That package is not publicly pullable**
(verified 2026-07-27: the GHCR token endpoint issues no anonymous pull token, the manifest request
returns **403**, and `github.com/agjs/voicebox/pkgs/container/voicebox` returns **404**). So the pin
here is a **source pin** — tag `v0.2.12` @ `539325c`, built locally — rather than an image-digest
pin. The local tag is `voicebox:0.2.12`; **never `latest`**. If the package is ever published, a
digest pin against it is strictly better and should replace this.

**3. "The project communicates with it only through a documented public API." — ✅ SATISFIED.**
Four documented HTTP routes, and nothing else: `GET /health`, `GET /v1/models`,
`POST /v1/audio/transcriptions`, `POST /v1/audio/speech` (`external/voicebox/src/voicebox/app.py:66,
74, 88, 143`). The STT leg uses pipecat's **stock** `OpenAISTTService` with `base_url` pointed at
voicebox — no shim, no patch. The TTS leg uses ~40 lines of explicit HTTP in
`examples/voice-pipecat/voicebox_tts.py`, and the reason is worth recording because it is the one
place the OpenAI-compatibility story breaks down:

> pipecat's `OpenAITTSService` resolves the voice through a **closed dict of the 13 OpenAI voice
> ids** (`pipecat/services/openai/tts.py:242`, `VALID_VOICES[self._settings.voice]`). Kokoro-FastAPI
> tolerated that because it *maps* OpenAI voice names server-side. voicebox does **not** map: it
> forwards `voice` straight to the engine (`src/voicebox/tts.py:52`, `tts_piper.py:78`), which knows
> only `af_heart`-style ids. Verified live against the running container: `voice: "af_heart"` → HTTP
> **200**, 45100 bytes; `voice: "alloy"` → HTTP **500**. There is no seam to override — the lookup
> is inline in `run_tts`, not a method. The two rejected alternatives were mutating pipecat's
> module-level `VALID_VOICES` dict (a library mutation that breaks silently when the constant moves)
> and patching voicebox to add an alias table (**which would void condition 2**). ~40 lines of
> explicit, documented HTTP against the published contract is the cheapest option that keeps both
> the library and the service unmodified.

**4. "The justification, residual risk, and operational boundary are documented." — ✅ SATISFIED.**
Justification and boundary: this section plus the compose file's header comment. Residual risk: the
next section, stated without softening.

**5. "A human operator explicitly approves the exception before integration." — ✅ SATISFIED
(2026-07-27).** The operator selected the separate-service exception path over the misaki fork,
with the reasoning recorded above, before any wiring was written. This ADR is the record of that
approval.

`license-policy.md` adds that "even when these conditions are met, the preferred decision is to
choose a permissively licensed alternative." That preference is acknowledged and **not** satisfied
today — the permissive alternative is real but not free (see *Revisit trigger*), and the exception
is narrow (examples/, loopback, never shipped) and reversible by one environment variable.

### Rejected alternative: fork voicebox onto a `misaki` G2P

`misaki` (MIT) is the grapheme-to-phoneme front end the PyTorch `kokoro` package uses; a Kokoro
binding built on it would carry **no** `phonemizer-fork` and **no** espeak-ng, i.e. a genuinely
permissive TTS path with no exception needed. Rejected for this pass on two grounds, both the
operator's:

1. It **breaks condition 2**. A fork is by definition modified source, so the exception's cleanest
   condition would be traded away in exchange for removing a dependency the exception already
   covers. That is a net loss in defensibility, not a gain.
2. It creates a **maintained fork of a 17-star, single-author, v0.2.x project** — permanent
   ownership of somebody else's build in an `examples/` directory.

It stays live as the revisit trigger below, not as a closed door.

## Residual risk (stated plainly)

- **The GPL-3.0 obligations are real; only the boundary keeps them off us.** Running unmodified GPL
  software as a separate process, spoken to over a network API, does not create a derivative work of
  the caller. Vendoring, linking, or redistributing it does. Everything protective here is that
  distinction — so the actions that void it are listed as a tripwire, not as advice.
- **What VOIDS this exception** (any one of these ends it; the exception does not stretch):
  1. **Vendoring** voicebox's source into the repo — including un-gitignoring `external/voicebox`,
     copying a file out of it, or committing a patched variant.
  2. **Shipping its image** (or any image built from it) as part of an aisotropy artifact, release,
     Helm chart, or product deployment.
  3. **Patching it in place** — editing `external/voicebox/**`, or building from a modified
     `pyproject.toml`/`Dockerfile` (this specifically includes upstream's own
     "drop `piper-tts`" recipe, which is a source modification even though upstream suggests it).
  4. **Pip-installing the GPL code into our venv** — `kokoro-onnx`, `pipecat-ai[kokoro]`, or
     `piper-tts` in `requirements*.txt`. That moves the GPL code out of the container and into our
     dependency graph, which is the whole thing the boundary prevents.
- **Maturity.** voicebox is **v0.2.12**, ~**17 GitHub stars**, a **single author**, upstream commit
  one day old at spike time. No security audit, no CVE history, no production references, and the
  `0.2.x` line carries **no API-stability promise**. Mitigation is honest but partial: it is **688
  LOC** (forkable in an afternoon if abandoned — though forking would void condition 2 and require
  amending this ADR), it exposes the **OpenAI audio contract**, so swapping it costs one base URL,
  and `VOICE_AUDIO_BACKEND=kokoro-fastapi` reverts to the previous stack **without a code change**.
  Scope containment is the real mitigation: `examples/`, a local dev dependency, never distributed.
- **One inference at a time per instance.** `app.py:46-49` takes a process-wide `threading.Lock`
  with an explicit rationale (predictable latency over throughput on CPU). Measured in the spike:
  concurrent STT+TTS **serialize** (wall/serial-sum ≈ 1.13). For a multi-tenant gateway that would
  be a hard throughput ceiling — you scale by running N instances. Irrelevant at PoC scale; recorded
  so nobody discovers it in production.
- **Barge-in against voicebox is UNVERIFIED.** Whether voicebox stops synthesizing when the client
  disconnects mid-PCM-stream was not proven (`_locked_pcm_chunks` suggests it stops at the next
  chunk boundary). What *was* re-measured is the leg we actually gate on — barge-in-to-silence at
  the **shim**, median **0.1 ms** — which is the constitutional ctx-cancellation property; the TTS
  side of a barge-in is untested for both backends.
- **The exception covers voicebox only.** It does not pre-clear any other GPL/AGPL dependency, and
  it does not extend to a *different* integration shape of voicebox itself (e.g. importing it as a
  library, which is not possible today and would need a new decision).

## The measured record (with its correction history)

**Never quote "8.6×".** The spike's first draft compared a fresh voicebox measurement against a
**four-day-old record** of the Kokoro-FastAPI baseline (3540 ms). That baseline was re-measured the
same day with the baseline's own function, and the ratio was corrected **down**:

| Case | voicebox 0.2.12 | Kokoro-FastAPI, re-measured 2026-07-27 | ratio |
|---|---|---|---|
| Short bracket (~40 ch) | 410.7 ms | 1647.4 ms | ~4.0× |
| Long bracket (~146 ch) | 1767.1 ms | 2134.7 ms | ~1.2× |

So: **~1.2–4× on the TTS leg depending on sentence length**, and the advantage nearly vanishes on
long text. Emulation was ruled out (both ran native `arm64`). The correction is kept in the record
because the retracted number is the one that would otherwise get quoted.

**Re-measured today, on this machine, wired into the PoC** — and this is the number that matters for
the PoC, because it is *this* stack's own metric:

```
examples/voice-pipecat/.venv/bin/python measure_ttfab.py --runs 5
  median llm_ttft  =    6.0 ms   (shim)
  median tts_ttfab = 3441.4 ms   (voicebox)
  median TTFAB     = 3447.4 ms
```

**That is indistinguishable from the 3543 ms Kokoro-FastAPI record — the TTS speedup does not show
up in this PoC's own TTFAB metric**, and saying otherwise would be the same error the spike already
corrected once. Two reasons, both measured rather than assumed:

- **The stub LLM's first sentence is long.** It echoes the system prompt: **105 characters**. Direct
  bracketing on the same running container, 5 runs each: **38-char sentence → 902 ms median**;
  **105-char stub sentence → 2211 ms median**. voicebox's advantage is concentrated in short first
  sentences, and this PoC's stub does not produce one.
- **The machine was loaded** (~95% swap, concurrent agent sessions, container capped at 4 CPUs) —
  which is also why 902 ms here versus the spike's 410.7 ms on the short bracket is not a
  contradiction, and why none of these absolute numbers should be treated as a machine-independent
  result.

**Quality, re-derived rather than inherited** (`examples/voice-pipecat/measure_wer.py`, the spike's
harness folded into the example as its §8 recommended):

| Case | 2026-07-23 (Kokoro-FastAPI → faster-whisper `base.en`) | 2026-07-27 (voicebox, `faster-distil-whisper-small.en`) |
|---|---|---|
| Round-trip, 4 sentences | 0.000 / 0.000 / 0.000 / **0.083** | 0.000 / 0.000 / 0.000 / **0.083** |
| Pinned fixtures, 3 WAVs | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.000 |

The **same** distribution on a **different STT model**, with the worst case being the identical
formatting artefact (`"Doctor"` → `"Dr."`), not an intelligibility loss. **Thresholds are therefore
unchanged** (round-trip 0.20, fixtures 0.15) — re-derived from measurement, not loosened, and not
assumed to transfer. The pinned fixtures did exactly the job they were committed for: the STT model
changed underneath them and the comparison stayed meaningful.

**The lane, executed:** `VOICE_LANE=real scripts/with-heavy-lane.sh … pytest examples/voice-pipecat/tests/ -q`
→ **16 passed in 103.54s**, including the file-based Pipecat pipeline test (WAV → voicebox STT →
shim LLM → voicebox TTS frames) against a live `cmd/aisotropyd`.

## Consequences

- **Gate satisfied.** The constitutional "without an explicit ADR" clause is met for voicebox's
  GPL-3.0 transitive dependencies, for the kokoro engine as configured **and** for the piper engine
  if selected.
- **`examples/voice-pipecat` is one container simpler**: two moving parts (Kokoro-FastAPI + an
  in-process whisper model) become one, with a ~3 s cold start and models baked into the image.
- **The core is untouched.** aisotropy still contains **zero audio code**; nothing in `internal/`,
  `aisotropy/`, `cmd/`, `deploy/` or any shipped artifact references voicebox. This ADR changes an
  example, not the runtime.
- **Reverting is one variable.** `VOICE_AUDIO_BACKEND=kokoro-fastapi` restores the previous stack in
  `bot.py`, the metric scripts and the whole ADR-0069 test lane, because all three now build their
  services from the same `audio_backend.py` factories.
- **The pipecat floor rises** to `>=0.0.108,<0.1` (still pre-1.0 — `pipecat-pinned-pre-1.0` in
  `manifests/capability-claims.yaml` stays green): the wiring needs
  `OpenAISTTService(base_url=…)` and the `run_tts(text, context_id)` signature.
- **A new executable metric lands**: `measure_wer.py` prints per-case WER instead of only asserting
  a bound, so the next backend change re-derives the thresholds in one command.
- **Adjacent patterns worth stealing are NOT part of this ADR.** The spike's option B items —
  bounded inputs on our shim (the unbounded `messages[]`, a constitutional "reject before spending"
  concern) and `Server-Timing` on the shim — are independent of this decision, remain
  recommended, and are deliberately left to their own change so a license decision and an API
  hardening do not ride in one commit.

## Revisit trigger

Re-open this decision — and prefer the permissive alternative `license-policy.md` asks for — when
**any** of these becomes true:

1. **A maintained, permissively-licensed Kokoro-ONNX binding exists whose G2P is not
   `phonemizer-fork`** (today: `misaki`, MIT). The bar for acting is that it can be *consumed*, not
   forked — i.e. voicebox upstream adopts it, or a `misaki`-based OpenAI-audio server exists that we
   can run unmodified. At that point the exception is unnecessary and should be dropped, not
   renewed.
2. **The scope stops being `examples/`.** Any proposal to run voicebox in a shipped deployment, a
   Helm chart, or a product artifact voids the exception as written and needs a new decision — not
   an amendment.
3. **voicebox is abandoned** (no upstream activity + an unpatched security issue). Then the choice
   is between `VOICE_AUDIO_BACKEND=kokoro-fastapi` (free, immediate) and a fork (which requires
   amending condition 2 here first).
4. **The GHCR package becomes publicly pullable.** Replace the source pin with an image-digest pin —
   strictly stronger evidence of "unmodified", and one less local build.

## Sources

- `docs/06-Daily/reports/voicebox-spike-2026-07-27.md` — the measured spike, the GPL finding, and
  the ~8.6× correction
- `external/voicebox` @ `v0.2.12` / `539325c` — `LICENSE` (MIT), `README.md:262-275` (the incomplete
  permissive recipe), `src/voicebox/{app,config,tts,tts_piper}.py`, `Dockerfile`
- `phonemizer-fork` GPLv3+ classifier; `piper-tts` GPL-3.0; espeak-ng GPL-3.0
- `.claude/rules/cos/license-policy.md` §Exceptions; `.claude/rules/constitutional-gates.md`
  §License policy
- ADR-0067 (`0067-grafana-agpl-exception-observability-stack.md`) — the sibling exception this one
  follows condition-for-condition
- ADR-0066 (the shim / LLM leg), ADR-0069 (the WER lane, fixtures and thresholds reused here)
- pipecat-ai 0.0.108 source: `services/openai/tts.py:46-60,242` (VALID_VOICES),
  `services/openai/stt.py:74-177`, `services/whisper/base_stt.py:220`,
  `services/stt_service.py:661-672`, `services/tts_service.py:491`
