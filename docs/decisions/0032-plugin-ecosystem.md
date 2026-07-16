---
type: ADR
title: "ADR-0032 — Plugin ecosystem: a declarative manifest + loader that grows the toolset without forking, MCP as the polyglot community path"
description: ADR-0016 named "grow the toolset without editing the core" as the thesis and delivered its first two levers (typed tools, MCP tools) as in-tree builder options. This ADR turns that into an ECOSYSTEM a third party ships into without a Go compiler or a PR against the core: a declarative `aisotropy-plugin.yaml` manifest (governance metadata + a scope-gated tool list), an `internal/plugin` parser/validator/loader/registry, and the `WithPlugin` / `WithPluginDir` / `AISOTROPY_PLUGIN_DIR` wiring that merges a validated plugin's tools into the runtime through the SAME MCP path and the SAME double scope-gate native and MCP tools already pass. `kind: mcp` is the deliberate community path — piggyback the existing Model Context Protocol ecosystem (any language, out-of-process) rather than invent a proprietary plugin format; `kind: go-import` is a first-party compile-time convention (Go has no safe runtime plugin loading, so the manifest contributes governance descriptors while behavior arrives via a normal Go import); `kind: wasm` is a RESERVED enum that validates but does not run. A permissive license allowlist (MIT/BSD/Apache/ISC) is enforced at load — a copyleft or source-available plugin is refused, because a plugin the core surfaces tools from is part of the deployed surface. HONEST TRUST MODEL: `trusted-inproc` Go has NO sandbox (Go cannot reduce its own in-process privilege) and is first-party-audited-only; `sandboxed` is an MCP SUBPROCESS today; a WASM/wazero in-process strong sandbox is the named strong follow-on, reserved but not built. HONEST SCOPE: an `aisotropy plugins` CLI is a named follow-on — the manifest, loader, registry, and builder wiring ship; the CLI wrapper does not.
tags: [decisions, adr, plugins, ecosystem, mcp, manifest, scope-gating, license-gate, trust-model, wasm, open-core, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0032 — Plugin ecosystem: a declarative manifest + loader that grows the toolset without forking, MCP as the polyglot community path

## Status

✅ **Accepted — 2026-07-16.** Delivers the third-party **plugin ecosystem** that
[ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) pointed at ("grow the toolset
without editing the core") but had realized only as *in-tree* builder options (its lever #1 typed
tools, lever #2 MCP tools). This ADR adds the surface a party **outside the repo** ships into without
a Go compiler or a PR: a declarative `aisotropy-plugin.yaml` manifest, the `internal/plugin`
parser/validator/loader/registry, and the public `WithPlugin` / `WithPluginDir` /
`AISOTROPY_PLUGIN_DIR` wiring that merges a validated plugin's tools into the runtime. It **extends**
ADR-0016 (does not supersede it) and sits above [ADR-0013](0013-public-extension-surface-facade-vs-promote.md)'s
façade. It does **not** open the isolation-critical memory backends — that tiering is the sibling
[ADR-0033](0033-open-core-extension-seams.md).

Everything here is **additive and default-safe**: no new dependency (the manifest parser reuses the
already-vendored `goccy/go-yaml`; MCP launch reuses the same gollem stdio path
[`WithMCPServer`](../../aisotropy/mcp.go) already runs), no OpenAPI contract change, and a runtime
with no `WithPlugin` / `WithPluginDir` and an unset `AISOTROPY_PLUGIN_DIR` boots byte-for-byte as it
did before.

## Context

ADR-0016 committed Aisotropy to being **both** a sealed multi-tenant runtime **and** a composable
framework, and set the thesis that the toolset grows *without editing the core*. Its first two levers
landed as builder options: typed tools (`WithTool`) and MCP tools (`WithMCPServer`). But both still
require the extender to **edit Go and rebuild the binary** — `WithMCPServer("scope", "node",
"server.js")` is a line of Go in the composition root. That is the right ergonomics for the operator
assembling their own bot; it is **not** an ecosystem. A community author who wants to publish "the
word-tools plugin" has no artifact to ship: no declarative unit a deployer can drop in, inspect,
license-check, and load without touching the core's source.

Three constraints shaped what that artifact could be:

1. **Anti-magic (ADR-0002).** A plugin manifest must carry **values and references only** — a name, a
   version, a license, a command to launch, a scope-gated tool list — never in-process Go behavior. A
   YAML file cannot hold a Go func, and we will not invent a mechanism that pretends it can (no
   reflection-loaded handlers, no silent prompt injection). Behavior for an out-of-process plugin
   lives in the external process the manifest merely names; behavior for an in-tree Go plugin arrives
   via a normal Go import.

2. **Go has no safe runtime plugin loading.** The stdlib `plugin` package is Linux/macOS-only,
   famously brittle across compiler/version/flag skew, and unusable as a cross-platform community
   distribution mechanism. So a "Go plugin" cannot be a *dynamically loaded* `.so` — it can only be a
   *documented compile-time convention*: the manifest supplies governance-scoped descriptors for
   discovery and scope-audit, while the handlers are linked in by the consumer's own Go import.

3. **A plugin is part of the deployed surface, so it inherits the constitutional gates.** Its tools
   must pass the **same double scope-gate** native and MCP tools pass (a forbidden tool never enters
   the model's payload). Its license must clear the **same permissive bar**
   (`.claude/rules/constitutional-gates.md`, [license-policy](../../.claude/rules/cos/license-policy.md))
   as any dependency — a copyleft or source-available plugin surfaced by the core would drag the
   whole service under that license's obligations.

The open question: **what is the smallest declarative artifact that lets a third party ship a
capability, and how does the runtime load it without violating anti-magic, the scope-gate, or the
license gate — while reusing the MCP execution path already in the tree rather than inventing a
proprietary format?**

## Goals

- A **declarative manifest** (`aisotropy-plugin.yaml`) as the single unit of a capability bundle:
  governance metadata (name, version, author, license, trust posture) plus a scope-gated tool list —
  values and references only, never behavior.
- A **loader** that turns a validated manifest into the **exact shapes the runtime already speaks**
  (a `[]tools.Tool` list + typed handlers), so a plugin tool is indistinguishable from a native or
  MCP tool downstream — including at the scope gate.
- **MCP as the polyglot community path**: reuse the existing Model Context Protocol ecosystem and the
  runtime's existing MCP launch/discovery, so a plugin can be written in **any** language and needs
  **no** Aisotropy recompile — piggybacking a live standard instead of minting a proprietary one.
- A **license allowlist enforced at load** (MIT / BSD-2/3-Clause / Apache-2.0 / ISC), rejecting
  copyleft and source-available licenses, because a surfaced plugin is part of the deployed surface.
- An **honest trust model** that only lets a manifest promise the isolation the runtime can actually
  keep, and names the WASM strong-sandbox tier as a reserved follow-on rather than pretending it
  exists.
- **Additive, default-safe wiring** on the public builder (`WithPlugin` / `WithPluginDir` +
  `AISOTROPY_PLUGIN_DIR`): a runtime that registers no plugin is byte-for-byte unchanged, and a
  manifest that fails to parse/validate/load is logged and skipped, never downing the runtime.

## Non-goals

- **NOT** a proprietary plugin execution format. The polyglot path **is** MCP — a live, external
  standard with a growing server ecosystem. Inventing an Aisotropy-specific RPC would fragment the
  author's effort and gain nothing; reusing MCP means an existing MCP server *is already* a plugin.
- **NOT** dynamic Go plugin loading. `kind: go-import` is a documented compile-time **convention**,
  not a runtime `.so` load: the manifest contributes governance-scoped tool **descriptors** (so they
  can be surfaced and scope-audited), while the handlers arrive via the consumer's Go import. A caller
  that surfaces the descriptors without also linking the Go handlers will have tools the model can see
  but not execute — stated plainly in the loader, not faked.
- **NOT** a runnable `kind: wasm` yet. `wasm` is a **reserved** enum value: it passes `Validate` (so a
  manifest can declare intent today) but `LoadTools` returns `ErrKindUnsupported` — nothing executes.
  The in-process WebAssembly sandbox (wazero) is the named strong-isolation follow-on, deliberately
  not built in this cut.
- **NOT** an `aisotropy plugins` CLI. The manifest schema, the loader, the registry (directory scan +
  single-file parse), and the builder wiring ship in this change; a command-line wrapper that manages
  a persistent registry (`plugins add` / `plugins list`) is a **named follow-on**. Loading today is
  via `WithPlugin` / `WithPluginDir` / `AISOTROPY_PLUGIN_DIR`.
- **NOT** loosening the scope gate or the license gate for plugins. A plugin tool passes the identical
  scope gate as any tool; a non-permissive license is refused at load, not warned about.
- **NOT** opening the isolation-critical memory backends (facts / semantic / working-memory stores)
  as freely-implementable plugin interfaces. That is deliberately a separate, sealed-driver design —
  [ADR-0033](0033-open-core-extension-seams.md).

## Options considered

### The plugin execution format

- **A proprietary Aisotropy plugin RPC / ABI.** Rejected — it would ask every community author to
  learn a bespoke protocol, and it would compete with, rather than ride, the MCP ecosystem the runtime
  already speaks (ADR-0016 lever #2). A standard we did not have to invent, with servers that already
  exist, is strictly better leverage.
- **Dynamic Go plugin `.so` loading (stdlib `plugin`).** Rejected — Linux/macOS-only, brittle across
  toolchain skew, and hostile to cross-platform community distribution. It also cannot satisfy
  anti-magic honestly (a loaded `.so` *is* opaque in-process behavior).
- **A declarative manifest that names an MCP subprocess (for polyglot) plus a documented Go-import
  convention (for first-party) (CHOSEN).** `kind: mcp` launches the server the manifest names and
  discovers its tools via the shared `api.BuildMCPTools` path
  ([`internal/plugin/loader.go`](../../internal/plugin/loader.go)); `kind: go-import` surfaces the
  manifest's declared tools as governance descriptors with empty handlers (behavior comes from the Go
  import). Both are honest about where behavior lives.

### Where a plugin's Go behavior can come from

- **Load it dynamically at runtime.** Rejected (above) — unsafe/unportable in Go, and dishonest under
  anti-magic.
- **Contribute descriptors at manifest-load, wire handlers at compile-time (CHOSEN).** `loadGoImport`
  returns the declared tools scope-gated with **empty** `Handlers`; the consumer's Go import registers
  the actual handlers. The loader states this convention in its doc comment ("a caller that surfaces
  these descriptors without also registering the Go handlers will have tools the model can see but not
  execute") rather than papering over the gap.

### The isolation posture a manifest may declare

- **Let a manifest claim any `trust` value it likes.** Rejected — a manifest that declares
  `trusted-inproc` for an out-of-process MCP plugin, or `sandboxed` for an in-process Go plugin, would
  be a lie the runtime cannot keep. Trust is a promise about isolation; the format must only allow the
  promises the mechanism actually delivers.
- **A validated `Trust` enum, honest about what each tier means (CHOSEN).** `trusted-inproc` is
  first-party Go with **no sandbox by construction** (Go cannot reduce its own in-process privilege);
  `sandboxed` is an MCP subprocess isolated from the Go core's address space. The enum validates, and
  the guide + template steer MCP plugins to `sandboxed`. The WASM tier that would give strong
  in-process isolation is named as reserved, not offered.

### The license posture

- **Warn on a non-permissive license but load anyway.** Rejected — a surfaced plugin is part of the
  deployed surface, so a copyleft/source-available plugin is a repo-wide license-gate violation
  (constitutional), not an advisory. A governance surface that quietly loaded it would defeat its own
  purpose.
- **Reject at load against a permissive allowlist (CHOSEN).** `Validate` normalizes the declared
  license and checks it against `permittedLicenses` (MIT / ISC / BSD-2/3-Clause / Apache-2.0);
  anything else — and an empty license — fails the manifest at parse time
  ([`internal/plugin/manifest.go`](../../internal/plugin/manifest.go)).

## Decision

### 1. The manifest — `aisotropy-plugin.yaml`, values and references only

`plugin.Manifest` ([`internal/plugin/manifest.go`](../../internal/plugin/manifest.go)) is the parsed
declarative surface: `Name`, `Version`, `Author`, `License`, `Description`, `Kind`, `Trust`, a
`Run{Command, Args}` subprocess spec (for `mcp`), a `Tools[]` list of `{Name, Description,
RequiredScope}` (yaml key `required_scope`), and `Personas []string` — persona **identifiers only**,
metadata the runtime records for discovery, with the literal base-prompt bodies supplied in Go via
`WithPersona` (a YAML file carries no runtime behavior, ADR-0002). `ParseManifest` reads and
`Validate`s in one gate, so every downstream consumer (registry, loader) can assume a well-formed
manifest: `Name` / `Version` / `License` required, `License` permissive, `Kind` one of
`mcp`/`go-import`/`wasm`, `Trust` empty-or-valid, and `run.command` required for `kind: mcp`.

### 2. `Kind` — three values, two runnable, one reserved

- **`mcp` (KindMCP).** The polyglot community path. `LoadTools` connects the server named by
  `run.command`/`run.args` through an **injected** `MCPConnector` (so `internal/plugin` imports no MCP
  client library and no public package), discovers its tools with the shared `api.BuildMCPTools`, and
  returns them scope-gated — the identical path `WithMCPServer` runs, so the resulting shapes are the
  same. A discovery failure after connect closes the subprocess before returning, so a failed load
  never leaks a process.
- **`go-import` (KindGoImport).** The first-party compile-time convention. `loadGoImport` returns the
  manifest's declared tools as scope-gated governance descriptors with **empty** handlers — the Go
  import the consumer links supplies the behavior. It never fails.
- **`wasm` (KindWASM).** **Reserved.** `Validate` accepts it (a manifest can declare intent), but
  `LoadTools` returns `ErrKindUnsupported` — nothing runs. The wazero in-process sandbox is the named
  follow-on.

### 3. The loader and registry — validated manifests to runtime shapes

`plugin.LoadTools(ctx, m, connect)` ([`internal/plugin/loader.go`](../../internal/plugin/loader.go))
dispatches on `Kind` and returns a `Loaded{Tools, Handlers, Close}` — the exact shapes the core
assembles (a `tools.Tool` list + `map[string]api.TypedToolHandler`), with a never-nil `Close` so a
caller can always `defer loaded.Close()`. `plugin.Load(dir)`
([`internal/plugin/registry.go`](../../internal/plugin/registry.go)) scans a directory
non-recursively for `*plugin*.yaml` / `*plugin*.yml`, parses+validates each in lexical order, and
returns an immutable `Registry` — strict: a malformed or non-permissive manifest makes `Load` return
an error naming the file, never a silent skip. The dependency direction is deliberate: `internal/plugin`
imports `internal/tools` and `internal/api` to reuse their shapes but **never** the public `aisotropy`
package — the public builder wires plugins in, not the other way round.

### 4. The scope gate — plugin tools are not a privileged back door

Every plugin tool carries its `RequiredScope`, so it passes the **same** gate native and MCP tools
pass: `Registry.Permitted(scopes)` ([`internal/tools/tools.go`](../../internal/tools/tools.go)) offers
a tool to the model **only** when the request's granted scopes include its required scope — a forbidden
tool never enters the model's payload (invisible, not merely refused). For an `mcp` plugin the single
server-wide scope is `mcpScope(m)` (the server is the source of truth for its own tool list, matching
`WithMCPServer`'s homogeneous-scope model); for a `go-import` plugin each declared tool's
`required_scope` gates it individually.

### 5. The license gate — permissive-only, enforced at load

`Validate` rejects a manifest whose `License` is empty or does not clear the permissive allowlist
(`normalizeLicense` folds spelling/punctuation variants, so "Apache 2.0" / "Apache-2.0" / "apache2.0"
all match one key). Copyleft (GPL/LGPL/AGPL/MPL) and source-available (BSL/SSPL/ELv2/Commons Clause/FSL)
are absent from the allowlist and therefore refused — the same posture as the repo-wide dependency
license gate, applied to plugins because a surfaced plugin *is* a dependency.

### 6. Trust model — honest about what isolation each tier delivers

`plugin.Trust` is validated metadata, not an enforced sandbox:

- **`trusted-inproc`** — first-party Go (`kind: go-import`) only. The plugin is compiled into the
  binary and runs in the core's own address space. There is **no sandbox, by construction**: Go cannot
  reduce its own in-process privilege, so an in-process Go plugin is exactly as trusted as the core.
  This tier means "audited, in-tree, first-party" and is not a posture a community plugin can honestly
  claim.
- **`sandboxed`** — for `kind: mcp`. The plugin runs as a **separate process**, isolated from the Go
  core's memory, communicating only over stdio, with the runtime — not the plugin — owning tenant
  identity and scoping. This is the honest tier for any third-party MCP plugin **today**, and what the
  template declares.
- **WASM strong sandbox — reserved follow-on.** An in-process WebAssembly sandbox (wazero) with
  capability-based limits would give strong isolation **without** a subprocess. It is the named strong
  follow-on, tied to `kind: wasm`; until it lands, "strong isolation" means "separate process".

### 7. Wiring — additive, default-safe, on the public builder

`WithPlugin(path)` registers a single manifest file; `WithPluginDir(dir)` registers a directory
scanned at boot; `AISOTROPY_PLUGIN_DIR` appends one more such directory
([`aisotropy/extend.go`](../../aisotropy/extend.go), wired in
[`aisotropy/app.go`](../../aisotropy/app.go)'s `startRuntime`). At boot the runtime parses the
`WithPlugin` files and `Load`s the directories, then `LoadTools` each manifest through an injected
`MCPConnector` that adapts gollem's stdio MCP client — the same one the `WithMCPServer` discovery loop
uses. A plugin's subprocess is owned by the runtime and closed at teardown. **Inert-safe**: a manifest
that fails to parse/validate/load is logged (`slog.Error`) and skipped, never downing the runtime; no
plugin options and an unset `AISOTROPY_PLUGIN_DIR` boot the pre-plugin composition root unchanged.
Declared `personas` are logged as metadata only ("supply prompt bodies via `WithPersona`").

### Honest scope — what ships vs. what is named-but-pending

- **Ships:** the manifest schema + validation, the loader (`mcp` + `go-import`), the registry (dir
  scan + single-file parse), the `WithPlugin` / `WithPluginDir` / `AISOTROPY_PLUGIN_DIR` wiring, the
  scope gate and license gate over plugin tools, the runnable MCP template
  ([`examples/plugin-template`](../../examples/plugin-template)), and the author guide
  ([`docs/guides/writing-a-plugin.md`](../guides/writing-a-plugin.md)).
- **Named follow-ons (honest gaps):** the `aisotropy plugins` CLI (`add` / `list` / `--help`) that
  would manage a persistent registry from the shell — **not shipped**; loading is via the options / env
  var. The `kind: wasm` in-process strong sandbox (wazero) — **reserved, not built**. Wiring the
  `go-import` handler-registration convention into a turnkey helper — a compile-time step the consumer
  does today by hand.

## Consequences

- **A third party ships a capability without forking or recompiling the core.** An `aisotropy-plugin.yaml`
  plus an MCP server (any language) is a complete, inspectable, license-checked, scope-gated unit the
  runtime loads via a builder option or an env var. This is ADR-0016's "grow the toolset without editing
  the core" made real for people outside the repo.
- **The ecosystem is MCP's, not a proprietary one.** Because `kind: mcp` reuses the Model Context
  Protocol path already in the tree, an existing MCP server is already an Aisotropy plugin — the author's
  effort compounds with a live standard rather than a bespoke format.
- **Plugin tools inherit the constitutional gates.** The scope gate and the permissive license gate
  apply to a plugin exactly as to a native tool or a core dependency — a plugin is not a privileged back
  door, and a copyleft/source-available plugin is refused at load.
- **The trust model is honest, and its strongest tier is named-not-faked.** `trusted-inproc` Go has no
  sandbox and is first-party-only; `sandboxed` means a subprocess today; the WASM in-process sandbox is
  reserved (`kind: wasm` validates but does not run). A reader cannot mistake a subprocess for a
  capability-limited sandbox, nor a Go import for isolated code.
- **No new dependency, no contract break, default-safe.** The parser reuses the vendored YAML library;
  MCP launch reuses the existing stdio path; a plugin-free runtime is byte-for-byte unchanged; a bad
  manifest is logged and skipped, not fatal.
- **A named non-goal-for-now: WASM.** The wazero in-process strong sandbox is the intended path to
  running *untrusted* plugin logic with strong isolation and no subprocess. It is explicitly reserved and
  not in this cut; until it lands, do not treat a `wasm` plugin as runnable, and do not present "in-process
  plugin" as "sandboxed".

## References

- **Extends:** [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) (dual-surface / sealed
  kernel + composition SDK; "grow the toolset without editing the core", MCP lever #2 — this ADR turns
  that into a third-party ecosystem) · [ADR-0013](0013-public-extension-surface-facade-vs-promote.md)
  (the `aisotropy.App` façade + builder the plugin options extend)
- **Sibling:** [ADR-0033](0033-open-core-extension-seams.md) (the tiered extension seams: the SAFE
  Tier-1 aliases/options this ecosystem's tool contributions ride, and the deliberately-sealed Tier-2
  memory backends this ADR does **not** open)
- **Invariants held:** [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (anti-magic: the
  manifest carries values/references only — no Go func in YAML; `go-import` handlers wired in Go,
  `mcp` behavior in the external process) · [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md)
  / [ADR-0011](0011-tenant-scoped-statement-builder.md) (tenant isolation by construction — the runtime,
  not the plugin, owns identity and scoping) · [license-policy](../../.claude/rules/cos/license-policy.md)
  (the permissive allowlist the plugin license gate enforces)
- **Plugin core:** [`internal/plugin/manifest.go`](../../internal/plugin/manifest.go) (`Manifest`,
  `Kind`, `Trust`, `ParseManifest`, `Validate`, `permittedLicenses`) ·
  [`internal/plugin/loader.go`](../../internal/plugin/loader.go) (`LoadTools`, `Loaded`,
  `MCPConnector`, `loadMCP`, `loadGoImport`, `ErrKindUnsupported`, `mcpScope`) ·
  [`internal/plugin/registry.go`](../../internal/plugin/registry.go) (`Registry`, `Load`,
  `IsManifestFile`)
- **Wiring + reused paths:** [`aisotropy/extend.go`](../../aisotropy/extend.go) (`WithPlugin`,
  `WithPluginDir`) · [`aisotropy/app.go`](../../aisotropy/app.go) (`startRuntime` plugin-load loop,
  the injected `MCPConnector`) · [`aisotropy/mcp.go`](../../aisotropy/mcp.go) (`WithMCPServer`, the
  same MCP launch path) · [`internal/api/mcp_tools.go`](../../internal/api/mcp_tools.go)
  (`BuildMCPTools`, `MCPClient`) · [`internal/tools/tools.go`](../../internal/tools/tools.go)
  (`Registry.Permitted`, the scope gate plugin tools pass)
- **Author surface:** [`examples/plugin-template`](../../examples/plugin-template) (a runnable,
  zero-dependency MCP-server plugin: manifest + `server.js`) ·
  [`docs/guides/writing-a-plugin.md`](../guides/writing-a-plugin.md) (the author's field manual:
  manifest fields, trust model, scope-gating, license gate)
