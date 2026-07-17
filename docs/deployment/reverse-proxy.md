---
type: Concept
title: Reverse proxy — keeping SSE token streaming alive end-to-end
description: aisotropyd serves plain HTTP and expects an external ingress to terminate TLS. This documents how to front it with Caddy (streams by default) or nginx (needs proxy_buffering off, but the X-Accel-Buffering:no header aisotropyd now emits makes it stream even at nginx defaults), the reference configs under deploy/proxy/, and the executable end-to-end proof under test/proxy/ — including the measured before/after finding that the native /chat/stream route was not proxy-safe until the sseAntiBuffering middleware landed.
tags: [deployment, reverse-proxy, sse, streaming, nginx, caddy, x-accel-buffering, buffering]
timestamp: 2026-07-17T00:00:00Z
---

# Reverse proxy — keeping SSE token streaming alive

`aisotropyd` serves **plain HTTP** on `:8080` and expects an external ingress to terminate TLS
(see [services-config](../../.claude/rules/services-config.md), `AISOTROPY_REQUIRE_TLS`). That split —
the proxy owns TLS, aisotropyd stays HTTP — is deliberate. But it introduces a hazard specific to the
three **token-by-token SSE routes**:

| Route | What it is |
|---|---|
| `POST /v1/tenant/{t}/user/{u}/chat/stream` | native aisotropy SSE (Huma `sse.Register`) |
| `POST /v1/tenant/{t}/user/{u}/chat/aisdk`  | Vercel AI SDK protocol adapter over the same `StreamCore` |
| `POST /v1/tenant/{t}/user/{u}/chat/agui`   | AG-UI protocol adapter over the same `StreamCore` |

Each emits tokens as they are produced and flushes every event. A reverse proxy that **buffers** the
response collects the whole event stream and delivers it to the client in one write at the very end —
turning a live token stream into a single delayed dump. The contract ("tokens arrive incrementally")
is silently broken with a 200 OK and no error anywhere.

## TL;DR

- **Caddy**: works out of the box. Caddy does not buffer proxied responses; the reference
  `flush_interval -1` just makes the intent explicit. No SSE-specific config needed.
- **nginx**: buffers proxied responses **by default** (`proxy_buffering on`). Two independent defenses
  make streaming work anyway:
  1. aisotropyd emits **`X-Accel-Buffering: no`** on every SSE route (the `sseAntiBuffering`
     middleware, [`aisotropy/sse_buffering.go`](../../aisotropy/sse_buffering.go)), which nginx honors
     **per response even under the default `proxy_buffering on`** — so the stream works through a
     **default nginx too**, no config change required.
  2. the reference `nginx.conf` **also** sets `proxy_buffering off` on the SSE location, so the intent
     is explicit and independent of the upstream header.
  You only lose streaming if you do BOTH: ignore the header **and** buffer (see the control below).

## Reference configs

Ready to use, under [`deploy/proxy/`](../../deploy/proxy/):

- [`Caddyfile`](../../deploy/proxy/Caddyfile) — swap `:80` for your hostname and Caddy auto-provisions
  TLS. The whole point of the split: the proxy owns TLS, aisotropyd stays HTTP.
- [`nginx.conf`](../../deploy/proxy/nginx.conf) — two server blocks: a plain **`:80`** block (for
  edge-terminated TLS, where an LB ahead of nginx owns TLS and nginx sees HTTP) and an active
  **`listen 443 ssl; http2 on;`** block that terminates TLS at nginx and is the recommended front
  door for browser SSE (see the HTTP/2 section below). Both set `proxy_buffering off` on the SSE
  location and `proxy_read_timeout 1h` for long-lived streams, default buffering elsewhere; both
  keep the proxy↔upstream hop on HTTP/1.1 (`proxy_http_version 1.1`). The `:443` block carries
  cert/key **placeholder paths** (`/etc/nginx/tls/tls.{crt,key}`) — the proxy owns TLS, aisotropyd
  stays plain HTTP. The k8s `overlays/prod` sets **`AISOTROPY_REQUIRE_TLS=1`**, so aisotropyd
  refuses plaintext and must sit behind a TLS-terminating proxy/ingress like this. Supply the
  cert/key out-of-band (mounted files / a secret); never commit them.

