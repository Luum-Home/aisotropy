#!/usr/bin/env bash
# Reproduce the Phase-2 streaming transport bake-off.
#
# Prereqs (all MIT/Apache/BSD/ISC): go >=1.25, k6 (SSE/WS corroboration),
# ghz (gRPC corroboration). protoc + protoc-gen-go[-grpc] only if regenerating
# proto/streampb (checked in, so not required to run).
#
# UDP-buffer precondition (WebTransport/HTTP-3 only): quic-go wants a 7 MB kernel
# UDP buffer per socket. On macOS that lands only if the socket-buffer ceiling is
# high enough — raise it once per boot if needed:
#     sudo sysctl -w kern.ipc.maxsockbuf=8388608   # >= 8 MB (usually already the default)
# On Linux:
#     sudo sysctl -w net.core.rmem_max=8388608 net.core.wmem_max=8388608
# If the ceiling is too low, quic-go logs a one-shot "failed to sufficiently
# increase buffer size" warning and QUIC still runs, just with a smaller window.
# SSE/HTTP-2/WebSocket/gRPC need NONE of this (that zero-config default is itself
# a point in their favor vs WebTransport).
#
# Usage:  ./run.sh            # full matrix -> results.txt (incl. WT n=1000 + n=5000)
#         ./run.sh quick      # n=10 only, skip WT n=1000/5000
set -uo pipefail
cd "$(dirname "$0")"

HOST=127.0.0.1
STATS="http://$HOST:18085/stats"
OUT=results.txt
MODE="${1:-full}"

echo "building..."
go build -o ./bin/bench-server ./cmd/bench-server
go build -o ./bin/loadgen ./cmd/loadgen
go build -o ./bin/wt-probe ./cmd/wt-probe

echo "starting bench-server (-wt) ..."
# 5000 concurrent WebTransport sessions need generous fd headroom on the loadgen.
ulimit -n 20000 2>/dev/null || ulimit -n 8192 2>/dev/null || true
./bin/bench-server -wt >/tmp/bench-server.run.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null' EXIT
sleep 1.5

{
  echo "# Phase-2 streaming transport bake-off — measured results"
  echo "# host: $(uname -mrs)  go: $(go version | awk '{print $3}')  date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# stub: 200 tokens @ 5ms cadence (~200 tok/s), model held constant across transports"
  echo
} > "$OUT"

NS="10 100 1000"; [ "$MODE" = quick ] && NS="10"

for N in $NS; do
  for T in sse-h1 sse-h2 ws grpc; do
    ./bin/loadgen -transport "$T" -n "$N" -count 200 -interval 5 -stats "$STATS" -label "$T" >> "$OUT" 2>&1
    sleep 1
  done
done

echo "== WebTransport (HTTP/3): shared client socket + bounded dials + handshake retry ==" >> "$OUT"
# Fixed model (see README "WebTransport, root-caused"): every session multiplexes
# over ONE shared client UDP socket (-wt-share-socket, default true); handshakes are
# bounded (-wt-dial-concurrency, default 256) so the wide handshake burst does not
# overrun quic-go's hardcoded 32-slot server accept queue; and a wedged handshake
# fails fast (-wt-dial-timeout 3s) and is retried on a fresh conn (-wt-dial-retries 4)
# to recover quic-go's flaky straggler tail. This holds 1000/1000 and 5000/5000.
WTNS="10 100"; [ "$MODE" = full ] && WTNS="10 100 1000 5000"
for N in $WTNS; do
  ./bin/loadgen -transport wt -n "$N" -count 200 -interval 5 -cancel-after 30000 -stats "$STATS" -label "wt" >> "$OUT" 2>&1
  sleep 1
done

echo "== WebTransport A/B #1: PRE-FIX model — per-dial socket, unbounded, no retry (3x) ==" >> "$OUT"
# Reproduces the original collapse mechanism: one UDP socket per dial + a 1000-wide
# unbounded handshake herd + no retry. Reliably drops streams (they wedge in the
# handshake and never reach the server handler). 3 runs to show it is repeatable.
if [ "$MODE" = full ]; then for r in 1 2 3; do
  ./bin/loadgen -transport wt -n 1000 -count 200 -interval 5 -cancel-after 15000 \
    -wt-share-socket=false -wt-dial-concurrency=0 -wt-dial-retries=0 -stats "$STATS" \
    -label "wt PRE-FIX per-socket/unbounded/no-retry (run $r)" >> "$OUT" 2>&1
  sleep 2
done; fi

echo "== WebTransport A/B #2: FIX minus retry — shared socket + bounded, no retry (3x) ==" >> "$OUT"
# Isolates the residual quic-go maturity tax: with the shared socket + bounded dials +
# tuned server but NO retry, most runs are 1000/1000 but a small, nondeterministic
# straggler tail (0-3%) intermittently wedges. Retry (the default WT rows above) is
# what turns "usually fine" into "robust". 3 runs to expose the variance.
if [ "$MODE" = full ]; then for r in 1 2 3; do
  ./bin/loadgen -transport wt -n 1000 -count 200 -interval 5 -cancel-after 15000 \
    -wt-dial-retries=0 -stats "$STATS" \
    -label "wt FIX-minus-retry bounded/shared/no-retry (run $r)" >> "$OUT" 2>&1
  sleep 2
done; fi

echo "== HTTP/1.1 6-conn-per-origin cap proof ==" >> "$OUT"
./bin/loadgen -transport sse-h1 -n 12 -count 100000 -interval 50 -h1-max-conns 6 -cancel-after 2000 -label "sse-h1 cap=6" >> "$OUT" 2>&1
./bin/loadgen -transport sse-h2 -n 12 -count 100000 -interval 50 -cancel-after 2000 -label "sse-h2 no-cap" >> "$OUT" 2>&1

echo "wrote $OUT"
kill $SRV 2>/dev/null
