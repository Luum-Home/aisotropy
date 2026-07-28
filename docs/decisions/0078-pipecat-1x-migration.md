---
type: ADR
title: "ADR-0078 — examples/voice-pipecat migrates from pipecat 0.0.108 to the 1.x line"
description: >
  The voice PoC's `pipecat-ai[silero,whisper,local]>=0.0.108,<0.1` ceiling existed for one
  concrete reason — bot.py and the file-based test lane both used `OpenAILLMContext` +
  `llm.create_context_aggregator()`, which pipecat 1.x removed. 0.0.108 itself was emitting the
  deprecation that names the replacement, so the ceiling was debt with an expiry date, not a
  design choice. This ADR migrates to `>=1.6,<2` after reading the 1.6.0 source rather than its
  changelog, records the four API moves that were larger than the advertised five call sites
  (VAD left the transport, `allow_interruptions` left PipelineParams, PipelineTask/PipelineRunner
  became PipelineWorker/WorkerRunner, and the default end-of-turn strategy became a smart-turn
  model), answers the open question about `voicebox_tts.py` with a NO, and fixes an
  Apple-Silicon-only import regression the migration exposed in the ADR-0077 revert switch.
tags: [decisions, adr, voice, pipecat, examples, dependency-upgrade, license-policy, capability-claims]
timestamp: 2026-07-28T00:00:00Z
supersedes: []
revisits: [0066, 0069, 0077]
---

# ADR-0078 — `examples/voice-pipecat` migrates from pipecat 0.0.108 to the 1.x line

## Status

**✅ Accepted (2026-07-28).** `examples/voice-pipecat/requirements.txt` moves from
`pipecat-ai[silero,whisper,local]>=0.0.108,<0.1` to `>=1.6,<2`. The ADR-0074 capability claim
`pipecat-pinned-pre-1.0` is **INVERTED, not deleted** → `pipecat-pinned-1x`, following the
`hydration-sequential` → `hydration-parallel` precedent of [ADR-0076](0076-as-built-deltas-parallel-hydration-shutdown-join.md).

Scope is `examples/` only. **The aisotropy core imports no pipecat and contains no audio code**;
nothing in `internal/`, `cmd/` or `aisotropy/` changed.

## Context

### The ceiling was debt, and it announced its own expiry

The `<0.1` ceiling was not a considered pin. `requirements.txt` said exactly why it existed:

> Still <1.0: pipecat 1.x removed `openai_llm_context`, which bot.py and the lane both use.

That is a true statement about a real blocker, but it describes **five call sites**, not an
architectural incompatibility. And 0.0.108 was already printing the migration instruction at
runtime, naming its own replacement:

> `OpenAIAssistantContextAggregator (likely created with create_context_aggregator()) is
> deprecated ... Use the universal LLMContext and LLMContextAggregatorPair instead.`

Staying on a line whose successor has been out for six minor versions, while the library itself
tells you the replacement on every run, is the kind of debt that only gets more expensive. Upstream
is at **1.6.0** and requires Python `>=3.11`; the example's venv was already 3.11.15, so the
interpreter was never the obstacle.

### The method: read the source, not the changelog

