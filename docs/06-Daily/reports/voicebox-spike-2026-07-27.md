---
type: research-report
title: "voicebox spike — can it be the audio half of aisotropy's voice sandwich? (measured)"
description: >
  Executable spike on voicebox v0.2.12 (external/voicebox, MIT) as a single-process replacement for
  the Kokoro-FastAPI TTS + faster-whisper STT legs of examples/voice-pipecat. Built the README's
  "purely permissive" recipe and ran it: TTS first-audio-byte median 410.7 ms warm (vs the
  2026-07-23 baseline's 3540 ms), STT WER 0.000 on all three ADR-0069 pinned fixtures. Headline
  finding is a license one, not a latency one — the permissive recipe as documented is INCOMPLETE:
  the Kokoro path drags in GPL-3.0 phonemizer-fork, so dropping piper-tts does not yield a
  permissive image. Recommendation is cherry-pick, not adopt.
tags: [research, spike, voice, tts, stt, voicebox, kokoro, latency, licenses, gpl, measured, adr-0066, adr-0069]
timestamp: 2026-07-27T00:00:00Z
---

# voicebox spike — 2026-07-27

**aisotropy HEAD:** `6f291fb`. **voicebox:** `external/voicebox` @ `539325c`, tag `v0.2.12`
(upstream commit dated 2026-07-26 — the clone is one day old). **Machine guard at every heavy
step:** `scripts/with-heavy-lane.sh --need-mb N` (verdict DEGRADED throughout; never BLOCKED).

**The question this spike answers.** Does routing the audio legs through voicebox simplify the
stack WITHOUT costing latency vs today's Kokoro-FastAPI baseline (median TTFAB 3543 ms,
`examples/voice-pipecat/README.md`)?

**The short answer.** Latency: it does not cost, it *saves* — **~1.2–4× faster on the TTS leg**
depending on sentence length (see the correction in §3: an earlier "8.6×" compared a measurement
against a stale record; the baseline has since been re-measured native and the gap is smaller,
and nearly vanishes on long text), and STT accuracy is at parity (WER 0.000). Simplification: real (two containers → one, plus a
protocol-level latency header we do not have). **But the recommendation is still not "adopt as
the audio backend", because the spike found a license defect that the project's own README does
not know about:** the documented "purely permissive" escape hatch does not actually produce a
permissive dependency set.

---

## 1. What voicebox is

A self-hosted, OpenAI-compatible speech server: STT (`faster-whisper`) + streaming TTS (`Piper`
or `Kokoro`) behind one FastAPI process. 688 LOC of source, 779 LOC of tests. Four routes:

| Route | Contract |
|---|---|
| `GET /health` | `{status, models_loaded}` — `starting` until both engines constructed (`app.py:66`) |
| `GET /v1/models` | STT model id + every Kokoro voice id as "models" (`app.py:74`) |
| `POST /v1/audio/transcriptions` | multipart; `json` / `text` / `verbose_json` (`app.py:88`) |
| `POST /v1/audio/speech` | JSON; `wav` (buffered) or `pcm` (streamed) (`app.py:143`) |

It is the *whole* audio half of the sandwich in one process — which is precisely why it is
interesting to us: `examples/voice-pipecat` currently needs a Kokoro-FastAPI container **plus** an
in-process faster-whisper model.

### Positioning against our stack

Our voice sandwich (ADR-0066 + ADR-0069) is `STT → aisotropy /v1/chat/completions shim → TTS`.
voicebox replaces **only the two audio legs**. It has no LLM, no tenancy, no memory — it is not a
competitor to the runtime, and adopting it would not touch the core's "zero audio code" property.

---

## 2. License posture — THE headline finding

### What the README claims

`external/voicebox/README.md:262-275` states: voicebox is MIT; `piper-tts` is GPL-3.0 and is
bundled in the prebuilt image; and —

> "If you need a purely permissive image, set `VOICEBOX_TTS_ENGINE=kokoro` and drop `piper-tts`
> from `pyproject.toml` and the Dockerfile."

### What is actually true (verified by building it)

I built exactly that recipe — `VOICEBOX_TTS_ENGINE=kokoro`, `piper-tts` removed from the
dependency list — and swept the resulting install. **The recipe is incomplete.** The Kokoro path
has its own GPL-3.0 dependency:

