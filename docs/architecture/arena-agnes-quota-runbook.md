---
type: Concept
title: "Runbook — running the live arena against agnes without exhausting the quota"
description: >
  How to launch a real (non-stub) arena sweep against agnes-2.0-flash without tripping its free-tier limits.
tags: [architecture, arena, agnes, quota, rate-limiting, runbook]
timestamp: 2026-08-03T00:00:00Z
---

# Runbook — running the live arena against agnes without exhausting the quota

> How to launch a real (non-stub) arena sweep against `agnes-2.0-flash` without
> tripping its limits. Written after a session that exhausted the free-tier quota
> by running full sweeps back-to-back. Companion to `arena-rate-limiting.md` (the
> `-rps` limiter mechanism) and `arena-infrastructure-parity.md` (the lane model).

## The core mental model — agnes free-tier has TWO limits

Conflating these is the mistake that burned a whole afternoon of quota. They are
different axes and need different mitigations:

| Limit | What it is | Tripped by | Mitigation |
|---|---|---|---|
| **Burst rate** | calls per second | `-concurrency > 1` firing many frameworks' calls at once | client-side pacing (`-rps`, adaptive) |
| **Cumulative quota** | total calls / tokens per rolling window (≈ per day) | **running many sweeps back-to-back** | **fewer total calls** — cap tasks, run once, don't repeat |

**Pacing (`-rps`) does NOT fix the quota.** Spacing calls in time keeps you under
the *rate* ceiling but does not reduce the *total*, and it is the total that the
quota caps. A perfectly-paced sweep still spends N calls; run it five times and
you've spent 5N. **The quota is protected by spending fewer calls, not by pacing.**

## Step 0 — pre-flight probe (is the quota available right now?)

One minimal call (~260 tokens). NEVER print the key — pass it via the header only,
no `-v`, print only status + body:

```bash
cd <repo-root>
set -a; source .env 2>/dev/null; set +a      # loads OPENAI_API_KEY / _BASE_URL / _MODEL
BASE="${OPENAI_BASE_URL:-https://api.openai.com/v1}"; MODEL="${OPENAI_MODEL:-agnes-2.0-flash}"
curl -s -o /tmp/probe.json -w "HTTP %{http_code}\n" --max-time 40 \
  -X POST "$BASE/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"Reply OK\"}]}"
head -c 400 /tmp/probe.json
```

- **HTTP 200** → quota available, proceed.
- **HTTP 429** (`"rate limit for free users … Upgrade to a Token Plan"`) → quota
  exhausted or rate-limited. Do NOT launch a sweep — wait for the window to reset
  (≈ a day) or use a paid Token Plan. Running anyway just piles 429s on 429s.

## Step 1 — infra up (real lane needs it; agnes is not enough)

The real lane also needs the local stores. Confirm before launching, or RAG/memory
cells fail (and each failed attempt still spent an agnes call via retry):

```bash
curl -s -o /dev/null -w "qdrant %{http_code}\n"  --max-time 5 http://localhost:6333/healthz
curl -s -o /dev/null -w "rqlite %{http_code}\n"  --max-time 5 http://localhost:4001/status
curl -s -o /dev/null -w "e5     %{http_code}\n"  --max-time 5 http://127.0.0.1:8899/health
```

All three `200`, and the per-framework Qdrant collections seeded
(`arenas/live/_shared/populate-qdrant.py`), before a real RAG run.

## Step 2 — launch conservatively (escalating tiers)

Run from `arenas/live/runner`. **Never set `ARENA_STUB`** for a real run (that's the
stub lane). Pick the smallest tier that answers your question; only escalate if it
came back clean and you still have quota.

### Tier A — smoke / first real run (~120–160 calls)
The safest real run: fully sequential, tiny task cap, correctness only.
```bash
go run . -frameworks ai-sdk,aisotropy,eino,genkit-go,langgraph,llamaindex,mastra,pydantic-ai \
  -concurrency 1 -rps 1 -retry 4 -max-tasks 2 -native-only -skip-bench -skip-dimensions
```

### Tier B — fuller correctness (~300–350 calls)
All tasks, still sequential. This is the "real quality numbers" pass.
```bash
go run . -concurrency 1 -rps 1.5 -retry 4 -native-only -skip-bench -skip-dimensions
```

### Tier C — add latency benchmarks + dimensions (adds ~80–150 calls)
Only when quota is comfortable. The bench pass is sequential/uncontended by design.
```bash
go run . -concurrency 1 -rps 1.5 -retry 4 -native-only
```

**Why these flags protect the quota:**
- `-concurrency 1` — sequential; the ~10–25 s per-call latency alone keeps the
  request rate near 0.05–0.1/s, well under any burst limit. This is the regime that
  was always clean.
- `-rps 1`…`1.5` — a low ceiling for the adaptive learned-ceiling limiter (starts at
  ¼ of the ceiling, backs off on any 429, never re-probes a known-bad rate). At
  `-concurrency 1` it barely bites, but it's the safety net if you raise concurrency.
- `-retry 4` — rides out a transient 429/502 with the long 429-aware backoff
  (3/6/12/24 s) instead of failing the cell and re-spending on the next run.
- `-max-tasks N` — the biggest quota lever: caps tasks per correctness mode, so total
  calls scale with N. Tier A's `-max-tasks 2` roughly halves the correctness spend.
- `-skip-bench -skip-dimensions` — the bench + dimension passes make real calls too;
  skip them until the cheap correctness pass is clean.
- Exclude `spring-ai` (the `candidate` adapter) from `-frameworks` until you mean to
  verify its real lane — it needs a built jar and adds calls.

## Step 3 — during / after the run

- **Watch for 429s** (should be ~0 at `-concurrency 1`):
  `grep -c "rate-limited (429)" <log>`. A rising count means the quota is depleting —
  stop the run (`pkill -f 'go run \. -frameworks'`) rather than grind through 429s.
- **The run self-labels its lane.** RESULTS.md carries `LANE: REAL`; a stub run
  carries `LANE: STUB` + a 🚨 leak banner if any adapter called the real model. Never
  compare a stub-lane RESULTS.md against a real one (see `arena-infrastructure-parity.md`).
- **Real spend is logged**: the final `wrote … RESULTS.md (… N model calls)` line is
  the billable count for that run. Track it against your quota budget.

## Anti-patterns (what actually exhausted the quota)

1. **Back-to-back full sweeps.** ~5–6 full sweeps in one afternoon (~2000+ calls) blew
   the daily quota. One capped run, reviewed, is worth more than five full ones.
2. **A "stub" run that wasn't offline.** aisotropy's stub used to call real agnes
   because the LLM provider env wasn't cleared (fixed — see the P0.2 commit and
   `clearBackendTierEnv`). Verify a stub run is truly offline: it should finish in
   ms/seconds with `llm=stub`, not ~20 s with `llm=openai:agnes-2.0-flash`. The
   `LANE LEAK` guard now catches this automatically.
3. **Re-running to "get clean numbers" after a rate-limited run.** A 429-heavy run
   means the quota is already tight; the fix is to *stop*, not to retry the whole
   sweep — that spends more against a limit that's already exceeded.

## When you genuinely need more

- **Higher concurrency / more frameworks / full task set** repeatedly → an agnes
  **Token Plan** (paid) raises the underlying limit; `-rps` + retry only make the
  free tier survivable, they don't raise its ceiling.
- **Iterating on adapter code** → use the **stub lane** (`ARENA_STUB=1`, zero agnes) to
  validate wiring for free, and reserve a single capped real run for the final numbers.
