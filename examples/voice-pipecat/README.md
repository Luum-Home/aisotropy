# Voice PoC — Pipecat over the aisotropy OpenAI-compat shim (ADR-0066)

The industry "voice sandwich" (STT → LLM → TTS) needs only an OpenAI-compatible
LLM backend. This PoC builds that sandwich with **zero audio code in the
aisotropy core**: Pipecat orchestrates local STT/TTS and points its stock
`OpenAILLMService` at aisotropy's `POST /v1/chat/completions` shim, passing a
**tenant JWT as the `api_key`**. Every voice turn runs the full aisotropy
pipeline server-side — tenant-isolated memory hydration, quota-before-spend,
guardrails, cost accounting — and interrupting the bot cancels the request,
which StreamCore turns into an immediate upstream LLM stop (barge-in as a
constitutional gate).

| Piece | Tool | License | Runs where |
|---|---|---|---|
| Orchestration | [Pipecat](https://github.com/pipecat-ai/pipecat) | BSD-2 | this process |
| VAD | Silero (via `pipecat-ai[silero]`) | MIT | this process |
| STT | faster-whisper (via `pipecat-ai[whisper]`) | MIT | this process (local model) |
| LLM | **aisotropy shim** (`/v1/chat/completions`) | — | `aisotropyd` |
| TTS | [Kokoro-FastAPI](https://github.com/remsky/Kokoro-FastAPI) | Apache-2.0 | separate container |

License notes: everything above is MIT/Apache-2.0/BSD, consistent with the
repo's license gate. F5-TTS official weights (CC-BY-NC) are deliberately NOT
used — see `docs/research/voice-pipeline-landscape-2026-07-23.md`.

## Files

| File | What it is |
|---|---|
| `bot.py` | The voice sandwich: mic → VAD → whisper STT → shim LLM → Kokoro TTS → speaker |
| `mint_jwt.py` | Dev HS256 tenant-JWT minting (stdlib only; matches `internal/auth/jwt.go`) |
| `measure_ttfab.py` | **Metric #1**: time-to-first-audio-byte (LLM TTFT + TTS first byte); `--assert-ttfab-ms N` = threshold mode, exit 1 on breach (ADR-0069) |
| `measure_bargein.py` | **Metric #2**: barge-in-to-silence (cancel → stream torn down); `--assert-bargein-ms N` = threshold mode, exit 1 on breach (ADR-0069) |
| `requirements.txt` | Python deps (own venv; this dir is never part of the Go module) |
| `requirements-test.txt` | Test-lane extras (pytest, websockets — licenses noted inline) |
| `tests/` | The voice testing lane (ADR-0069): WER round-trip, pinned fixtures, file-based pipeline test |

## Bring-up

### 1. aisotropyd with JWT auth (the shim refuses to serve without it)

```bash
# From the repo root. The shim route requires a REAL authenticator: with
# AISOTROPY_JWT_SECRET unset the server boots deny-all (401 everywhere), and
# even a NoAuth server 401s the shim by construction (ADR-0066).
export AISOTROPY_JWT_SECRET='dev-secret-change-me-32-bytes-min!!'   # >= 32 bytes
go run ./cmd/aisotropyd
# No ANTHROPIC/OPENROUTER key needed to smoke-test: llm.FromEnv() falls back
# to the deterministic stub client. Set a real key for actual conversations.
```

> **Known issue (2026-07-23):** under go1.26.5 `cmd/aisotropyd` can deadlock at
> boot (slog CtxHandler ↔ std-log bridge recursion, `main.go:172`; tracked as a
> spawned fix task). Workaround until fixed: run a minimal main that imports the
> public façade — `aisotropy.App` registers the same shim route — which is how
> the pipeline test's live pass was produced.

### 2. A tenant JWT (this is the OpenAI `api_key`)

```bash
cd examples/voice-pipecat
export AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice)
```

### 3. Python environment

```bash
# Python <= 3.13 required: the pinned pipecat-ai 0.0.x line depends on numba,
# which does not build on 3.14 (verified 2026-07-23).
python3.13 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
# macOS: local audio needs portaudio first:  brew install portaudio
```

### 4. Kokoro-FastAPI (TTS, separate container)

```bash
docker run -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:latest   # or -gpu
```

### 5. Talk

```bash
python3 bot.py
# speak; interrupt the bot mid-reply to feel barge-in. Ctrl-C to quit.
```

First run downloads the faster-whisper model (`WHISPER_MODEL`, default
`distil-medium.en`).

## The two executable metrics (evidencia ejecutable)

No latency claims without a command. Each script prints per-run numbers and a
median; run them, paste the output:

```bash
# Time-to-first-audio-byte: shim TTFT + Kokoro first audio byte (serial floor
# of the sandwich). --skip-tts measures the LLM leg alone.
python3 measure_ttfab.py --runs 5

# Barge-in-to-silence: start a reply, cancel on the first token (what Pipecat
# does when VAD detects user speech), measure cancel -> stream torn down.
python3 measure_bargein.py --runs 5
```

Both scripts talk to the shim through the SAME client stack Pipecat uses
(`AsyncOpenAI`, `stream:true` + `stream_options.include_usage`), so their
numbers transfer to the bot.

Threshold mode (ADR-0069) turns either script into a CI-usable gate — the
measurement is unchanged; a breached median exits 1 (exit 2 stays reserved for
configuration/connectivity errors):

```bash
python3 measure_ttfab.py --runs 5 --assert-ttfab-ms 800
python3 measure_bargein.py --runs 5 --assert-bargein-ms 250
```

## The voice testing lane (ADR-0069) — no audio hardware needed

`tests/` holds the automated lane: TTS→STT round-trip gated on WER, pinned WAV
fixtures with known transcripts, and a file-based Pipecat pipeline test (WAV in
→ captured TTS frames out). It is **opt-in** — without `VOICE_LANE=real` every
model-dependent test SKIPs with an actionable reason and only the pure-stdlib
WER unit tests run (safe for any CI, zero downloads):

```bash
pip install -r requirements-test.txt        # pytest + websockets
pytest tests/ -q                            # lane off: 8 passed, 8 skipped

# The real lane needs: the Kokoro container (step 4 above; image ~several GB,
# first pull is slow) and the faster-whisper model (base.en, ~74MB,
# auto-downloads on first run into the HF cache).
# Canonical invocation — resource-guard + cross-session lock + nice
# (from the repo root; the lane also self-checks the guard and skips when
# the machine is saturated, degrading to tiny.en when it is merely tight):
VOICE_LANE=real scripts/with-heavy-lane.sh pytest examples/voice-pipecat/tests/ -q
# or directly, from this directory:
VOICE_LANE=real pytest tests/ -q

# The pipeline test additionally needs aisotropyd up with JWT auth (steps 1-2):
VOICE_LANE=real AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice) \
  pytest tests/test_pipeline_file.py -q
```

Knobs for constrained machines: `VOICE_STT_MODEL=tiny.en` (~40MB; slightly
higher WER than the base.en the thresholds were calibrated on) and
`VOICE_STT_THREADS=2`. WER thresholds and their measured grounding live in
ADR-0069 and in the test files' comments.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `AISOTROPY_OPENAI_BASE_URL` | `http://localhost:8080/v1` | shim base URL (SDK appends `/chat/completions`) |
| `AISOTROPY_TENANT_JWT` | — (required) | tenant JWT, passed as the OpenAI api_key |
| `KOKORO_BASE_URL` | `http://localhost:8880/v1` | Kokoro-FastAPI base URL |
| `KOKORO_VOICE` | `alloy` | TTS voice id — OpenAI-style, mapped server-side by Kokoro-FastAPI (see ADR-0069 §3) |
| `WHISPER_MODEL` | `distil-medium.en` | faster-whisper model name |

## Honest status (what has actually been executed)

- The **shim** itself is fully tested in Go (`internal/api/openai_compat_test.go`),
  including a round-trip through this repo's own OpenAI-compat client and the
  adversarial identity suite (no JWT → 401; NoAuth server → still 401; body
  cannot influence tenant/user).
- The **LLM leg of this PoC** (openai-SDK-shaped streaming against a live
  `aisotropyd` running the stub LLM client, JWT as api_key) was smoke-tested at
  implementation time — transcript in the ADR-0066 apply report.
- The **STT/TTS legs and the full sandwich** are now covered by the ADR-0069
  test lane and HAVE been executed end-to-end (2026-07-23): the WER round-trip
  and fixture tests passed against the real Kokoro container + faster-whisper
  base.en, and the file-based pipeline test (WAV → whisper STT → shim LLM →
  Kokoro TTS frames, via `pipecat.tests.utils.run_test`) passed against a live
  stub-backed aisotropyd — no audio hardware involved.
- `bot.py` itself (mic/speaker via `LocalAudioTransport`) still requires real
  audio hardware and has NOT been run here. The latent voice-id issue found
  while building the lane — pipecat 0.0.x's `OpenAITTSService` rejects
  non-OpenAI voice ids (`VALID_VOICES` lookup), so a Kokoro id like `af_heart`
  produced a silent error at the TTS leg — was fixed 2026-07-23: `bot.py` now
  defaults `KOKORO_VOICE` to `alloy` (an OpenAI-style id, mapped server-side by
  Kokoro-FastAPI — the same voice the ADR-0069 test lane uses). Setting
  `KOKORO_VOICE` to a raw Kokoro id still triggers the pipecat rejection.
  Run steps 1–5 above to produce real numbers; do not quote any until
  you have.
