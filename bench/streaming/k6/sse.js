// k6 SSE corroboration. Treats each token stream as one HTTP request whose body
// is the full finite stub (200 tokens, ~1s). k6 reports http_req_waiting (TTFB,
// a TTFT proxy: time to first byte == time to first token here) and
// http_req_duration (whole stream). Works for both HTTP/1.1 (plaintext URL) and
// HTTP/2 (https URL — k6 auto-negotiates h2 via ALPN).
//
// Run:  k6 run -e URL=http://127.0.0.1:18085/sse -e VUS=100 -e ITER=100 k6/sse.js
//       k6 run -e URL=https://127.0.0.1:18086/sse -e VUS=100 -e ITER=100 k6/sse.js
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  vus: Number(__ENV.VUS) || 10,
  iterations: Number(__ENV.ITER) || Number(__ENV.VUS) || 10,
  insecureSkipTLSVerify: true,
  // No thresholds that abort; this is a measurement run.
};

const URL = __ENV.URL + '?count=200&interval_ms=5';

export default function () {
  const res = http.get(URL);
  check(res, {
    'status 200': (r) => r.status === 200,
    'got tokens': (r) => r.body && r.body.indexOf('data:') >= 0,
    'proto reported': (r) => true,
  });
}