## HTTP/2 for SSE — beating the 6-connections-per-domain cap

An SSE response holds **one HTTP connection open for its whole lifetime**. Over **HTTP/1.1** a
browser opens only **~6 connections per domain** (the long-standing limit every major browser
implements). So ~6 concurrent SSE streams to this origin **saturate the pool**, and every further
request to the same host — more streams, plus ordinary JSON chat and asset fetches — **stalls**
until one frees up. A single page that opens a couple of streams and still needs to talk to the API
can deadlock itself.

**HTTP/2 removes the cap**: it multiplexes many independent streams over **one** TCP connection, so
the per-domain connection limit no longer bounds concurrent SSE. Browsers only negotiate HTTP/2 over
**TLS**, which is why the fix lives on the `:443 ssl` server block (`http2 on;`) rather than the
plain `:80` one. Nothing about aisotropyd changes: HTTP/2 is a **client↔proxy** concern, and the
proxy↔aisotropyd hop stays HTTP/1.1 with the same anti-buffering knobs. ingress-nginx already serves
HTTP/2 on its TLS listener by default, so a TLS-terminating ingress gets the same benefit with no
extra annotation.

HTTP/2 is the **concrete, adopted fix** here. **WebTransport / HTTP/3** (QUIC) would multiplex
without any of HTTP/1.1's per-domain history and avoid head-of-line blocking at the transport layer,
but it is a **future option, not adopted** — noted for completeness per the protocols research, not
wired into these reference configs.

> Verified: `nginx -t` against `deploy/proxy/nginx.conf` (with a throwaway self-signed cert at the
> placeholder paths and the upstream name resolvable) reports *"syntax is ok / test is successful"* —
> the active `:443 http2` block parses and loads.

### Kubernetes / ingress-nginx

The SSE-relevant knobs as ingress annotations on the streaming paths:

```yaml
nginx.ingress.kubernetes.io/proxy-buffering: "off"
nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
```

The Caddy equivalent is `flush_interval -1` on the `reverse_proxy` block. And because aisotropyd emits
`X-Accel-Buffering: no` itself, ingress-nginx honors it per response even without the annotation — the
annotation is belt to the header's suspenders.

## Cross-origin browser clients (CORS) — `AISOTROPY_CORS_ALLOWED_ORIGINS`

A browser UI served from a **different origin** than the API (e.g. the reference
[`examples/clients/chat-ui.html`](../../examples/clients/) opened from `http://localhost:5173`
while aisotropyd runs on `:8080`) cannot read the API's responses unless aisotropyd returns CORS
headers — otherwise every `fetch` fails with a bare *"Failed to fetch"*, before any status is seen.

aisotropyd ships an **opt-in, exact-origin-allowlist** CORS middleware
([`aisotropy/cors.go`](../../aisotropy/cors.go)), wired as the **outermost** router middleware so a
browser preflight is answered before the `/v1` auth gate and the header lands on **every** response,
**including the SSE streams**.

| `AISOTROPY_CORS_ALLOWED_ORIGINS` | Behavior |
|---|---|
| **unset / blank** (default) | **No CORS headers emitted** — same-origin only. Secure default. |
| `https://app.example.com,http://localhost:5173` | For a request whose `Origin` **exactly matches** an entry, that origin is echoed back in `Access-Control-Allow-Origin`; an origin not on the list gets **no** CORS header (the browser blocks it). |

The value is a **comma-separated list of exact origins** (scheme + host + optional port). It matters
that this is an **allowlist, never `*`**: aisotropyd is a **multi-tenant, bearer-authenticated** API,
and a wildcard would let any web page drive it with a victim's token. The middleware:

- echoes the request `Origin` **only** when it is on the allowlist — never `*`;
- allows the methods/headers the API actually needs — `GET, POST, OPTIONS` and
  `Content-Type, Authorization, Last-Event-ID` (the last for SSE stream resume);
- answers the preflight `OPTIONS` with `204` and an `Access-Control-Max-Age`, short-circuiting before
  auth;
- does **not** send `Access-Control-Allow-Credentials`: the API authenticates via the `Authorization`
  bearer header (which the client sets explicitly and does not require credentials mode), so cookies
  are never in scope.

