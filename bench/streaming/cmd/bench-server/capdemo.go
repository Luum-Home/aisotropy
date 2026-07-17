package main

import "net/http"

// capDemoHandler serves a page that opens many concurrent EventSource (SSE)
// connections to the SAME origin and reports how many actually reach the "open"
// state. Load it from an HTTP/1.1 origin and the browser's per-host connection
// cap (6 in Chrome/Firefox) stalls the 7th+; load the identical page from an
// HTTP/2 origin and all N open at once because h2 multiplexes them over one TCP
// connection. This is the REAL browser surface behind the h1-vs-h2 finding —
// the cap is a browser client policy, not a server or protocol limit.
func capDemoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(capDemoHTML))
}

const capDemoHTML = `<!doctype html>
<meta charset="utf-8">
<title>SSE connection-cap demo</title>
<body style="font-family:monospace">
<h3>SSE per-origin connection cap</h3>
<p>N = <span id="n">20</span> EventSource connections to <code id="origin"></code></p>
<button onclick="run()">open N streams</button>
<pre id="out"></pre>
<script>
const N = 20;
document.getElementById('n').textContent = N;
document.getElementById('origin').textContent = location.origin + '/sse?count=100000&interval_ms=50';
function run() {
  const out = document.getElementById('out');
  let opened = 0, firstMsg = 0;
  const t0 = performance.now();
  const states = new Array(N).fill('init');
  for (let i = 0; i < N; i++) {
    const es = new EventSource('/sse?count=100000&interval_ms=50');
    es.onopen = () => { states[i] = 'OPEN'; opened++; render(); };
    es.onmessage = () => { if (states[i] !== 'MSG') { states[i] = 'MSG'; firstMsg++; render(); } };
    es.onerror = () => { states[i] = 'ERR'; render(); };
  }
  function render() {
    const dt = (performance.now() - t0).toFixed(0);
    out.textContent =
      'elapsed ' + dt + 'ms\n' +
      'opened(handshake): ' + opened + ' / ' + N + '\n' +
      'receiving tokens : ' + firstMsg + ' / ' + N + '\n\n' +
      states.map((s,i)=> i.toString().padStart(2)+': '+s).join('\n');
  }
  render();
}
</script>
</body>`
