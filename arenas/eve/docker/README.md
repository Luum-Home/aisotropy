# eve arena — Docker smoke

Containerized build+boot of the eve arena. Builds the real eve app (`npm install` +
`eve build`) and boots the compiled Nitro server (`.output/server/index.mjs`) with an
offline deterministic mock model — no API key, no network at runtime. Additive to the
native run in `../README.md`. Run with `./run.sh` (skips cleanly if Docker is
unavailable).

## Relation to eve's official packaging

**Honest finding: eve has NO official Docker guidance.** Its documented deployment
target is **Vercel** — eve agents compile to Vercel Functions, and Docker appears in
eve's docs only as a *local sandbox backend*, not as a server image.

**Best-available official source:** (build/serve contract)
<https://github.com/vercel/eve> — README: `eve build` produces a self-contained
**Nitro `.output/` app**, and `eve start` "serve[s] the built `.output/` app"
(runtime is "Nitro + Workflows"). The Vercel-targeted deployment guide is
<https://eve.dev/docs/guides/deployment>.

**How this image relates:** absent an official Dockerfile, this image boots eve's own
official build artifact — `node .output/server/index.mjs`, i.e. exactly what
`eve start` serves — inside a plain `node:24` container (eve mandates Node ≥ 24). This
is the reasonable default for a container smoke, and it exercises the real compiled
eve server rather than a stand-in.