```bash
# Allow the local dev UI and the deployed app origin:
export AISOTROPY_CORS_ALLOWED_ORIGINS="http://localhost:5173,https://app.example.com"
```

Note the division of labor with the reverse proxy: a proxy can also inject CORS headers, but doing it
in aisotropyd keeps the allowlist next to the auth model and guarantees the header is present on the
SSE routes even when the proxy is a pass-through. If your proxy already adds CORS headers, leave
`AISOTROPY_CORS_ALLOWED_ORIGINS` unset to avoid duplicate `Access-Control-Allow-Origin` headers.

## The before/after finding (why the middleware exists)

The header is not cosmetic. **Before** the `sseAntiBuffering` middleware, the native `/chat/stream`
route was **not proxy-safe**: it is served by Huma's `sse.Register`, whose response body function
aisotropy does not control — it emitted only `Content-Type: text/event-stream` and **no**
anti-buffering header. The Vercel AI SDK adapter (`/chat/aisdk`) likewise omitted it. So a default
nginx in front of those two routes would buffer the stream into a single dump. (The raw AG-UI adapter
already set the header by hand, so only it was safe.)

The fix ([`aisotropy/sse_buffering.go`](../../aisotropy/sse_buffering.go)) is a chi middleware wired via
`router.Use` in `app.go`. It runs before any bytes are written, so `X-Accel-Buffering: no` lands in the
header block flushed on the first event, covering **all three** SSE routes uniformly — including the
one whose handler is out of reach. Non-streaming JSON routes are left untouched.

## Executable proof — `test/proxy/`

The claim above is not asserted from a document; it is proven by a self-contained end-to-end test under
[`test/proxy/`](../../test/proxy/). Run it with:

```bash
test/proxy/run.sh
# or:  go test -tags proxy ./test/proxy/ -v -count=1 -timeout 300s
```

It stands up (via `docker compose`) one `aisotropyd` (keyless stub LLM + NoAuth — the zero-config boot)
fronted by three proxies on one network, then drives `POST …/chat/stream` through each and captures
**per-chunk arrival timestamps**:

| Proxy | Config | Assertion | Measured (representative run) |
|---|---|---|---|
| Caddy | `deploy/proxy/Caddyfile` | streams: ≥3 reads, first→last span > 100ms | **50 reads over ~790ms** ✓ |
| nginx | `deploy/proxy/nginx.conf` (shipped reference) | streams: ≥3 reads, span > 100ms | **50 reads over ~780ms** ✓ |
| nginx | `test/proxy/nginx-buffered.conf` (CONTROL — buffers on purpose) | buffers: single dump, span ≤ 50ms | **1 read, 0ms span** ✓ |

The stub LLM emits ~30 words at 15ms spacing, so a live stream arrives as ~50 reads spread over
hundreds of ms; a buffered response arrives as one dump with a near-zero span. That timing signature is
the proof buffering is OFF end-to-end.

### The control has teeth — and an honest caveat

The control (`nginx-buffered.conf`) exists to prove the streaming assertion would catch a regression.
It reproduces a real operator footgun and single-dumps the stream. **The honest finding while building
it:** `proxy_buffering on` **alone is not enough** to break this stream. aisotropyd flushes every token,
and the 15ms-per-token pacing makes the upstream socket would-block after each token; modern nginx
forwards its partial buffer to the client on would-block, so a per-event-flushing upstream *survives*
plain proxy buffering (and survives even `postpone_output`, which the flush flag bypasses). To force a
genuine single dump the control adds **`gzip on` for `text/event-stream`**: the gzip body filter must
accumulate the whole response to compress it and does not honor the upstream flush. This is not
contrived — enabling gzip site-wide is one line, and every browser sends `Accept-Encoding: gzip`, so
this misconfiguration really does break SSE for real users. The control also `proxy_ignore_headers
X-Accel-Buffering`, demonstrating that once buffering truly engages, the upstream header is the thing
that would otherwise have saved the stream.

The test **skips cleanly** (never a fake pass) when docker or the compose plugin are unavailable, and
tears down all containers, volumes, and the built image on exit.