```
kokoro-onnx 0.5.0 requires: espeakng-loader>=0.2.4, numpy, onnxruntime, phonemizer-fork>=3.3.2
                                                                       ^^^^^^^^^^^^^^^^^^^^^^^
phonemizer-fork 3.3.2 → License :: OSI Approved :: GNU General Public License v3 or later (GPLv3+)
```

It is not optional and it is not lazy: `kokoro_onnx/tokenizer.py:8` does
`from phonemizer.backend.espeak.wrapper import EspeakWrapper` at module import, and that
`phonemizer` package *is* the `phonemizer-fork` distribution (verified: the imported
`phonemizer/__init__.py` resolves to dist `phonemizer-fork 3.3.2`). Every Kokoro synthesis call
goes through it.

Two further GPL facts the README does not mention:

- **espeak-ng itself is GPL-3.0**, and the permissive image still installs it
  (`apt-get install espeak-ng espeak` — required, see §5). The `espeakng-loader` wheel additionally
  *bundles* `libespeak-ng.dylib`/`.so` and declares no license metadata at all.
- Piper's **voices** (amy/bryce/lessac) are public-domain/MIT — it is the `piper-tts` **code** that
  is GPL. Dropping the dependency does not require dropping any model.

**Verdict against this repo's gate.** `.claude/rules/constitutional-gates.md` → "Do not introduce a
new dependency with a copyleft (GPL/AGPL) or source-available/BUSL license without an explicit
ADR." GPL-3.0 is copyleft. The `license-policy.md` tables do not list plain GPL-3.0 explicitly
(they block AGPL/SSPL/BSL/ELv2/Commons Clause/FSL), so this is **not an automatic BLOCKER** — but
it is unambiguously an **ADR-gated** decision, and it applies to the *permissive* configuration,
not just the prebuilt image.

Practically, the GPL exposure is the same shape as Asterisk's in the landscape report: if voicebox
runs as a **separate, unmodified service talked to over its documented HTTP API**, the
license-policy §Exceptions path is available. If we ever **vendor** its code or ship its image as
part of our product, GPL obligations attach. That distinction is the whole ballgame and it is why
"cherry-pick the patterns" is a materially different risk posture from "adopt the backend".

### Corrected permissive recipe (what would actually be needed)

To get a genuinely permissive TTS path you must replace the **phonemizer**, not just the engine:
use a Kokoro binding whose G2P is not `phonemizer-fork` (e.g. `misaki`, MIT — which is what the
PyTorch `kokoro` package uses), or accept espeak-ng+phonemizer as an ADR'd GPL service boundary.
`VOICEBOX_TTS_ENGINE=kokoro` alone does not do it.

---

## 3. Measured comparison

**Method.** Mirrors `examples/voice-pipecat/measure_ttfab.py`'s TTS leg exactly: streaming request,
stopwatch from request start to **first audio byte**, N runs, median reported, every run printed
(harness: `measure_voicebox.py`, reproduced in §8). Run in Docker on **linux/arm64, native, not
emulated** (`docker exec … uname -m` → `aarch64`) — the same shape as the baseline, which was also
a container. Machine guard DEGRADED at measurement time.

### TTS — time to first audio byte

> **⚠ CORRECTION (2026-07-27, same day) — the baseline was re-measured and the ratio below is
> WRONG.** This section's comparison put a measurement against a four-day-old *record*; §6 flagged
> that as the report's largest caveat. The baseline has now been re-run. Both of the caveat's
> questions are answered, and one of them the other way:
>
> - **Emulation: ruled out.** `ghcr.io/remsky/kokoro-fastapi-cpu:latest` publishes `linux/arm64`
>   and `linux/amd64`; the pulled image reports `Architecture: arm64` on this arm64 host. The
>   baseline ran **native**, not emulated.
> - **But the 3540 ms record does not reproduce.** Re-measured today with the baseline's OWN
>   function (`measure_ttfab.py:tts_first_audio_byte` — `response_format:pcm`, `stream:true`,
>   stopwatch to the first streamed chunk), unique text per run, warm-up excluded, on the same
>   two brackets voicebox was measured against:
>
> | Case | voicebox 0.2.12 | Kokoro-FastAPI, **re-measured** | ratio |
> |---|---|---|---|
> | Short bracket (~40 ch) | 410.7 ms | **1647.4 ms** (996.8–2119.0) | **~4.0×** |
> | Long bracket (~146 ch) | 1767.1 ms | **2134.7 ms** (1895.7–2667.0) | **~1.2×** |
>
> So voicebox is still faster, but by **~1.2–4×, not 8.6×** — and on the long bracket the
> advantage nearly disappears. The 3540 ms record was most likely inflated by the machine state of
> 2026-07-23 (that run happened at ~90% swap under several concurrent agent sessions) and by the
> stub echo's longer, variable text. **Do not quote 8.6× anywhere.**
>
> *Methodology note worth keeping.* The first re-measurement attempt used `curl -w
> %{time_starttransfer}` and produced **2.8 ms** — an impossible number for TTS synthesis, and the
> reason is instructive: on a chunked response that field reports the first **HTTP** byte, which
> the server flushes long before the first **audio** byte. Only reusing the baseline's own
> streaming function produced a comparable figure. A number that beats expectation by three orders
> of magnitude is a bug report about the measurement, not a result.
>
> The recommendation (§7) is unchanged — it never rested on latency, it rests on the GPL finding.

