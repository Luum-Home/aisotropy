// Command weatherbot is the SMALLEST possible bot you can build on Aisotropy —
// the "hello agent" a newcomer runs first. It is one typed tool, one persona,
// and a keyless boot; nothing else.
//
// It is a SEPARATE Go module (examples/weatherbot/go.mod) with a replace
// directive pointing at the repo root, and it imports ONLY the public façade
// package github.com/Luum-Home/aisotropy/aisotropy — never any private package.
// Because it is a distinct module, Go's own internal-package rule PHYSICALLY
// forbids it from reaching into aisotropy/internal/..., so the fact that it
// compiles is the "public surface only" guarantee: if the façade were missing
// anything this bot needs, this module would not build.
//
// The bot registers one scope-gated TYPED tool (get_weather) whose arguments are
// a Go struct — the same "struct as the single contract" idea Aisotropy uses for
// HTTP/OpenAPI, now for tool inputs (ADR-0016). The struct drives BOTH the JSON
// schema the model is shown AND the decoding/validation of the model's
// arguments. A real handler reads the calling tenant from ctx (read-only, never
// constructed) and returns a canned forecast. Then it boots the full runtime
// with aisotropy.New(...).Run(ctx).
//
// Run it keyless (no API key, no Docker) — see README.md:
//
//	go run .
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// getWeatherArgs is the get_weather tool's argument contract. Its json/jsonschema
// tags drive the schema the model is shown and the decode of what it returns —
// including the non-string Days field, which a stringly-typed tool seam could
// not carry.
type getWeatherArgs struct {
	City  string `json:"city" jsonschema:"description=the city to look up"`
	Units string `json:"units" jsonschema:"enum=c,enum=f,description=temperature units"`
	Days  int    `json:"days" jsonschema:"minimum=1,maximum=14,description=forecast horizon in days"`
}

// getWeather is the typed handler: it receives the DECODED args (not a string
// map) and the request's already-scoped ctx, from which it reads the calling
// tenant (the only identity a consumer may access — read-only, never
// constructed), demonstrating tenant-aware tool logic without ever touching an
// internal memory scope. A real bot would call a weather API here.
func getWeather(ctx context.Context, args getWeatherArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	city := args.City
	if city == "" {
		city = "your area"
	}
	unit := "°C"
	if args.Units == "f" {
		unit = "°F"
	}
	days := args.Days
	if days <= 0 {
		days = 1
	}
	return fmt.Sprintf("Weather for tenant %q in %s over %d day(s): 22%s, clear skies, light breeze.", tenant, city, days, unit), nil
}

func main() {
	app := aisotropy.New(
		// One scope-gated TYPED tool: callers need the read:weather scope for the
		// model to be offered get_weather at all, and its arguments are validated
		// against getWeatherArgs before getWeather runs (invalid args are reflected
		// back to the model, never coerced).
		aisotropy.WithTypedTool("get_weather", "get the current weather for a city", "read:weather", getWeather),

		// A per-product persona: a literal prompt the bot author wrote.
		aisotropy.WithPersona("weatherbot", aisotropy.Persona{
			BasePrompt: "You are WeatherBot. Answer weather questions concisely and cite the city.",
		}),

		// EXPLICITLY run without a credential check so this example is usable out
		// of the box: the URL path tenant/user are trusted verbatim. Without this
		// the runtime defaults to DENY-ALL and every /v1 request gets 401 — that
		// default is deliberate (fail closed), but for a local hello-world we opt
		// out on purpose. A real deployment swaps NoAuth() for a JWT authenticator;
		// see the helpdesk example and docs/guides/build-your-own-agent.
		aisotropy.WithAuthenticator(aisotropy.NoAuth()),
	)

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("weatherbot exited with error: %v", err)
	}
}
