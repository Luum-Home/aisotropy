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
| LLM | **aisotropy shim** (`/v1/chat/completions`) | — | `aisotropyd` |
| **STT + TTS** | [voicebox](https://github.com/agjs/voicebox) `v0.2.12` | MIT source, **GPL-3.0 engine deps** | separate container |

Both audio legs come from **one** voicebox container (`docker-compose.voicebox.yml`).
The previous stack — Kokoro-FastAPI (Apache-2.0) for TTS plus an in-process
faster-whisper (MIT) for STT — is still one variable away:
`VOICE_AUDIO_BACKEND=kokoro-fastapi`.

> **License note — read before changing the audio wiring.** voicebox's own source
> is MIT, but **both** of its TTS engines pull **GPL-3.0** code: `phonemizer-fork`
> (a hard, import-time dependency of `kokoro-onnx`), `piper-tts`, and system
> espeak-ng. Upstream's documented "purely permissive image" recipe does **not**
> remove that — verified by building it. voicebox is adopted under
> `license-policy.md` §Exceptions as a **separate, unmodified, HTTP-only**
> service, formalized condition-by-condition in
> [ADR-0077](../../docs/decisions/0077-voicebox-gpl-exception-audio-backend.md).
> **Nothing GPL is pip-installed** — the container boundary is the whole point.
> Vendoring voicebox's source, shipping its image in an aisotropy artifact,
> patching it in place, or adding `kokoro-onnx`/`pipecat-ai[kokoro]` to
> `requirements*.txt` **voids the exception**.

F5-TTS official weights (CC-BY-NC) are deliberately NOT used — see
`docs/research/voice-pipeline-landscape-2026-07-23.md`.

## Files

| File | What it is |
|---|---|
| `bot.py` | The voice sandwich: mic → VAD → STT → shim LLM → TTS → speaker |
| `audio_backend.py` | **The backend switch** (`VOICE_AUDIO_BACKEND`) + the pipecat STT/TTS constructors `bot.py`, the metric scripts and the test lane all share |
| `voicebox_tts.py` | pipecat TTS service for voicebox's `/v1/audio/speech` — and the file:line reasons pipecat's stock `OpenAITTSService` cannot drive it |
| `docker-compose.voicebox.yml` | The voicebox container: pinned to upstream tag `v0.2.12`, built from the unmodified clone, loopback-only, no volumes |
| `mint_jwt.py` | Dev HS256 tenant-JWT minting (stdlib only; matches `internal/auth/jwt.go`) |
| `measure_ttfab.py` | **Metric #1**: time-to-first-audio-byte (LLM TTFT + TTS first byte); `--assert-ttfab-ms N` = threshold mode, exit 1 on breach (ADR-0069) |
| `measure_bargein.py` | **Metric #2**: barge-in-to-silence (cancel → stream torn down); `--assert-bargein-ms N` = threshold mode, exit 1 on breach (ADR-0069) |
| `measure_wer.py` | **Metric #3**: per-case WER for the round-trip sentences and the pinned fixtures — prints the numbers the lane only asserts a bound on (ADR-0077) |
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

> **Fixed (2026-07-23, same day):** under go1.26.5 `cmd/aisotropyd` deadlocked at
> boot (log↔slog bridge cycle: the o11y CtxHandler wrapped the stdlib's implicit
> default handler, which writes through the `log` package that `slog.SetDefault`
> had pointed back at it). The base is now an explicit stderr TextHandler;
> boot + `/healthz` + the shim's 401 verified live post-fix.

### 2. A tenant JWT (this is the OpenAI `api_key`)

```bash
cd examples/voice-pipecat
export AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice)
```

### 3. Python environment

```bash
# Python >= 3.9, < 3.14: the pinned pipecat-ai 0.0.x line depends on numba,
# which does not build on 3.14 (verified 2026-07-23). Any interpreter in that
# range works — 3.11 is what this was last rebuilt with.
python3.11 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
# macOS: local audio needs portaudio first:  brew install portaudio
```

**Check the venv before you use it** — a virtualenv is a machine-local artifact
that rots silently while the repo stays green:

```bash
./venv-preflight.sh
```

On 2026-07-27 this example failed with a bare
`no such file or directory: .venv/bin/python` because Homebrew had removed
`python@3.13`, leaving the venv's interpreter a **dangling symlink** — `ls`
still showed the file. That error names neither the cause nor the fix. The
preflight does: it distinguishes *missing* / *dangling* / *wrong version* /
*incomplete install*, prints the exact recreate commands, and treats "skip-lane
usable but the real lane's packages are absent" as a warning (exit 0) rather
than a failure, because the offline WER unit tests still run in that state.
A rotted venv is not a test failure and must not look like one.

### 4. The audio backend (STT + TTS, separate container)

**Default — voicebox.** It is built from the **unmodified** upstream clone, so
you need that clone once (it lands in the gitignored `external/`, i.e. it is
never repo content — that is part of what keeps the license exception valid,
[ADR-0077](../../docs/decisions/0077-voicebox-gpl-exception-audio-backend.md)):

```bash
# once, from the repo root — pinned to the exact tag this was verified against
git clone https://github.com/agjs/voicebox external/voicebox
git -C external/voicebox checkout v0.2.12          # commit 539325c

# from this directory. First build downloads + BAKES the models: ~3.2 GB image,
# a few minutes. Run it through the machine guard on a shared/loaded box:
#   scripts/with-heavy-lane.sh --need-mb 2048 docker build -t voicebox:0.2.12 external/voicebox
docker compose -f docker-compose.voicebox.yml up -d --build
curl -fsS localhost:8790/health        # {"status":"ok","models_loaded":true} in ~3s
```

Teardown leaves nothing behind (no volumes):
`docker compose -f docker-compose.voicebox.yml down`.

> Upstream's README suggests pulling `ghcr.io/agjs/voicebox:0.2.12` instead.
> That package is **not publicly pullable** (checked 2026-07-27: anonymous token
> refused, manifest 403, package page 404), which is why the build path above is
> the documented one and why the pin is the *source* tag rather than an image
> digest.

**Fallback — the previous stack**, if voicebox is unavailable or you want to
compare:

```bash
export VOICE_AUDIO_BACKEND=kokoro-fastapi
docker run -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:latest   # or -gpu
```

That backend also downloads the faster-whisper model on first run
(`WHISPER_MODEL`, default `distil-medium.en`). The voicebox backend downloads
nothing at runtime — its models are baked into the image.

### 5. Talk

```bash
python3 bot.py
# speak; interrupt the bot mid-reply to feel barge-in. Ctrl-C to quit.
```

## The three executable metrics (evidencia ejecutable)

No latency or quality claims without a command. Each script prints per-run
numbers and a median; run them, paste the output:

```bash
# Time-to-first-audio-byte: shim TTFT + backend first audio byte (serial floor
# of the sandwich). --skip-tts measures the LLM leg alone.
python3 measure_ttfab.py --runs 5

# Barge-in-to-silence: start a reply, cancel on the first token (what Pipecat
# does when VAD detects user speech), measure cancel -> stream torn down.
python3 measure_bargein.py --runs 5

# Per-case WER for the round-trip sentences and the pinned fixtures. The lane
# ASSERTS a bound; this prints the number, so a threshold can be re-derived
# from measurement instead of inherited. voicebox backend only.
python3 measure_wer.py
```

The first two talk to the shim through the SAME client stack Pipecat uses
(`AsyncOpenAI`, `stream:true` + `stream_options.include_usage`), so their
numbers transfer to the bot. `measure_wer.py` imports the lane's own sentences,
fixtures and scorer, so it can never drift from what the tests assert.

Threshold mode (ADR-0069) turns either latency script into a CI-usable gate —
the measurement is unchanged; a breached median exits 1 (exit 2 stays reserved
for configuration/connectivity errors):

```bash
python3 measure_ttfab.py --runs 5 --assert-ttfab-ms 7000
python3 measure_bargein.py --runs 5 --assert-bargein-ms 250
```

**Measured 2026-07-27 on the voicebox backend** (stub LLM, 5 runs, machine at
~95% swap under concurrent sessions — absolute values are machine-dependent):
median `llm_ttft` **6.0 ms**, median `tts_ttfab` **3441.4 ms**, median TTFAB
**3447.4 ms**; barge-in-to-silence median **0.1 ms**. That is
*indistinguishable* from the 2026-07-23 Kokoro-FastAPI record (median TTFAB
3543 ms), so the 7000 ms threshold still holds with ~2× headroom and is left
unchanged.

**Why the backend swap did not move TTFAB, measured rather than guessed:** the
stub LLM's first sentence echoes the system prompt and is **105 characters**,
and voicebox's advantage is concentrated in short sentences. Bracketed directly
against the same running container, 5 runs each: **38-char sentence → 902 ms**
median, **105-char stub sentence → 2211 ms** median. The 2026-07-27 spike
measured ~1.2–4× on the TTS leg depending on sentence length (an earlier "8.6×"
in that report is **retracted** — it compared against a stale baseline record).
A sub-second TTFAB assert only becomes meaningful with a short first sentence,
a GPU path, or sentence-chunked synthesis; calibrate against measured runs on
that stack first — an aspirational threshold fails always and gates nothing.

## The voice testing lane (ADR-0069) — no audio hardware needed

`tests/` holds the automated lane: TTS→STT round-trip gated on WER, pinned WAV
fixtures with known transcripts, and a file-based Pipecat pipeline test (WAV in
→ captured TTS frames out). It is **opt-in** — without `VOICE_LANE=real` every
model-dependent test SKIPs with an actionable reason and only the pure-stdlib
WER unit tests run (safe for any CI, zero downloads):

```bash
pip install -r requirements-test.txt        # pytest + websockets
pytest tests/ -q                            # lane off: 8 passed, 8 skipped

# The real lane needs the audio backend from step 4 (voicebox: ~3.2 GB image,
# first build is slow, models baked in — nothing downloads at test time).
# On VOICE_AUDIO_BACKEND=kokoro-fastapi it also needs the faster-whisper model
# (base.en, ~74MB, auto-downloads on first run into the HF cache).
# Canonical invocation — venv preflight, then resource-guard + cross-session
# lock + nice (from the repo root; the lane also self-checks the guard and skips
# when the machine is saturated, degrading to tiny.en when it is merely tight):
examples/voice-pipecat/venv-preflight.sh &&
  VOICE_LANE=real scripts/with-heavy-lane.sh \
    examples/voice-pipecat/.venv/bin/python -m pytest examples/voice-pipecat/tests/ -q
# or directly, from this directory:
VOICE_LANE=real pytest tests/ -q

# The pipeline test additionally needs aisotropyd up with JWT auth (steps 1-2):
VOICE_LANE=real AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice) \
  pytest tests/test_pipeline_file.py -q
```

Knobs for constrained machines (**`kokoro-fastapi` backend only** — with
voicebox the STT model is fixed server-side by `VOICEBOX_STT_MODEL`):
`VOICE_STT_MODEL=tiny.en` (~40MB; slightly higher WER than the base.en the
thresholds were calibrated on) and `VOICE_STT_THREADS=2`. WER thresholds and
their measured grounding live in ADR-0069, ADR-0077 and the test files'
comments — re-derive them with `measure_wer.py`, never loosen them silently.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `AISOTROPY_OPENAI_BASE_URL` | `http://localhost:8080/v1` | shim base URL (SDK appends `/chat/completions`) |
| `AISOTROPY_TENANT_JWT` | — (required) | tenant JWT, passed as the OpenAI api_key |
| `VOICE_AUDIO_BACKEND` | `voicebox` | `voicebox` \| `kokoro-fastapi` — selects **both** audio legs. A typo fails at boot; it never silently falls back |
| `VOICEBOX_BASE_URL` | `http://localhost:8790/v1` | voicebox base URL |
| `VOICEBOX_VOICE` | `af_heart` | a **Kokoro** voice id. Not an OpenAI name: voicebox does no name mapping (`alloy` → HTTP 500) |
| `VOICEBOX_API_KEY` | — (unset) | when voicebox runs with a key, clients send it as a bearer token |
| `KOKORO_BASE_URL` | `http://localhost:8880/v1` | Kokoro-FastAPI base URL (fallback backend) |
| `KOKORO_VOICE` | `alloy` | TTS voice id — OpenAI-style, mapped server-side by Kokoro-FastAPI (see ADR-0069 §3) |
| `WHISPER_MODEL` | `distil-medium.en` | faster-whisper model name (fallback backend only) |

## Honest status (what has actually been executed)

- The **shim** itself is fully tested in Go (`internal/api/openai_compat_test.go`),
  including a round-trip through this repo's own OpenAI-compat client and the
  adversarial identity suite (no JWT → 401; NoAuth server → still 401; body
  cannot influence tenant/user).
- The **LLM leg of this PoC** (openai-SDK-shaped streaming against a live
  `aisotropyd` running the stub LLM client, JWT as api_key) was smoke-tested at
  implementation time — transcript in the ADR-0066 apply report.
- The **STT/TTS legs and the full sandwich** are covered by the ADR-0069 test
  lane and HAVE been executed end-to-end. On the **kokoro-fastapi** backend
  (2026-07-23): 16/16 passed in 64s. On the **voicebox** backend (2026-07-27,
  after the ADR-0077 swap): **16 passed in 103.54s** via the canonical
  `with-heavy-lane.sh` invocation against a live `cmd/aisotropyd` — including
  the file-based pipeline test (WAV → voicebox STT → shim LLM → voicebox TTS
  frames, via `pipecat.tests.utils.run_test`), no audio hardware involved. The
  103.54s vs 64s difference is not a comparable measurement: the machine was at
  ~95% swap under concurrent sessions and the lane ran at `nice 19` with
  `OMP_NUM_THREADS=2`.
- **WER re-derived across the backend swap, not inherited** (2026-07-27,
  `measure_wer.py`): the STT model changed (`faster-distil-whisper-small.en`
  server-side, vs the previous in-process `base.en`) and the distribution came
  out identical — round-trip 0.000 / 0.000 / 0.000 / **0.083**, pinned fixtures
  0.000 × 3, worst case the same `"Doctor"` → `"Dr."` formatting artefact.
  Thresholds unchanged (0.20 / 0.15) because measurement says so, not because
  they were carried over.
- **Latency measured on the voicebox backend** (2026-07-27, stub LLM, 5 runs):
  median `llm_ttft` **6.0 ms**, median `tts_ttfab` **3441.4 ms**, median TTFAB
  **3447.4 ms**; barge-in-to-silence median **0.1 ms**. Honest reading: that is
  *the same* as the 2026-07-23 Kokoro-FastAPI record (3543 ms) — **the backend
  swap did not speed up this PoC's TTFAB**, because the stub's first sentence is
  105 characters and voicebox's advantage lives in short sentences (38 ch →
  902 ms vs 105 ch → 2211 ms, measured on the same container). The swap's case
  is one-container simplicity and the license question being settled explicitly
  (ADR-0077), not a TTFAB win.
- **NOT verified**: whether voicebox stops synthesizing when the client
  disconnects mid-PCM-stream (the TTS half of barge-in). The 0.1 ms figure above
  is the *shim* leg — the constitutional ctx-cancellation property — and the TTS
  half is untested on both backends.
- `bot.py` itself (mic/speaker via `LocalAudioTransport`) still requires real
  audio hardware and has NOT been run here. Its services are now built by the
  same `audio_backend.py` factories the lane exercises, so the untested surface
  is the audio *transport*, not the STT/LLM/TTS chain. The pipecat voice-id trap
  that bit the Kokoro path (`OpenAITTSService`'s `VALID_VOICES` lookup rejects
  non-OpenAI ids) is why the voicebox TTS leg is explicit HTTP instead — see
  `voicebox_tts.py`. Run steps 1–5 above to produce real numbers; do not quote
  any until you have.
