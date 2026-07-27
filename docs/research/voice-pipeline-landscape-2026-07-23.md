---
type: Research Report
title: "Voice pipeline landscape — can aisotropy speak? (STT/TTS/telephony, mid-2026)"
description: Code-grounded assessment of aisotropy's voice support (none today — verified against the API surface, not the docs) and of the standard voice-agent sandwich (STT → LLM runtime → TTS) it is well positioned to power as the brain. Covers Pipecat, Whisper, Kokoro-82M/Kokoro-FastAPI, Chatterbox, F5-TTS, ElevenLabs and Asterisk telephony, each with verified license posture against the repo's MIT/Apache-only gate. Conclusion: the core needs zero audio code — a voice gateway consumes the existing SSE stream; the one shim worth building in-repo is the OpenAI-compatible endpoint already ranked "#1.5" by the frontend-integration report.
tags: [research, integration, voice, tts, stt, pipecat, whisper, kokoro, chatterbox, f5-tts, elevenlabs, asterisk, telephony, adoption, licenses]
timestamp: 2026-07-23T00:00:00Z
---

# Voice pipeline landscape — can aisotropy speak?

> **Outcome addendum (same day, 2026-07-23).** This report's recommendations were executed the
> day it was written, through two SDD cycles:
> - **Move #1 shipped**: the OpenAI-compat shim is implemented and adversarially verified —
>   [ADR-0066](../decisions/0066-openai-compat-shim-voice-gateway.md) (Accepted),
>   `internal/api/openai_compat.go`.
> - **Move #2 shipped as code + test lane**: the Pipecat PoC lives at `examples/voice-pipecat/`
>   and its hardware-free, WER-gated test lane is
>   [ADR-0069](../decisions/0069-voice-testing-lane-wer-roundtrip.md) (Accepted) — measured
>   round-trip WER 0.000–0.100 (Kokoro af_heart + whisper base.en), thresholds 0.20/0.15.
> - The "no latency numbers exist yet" caveat below is partially closed: stub-lane
>   TTFT/barge-in are measured; full-stack numbers with real STT/TTS models remain pending an
>   idle-machine run.
> - Still true as written: the CORE has no audio code (by design — that was the
>   recommendation), telephony/Asterisk remains deferred, F5-TTS official weights remain barred.
> - **Audio-backend spike (2026-07-27)**: [voicebox](../06-Daily/reports/voicebox-spike-2026-07-27.md)
>   evaluated as a single-process replacement for the STT+TTS legs — measured TTS first-audio-byte
>   **410.7 ms** warm vs this stack's re-measured 1647 ms (~4x short text, ~1.2x long — an initial
>   8.6x was corrected same-day after re-measuring the baseline native) and STT WER 0.000 on the
>   ADR-0069 fixtures, but its
>   README's "purely permissive" recipe is incomplete (the Kokoro path pulls GPL-3.0
>   `phonemizer-fork`); recommendation is cherry-pick the patterns, not adopt.
> The body below is the original analysis, unmodified.

