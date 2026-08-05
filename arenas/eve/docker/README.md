# eve arena — Docker smoke

Containerized build+boot of the eve arena. Builds the real eve app (`npm install` +
`eve build`) and boots the compiled Nitro server (`.output/server/index.mjs`) with an
offline deterministic mock model — no API key, no network at runtime. Additive to the
native run in `../README.md`. Run with `./run.sh` (skips cleanly if Docker is
unavailable).

## Relation to eve's official packaging

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md -->
**Honest finding: eve publishes no official Dockerfile and no official *server*
image.** That narrow claim survived the 2026-08-04 five-point sweep. The three
supporting sentences that used to stand here did **not**, and are corrected below.

- **"Its documented deployment target is Vercel" — WRONG.** `docs/guides/deployment.md`
  §8 is titled *"Deploy without Vercel"* and states eve "can also run as a normal Node
  service behind your own process manager, **container platform**, or reverse proxy",
  with the exact commands (`eve build`; `PORT=3000 eve start --host 0.0.0.0`) plus a
  self-host checklist (persist `.eve/.workflow-data`, forward **both** `/eve/` and
  `/.well-known/workflow/`, swap `vercelOidc()`). That is first-party self-host guidance.
- **"eve agents compile to Vercel Functions" — WRONG as an unconditional.** Same doc,
  *"How portability works"*: Nitro is the HTTP host layer; the Vercel Build Output bundle
  is written **only when `VERCEL` is set**. A plain `eve build` skips it.
- **"Docker appears in eve's docs only as a local sandbox backend" — WRONG.**
  `deployment.md:148` lists Docker as a supported sandbox backend **for self-deployed
  processes**, i.e. inside the non-Vercel deployment guidance, not only in `sandbox.mdx`.

What remains true and is the reason this image exists: there is no `Dockerfile` in the
eve repo for the *server*, and the one published image, `ghcr.io/vercel/eve:latest`, is
described by eve's own docs as "eve's published **sandbox** runtime image"
(`docs/sandbox.mdx:150`) — a sandbox for agent-run code, not a server image.

**Best-available official source:** (build/serve contract)
<https://github.com/vercel/eve> — README: `eve build` produces a self-contained
**Nitro `.output/` app**, and `eve start` "serve[s] the built `.output/` app"
(runtime is "Nitro + Workflows"). The deployment guide, which covers **both** the
Vercel and the non-Vercel path, is <https://eve.dev/docs/guides/deployment>.

**How this image relates:** absent an official Dockerfile, this image boots eve's own
official build artifact — `node .output/server/index.mjs`, i.e. exactly what
`eve start` serves — inside a plain `node:24` container (eve mandates Node ≥ 24). This
is the reasonable default for a container smoke, and it exercises the real compiled
eve server rather than a stand-in.
