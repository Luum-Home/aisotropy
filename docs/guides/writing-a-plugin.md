---
type: Guide
title: Writing a plugin
description: Ship a capability to Aisotropy without recompiling the core — the plugin manifest, the trust model, scope-gating, and the license gate, with a runnable MCP template.
tags: [guides, howto, plugins, mcp, extension]
timestamp: 2026-07-16T00:00:00Z
---

# Writing a plugin

A **plugin** adds capability to Aisotropy — tools and personas — that the runtime
loads from a manifest instead of your having to fork and recompile the core. This
guide is the author's field manual: the manifest fields, the trust model, how
scope-gating protects a tenant, and the license gate. It pairs with a runnable
starting point you copy: [`examples/plugin-template`](../../examples/plugin-template).

> What exists today: the **manifest schema, the plugin loader, and the plugin
> registry** (directory scan + single-file load, wired into the runtime via
> `WithPlugin` / `WithPluginDir` / `AISOTROPY_PLUGIN_DIR`) ship in this change. An
> `aisotropy plugins` CLI and a WASM strong-sandbox tier are **named follow-ons**,
> not yet shipped (see [Trust model](#trust-model) and [Point Aisotropy at your
> plugin](#point-aisotropy-at-your-plugin)); this guide flags what is shipping vs.
> planned inline so nothing here overpromises.

## The two kinds of plugin

| `kind` | What it is | Language | When |
|---|---|---|---|
| `mcp` | An out-of-process [Model Context Protocol](https://modelcontextprotocol.io) server the runtime launches as a subprocess and talks to over stdio | **Any** | Third-party / community plugins — the polyglot, no-recompile path (ADR-0016 lever #2). **Start here.** |
| `go-import` | A first-party Go plugin whose handlers are linked in at compile time; the manifest contributes governance-scoped tool descriptors | Go only | Audited, trusted, in-tree code that needs native speed and direct access |

A third value, `wasm`, is a **reserved** enum for a future in-process WebAssembly
sandbox: a manifest may declare it (it validates), but the loader returns a clear
"not yet supported" error — nothing runs. Do not ship on `wasm` yet.

The template — and everything below — centers on `kind: mcp`, because it is the
path a community author actually ships on: no Go, no rebuild of Aisotropy, just a
subprocess speaking a small protocol.

## The manifest

Every plugin is a directory containing `aisotropy-plugin.yaml`. The filled-in
template is [`examples/plugin-template/aisotropy-plugin.yaml`](../../examples/plugin-template/aisotropy-plugin.yaml);
here is what each field means.

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Stable plugin id used by the registry (and the planned `aisotropy plugins` CLI). |
| `version` | string | Semver (`0.1.0`). |
| `author` | string | You — `Name <email>`. |
| `license` | string | SPDX id. Gated: must be permissive (see [License gate](#license-gate)). |
| `kind` | `mcp` \| `go-import` \| `wasm` (reserved) | Execution kind (table above). |
| `trust` | `sandboxed` \| `trusted-inproc` | The isolation tier (see [Trust model](#trust-model)). |
| `run.command` | string | Executable to launch the MCP server (e.g. `node`, `python`, `./server`). |
| `run.args` | string[] | Arguments (e.g. `["server.js"]`). Launched with the plugin dir as CWD. |
| `tools[]` | list | The tools the plugin contributes. Each: `name`, `description`, `required_scope`. |
| `personas[]` | string[] | Persona **identifiers** the plugin ships (metadata only). The literal base-prompt bodies are supplied in Go via `WithPersona` — a YAML manifest carries no runtime behavior (ADR-0002). |

Two rules keep a manifest honest:

1. **Every `tools[].name` must be a tool the server really exposes** over MCP
   `tools/list`. The name is the join key; the manifest does not invent tools,
   it declares which of the server's tools to surface and under what scope.
2. **The schema is the server's, not the manifest's.** Aisotropy pulls each
   tool's JSON Schema from the live server and validates the model's arguments
   against it before the call crosses the process boundary — a bad argument
   reflects back to the model, it never reaches your handler.

## Trust model

`trust` is a promise about isolation, and Aisotropy only lets you make the
promise the runtime can actually keep:

- **`trusted-inproc`** — for `kind: go-import` (first-party Go) **only**. A Go plugin
  is compiled into the binary and runs in the core's own address space. There is
  **no sandbox**, by construction: Go cannot reduce its own in-process privilege,
  so a Go plugin is as trusted as the core itself. This tier means "audited,
  in-tree, first-party" — it is not available to community plugins.
- **`sandboxed`** — for `kind: mcp`. The plugin runs as a **separate process**,
  isolated from the Go core's memory; it communicates only over stdio, and the
  runtime — not the plugin — owns tenant identity and scoping. This is the honest
  tier for any third-party MCP plugin today, and what the template declares.
- **WASM strong sandbox (planned)** — a future tier that runs untrusted plugin
  logic in an in-process WebAssembly sandbox with capability-based limits, giving
  strong isolation without a subprocess. It is a **named follow-on**, not
  shipping in this change; do not declare it yet.

Pick `sandboxed` for MCP plugins. `trusted-inproc` is the posture for
first-party Go (`kind: go-import`) code.

## Scope-gating

Multi-tenant isolation is Aisotropy's spine, and it extends to plugin tools
unchanged. Each tool declares a `required_scope`. A tool is offered to the model
**only when the request's granted scopes include that scope** — and a forbidden
tool never enters the model's payload at all. It is invisible, not merely
refused: the model cannot call, mention, or be tempted by a tool it was never
shown.

Concretely, the template's `word_count` requires `text:analyze`:

```bash
# granted → the model may call word_count
-d '{"prompt":"...","product_id":"demo","session_id":"s1","scopes":["text:analyze"]}'

# not granted → word_count is never in the payload
-d '{"prompt":"...","product_id":"demo","session_id":"s1","scopes":[]}'
```

This is the **same** gate native and MCP-server tools already pass through — a
plugin tool is not a privileged back door. Leave `required_scope` empty only for a
tool that is genuinely safe for every request.

## License gate

Aisotropy's dependency stack is deliberately 100% permissive (MIT / Apache-2.0 /
BSD / ISC), and a plugin is a dependency. The manifest's `license` field is
gated: a plugin declaring a copyleft or source-available license — GPL, AGPL,
BSL, SSPL, ELv2, and the like — is **refused at load**. Declare a permissive
license (`MIT` in the template) and make sure your plugin's own dependencies
honor it too.

## Point Aisotropy at your plugin

Three mechanisms ship in this change, all on the `aisotropy.App` builder (or its
`AISOTROPY_PLUGIN_DIR` env equivalent) — Aisotropy parses and validates the
manifest, launches an `mcp` server, and registers the tools scope-gated:

```go
// In-code, when you build the App (docs/guides/build-your-own-agent.md):
app := aisotropy.New(
    aisotropy.WithPlugin("./your-plugin/aisotropy-plugin.yaml"), // one manifest file
    aisotropy.WithPluginDir("./plugins"),                        // scan a directory
)
```

```bash
# Or by environment — one directory scanned at boot, no code change:
export AISOTROPY_PLUGIN_DIR=/absolute/path/to/your-plugin
# Aisotropy reads aisotropy-plugin.yaml, launches the server, registers the tools.
```

> **Planned (not yet shipped):** an `aisotropy plugins` CLI (`add` / `list` /
> `--help`) that manages a persistent registry from the command line. The loader,
> registry, and the `WithPlugin` / `WithPluginDir` wiring above ship now; the CLI
> wrapper is a named follow-on. Until it lands, use the options or the env var.

Run keyless the whole time: with no provider key set, the stub LLM exercises the
full path (validation → scope-gated tools → your subprocess → memory) so you can
verify wiring before spending a token.

## Next steps

- Copy [`examples/plugin-template`](../../examples/plugin-template) and follow its
  README's "Make it yours" checklist.
- For the in-process (Go) extension surface a first-party author uses instead,
  see [Build your own agent](build-your-own-agent.md).
- For mapping an open `SKILL.md` into Aisotropy primitives, see
  [Importing Agent Skills](importing-agent-skills.md).