> **Purpose.** Answer, code-grounded: does aisotropy support voice (speech in, speech out,
> telephony)? And if not: what is the correct architecture to add it, with which pieces, and what
> does the repo's [license gate](../../.claude/rules/cos/license-policy.md) allow?
>
> **Method.** Repo-wide grep for voice/audio surface (command below); read of the API package
> file list (`internal/api/`); web verification of every license claim for the external pieces
> (see [References](#references)). External claims are cited to the canonical repo/docs — the
> voice frameworks were **not** cloned and source-audited at the depth of the
> [frontend landscape report](frontend-ui-integration-landscape.md); maturity labels follow
> [capability-absence-claims](../../.claude/rules/capability-absence-claims.md).

## TL;DR

**aisotropy has no voice support today — and it should not grow any in the core.**

Evidence (the absence claim, executable):

```bash
git grep -rin -E 'elevenlabs|kokoro|chatterbox|f5|pipecat|whisper|asterisk|\btts\b|\bstt\b|speech|audio|voice' -- internal/ aisotropy/ cmd/
```

Zero real hits (only false positives: `#f5f6f8` CSS color, "voice" as the bot's *persona voice*
in `cmd/aisotropy/templates/main.go.tmpl`, "transcript" as *text chat* transcript). The full API
surface (`internal/api/`) is text-in/text-out: `chat`, `chat/stream` (SSE), `chat/agent`, the
AI-SDK adapter, AG-UI, MCP. No audio endpoint, no byte ingest, no multimodal contract. Level on
the enforcement ladder: **L0 — no concept** (self-applied, symmetric rigor).

**The correct architecture is the standard voice-agent sandwich, with aisotropy as the brain:**

```
Phone/Mic → Asterisk (SIP/ARI) or WebRTC/WebSocket
   → STT (Whisper / faster-whisper)
      → aisotropy POST .../chat/stream   (SSE, token-by-token)
   → TTS (Kokoro-FastAPI / Chatterbox / ElevenLabs / …)
→ audio back to the caller
```

The core needs **zero audio code** for this. Ranked adoption path:

| # | Move | Unlocks | Effort | Where |
|---|---|---|---|---|
| **1** | **OpenAI-compatible `/v1/chat/completions` shim** (already ranked "#1.5" by the [frontend report](frontend-ui-integration-landscape.md#tldr--the-ranked-answer)) | **Pipecat out of the box** (`OpenAILLMService` + `base_url`), plus Open WebUI / LibreChat — one shim, two audiences | S | in-repo, `internal/api` |
| **2** | **Pipecat gateway PoC** (Python): Whisper STT + Kokoro-FastAPI TTS + aisotropy as LLM service | End-to-end local voice agent, barge-in, VAD — all frameworks' plumbing for free | S–M | out-of-repo example/`examples/` |
| 3 | **Go-native voice gateway** importing `aisotropy.App` in-process | No Python hop; single-binary voice bot; showcases the adoption surface | M | separate module |
| 4 | **Telephony leg**: Asterisk (ARI) or a CPaaS in front of #2 | Real phone calls (PSTN/SIP) | M, needs ADR (GPLv2 exception) | separate service |

## Why aisotropy is already voice-shaped (without knowing it)

Three properties the voice use case needs are already load-bearing in the core:

1. **Token-by-token SSE** (`internal/api/chat_stream.go`) — incremental TTS (synthesize
   sentence-by-sentence as tokens arrive) is what determines perceived latency in voice. A
   runtime that only returns complete responses is disqualified for voice; aisotropy is not.
2. **Cancellation by `context`** — the [constitutional gate](../../.claude/rules/constitutional-gates.md)
   requires that a cancelled SSE request stop the upstream LLM call immediately. That is
   exactly **barge-in** (the user starts talking over the bot): the voice orchestrator's VAD
   fires, it cancels the in-flight request, generation stops. Voice frameworks implement
   interruption handling on their side; they need the LLM backend to honor cancellation —
   aisotropy does, by constitutional requirement rather than by accident.
3. **Importable Go package** (`aisotropy.App`, the [adoption surface](../architecture/adoption-surface.md))
   — a Go voice gateway can embed the runtime in-process instead of hopping HTTP.

Multi-tenancy carries over untouched: the voice gateway is just another client of
`/v1/tenant/{tenant_id}/user/{user_id}/chat/stream`; identity still travels via path → ctx.

## The pieces, verified

| Piece | Role | License (verified) | Gate verdict | Maturity for this use |
|---|---|---|---|---|
| [Pipecat](https://github.com/pipecat-ai/pipecat) | Voice-agent orchestrator: pipeline of frames, VAD, turn-taking, barge-in, transport (WebRTC/WebSocket/telephony). aisotropy plugs in as the LLM service | BSD-2-Clause | ✅ | NATIVE path for custom LLMs: [`OpenAILLMService` with `base_url`](https://docs.pipecat.ai/server/services/llm/openai) pointed at any OpenAI-compatible endpoint — the documented pattern its own Groq/Ollama/Google services use |
| [Whisper](https://github.com/openai/whisper) / [faster-whisper](https://github.com/SYSTRAN/faster-whisper) | STT (local) | MIT / MIT | ✅ | Industry default for self-hosted STT |
| [Kokoro-82M](https://huggingface.co/hexgrad/Kokoro-82M) + [Kokoro-FastAPI](https://github.com/remsky/Kokoro-FastAPI) | TTS, lightweight local (82M params), served with an OpenAI-compatible `/v1/audio/speech` | Apache-2.0 (weights **and** wrapper) | ✅ | Best fit for a self-hosted default: small, fast, license-clean |
| [Chatterbox](https://github.com/resemble-ai/chatterbox) (Resemble AI) | TTS local, zero-shot voice cloning, emotion control | MIT ([LICENSE](https://github.com/resemble-ai/chatterbox/blob/master/LICENSE)) | ✅ | Heavier than Kokoro; brings cloning if needed |
| [F5-TTS](https://github.com/swivid/f5-tts) | TTS, zero-shot | ⚠️ **Code MIT, official weights CC-BY-NC-4.0** (Emilia training data; [discussion #997](https://github.com/SWivid/F5-TTS/discussions/997)) — NC survives fine-tuning | 🚫 for SaaS (weights) | Use [OpenF5-TTS-Base](https://huggingface.co/mrfakename/OpenF5-TTS-Base) (Apache-2.0 retrain) if the architecture is wanted commercially |
| [ElevenLabs](https://elevenlabs.io) | TTS cloud, top quality | Proprietary paid API | ✅ (service, not dependency) | No copyleft risk; vendor lock + per-char cost + audio leaves the perimeter |
| [Asterisk](https://www.asterisk.org) | Telephony (PSTN/SIP PBX); bots attach via [ARI](https://docs.asterisk.org/Asterisk_22_Documentation/API_Documentation/Asterisk_REST_Interface/) | ⚠️ **GPLv2** | Allowed **only** under the [license-policy exception](../../.claude/rules/cos/license-policy.md#exceptions): separate service, unmodified, public API (ARI/SIP), documented + operator-approved → **requires an ADR** | The self-hosted telephony standard; CPaaS (Twilio et al.) is the no-GPL alternative |
| LangGraph | **Not a voice piece** — it is the arena competitor ([competitive landscape](competitive-landscape.md)). Neither it nor aisotropy ships voice natively; voice frameworks like Pipecat consume either as an LLM/agent backend the same way | MIT | — | Included here only to answer "does LangGraph change the picture?" — it doesn't |

## The one thing worth building in-repo: the OpenAI-compat shim

The [frontend landscape report](frontend-ui-integration-landscape.md) already identified an
OpenAI-compatible `/chat/completions` shim as a near-zero-cost "#1.5" for Open WebUI/LibreChat.
This report **upgrades that recommendation**: the same shim is the NATIVE integration point for
Pipecat (and for most voice orchestrators, which standardize on the OpenAI wire format for LLM
backends). One adapter, two ecosystems (self-hosted chat UIs + voice agents).

Two design notes, both already learned elsewhere in this repo:

- **Chat Completions only, never the Responses API** — the exact compatibility-spectrum lesson
  of [openai-compat-boundary](openai-compat-boundary-2026-07-17.md). Pipecat's `base_url`
  pattern targets classic `/v1/chat/completions`; that is the stable contract tier.
- **Identity mapping is the real design question, not the wire format.** The OpenAI shape has no
  tenant/user path params. The shim must map identity from the auth layer (JWT claims →
  tenant/user, per [ADR-0024](../decisions/0024-authenticated-identity-seam.md)) — never from a body field
  (constitutional gate: `tenant_id` via validated boundary → ctx only).

## Recommendation

1. **Do not add audio to the core.** The sandwich architecture is the industry norm for a
   reason; aisotropy's differentiators (isolation, tiered memory, gates) are all
   modality-agnostic and carry over for free.
2. **Build the OpenAI-compat shim** (move #1) as the enabling adapter — it was already on the
   roadmap-adjacent list for frontend reasons.
3. **Prove the sandwich with a PoC** (move #2): Pipecat + faster-whisper + Kokoro-FastAPI +
   aisotropy `chat/stream`, all local, all MIT/Apache/BSD. Measure: time-to-first-audio-byte and
   barge-in-to-silence latency (the two numbers that matter in voice).
4. **Defer telephony** (move #4) until the PoC proves the loop; when it lands, Asterisk needs
   the license-exception ADR, or a CPaaS sidesteps GPL entirely.
5. **Default TTS stack**: Kokoro-FastAPI (license-clean, light). Chatterbox as the cloning
   option. **F5-TTS official weights are barred for commercial use** — if its quality is wanted,
   the Apache-2.0 OpenF5 retrain is the only clean path. ElevenLabs as the managed-quality tier.

## Honest caveats

- The voice frameworks were **not cloned and source-audited**; contract claims (Pipecat's
  `base_url` pattern, Kokoro-FastAPI's OpenAI-style audio endpoint) come from their official
  docs/READMEs, labeled per the ecosystem-sweep rule. Before implementation, the Pipecat LLM
  service contract deserves a source read (`src/pipecat/services/openai/`).
- Whisper/faster-whisper/Asterisk license claims cite canonical sources; they were not
  re-verified against a fresh fetch today (low drift risk, flagged for completeness).
- No latency numbers exist yet — "SSE is voice-shaped" is an architectural argument, not a
  measurement. The PoC's two metrics (first-audio-byte, barge-in-to-silence) are what would
  make it evidence, per [evidencia ejecutable](../../.claude/rules/cos/RULES-COMPACT.md).
- ElevenLabs "top quality" is reputational consensus, not a benchmark run here.

## References

- Pipecat: [repo](https://github.com/pipecat-ai/pipecat) (BSD-2), [OpenAI-compatible LLM docs](https://docs.pipecat.ai/server/services/llm/openai)
- Whisper: [openai/whisper](https://github.com/openai/whisper) (MIT); faster-whisper: [SYSTRAN/faster-whisper](https://github.com/SYSTRAN/faster-whisper) (MIT)
- Kokoro: [hexgrad/Kokoro-82M](https://huggingface.co/hexgrad/Kokoro-82M) (Apache-2.0), [remsky/Kokoro-FastAPI](https://github.com/remsky/Kokoro-FastAPI) (Apache-2.0)
- Chatterbox: [resemble-ai/chatterbox](https://github.com/resemble-ai/chatterbox), [LICENSE (MIT)](https://github.com/resemble-ai/chatterbox/blob/master/LICENSE)
- F5-TTS: [SWivid/F5-TTS](https://github.com/swivid/f5-tts) (code MIT), [weights CC-BY-NC discussion #997](https://github.com/SWivid/F5-TTS/discussions/997), [HF discussion on commercial use](https://huggingface.co/SWivid/F5-TTS/discussions/7); Apache alternative: [OpenF5-TTS-Base](https://huggingface.co/mrfakename/OpenF5-TTS-Base)
- Asterisk: [asterisk.org](https://www.asterisk.org) (GPLv2), [ARI docs](https://docs.asterisk.org/Asterisk_22_Documentation/API_Documentation/Asterisk_REST_Interface/)
- In-repo siblings: [frontend-ui-integration-landscape](frontend-ui-integration-landscape.md), [openai-compat-boundary-2026-07-17](openai-compat-boundary-2026-07-17.md), [competitive-landscape](competitive-landscape.md)
