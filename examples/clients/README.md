# Client SDKs & reference chat UI

Reference clients that consume `aisotropyd` from **outside** Go, generated from the runtime's own
OpenAPI 3.1 contract (`GET /openapi.json`, Huma-generated). This is the "here's how a front-end / a
non-Go service consumes it" surface.

Full walkthrough: [`docs/guides/client-sdks.md`](../../docs/guides/client-sdks.md).
Stability contract: [`docs/guides/api-versioning-policy.md`](../../docs/guides/api-versioning-policy.md).

## Contents

| Path | What it is |
|---|---|
| [`openapi.json`](openapi.json) | Committed snapshot of `/openapi.json` (OpenAPI 3.1). Clients generate from this. |
| [`typescript/`](typescript) | Typed TS client — `openapi-typescript` (types) + `openapi-fetch` (JSON) + a hand-written SSE reader. `npm run typecheck` is the gate. |
| [`python/`](python) | Typed Python client — `openapi-python-client` (generated `aisotropy_client/`) + `sse.py` (SSE over `httpx`). |
| [`chat-ui.html`](chat-ui.html) | Single static HTML/JS page that streams a chat over SSE. No framework, no build step. |

All generators are **MIT**-licensed (openapi-typescript 7.13.0, openapi-fetch 0.17.0,
openapi-python-client 0.29.0), consistent with the project's MIT/Apache-2.0-only stack.

## Reproduce

```bash
# 0. Save the contract from a running keyless dev server (loopback + NoAuth).
AISOTROPY_ALLOW_NOAUTH=1 AISOTROPY_ADDR=127.0.0.1:8080 go run ./cmd/aisotropyd &
curl -s http://127.0.0.1:8080/openapi.json -o examples/clients/openapi.json

# 1. TypeScript
cd examples/clients/typescript && npm install && npm run generate && npm run typecheck
AISOTROPY_BASE_URL=http://127.0.0.1:8080 npx tsx src/example.ts   # optional live run

# 2. Python
cd ../python && python3 -m venv .venv && . .venv/bin/activate
pip install openapi-python-client==0.29.0
openapi-python-client generate --path ../openapi.json --meta none \
  --output-path ./aisotropy_client --overwrite
pip install -r requirements.txt
AISOTROPY_BASE_URL=http://127.0.0.1:8080 python example.py         # live run

# 3. UI — open chat-ui.html in a browser, set Base URL to the server, Send.
```
