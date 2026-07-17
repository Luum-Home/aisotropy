# Phase-2 Streaming Transport Bake-Off

Measures the **transport** cost of token streaming across five wire protocols,
holding the "model" constant as a deterministic token-rate stub so every delta is
attributable to the protocol and its framing — not to model latency.

- **SSE over HTTP/1.1** (the surface aisotropy ships today)
- **SSE over HTTP/2**
- **gRPC server-streaming** (`google.golang.org/grpc`, Apache-2.0)
- **WebSocket** (`github.com/coder/websocket`, ISC)
- **WebTransport / HTTP-3** (`github.com/quic-go/webtransport-go`, MIT) — *experimental, see caveats*

Everything here is a **standalone Go module** (`aisotropy.local/bench/streaming`)
with its **own `go.mod`** — it adds **zero** dependencies to the aisotropy
production module and imports nothing from `internal/`.

> **Grounding note.** The task referenced `docs/research/inter-service-protocols-2026-07-17.md`
> §7.1–7.5; that file is **not present** in the repo. The benchmark was built from
> the task's own (self-sufficient) spec plus the existing SSE surface documented in
> `docs/research/frontend-ui-integration-landscape.md`. The §7.2 (backpressure) and
> §7.3 (cancellation-correctness) gates are implemented as described below.

---

## Harness

```
cmd/bench-server   multi-transport server; ONE deterministic token stub served over
                   SSE-h1 (:18085), SSE-h2/TLS (:18086), WebSocket (:18087),
                   gRPC (:18088), WebTransport/HTTP3-UDP (:18089, -wt).
                   Every HTTP listener exposes GET /stats (JSON) for live
                   goroutine/heap/active-stream + cancellation counters.
cmd/loadgen        precision client: opens N concurrent streams per transport,
                   timestamps EVERY token frame -> true TTFT + inter-token
                   p50/p95/p99 + sustained tok/s; polls /stats for peaks.
cmd/wt-probe       single-session WebTransport honesty probe.
internal/tokens    the shared stub: N tokens at a fixed cadence, first token
                   immediate. Emit() honors ctx cancellation (the §7.3 gate).
proto/             gRPC contract (TokenStreamer.Stream) + generated code.
k6/                k6 scripts (SSE + WS corroboration).
run.sh             one-command reproduce -> results.txt
results.txt        last measured run (host/date in its header).
```

Why a custom `loadgen` **and** k6/ghz? k6 and ghz measure at **connection**
granularity (TTFB, whole-stream duration); they cannot report **inter-token**
latency for a long-lived token stream. `loadgen` reads every frame with a
timestamp, so it yields the per-token percentiles that matter for a chat UX.
k6 (SSE/WS) and ghz (gRPC) are run as an **independent cross-check** on
throughput/duration/success.

### The two correctness gates

- **§7.3 cancellation-correctness** — killing the client MUST cancel the server
  stub. `loadgen -cancel-after Nms` cancels each client mid-stream; the server's
  `stream_cancelled` counter must rise by exactly N and `stream_active` must
  return to 0. **Verified for all five transports** (SSE-h1/h2, WS, gRPC, WT):
  delta = N, active → 0.
- **§7.2 backpressure** — the stub uses a `time.Ticker`; a slow/blocked client
  write returns an error from `Emit`'s callback, which stops emission for that
  stream rather than unbounded-buffering. `MaxConnsPerHost` on the SSE-h1 client
  reproduces the browser connection-cap starvation (below).

---

## Tools

| Tool | Role | Install | License |
|---|---|---|---|
| Go ≥1.25 | build + `loadgen`/`wt-probe` | (system) | BSD-3 |
| k6 | SSE + WS corroboration | `brew install k6` | AGPL-3.0 *(external CLI, not linked or shipped — used as a measurement tool only, no source integration)* |
| ghz | gRPC corroboration | `go install github.com/bojand/ghz/cmd/ghz@latest` | Apache-2.0 |
| protoc + protoc-gen-go[-grpc] | regenerate `proto/streampb` (optional; checked in) | `brew install protobuf`, `go install …` | BSD-3 / Apache-2.0 |

