---
type: Tutorial
title: "08 — Extend the runtime"
description: Add capabilities without forking — load plugins via a manifest, and bring your own sandbox backend by implementing SandboxProvider.
tags: [tutorial, plugins, extension, sandbox-provider, open-core]
timestamp: 2026-07-16T00:00:00Z
---

# 08 — Extend the runtime

**Goal:** add capability without recompiling the core, and swap the sandbox backend without touching
the runtime.

Aisotropy is **open-core**: a stable public façade over a sealed engine. Two extension paths matter
for most bots — plugins (new tools) and a custom sandbox backend (new isolation substrate).

## Plugins: ship tools without recompiling

A plugin is a capability declared by an `aisotropy-plugin.yaml` manifest — today, `kind: mcp` points
the runtime at an external MCP server whose tools get merged in, **scope-gated identically** to native
tools. Load them by path or by directory:

```go
app := aisotropy.New(
	aisotropy.WithPlugin("./plugins/search/aisotropy-plugin.yaml"), // one manifest by path
	aisotropy.WithPluginDir("./plugins"),                            // every manifest in a dir
)
```

(The `AISOTROPY_PLUGIN_DIR` env var adds a directory too.) Loading is best-effort: a manifest that
fails to parse or a plugin that fails to start is logged and skipped, never downing the runtime.

## A custom sandbox backend

Lesson 06's tasks run inside a `SandboxProvider`. The built-ins are `DockerSandbox()` and
`PodmanSandbox()`; bring your own by implementing the `SandboxProvider` interface (Kubernetes,
Firecracker, gVisor/Kata via the spec's `Runtime` field) and passing it per task:

```go
res, err := app.RunTask(ctx, aisotropy.TaskRequest{
	RepoDir:  "./my-repo",
	Prompt:   "…",
	Scopes:   aisotropy.SandboxToolScopes(),
	Provider: myK8sProvider, // implements aisotropy.SandboxProvider; nil defaults to DockerSandbox()
})
```

The key-broker invariant is preserved whatever the backend: the API key never enters the sandbox,
because the model call is host-side regardless of *where* the hands run. A Kubernetes provider that
creates a Pod in a per-tenant namespace maps k8s isolation cleanly onto tenant isolation.

**Runnable example:** [`examples/plugin-template`](../../examples/plugin-template) — a `kind: mcp`
plugin manifest with a tiny MCP server you can point `WithPlugin` at.

**Depth:** [writing a plugin](../guides/writing-a-plugin.md),
[ADR-0032 — plugin ecosystem](../decisions/0032-plugin-ecosystem.md),
[ADR-0033 — open-core extension seams](../decisions/0033-open-core-extension-seams.md),
[ADR-0035 — WASM plugin sandbox](../decisions/0035-wasm-plugin-sandbox.md), and the custom-provider
reference in [sandbox providers](../architecture/sandbox-providers.md).

Next: [09 — Serve over HTTP](09-serve-http.md).
