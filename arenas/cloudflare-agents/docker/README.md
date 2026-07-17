# Cloudflare Agents arena — containerized smoke

Docker image that boots `wrangler dev` (which embeds real `workerd` + Durable
Objects via Miniflare) inside a container, then drives the per-Durable-Object
isolation smoke over HTTP and asserts every response. Offline, no API key.

```bash
./docker/run.sh          # build -> boot wrangler dev (workerd) -> drive HTTP smoke -> assert -> cleanup
```

## Official source

- **workerd (the runtime):** <https://github.com/cloudflare/workerd> — "a
  JavaScript / Wasm server runtime based on the same code that powers Cloudflare
  Workers"; a stated use case is running Workers apps "on your own
  infrastructure".
- **Local development (`wrangler dev` = workerd via Miniflare, same runtime as
  production):** <https://developers.cloudflare.com/workers/development-testing/>
- **Wrangler commands (`wrangler deploy` → the edge):** <https://developers.cloudflare.com/workers/wrangler/commands/>

## How this image relates to the official guidance — and the honest deploy caveat

**The true deploy target of a Cloudflare Worker/Agent is Cloudflare's edge, via
`wrangler deploy` — not a Docker image.** A Worker is uploaded to Cloudflare's
network and executed there by `workerd`; there is no container in the production
path. So unlike a normal server, "the Official Docker image" for this arena is
not a deploy artifact.

What Docker *is* good for here is the **self-host / local-fidelity runtime**.
`workerd` is Cloudflare's Official open-source Workers runtime, and Cloudflare
documents that `wrangler dev` runs that **same** `workerd` locally (through
Miniflare) as runs in production at the edge. Running `workerd`-in-Docker is an
officially-supported `workerd` use case ("run applications … on your own
infrastructure"). This image therefore uses `wrangler dev` as the **container
stand-in for the edge runtime**: real `workerd`, real Durable Objects, real
per-object embedded SQLite — reproducibly, in CI — but it is explicitly *not* the
production edge.

What the container **cannot** reproduce (rented Cloudflare-platform properties):
global Durable-Object uniqueness and platform-managed durability. Those live only
on Cloudflare's edge; see the arena README "Sovereignty caveat". The container
proves the runtime + isolation semantics run identically off-platform; it does
not claim to be the edge.

Base-image / packaging notes: `node:22-bookworm-slim` (Debian/glibc — `workerd`
links glibc and does not run cleanly on musl/Alpine); a fresh in-image
`pnpm install` pulls the Linux-native `@cloudflare/workerd-linux-*` binary rather
than reusing the host's darwin build. Permissive bases only, no adopter branding.
The top-of-`Dockerfile` note records the same rationale inline.
