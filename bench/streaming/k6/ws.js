// k6 WebSocket corroboration. Opens a WS, counts token frames until the server
// closes (after the 200-token stub), records time-to-first-message (TTFT proxy)
// and total session duration via custom Trends.
//
// Run:  k6 run -e URL=ws://127.0.0.1:18087/ws -e VUS=100 -e ITER=100 k6/ws.js
import ws from 'k6/ws';
import { check } from 'k6';
import { Trend, Counter } from 'k6/metrics';

export const options = {
  vus: Number(__ENV.VUS) || 10,
  iterations: Number(__ENV.ITER) || Number(__ENV.VUS) || 10,
};

const ttft = new Trend('ws_ttft_ms', true);
const dur = new Trend('ws_session_ms', true);
const toks = new Counter('ws_tokens');
const URL = __ENV.URL + '?count=200&interval_ms=5';

export default function () {
  const start = Date.now();
  let first = 0;
  let n = 0;
  const res = ws.connect(URL, {}, function (socket) {
    socket.on('message', function () {
      n++;
      if (first === 0) first = Date.now();
      toks.add(1);
    });
    socket.on('close', function () {
      if (first > 0) ttft.add(first - start);
      dur.add(Date.now() - start);
    });
    socket.setTimeout(function () { socket.close(); }, 5000);
  });
  check(res, { 'ws status 101': (r) => r && r.status === 101 });
  check(n, { 'received tokens': (v) => v > 0 });
}
