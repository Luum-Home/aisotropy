// Command weatherbot is a worked example of building a bot on top of Aisotropy
// against the PUBLIC surface only (Phase 8 adoption goal).
//
// It is a SEPARATE Go module (examples/weatherbot/go.mod) with a replace
// directive pointing at the repo root, and it imports ONLY the public façade
// package github.com/Luum-Home/aisotropy/aisotropy — never any private package.
// Because it is a distinct module, Go's own internal-package rule PHYSICALLY
// forbids it from reaching into the private packages, so the fact that it
// compiles is the "public surface only" guarantee: if the façade were missing
// anything this bot needs, this module would not build.
//
// The bot registers one scope-gated tool (get_weather), a real ToolExecutor that
// reads the calling tenant from ctx and returns a canned forecast, and a persona,
// then boots the full Aisotropy runtime with aisotropy.New(...).Run(ctx).
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// weatherExecutor is a real ToolExecutor: it dispatches the bot's registered
// tools by name. It reads the calling tenant from ctx (the only identity a
// consumer may access — read-only, never constructed) so its output is scoped to
// whoever made the request, demonstrating tenant-aware tool logic without ever
// touching an internal memory scope.
type weatherExecutor struct{}

// Execute runs the named tool. get_weather returns a canned forecast; any other
// name is an error so the agent loop surfaces the misconfiguration rather than
// silently succeeding.
func (weatherExecutor) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	switch name {
	case "get_weather":
		city := args["city"]
		if city == "" {
			city = "your area"
		}
		return fmt.Sprintf("Weather for tenant %q in %s: 22°C, clear skies, light breeze.", tenant, city), nil
	default:
		return "", fmt.Errorf("weatherbot: unknown tool %q", name)
	}
}

func main() {
	app := aisotropy.New(
		// One scope-gated tool: callers need the read:weather scope for the model
		// to be offered get_weather at all.
		aisotropy.WithTool(aisotropy.Tool{
			Name:          "get_weather",
			Description:   "get the current weather for a city",
			RequiredScope: "read:weather",
		}),
		// A real executor (not the demo echo) backs the tool.
		aisotropy.WithToolExecutor(weatherExecutor{}),
		// A per-product persona: a literal prompt the bot author wrote.
		aisotropy.WithPersona("weatherbot", aisotropy.Persona{
			BasePrompt: "You are WeatherBot. Answer weather questions concisely and cite the city.",
		}),
	)

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("weatherbot exited with error: %v", err)
	}
}