**License gate on shipped module deps** (`go.mod`): grpc (Apache-2.0),
protobuf (BSD-3), coder/websocket (ISC), quic-go + webtransport-go (MIT),
golang.org/x/* (BSD-3). All in the permissive allow-list — no GPL/AGPL/BSL in
the module.

> k6 itself is AGPL, but it is an **external load-testing binary** invoked from
> the shell — its source is neither linked into nor vendored by this module, so
> it does not encumber aisotropy. ghz is the primary gRPC tool and is Apache-2.0.

---

## Reproduce

```bash
cd bench/streaming
./run.sh            # full matrix (N∈{10,100,1000}) -> results.txt   (~90s)
./run.sh quick      # N=10 only, faster smoke

# k6 / ghz cross-check (server must be running: ./bin/bench-server -wt &)
k6 run -e URL=http://127.0.0.1:18085/sse  -e VUS=100 -e ITER=100 k6/sse.js   # SSE h1
k6 run -e URL=https://127.0.0.1:18086/sse -e VUS=100 -e ITER=100 k6/sse.js   # SSE h2
k6 run -e URL=ws://127.0.0.1:18087/ws     -e VUS=100 -e ITER=100 k6/ws.js    # WS
ghz --insecure --proto ./proto/stream.proto \
    --call bench.stream.v1.TokenStreamer.Stream \
    -d '{"tenant_id":"t","user_id":"u","count":200,"interval_ms":5}' \
    -c 100 -n 100 127.0.0.1:18088                                            # gRPC
```

Ports 18085–18089 are used to avoid colliding with local services. The stub is
200 tokens @ 5ms cadence (~200 tok/s) unless overridden with `-count`/`-interval`.

---

## Results (this machine: Apple arm64, go1.25.3; see `results.txt` header)

### Headline 1 — HTTP/1.1 6-connections-per-origin cap is real; HTTP/2 removes it

12 long-lived SSE streams to one origin, client deadline 2s:

| client | streams that opened | note |
|---|---|---|
| **HTTP/1.1**, per-host cap = 6 (browser policy) | **6 / 12** | streams 7–12 fail `context deadline exceeded` — the pool is saturated by 6 live streams, the rest never open |
| **HTTP/2**, single multiplexed conn | **12 / 12** | all stream concurrently over one TCP connection |

The cap is a **client (browser) policy**, not a protocol/server limit: Chrome and
Firefox open at most ~6 TCP connections per origin, and each SSE `EventSource`
holds one for its whole lifetime, so the 7th concurrent stream to an origin
starves. `loadgen -h1-max-conns 6` reproduces it deterministically; the browser
version is served at `/capdemo` (open it from the h1 origin vs the h2 origin).
HTTP/2 multiplexes all streams over one connection and the cap disappears.

### Headline 2 — token-streaming ranking under load (N=1000 concurrent)

| transport | TTFT p50 | TTFT p99 | sustained tok/s | goroutines/stream | peak heap | failures |
|---|---|---|---|---|---|---|
| **gRPC** | **11 ms** | **16 ms** | **196,670** | ~1.0 | 18 MB | 0 |
| **SSE / HTTP-2** | 17 ms | 27 ms | 195,159 | ~1.0 | 28 MB | 0 |
| **WebSocket** | 39 ms | 83 ms | 180,685 | ~2.0 | 32 MB | 0 |
| **SSE / HTTP-1.1** | 214 ms | 603 ms | 124,914 | ~2.0 | 17 MB | 0 |
| WebTransport | 95 ms | 212 ms | 7,663 | ~7 | 82 MB | **232 / 1000** |

At N=10 and N=100 all four HTTP-family transports are within a few ms of each
other (transport keeps up with the stub; inter-token p50 = 5ms everywhere). The
spread only opens **under concurrency**, which is where the ranking is decided.
Full per-N numbers in `results.txt`.

Key structural finding (from `/stats`): **SSE-h1 and WebSocket use ~2 goroutines
per stream** (each needs its own TCP connection + read pump), while **SSE-h2 and
gRPC multiplex N streams over one HTTP/2 connection at ~1 goroutine per stream** —
half the server-side scheduling cost. gRPC's binary Protobuf framing also gives
it the lowest TTFT. SSE-h1's TTFT **collapses at scale** (214 ms p50) because
1000 independent TCP connections contend on accept/handshake.

### Honest bake-off ranking for **token streaming**

1. **SSE over HTTP/2** — *recommended default for browser-facing token streaming.*
   Native browser support (`EventSource`), one multiplexed connection, throughput
   statistically tied with gRPC, low TTFT, zero failures, and it's the surface
   aisotropy **already emits** (Huma `event: token` SSE) — the only change is
   serving it over h2 instead of h1.
2. **gRPC server-streaming** — *best raw numbers* (lowest TTFT, top throughput,
   1 goroutine/stream), ideal **service-to-service**. Browser-hostile: needs a
   grpc-web/Connect proxy, so not a drop-in for in-browser copilots.
3. **WebSocket** — solid and bidirectional, but 2 goroutines/stream and a higher
   TTFT tail; the bidirectionality is wasted on pure server→client token flow.
   Reach for it only when the client streams **up** as much as down.
4. **SSE over HTTP/1.1** — works, but the 6-per-origin cap and the TTFT collapse
   at scale make it the wrong default. This is the current surface; moving it to
   h2 (option 1) is the single highest-leverage fix.
5. **WebTransport / HTTP-3** — **deferred** (see below).

---

## What I could NOT build reliably: WebTransport

The datapath **does work** — after a non-obvious 4-part incantation on **both**
ends (`webtransport.ConfigureHTTP3Server` + `http3.ConfigureTLSConfig` + QUIC
`EnableDatagrams` + `EnableStreamResetPartialDelivery`); miss any one and the
client aborts with a different cryptic error (`server didn't enable WebTransport`,
`… datagram support`, `… stream reset partial delivery required`). `wt-probe`
confirms a clean single session: 200 tokens, TTFT 5 ms.

But it does **not** hold up as a benchmark subject at scale in the Go stack:

- **N=10, N=100: OK** (with a client-side deadline guard — some streams otherwise
  hang waiting for a stream FIN that never arrives).
- **N=1000: FAILS** — **232/1000 streams error** with
  `error waiting for HTTP/3 settings: context deadline exceeded` (QUIC handshakes
  time out under load), sustained throughput collapses ~**25×** (7.6k vs ~195k
  tok/s), TTFT p99 balloons to 212 ms, and the server spends **~7 goroutines per
  stream** (8k+ goroutines, 82 MB heap).

This matches the research flag that the **Go WebTransport server story is still
immature**. Browser support is also gated (Chromium behind flags/origin-trials,
no Safari/Firefox stable). **Verdict: defer.** Revisit when quic-go's
WebTransport server reaches 1.0 and browser support is unflagged; it is not a
credible token-streaming transport for aisotropy today.

### Other caveats / uncertainties

- Single-host loopback measurement: no real network RTT, loss, or proxy buffering
  (nginx/Caddy). The **relative** ranking is robust; **absolute** ms will shift on
  a real network (TLS/QUIC handshakes cost more with real RTT, which *widens*
  gRPC/h2's TTFT lead over h1).
- The stub is uniform-cadence; a real decoder is bursty. Backpressure behavior
  under bursty production + a genuinely slow client is only partially exercised.
- `/stats` `max_active` is a **global** high-water mark on the shared server
  process, so later runs inherit an earlier run's peak (visible as `max_active:
  1000` on the WT rows). Goroutine/heap peaks ARE per-run (sampled live by
  `loadgen` during that run's window).
- k6's default summary folds `http_req_waiting`; SSE TTFT precision comes from
  `loadgen`, with k6 corroborating duration/throughput/success (0% failures).