| Case | voicebox 0.2.12 (Kokoro-ONNX) | Baseline: Kokoro-FastAPI CPU (2026-07-23, superseded above) |
|---|---|---|
| **Warm, short sentence (38 ch)** | **410.7 ms** median (374.1–451.9) | 3540 ms median (2.3–3.7 s) |
| Cold, first 5 runs after boot | 1108.7 ms median (651.1–1756.9) | — |
| Long single first sentence (146 ch) | 1767.1 ms median (1666–1805) | — |
| Multi-sentence, short opener | **347.6 ms** to first byte; 2007.5 ms to last | — |
| `response_format=wav` (buffered) | 546.2 ms median | n/a (baseline used pcm) |
| STT, 2–6 s fixture | 670–914 ms | — |

**Projected full-sandwich TTFAB.** Our LLM leg (the ADR-0066 shim) measured **3.1 ms** median.
Sandwich floor = LLM TTFT + TTS TTFAB:

| Stack | Projected median TTFAB |
|---|---|
| Today (shim + Kokoro-FastAPI CPU) | **3543 ms** (measured 2026-07-23) |
| shim + voicebox, warm, short first sentence | **~414 ms** (3.1 + 410.7) |
| shim + voicebox, long first sentence | ~1770 ms |

**~8.6× faster warm; ~2× faster even in the worst comparable case.** ⚠ SUPERSEDED — see the
correction box above: re-measurement puts the real range at ~1.2–4×. The most likely explanation is
ONNX-runtime Kokoro vs Kokoro-FastAPI's PyTorch path, but I did not profile the baseline to prove
that — see §6.

### STT — accuracy on our own pinned fixtures

Run against `examples/voice-pipecat/tests/fixtures/*.wav` using **this repo's own WER helper**
(`examples/voice-pipecat/tests/wer.py`) and the same fixture→transcript table as
`tests/test_fixtures.py`, so the numbers are directly comparable to ADR-0069's 0.15 threshold:

| Fixture | Reference | voicebox hypothesis | WER |
|---|---|---|---|
| `greeting.wav` | `Hello there.` | `Hello there.` | **0.000** |
| `numbers.wav` | `The meeting starts at 9 on March 3.` | `The meeting starts at 9 on March 3.` | **0.000** |
| `pangram.wav` | `The quick brown fox jumps over the lazy dog.` | `The quick brown fox jumps over the lazy dog.` | **0.000** |

Mean 0.000, worst 0.000, threshold 0.15 → **PASS**. Parity with the baseline's measured 0.000, on a
*different* model (`Systran/faster-distil-whisper-small.en` vs our `base.en`) — i.e. the fixtures
would keep passing through a swap.

### Operational

| Property | Measured |
|---|---|
| Permissive image size | **1.46 GB** (models mounted, not baked; baking adds ~657 MB) |
| Cold start to `models_loaded:true` | **3 s** (models pre-cached, `HF_HUB_OFFLINE=1`) |
| Model footprint | Kokoro `model.onnx` 310 MB + `voices.bin` 26 MB + whisper ≈ 657 MB total |

---

## 4. Adoptable patterns (with `file:line`)

Verified against the source in `external/voicebox`, and each probed live where probeable.

### 4.1 `Server-Timing` on inference responses — ADOPTABLE, with a correction

