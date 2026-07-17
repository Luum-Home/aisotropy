# Phase-2 Streaming Transport Bake-Off

Measures the **transport** cost of token streaming across five wire protocols,
holding the "model" constant as a deterministic token-rate stub so every delta is
attributable to the protocol and its framing — not to model latency.

- **SSE over HTTP/1.1** (the surface aisotropy ships today)
- **SSE over HTTP/2**
- **gRPC server-streaming** (`google.golang.org/grpc`, Apache-2.0)
- **WebSocket** (`github.com/coder/websocket`, ISC)
- **WebTransport / HTTP-3** (`github.com/quic-go/webtransport-go`, MIT) — *holds at N=1000/5000 with tuning; see "WebTransport, root-caused"*

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
                   WebTransport flags (defaults hold at scale; see root-cause below):
                     -wt-share-socket=true   one shared client UDP socket for all
                                             sessions (fair analog of gRPC/h2's one
                                             multiplexed conn); false = the old
                                             collapsing socket-per-dial model.
                     -wt-dial-concurrency=256 cap on simultaneous QUIC handshakes.
                     -wt-dial-timeout=3000ms  per-attempt handshake timeout.
                     -wt-dial-retries=4       fresh-conn retries for wedged dials.
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
  `stream_cancelled` counter must rise by N and `stream_active` must return to 0.
  **Verified for all five transports** (SSE-h1/h2, WS, gRPC, WT): active → 0, delta
  = N when the cancel window is past every dial. For SSE/gRPC the client's own
  `Do`/`Recv` observes ctx; the QUIC stream read does **not**, so WebTransport wires
  cancellation explicitly — a watchdog closes the session on `ctx.Done()`, which
  tears down the QUIC connection and stops the server stub via `conn.Context()`.
  (Measured: cancel-after=400 ms over a 4 s stream → 50/50 cancelled, active → 0.)
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
| **gRPC** | **17 ms** | **21 ms** | **196,678** | ~1.0 | 9 MB | 0 |
| **SSE / HTTP-2** | 48 ms | 59 ms | 187,243 | ~1.0 | 24 MB | 0 |
| **WebSocket** | 34 ms | 70 ms | 181,057 | ~2.0 | 32 MB | 0 |
| **SSE / HTTP-1.1** | 35 ms | 81 ms | 182,337 | ~2.0 | 18 MB | 0 |
| WebTransport *(tuned)* | 114 ms | 377 ms – 3.0 s† | 49k–138k | ~9 | 100 MB | **0 / 1000** |

† WebTransport now holds **1000/1000** (and **5000/5000**), but a nondeterministic
straggler tail (0-3% of dials) wedges in the quic-go handshake and is recovered by a
fresh-conn retry — when that fires, those streams' TTFT jumps to ~3 s (the handshake
timeout + retry) and the run's sustained tok/s halves. Clean runs (no straggler) finish
in ~1.5 s at 120k-140k tok/s; unlucky runs in ~4 s at ~49k. See "WebTransport,
root-caused" for why, and the A/B rows in `results.txt`.

At N=10 and N=100 all four HTTP-family transports are within a few ms of each
other (transport keeps up with the stub; inter-token p50 = 5ms everywhere). The
spread only opens **under concurrency**, which is where the ranking is decided.
Exact ms shift run-to-run on a loaded laptop (SSE-h1's TTFT in particular swings
from ~35 ms to >200 ms depending on background load); the **relative** ranking is
stable. Full per-N numbers, and the WebTransport A/B isolation runs, in `results.txt`.

Key structural finding (from `/stats`): **SSE-h1 and WebSocket use ~2 goroutines
per stream** (each needs its own TCP connection + read pump), while **SSE-h2 and
gRPC multiplex N streams over one HTTP/2 connection at ~1 goroutine per stream** —
half the server-side scheduling cost. gRPC's binary Protobuf framing also gives
it the lowest TTFT. SSE-h1's TTFT can **collapse under contention** (up to ~214 ms
p50 in a loaded earlier run; ~35 ms in the run above) because its 1000 independent
TCP connections contend on accept/handshake — a tail the multiplexed transports
(h2, gRPC) avoid.

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
4. **SSE over HTTP/1.1** — works, but the 6-per-origin cap and the TTFT tail under
   contention make it the wrong default. This is the current surface; moving it to
   h2 (option 1) is the single highest-leverage fix.
5. **WebTransport / HTTP-3** — **viable at scale, but not worth adopting yet.** No
   longer deferred for "it collapses" — with the tuning below it holds 1000/1000 and
   5000/5000. But it is the heaviest transport by far (~9 goroutines and ~100 MB for
   1000 streams vs ~1 goroutine / 24 MB for SSE-h2), its handshake has a flaky
   straggler tail that needs client retry to paper over, it demands an OS UDP-buffer
   precondition that the TCP transports do not, and browser support is still gated
   (Chromium behind flags/origin-trials; no Safari/Firefox stable). It buys nothing
   over SSE-h2 for server→client token streaming. Revisit only if a specific need
   (unreliable datagrams, head-of-line-blocking-free multi-stream) appears.

---

## WebTransport, root-caused

The earlier verdict was "defer — it collapses at N=1000 (232/1000 handshake
timeouts, ~25× throughput collapse)." That collapse was **real but not a QUIC
protocol limit**; it was two concrete, fixable causes in *how the bench drove the
Go stack*, plus one genuine quic-go maturity tax. All three are now diagnosed with
executable evidence (source lines in `quic-go@v0.60.0` / `webtransport-go@v0.11.1`,
`lsof`, and `netstat`), and the datapath now holds **1000/1000 and 5000/5000**.

### Cause 1 (primary): a socket-per-dial client + an unbounded handshake herd

