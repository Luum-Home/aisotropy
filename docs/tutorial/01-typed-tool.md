---
type: Tutorial
title: "01 — A typed tool"
description: Register a tool whose arguments are a Go struct with WithTypedTool — one struct drives the model's schema, the decode, and fail-closed reflection.
tags: [tutorial, tools, typed, struct-as-contract]
timestamp: 2026-07-16T00:00:00Z
---

# 01 — A typed tool

**Goal:** give the agent a capability. A typed Go function is the whole ergonomic surface.

`WithTypedTool` registers a tool whose arguments are a Go struct `T`. This is the
[struct-as-contract](../concepts.md#5-the-struct-is-the-single-contract-anti-magic) idea applied to
tool inputs: the **same struct** drives three things at once —

1. the **JSON Schema** the model is shown (from the struct's `jsonschema` tags),
2. the **decode** of the model's returned arguments into a `T`, and
3. **fail-closed reflection**: if the model's arguments don't fit `T`, the runtime hands the error
   back to the model to correct itself — it never coerces bad arguments.

```go
type getWeatherArgs struct {
	City  string `json:"city"  jsonschema:"description=the city to look up"`
	Units string `json:"units" jsonschema:"enum=c,enum=f,description=temperature units"`
	Days  int    `json:"days"  jsonschema:"minimum=1,maximum=14,description=forecast horizon in days"`
}

func getWeather(ctx context.Context, args getWeatherArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx) // read-only identity, never constructed
	return fmt.Sprintf("Weather for %q in %s: 22°C, clear.", tenant, args.City), nil
}

app := aisotropy.New(
	// "read:weather" is the required scope — the model is offered this tool only
	// when the request's granted scopes include it (empty string = always available).
	aisotropy.WithTypedTool("get_weather", "get the current weather for a city", "read:weather", getWeather),
	aisotropy.WithPersona("weatherbot", aisotropy.Persona{
		BasePrompt: "You are WeatherBot. Answer weather questions concisely and cite the city.",
	}),
)
```

Two things to notice. The handler reads the caller's tenant with `TenantFromContext` — the only
identity a tool may touch, and **read-only**: there is no way to construct a memory scope from a
handler. And the third argument, `"read:weather"`, is a **scope gate** — you'll wire real scopes to it
in [lesson 04](04-auth-scopes.md).

The non-string `Days int` field is the payoff of *typed*: it survives on the native tool-calling path
(a real provider). On the keyless stub's structured-output fallback, arguments arrive flattened to
strings, so only string-shaped fields are guaranteed there — the documented 0.x limitation.

**Runnable example:** [`examples/weatherbot`](../../examples/weatherbot) — this exact tool, wired into
a booting runtime.

**Depth:** [ADR-0016 — dual-surface: sealed runtime + composition SDK](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md)
(lever #1, typed tools).

Next: [02 — Memory & sessions](02-memory-sessions.md).