`app.py:119` (`stt;dur=…`) and `app.py:186` (`tts;dur=…`) attach a standard `Server-Timing`
response header carrying server-measured inference milliseconds. Verified live:
`Server-Timing: stt;dur=891.6` and `tts;dur=710.8` (client-observed 721.4 ms — consistent).

**Correction to the spike's premise:** it is *not* on "every inference". The **PCM streaming path
has no `Server-Timing`** (`app.py:189-197` sets only `X-Audio-*`) — necessarily so, since duration
is unknown when the header flushes. So the header covers the buffered paths only, which is exactly
the sub-case where it is cheap. That nuance is part of what makes it adoptable: the pattern is
"emit server-measured timing wherever the response is buffered", not "instrument everything".

Our shim (`internal/api/openai_compat.go`) emits no equivalent. A `Server-Timing: llm;dur=…` (and a
`ttft` metric) on the non-streaming path would be a genuinely cheap protocol-level observability
win, and unlike a metrics backend it needs no infrastructure.

### 4.2 Bounded inputs — ADOPTABLE, and the sharpest contrast with our shim

| Guard | Source | Probe result |
|---|---|---|
| Upload cap: `read(max_bytes+1)` then 413 | `app.py:95-100` | 26 MB upload → **HTTP 413** |
| Input chars → 400 | `app.py:157-160` | 6000 chars → **400** `"input exceeds 4000 character limit"` |
| Empty input → 400 | `app.py:155` | `"   "` → **400** |
| Unsupported format → 400 | `app.py:151` | `mp3` → **400** |
| Speed range 0.25–4.0 → 400 | `app.py:14-30, 149` | `99` → **400** |
| Undecodable audio → 400, not 500 | `app.py:116-117`, `stt.py:64-67` | junk file → **400** |
| Decoded-duration cap enforced *during* decode | `stt.py:35-48` | (not probed — see §6) |

The `read(max_bytes+1)` idiom is the detail worth stealing: it bounds *before* buffering, so an
oversized upload never lands in memory. And `stt.py:35-48` caps decoded duration **as frames are
resampled**, which defeats a decompression bomb that is small on the wire — a check on
`Content-Length` alone would not.

