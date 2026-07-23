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
| `measure_ttfab.py` | **Metric #1**: time-to-first-audio-byte (LLM TTFT + TTS first byte) |
| `measure_bargein.py` | **Metric #2**: barge-in-to-silence (cancel → stream torn down) |
| `requirements.txt` | Python deps (own venv; this dir is never part of the Go module) |

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

### 2. A tenant JWT (this is the OpenAI `api_key`)

```bash
cd examples/voice-pipecat
export AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice)
```

### 3. Python environment

```bash
python3 -m venv .venv && . .venv/bin/activate
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

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `AISOTROPY_OPENAI_BASE_URL` | `http://localhost:8080/v1` | shim base URL (SDK appends `/chat/completions`) |
| `AISOTROPY_TENANT_JWT` | — (required) | tenant JWT, passed as the OpenAI api_key |
| `KOKORO_BASE_URL` | `http://localhost:8880/v1` | Kokoro-FastAPI base URL |
| `KOKORO_VOICE` | `af_heart` | Kokoro voice |
| `WHISPER_MODEL` | `distil-medium.en` | faster-whisper model name |

## Honest status (what has actually been executed)

- The **shim** itself is fully tested in Go (`internal/api/openai_compat_test.go`),
  including a round-trip through this repo's own OpenAI-compat client and the
  adversarial identity suite (no JWT → 401; NoAuth server → still 401; body
  cannot influence tenant/user).
- The **LLM leg of this PoC** (openai-SDK-shaped streaming against a live
  `aisotropyd` running the stub LLM client, JWT as api_key) was smoke-tested at
  implementation time — transcript in the ADR-0066 apply report.
- `bot.py` and the STT/TTS legs require local audio hardware and model
  downloads (faster-whisper weights, the Kokoro container), which the
  implementation environment did not have: they are complete, syntax-checked
  code but have NOT been executed end-to-end here. Run steps 1–5 above to
  produce real numbers; do not quote any until you have.
