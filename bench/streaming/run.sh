#!/usr/bin/env bash
# Reproduce the Phase-2 streaming transport bake-off.
#
# Prereqs (all MIT/Apache/BSD/ISC): go >=1.25, k6 (SSE/WS corroboration),
# ghz (gRPC corroboration). protoc + protoc-gen-go[-grpc] only if regenerating
# proto/streampb (checked in, so not required to run).
#
# Usage:  ./run.sh            # full matrix -> results.txt
#         ./run.sh quick      # n=10 only, skip WT n=1000
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
ulimit -n 8192 2>/dev/null || true
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

echo "== WebTransport (experimental; needs client deadline guard) ==" >> "$OUT"
WTNS="10 100"; [ "$MODE" = full ] && WTNS="10 100 1000"
for N in $WTNS; do
  ./bin/loadgen -transport wt -n "$N" -count 200 -interval 5 -cancel-after 20000 -stats "$STATS" -label "wt" >> "$OUT" 2>&1
  sleep 1
done

echo "== HTTP/1.1 6-conn-per-origin cap proof ==" >> "$OUT"
./bin/loadgen -transport sse-h1 -n 12 -count 100000 -interval 50 -h1-max-conns 6 -cancel-after 2000 -label "sse-h1 cap=6" >> "$OUT" 2>&1
./bin/loadgen -transport sse-h2 -n 12 -count 100000 -interval 50 -cancel-after 2000 -label "sse-h2 no-cap" >> "$OUT" 2>&1

echo "wrote $OUT"
kill $SRV 2>/dev/null