**Contrast (given, not re-verified here — it is the coordinator's finding):** our shim accepts an
unbounded `messages[]`; a verify pass fed it 20k messages and got 200 OK. voicebox refuses four
different oversized/malformed inputs with correct 4xx codes. This is the single most directly
transferable lesson of the spike, and it is a constitutional-gate concern ("reject/degrade before
spending"), not a nicety: an unbounded `messages[]` is unbounded *token spend* and unbounded
hydration work before any quota check can matter.

### 4.3 The WAV-vs-PCM contract split — INDEPENDENT CONFIRMATION of our own 2026-07-27 fix

`app.py:168-187` buffers a **complete, correctly-sized** WAV for `response_format=wav`, with the
comment (`app.py:169-172`) that a streaming placeholder-size header *"decodes to distorted audio"*
in strict decoders. Non-WAV formats stream raw PCM and declare the format out-of-band via
`X-Audio-Sample-Rate` / `X-Audio-Channels` / `X-Audio-Sample-Format` (`app.py:192-196`).

Verified live on a real response: file 128044 B, RIFF field 128036 (= len−8 ✓), data field 128000
(= len−44 ✓), **no `0xFFFFFFFF` placeholder**, and Python's strict `wave` module decodes it cleanly
(1ch/24000Hz/16bit/64000 frames/2.67 s).

The confirmation is sharper than expected: `wav.py:5-17` still defines `wav_header()`, the
**placeholder-`0xFFFFFFFF`** version — and `grep -rn wav_header --include=*.py` finds **exactly one
hit, its own definition**. It is dead code: the vestige of the streaming-header approach they
abandoned. An independent project hit our `nframes 0x7FFFFFFF` defect, reached the same conclusion
(buffer the WAV, stream only raw PCM), and left the discarded helper behind. Worth recording
because it upgrades our fix from "a thing we learned" to "the known-correct contract".

### 4.4 The global inference lock — ADOPTABLE AS A *DOCUMENTED TRADE-OFF*, not as a design

`app.py:46-49` takes one process-wide `threading.Lock` with an explicit rationale: *"This
CPU-oriented server favors predictable latency over throughput. Keeping one model inference active
prevents STT and TTS from fighting for the same cores."* `_locked_pcm_chunks` (`app.py:33-41`) is
the refinement — it holds the lock only while synthesizing **each chunk**, releasing it while the
chunk is on the wire, so a slow client cannot hold the model hostage.

**Measured, and this is a real constraint:** concurrent STT+TTS *serialize*. Serial baseline
stt 683 ms + tts 579 ms = 1263 ms; fired concurrently, wall = 1421 ms → **wall/serial-sum = 1.13**
(≈1.0 means fully serialized; ≈0.5 would mean parallel). One voicebox instance therefore serves
**one inference at a time**. For a multi-tenant voice gateway that is a hard throughput ceiling —
you scale by running N instances, not by concurrency within one.

What is adoptable is the *discipline*, not the lock: **document the concurrency posture as a
deliberate choice with its rationale in the code**. The lock itself is specific to their
single-box CPU deployment and would be wrong for our runtime.

### 4.5 Smaller things worth noting

- **Fail-closed bind auth** (`config.py:24-39`): refuses a non-loopback listen without
  `VOICEBOX_API_KEY` unless `VOICEBOX_ALLOW_INSECURE_BIND=true` is set explicitly. Default-deny on
  the *network posture*, with a documented opt-out for the container case. Same family as our
  ADR-0024 NoAuth warning, but enforced rather than warned.
- **Constant-time key compare** (`app.py:62`): `hmac.compare_digest`, and `/health` is exempted so
  liveness works without a credential.
- **Pinned model revisions** (`config.py:73-83`): every model pinned to a **commit SHA**, not a tag
  — the supply-chain-guard digest-pinning discipline applied to model weights. We pin container
  digests; we do not pin model revisions anywhere.
- **Config validated once, at load, into a frozen dataclass** (`config.py:42-124`) — invalid
  `VOICEBOX_*` fails at boot with a named error, not at first request.
- **Language validated before decode** (`stt.py:104-110`): rejects an unsupported language *before*
  spending CPU on audio decode — literally "reject before spending", in their STT path.

---

## 5. What it cost to run this (and one upstream bug found)

Both are honest obstacles a future adopter will hit.

**voicebox's Kokoro engine does not run natively on macOS arm64.** The `espeakng-loader` 0.2.4
wheel ships a `libespeak-ng.dylib` whose data path is baked to the upstream CI path
(`/Users/runner/work/espeakng-loader/…/espeak-ng-data/phontab`), and it **ignores the `path`
argument to `espeak_Initialize` entirely** — proved by calling the C entrypoint directly via
ctypes with three different candidate paths (the loader dir, the data dir, and `NULL`); all three
produced the identical compiled-in-path error, and espeak then `exit()`s the process. This is an
upstream `espeakng-loader` packaging defect, **not** a voicebox bug, and it is invisible in Docker
because the Dockerfile installs a system `espeak-ng` (`Dockerfile:9-10`) whose compiled-in default
happens to be correct. Consequence: the bare-metal macOS path is a dead end; Docker is mandatory
on this platform. (This is also why measuring in Docker was the *better* choice anyway — the
baseline was a container too.)

**`piper` is a lazy import — the one part of the permissive recipe that works cleanly.**
`tts_piper.py` imports `piper` only inside `PiperTtsEngine.__init__` (`tts_piper.py:40`) and
`.synthesize_stream` (`tts_piper.py:87`), never at module level. Verified: with `piper` **not
installed**, `import voicebox.app, voicebox.config, voicebox.tts, voicebox.tts_piper,
voicebox.__main__` all succeed. So dropping the dependency needs **no source patch** — only the
`pyproject.toml` line and (if baking models) the `scripts/fetch_models.py:24-38` Piper-voice loop.

---

## 6. Limits — what I did NOT verify

Stated plainly, because a partial spike honestly reported is the deliverable.

**On the comparison itself:**
- **The baseline was not re-run today.** The 3540 ms figure is the recorded 2026-07-23 number from
  `examples/voice-pipecat/README.md`. The `ghcr.io/remsky/kokoro-fastapi-cpu` image is **no longer
  present on this machine** (`docker images` — it has been pruned), and re-pulling it is a
  multi-GB download I judged out of budget on a DEGRADED machine. So this is
  **measured-vs-recorded**, not measured-vs-measured, on the same machine ~4 days apart under
  different load. ⚠ SUPERSEDED — the re-measurement in §3 shows load noise DID explain much of it
  (1647 ms today vs the 3540 ms record on the same native image), but
  I cannot rule out a contribution.
- **The input text differs from the baseline's.** The baseline synthesized the first sentence of
  the stub LLM's echo, which inlines the whole system prompt — long and variable. I could not
  reproduce it exactly, so I bracketed instead: 38-char sentence (410.7 ms) and a 146-char
  stub-echo-shaped sentence (1767.1 ms). Even the pessimistic bracket beats the baseline ~2×.
- **I did not profile *why* voicebox is faster.** ONNX-runtime vs PyTorch is the plausible cause;
  I did not instrument Kokoro-FastAPI to confirm it. It is also possible the baseline container was
  amd64-emulated on this arm64 host, which would inflate it substantially — **unknown, and it would
  materially change the ratio.** This is the single largest caveat in the report.
- **Whisper models differ** (voicebox `faster-distil-whisper-small.en` vs our `base.en`). WER tied
  at 0.000 on three short fixtures, which is too coarse to rank them; it shows only "no regression
  on our pinned set".

**On voicebox itself:**
- **No barge-in / cancellation test.** Whether voicebox stops synthesizing when the client
  disconnects mid-PCM-stream is **unverified**, and it is a constitutional-gate-adjacent property
  for us (ADR-0069 measures barge-in at 0.1 ms today). `_locked_pcm_chunks` suggests it would stop
  at the next chunk boundary, but I did not prove it.
- **No load/concurrency test beyond N=2.** The serialization result is from a single 2-way probe.
- **`verbose_json` / timestamps, `stt.py:35-48`'s duration cap, the `speed` parameter's audible
  effect, GPU (`Dockerfile.cuda`), and the Piper engine** were all left unexercised.
- **I did not run voicebox's own test suite** (779 LOC, incl. `tests/test_latency.py`'s
  RTF < 0.5 and first-chunk < 2 s guards).
- **No end-to-end sandwich run.** I measured the TTS and STT legs in isolation against the same
  fixtures/methodology as the baseline; I did not wire voicebox into `bot.py` or the ADR-0069
  pipeline test.
- **Maturity: early-stage.** v0.2.12, ~17 GitHub stars, single author, upstream commit one day old.
  688 LOC is small enough to fork and own, which cuts both ways. No security audit, no CVE history,
  no production references. The `0.2.x` version line means no API-stability promise.

---

## 7. Recommendation

**Cherry-pick the patterns. Do not adopt as the audio backend — yet.**

The latency case for adoption is genuinely strong and I expected to be writing the opposite
conclusion. What blocks it is not performance, it is that **the permissive build recipe does not
deliver a permissive build**, and that is exactly the class of claim this repo requires evidence
for before it ships.

| Option | What it means | Trade-off |
|---|---|---|
| **A. Adopt as the audio backend** | Replace Kokoro-FastAPI + in-process faster-whisper with one voicebox container in `examples/voice-pipecat` | **Upside, measured (corrected):** ~1.2–4× faster TTS leg depending on sentence length (re-measured baseline 1647 ms short / 2135 ms long vs voicebox 411 / 1767), STT WER unchanged at 0.000, two moving parts → one, 3 s cold start, `Server-Timing` for free. **Cost:** requires an **ADR for GPL-3.0** (`phonemizer-fork` + espeak-ng) even in the "permissive" config — defensible only under the license-policy §Exceptions service boundary (separate, unmodified, HTTP-only). Adds a dependency on a v0.2.x, ~17-star, single-author project. One inference at a time per instance. Barge-in unverified. |
| **B. Cherry-pick the patterns only** ← **recommended** | Take the ideas into our own code; leave voicebox as a reference clone | **Upside:** the two highest-value items — **bounded inputs on the shim** (the 20k-`messages[]` hole; a constitutional-gate concern) and **`Server-Timing` on the shim** — are ours to implement in Go, cost days not weeks, carry **zero** license or supply-chain exposure, and are independently valuable whatever we do about audio. Plus: model-revision pinning, fail-closed bind auth, and independent confirmation of our WAV contract. **Cost:** we keep the 3543 ms TTFAB and the two-container topology. The latency win is real and we would be leaving it on the table. |
| **C. Neither** | Close the spike, change nothing | **Upside:** zero cost, zero risk. **Cost:** wastes a measured result, and leaves the shim's unbounded `messages[]` — which this spike sharpened into a concrete, exploitable contrast — unaddressed. **Not recommended.** |

**Suggested sequencing.** Take **B now** (bounded inputs + `Server-Timing` on the shim — neither
depends on any voicebox decision). Keep **A open** as a follow-up gated on three things, in order:
(1) re-measure the Kokoro-FastAPI baseline on an idle machine *with architecture confirmed* to
close the biggest caveat in §6; (2) decide the GPL question deliberately via ADR, or find a
`misaki`-based Kokoro path that removes it; (3) verify barge-in. If the ~8× holds after (1), the
latency argument becomes strong enough that (2) is worth doing properly rather than avoiding.

**What NOT to do:** do not vendor voicebox's source into the repo, and do not ship its image as
part of a product artifact, without the ADR in (2). MIT source over GPL dependencies is safe to
*run* as a separate service and unsafe to *absorb*.

---

## 8. Reproducing this (evidencia ejecutable)

Everything below ran on this machine today. Scratch artifacts were removed; the two pre-existing
`aisotropy-qdrant-1` / `aisotropy-rqlite-1` containers were left untouched.

```bash
# 0. Guard, before every heavy step (verdict was DEGRADED throughout, never BLOCKED)
bash scripts/resource-guard.sh --explain

# 1. Toolchain: voicebox pins requires-python >=3.11,<3.12; this machine has only 3.14
uv python install 3.11

# 2. The PERMISSIVE build context = the clone with piper-tts dropped (README recipe)
grep -v 'piper-tts' external/voicebox/pyproject.toml > $CTX/pyproject.toml
cp -R external/voicebox/src $CTX/src            # + Dockerfile with VOICEBOX_TTS_ENGINE=kokoro
docker build -t voicebox-permissive:spike $CTX  # 1.46 GB, models NOT baked

# 3. The license finding — the reason for the recommendation
docker run --rm voicebox-permissive:spike python -c "
import importlib.metadata as m
print(m.requires('kokoro-onnx'))
print(m.metadata('phonemizer-fork')['Classifier'])"
#   -> phonemizer-fork>=3.3.2 ... GNU General Public License v3 or later (GPLv3+)

# 4. Run with the host HF cache mounted (models pre-fetched; HF_HUB_OFFLINE=1)
docker run -d --name voicebox-spike -p 8790:8790 -v $HF:/models/huggingface \
  -e HF_HUB_OFFLINE=1 -e VOICEBOX_TTS_ENGINE=kokoro voicebox-permissive:spike
curl -s localhost:8790/health          # {"status":"ok","models_loaded":true} in 3s
docker exec voicebox-spike uname -m    # aarch64 — native, NOT emulated

# 5. The measurements (harnesses mirror examples/voice-pipecat/measure_ttfab.py)
python measure_voicebox.py --runs 5                 # TTS TTFAB: 410.7 ms median warm
python measure_wer.py                               # STT: WER 0.000 x3 (repo's own wer.py)

# 6. Cleanup — verified
docker rm -f voicebox-spike && docker rmi voicebox-permissive:spike
```

The two measurement harnesses (`measure_voicebox.py`, `measure_wer.py`) were written in the session
scratchpad rather than committed: they target a container that no longer exists, and
`measure_wer.py` is a thin driver over the repo's existing `examples/voice-pipecat/tests/wer.py`.
**If option A or B advances, the right move is to fold them into
`examples/voice-pipecat/` as a second backend for the existing lane** rather than re-create them —
they are ~60 lines each and reproduced in behaviour by the commands above.

## 9. See also

- `docs/research/voice-pipeline-landscape-2026-07-23.md` — the landscape this spike extends
- `docs/decisions/0066-openai-compat-shim-voice-gateway.md` — the shim (the LLM leg; the
  `Server-Timing` and bounded-input recommendations land here)
- `docs/decisions/0069-voice-testing-lane-wer-roundtrip.md` — the WER lane, fixtures and thresholds
  reused for the STT measurement
- `examples/voice-pipecat/README.md` — the 3543 ms baseline this compares against
- `.claude/rules/cos/license-policy.md`, `.claude/rules/constitutional-gates.md` — the gates §2 applies