The old `loadgen` built a fresh `webtransport.Dialer` **per stream**. Its default
dial path is `quic.DialAddrEarly`, which calls **`net.ListenUDP` — one brand-new
UDP socket per dial** (verified: `lsof` showed **400 client UDP sockets at N=400**,
each single-use with its own read/timer goroutines → the "~7 goroutines/stream").
That is the *opposite* of how gRPC and SSE-h2 are driven here (one shared
connection multiplexing N streams). 1000 sockets firing QUIC Initials
simultaneously is a thundering herd that overruns the server's accept path.

The server side compounds it: quic-go's connection accept queue is a **hardcoded
32 slots** (`protocol.MaxAcceptQueueSize`; `server.go:894` does a *non-blocking*
send to a 32-deep channel and `closeWithTransportError(ConnectionRefused)` on
overflow). A 1000-wide burst overruns it; losers stall.

**Fix:** drive WebTransport the way gRPC/h2 are driven — one shared
`quic.Transport` over one big-buffered client UDP socket (`-wt-share-socket`,
default on) — and **bound the handshake concurrency** (`-wt-dial-concurrency=256`).
Bounding the herd is the decisive lever: with it unbounded, even the shared socket
still degrades; with it bounded, the collapse disappears.

### Cause 2 (residual, genuine quic-go tax): a flaky handshake straggler tail

Even bounded + shared, a **small, nondeterministic fraction of dials (0-3%)** wedge:
the QUIC connection comes up but the peer's HTTP/3 **SETTINGS frame never arrives**,
so the client hangs on `error waiting for HTTP/3 settings`. Evidence this is a real
handshake wedge, not a resource limit: the server's `stream_opened` counter matches
the client's success count **exactly** (the wedged dials **never reach the handler**),
and `netstat -s -p udp` shows **no** increase in "dropped due to full socket buffers"
during the wedge. This is the maturity tax the research flagged — a handshake-tail
reliability gap in the quic-go WebTransport server at v0.60.

**Mitigation:** a short per-attempt handshake timeout (`-wt-dial-timeout=3s`) so a
wedged dial fails fast, plus a bounded **fresh-conn retry** (`-wt-dial-retries=4`).
A fresh attempt clears the wedge. This turns "usually 985-1000/1000" into a robust
**1000/1000 across many runs** — at the cost of ~3 s TTFT on the retried streams.

### Cause 3 (operational precondition): the UDP buffer

quic-go wants a **7 MB kernel UDP buffer per socket**
(`protocol.DesiredReceiveBufferSize`). On *this* macOS host it lands silently
(`kern.ipc.maxsockbuf` is 8 MB, so `SetReadBuffer(7MB)` succeeds and no warning
fires — so the buffer was **not** the bottleneck here). But on a host with a lower
ceiling it would be: quic-go logs a one-shot "failed to sufficiently increase buffer
size" warning and runs with a smaller window, which *does* cause loss-driven
handshake timeouts under load. So the honest operational note is: **WebTransport
carries a required sysctl precondition** (`kern.ipc.maxsockbuf >= 8388608` on macOS;
`net.core.rmem_max`/`wmem_max >= 8388608` on Linux) that **SSE/HTTP-2/WebSocket/gRPC
do not** — a real, if one-time, operational cost. The server now also requests the
7 MB buffer explicitly on its listener socket, and the client on its shared socket.

### The datapath wiring (unchanged, still fiddly)

The 4-part incantation on **both** ends is still required
(`webtransport.ConfigureHTTP3Server` + `http3.ConfigureTLSConfig` + QUIC
`EnableDatagrams` + `EnableStreamResetPartialDelivery`); miss any one and the client
aborts with a different cryptic error. `wt-probe` confirms a clean single session:
200 tokens, TTFT ~3 ms.

### Verdict: viable-with-tuning, but SSE-h2 is still the right default

WebTransport is **no longer disqualified for collapsing** — it holds at N=1000 and
N=5000. But making it hold took a shared transport, a bounded+retried dial strategy,
server QUIC tuning, and a UDP-buffer precondition — and even then it is the **heaviest
transport** (~9 goroutines & ~100 MB / 1000 streams vs ~1 goroutine & ~24 MB for
SSE-h2), with a flaky handshake tail and gated browser support. For aisotropy's
server→client token streaming it buys nothing over the SSE-h2 surface we already
emit. **Adopt SSE-h2; keep WebTransport on the shelf** for a future need that
actually exploits QUIC (unreliable datagrams, HOL-blocking-free multiplexing).

### Other caveats / uncertainties

- Single-host loopback measurement: no real network RTT, loss, or proxy buffering
  (nginx/Caddy). The **relative** ranking is robust; **absolute** ms will shift on
  a real network (TLS/QUIC handshakes cost more with real RTT, which *widens*
  gRPC/h2's TTFT lead over h1).
- The stub is uniform-cadence; a real decoder is bursty. Backpressure behavior
  under bursty production + a genuinely slow client is only partially exercised.
- `/stats` `max_active` is a **global** high-water mark on the shared server
  process, so later runs inherit an earlier run's peak (visible as `max_active:
  1970` on the WT rows, inherited from the N=5000 run). Goroutine/heap peaks ARE
  per-run (sampled live by `loadgen` during that run's window).
- The WebTransport straggler tail is **nondeterministic**: any single N=1000 run may
  be a clean ~1.5 s / 120k-tok/s run or a ~4 s / ~49k-tok/s run where retry fired.
  The A/B rows in `results.txt` run each isolation config 3× precisely because one
  run is not enough to characterize a flaky tail. Correctness (streams ok) is robust
  with retry on; latency/throughput of a given run is not.
- k6's default summary folds `http_req_waiting`; SSE TTFT precision comes from
  `loadgen`, with k6 corroborating duration/throughput/success (0% failures).