Per `.claude/rules/dependency-reuse-first.md`, the 1.x surface was **inventoried from the installed
source before any code changed** — `pipecat-ai==1.6.0` installed `--no-deps` into a throwaway venv
(the example's own `.venv` had to keep working until the migration was proven), then read. Every
API claim below cites `file:line` in that source.

This mattered. The dispatch estimated the migration surface at five call sites. Reading found
**four additional moves** that a changelog skim would have missed, three of which would have failed
at runtime rather than at import.

## Decision

### The API deltas found in the 1.6.0 source

| 0.0.108 | 1.6.0 | Evidence |
|---|---|---|
| `processors.aggregators.openai_llm_context.OpenAILLMContext` | `processors.aggregators.llm_context.LLMContext` | module deleted; `llm_context.py:95` |
| `llm.create_context_aggregator(ctx)` | `LLMContextAggregatorPair(ctx, ...)` | **zero** `def create_context_aggregator` remain in the package; `llm_response_universal.py:2131` |
| `TransportParams(vad_analyzer=...)` | `LLMUserAggregatorParams(vad_analyzer=...)` | field gone from `base_transport.py:65-89`; now `llm_response_universal.py:173` |
| `PipelineParams(allow_interruptions=True)` | *(removed)* — interruptions are a turn-strategy property, **default on** | zero hits package-wide; `user_start/base_user_turn_start_strategy.py:56` |
| `PipelineTask` / `PipelineRunner` | `PipelineWorker` / `WorkerRunner` | `pipeline/task.py` and `pipeline/runner.py` are deprecation shims (1.3.0, removal 2.0.0) |
| `PipelineParams` in `pipeline.task` | moved to `pipeline.worker` (re-exported) | `pipeline/worker.py:137` |
| default end-of-turn = VAD silence | default = `TurnAnalyzerUserTurnStopStrategy(LocalSmartTurnAnalyzerV3)` | `turns/user_turn_strategies.py:43-51` |
| `model=` on STT/LLM services | `settings=Service.Settings(model=...)` | deprecated 0.0.105, removal 2.0.0; `openai/stt.py:89-91` |

**VAD moving downstream of STT looked like a break and is not.** In 1.x the analyzer sits on the
user context aggregator, which is *after* the STT service in the pipeline — yet `SegmentedSTTService`
still segments on `VADUser*SpeakingFrame` (`stt_service.py:788-796`). The aggregator emits those
with `broadcast_frame`, which pushes **upstream and downstream** (`frame_processor.py:761-777`). The
frames reach the STT service behind them. This was verified by reading, then confirmed by the lane.

**The turn-taking default was deliberately NOT adopted.** 1.x's out-of-the-box end-of-turn is a
semantic smart-turn ONNX model (shipped inside the wheel; `onnxruntime` is now a base dependency).
That is a genuine upgrade, but adopting it is a **behavior change, not a migration**, so
`audio_backend.make_context_aggregators` pins `SpeechTimeoutUserTurnStopStrategy()` — which fires on
`VADUserStoppedSpeakingFrame` (`user_stop/speech_timeout_user_turn_stop_strategy.py:163`) and
therefore reproduces 0.0.x's behavior exactly. Adopting smart-turn is a separate, measurable
decision; it is not smuggled in under a version bump.

### `voicebox_tts.py` stays — the answer is NO

The dispatch asked whether 1.x made pipecat's TTS voice list overridable, which would have made
ADR-0077's ~40 lines of explicit HTTP unnecessary. **It did not.** `VALID_VOICES` is still a closed
dict of the 13 OpenAI voice ids (`services/openai/tts.py:49-63`), still consulted inline in
`run_tts` (`tts.py:251`, `tts.py:262`), still with no override seam.

1.x improved the **failure mode** without touching the **constraint**: 0.0.108 raised a bare
`KeyError` out of the `VALID_VOICES[...]` subscript; 1.6.0 checks membership first and yields a
named `ErrorFrame` naming the voice and listing the valid set. Better diagnostics, same refusal —
`af_heart` remains unsendable, and voicebox still forwards `voice` straight to Kokoro with no
OpenAI-name mapping. `voicebox_tts.py`'s docstring was re-derived against 1.6.0 line numbers rather
than left citing 0.0.108's.

The other ADR-0077 dependency **did** survive intact: `OpenAISTTService` still accepts `base_url`
(`openai/stt.py:76` → `AsyncOpenAI(api_key, base_url)` at `whisper/base_stt.py:228`), and the
`TTSService.run_tts(self, text, context_id)` signature `voicebox_tts.py` implements is unchanged
(`tts_service.py:487`).

### An Apple-Silicon import regression the migration exposed

`VOICE_AUDIO_BACKEND=kokoro-fastapi` is ADR-0077's revert switch, and that ADR's premise is that
leaving voicebox is **one variable, not a code edit**. On 1.x that switch was silently broken on the
primary development platform.

In 0.0.108 both dependency guards in `services/whisper/stt.py` sat under `if TYPE_CHECKING:`
(lines 29-42) — **never executed at runtime**. 1.6.0 runs them at module scope and hard-requires
`mlx_whisper` when `platform.system()=="Darwin" and platform.machine()=="arm64"` (`stt.py:41-48`),
even for the plain faster-whisper `WhisperSTTService`. `mlx_whisper` lives in a *different* extra
(`[mlx-whisper]`), so `pipecat-ai[whisper]` alone no longer imports the service on an M-series Mac.

This was **verified in both directions rather than assumed**: the pre-migration venv was kept on
disk and `from pipecat.services.whisper.stt import WhisperSTTService` succeeds on 0.0.108 and raises
`ImportError: Missing module: No module named 'mlx_whisper'` on 1.6.0, on the same machine.

Fix: `mlx-whisper~=0.4.2; sys_platform == "darwin" and platform_machine == "arm64"` in
`requirements.txt`. **Marked, not unconditional** — it pulls `mlx` + `torch`, and on Linux/Intel the
1.6.0 guard does not fire at all, so nothing should be added there. License checked before adding,
per the repo's hard gate: mlx-whisper is **MIT**. The default voicebox backend never reaches this
code path (it uses `OpenAISTTService` over HTTP).

### Requirements hygiene the migration made possible

Three pins in the requirements files were **claims that the 1.x package now guarantees itself**, and
a requirement that restates a guarantee is a lie waiting to rot:

- `websockets>=13` in `requirements-test.txt` worked around 0.0.x importing `websockets` at module
  scope without any extra pulling it in. On 1.x `websockets>=13.1` is an unconditional dependency of
  `pipecat-ai`. **Removed**, with the reason kept in place of the line.
- `openai` and `onnxruntime` likewise became base dependencies.
- The `[silero]` extra is now **declared but empty** (its only dependency, `onnxruntime`, was
  promoted to base). It is kept anyway, for intent — and the comment says so, so a future reader does
  not "discover" it is redundant and delete the signal.

The **GPL boundary of ADR-0077 is untouched**: nothing GPL is pip-installed, `kokoro-onnx` /
`piper-tts` / `pipecat-ai[kokoro]` still appear nowhere, and the
`voice-audio-gpl-stays-in-the-container` guard claim is still green at 0.

## Verification

Every row below is real output from this machine, not a claim.

| # | Check | Result |
|---|---|---|
| 1 | `examples/voice-pipecat/venv-preflight.sh` after rebuilding the venv on 1.6.0 | `✅ python 3.11.15, test floor and voice-lane imports all present` (exit 0) |
| 2 | Skip lane — `pytest tests/ -q`, no `VOICE_LANE` | **8 passed, 8 skipped** in 0.02s |
| 3 | **Real lane** — `VOICE_LANE=real`, voicebox container + live `cmd/aisotropyd` | **16 passed** in 39.96s |
| 3b | The headline file-based pipeline test, isolated | `test_pipeline_wav_in_tts_frames_out PASSED` in 43.15s |
| 4 | `python3 -m py_compile` on all 4 changed `.py`; `bash -n venv-preflight.sh` | clean |
| 5 | `scripts/gap-claims-guard.sh` before / after | `17 ok / 0 drifted` → `17 ok / 0 drifted`, `pipecat-pinned-1x` green |

**16 passed is the same count as the pre-migration baseline** recorded in ADR-0077 (16 passed in
103.54s on 0.0.108) — the migration neither lost nor gained coverage, and the file-based pipeline
test still drives WAV → voicebox STT → aisotropy shim LLM → voicebox TTS frames end to end.

The three warnings the real lane emits are all **internal to pipecat**, none from this repo's code:
stdlib `audioop` deprecation on 3.11, pipecat's own `AudioContextTTSService` alias, and
`SpeechTimeoutUserTurnStopStrategy` overriding a `reset` method pipecat deprecated in 1.6.0. The
`model=` deprecation the first migration cut *did* emit from our own code was closed by moving all
four service constructions to the canonical `settings=` form — safe because `apply_update` is a
delta merge that leaves unset fields at the service's defaults (`services/settings.py:226-231`),
confirmed by asserting `language` survives.

Everything ran under `scripts/with-heavy-lane.sh` on a machine the guard classified **DEGRADED**
(swap 92-95%, free RAM 129-224 MB) with concurrent sessions active.

### What was NOT verified

- **`bot.py` has never been executed** — it needs `portaudio`, which is absent on this machine.
  That is pre-existing (ADR-0069 already recorded the mic/speaker path as unrun) and unchanged by
  this migration. Every 1.x symbol `bot.py` imports *except* `LocalAudioTransport` was imported and
  constructed successfully, and `LocalAudioTransport`'s failure was reproduced explicitly
  (`ImportError: Missing module: No module named 'pyaudio'`) rather than assumed.
- **The `mlx-whisper` line was not installed.** Its resolution on this platform was proven with
  `pip install --dry-run` (would install `mlx-0.32.0`, `mlx-metal-0.32.0`, `mlx-whisper-0.4.3`,
  `torch-2.13.0`), but the multi-GB install was **not** performed on a machine at ~93% swap. The
  consequence is stated plainly: **the `kokoro-fastapi` fallback backend has not been executed on
  1.x.** The default voicebox path is fully covered; the fallback's STT leg is code-read and
  dry-run-resolved only.
- The `[local]` extra is deliberately absent from the test lane's install (it was on 0.0.108 too),
  so the lane never depended on portaudio and still does not.

## Consequences

- The example tracks a **supported** pipecat line and no longer accumulates deprecation debt.
- The `<2` ceiling is real, not decorative: pipecat 2.0.0 is documented to remove every deprecated
  alias this migration leans on (`PipelineTask`, `PipelineRunner`, the `model=` constructor
  arguments). `pipecat-pinned-1x`'s `on_close` note says the ceiling must not be widened without
  re-reading the 2.x source the way this ADR read 1.6.0's.
- Turn-taking behavior is **unchanged on purpose**. Adopting 1.x's smart-turn default is a live,
  separate decision with a measurable outcome (end-of-turn latency and false-cut rate), and it now
  has a one-line seam in `audio_backend.make_context_aggregators`.
- `audio_backend.py` gains `make_context_aggregators`, extending the module's stated job — the
  place `bot.py` and the ADR-0069 lane build the **same** chain. In 0.0.x the context construction
  was a single provider-owned call with nothing to share; in 1.x it carries the VAD analyzer and the
  turn policy, which is exactly the kind of configuration that drifts between a bot and its test.
- The revert switch of ADR-0077 stays honest on Apple Silicon — at the cost of a marked, MIT,
  multi-GB optional dependency that this machine did not install.

## Revisit triggers

- pipecat **2.0.0** ships → the `<2` ceiling becomes the next instance of exactly this ADR.
- Upstream restores the pre-1.x behavior of `services/whisper/stt.py`'s mlx guard (or moves it back
  under `TYPE_CHECKING`) → the `mlx-whisper` marker line can be deleted; re-verify, do not assume.
- A measured comparison of `SpeechTimeoutUserTurnStopStrategy` vs `LocalSmartTurnAnalyzerV3` on this
  PoC's fixtures → adopt or reject the 1.x default on evidence.
- `OpenAITTSService` grows an override seam for `VALID_VOICES` → `voicebox_tts.py` can be retired.
  Checked at 1.6.0 and the answer was no; check again, do not assume it stayed no.

## As shipped

`examples/voice-pipecat/{bot.py,audio_backend.py,voicebox_tts.py,requirements.txt,requirements-test.txt,venv-preflight.sh}`,
`examples/voice-pipecat/tests/test_pipeline_file.py`, `manifests/capability-claims.yaml`,
`docs/architecture/capabilities-and-gaps.md`, `docs/decisions/index.md`.
