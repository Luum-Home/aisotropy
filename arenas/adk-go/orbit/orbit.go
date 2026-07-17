// Package orbit builds the "Orbit" multi-tenant support chatbot on top of the
// REAL Google ADK Go framework (google.golang.org/adk/v2): a real llmagent, a
// real runner, and the real in-memory session.Service — wired to an offline
// FakeModel so it runs with no API key.
//
// The whole point of this arena is the isolation model. ADK Go scopes session
// and state by a composite (appName, userID, sessionID) key — three native
// slots, with appName bound at runner construction (see New/NewWithModel). A
// deployment can bind any axis to appName by running one runner per value of it
// (e.g. one runner per product, or per tenant); that axis is then native and
// non-droppable per request. aisotropy binds four axes (tenant, user, session,
// product) from context.Context. Mapping four axes onto three native slots
// leaves exactly one axis without a slot of its own — it rides in userID by
// convention, and that one is the drop-vulnerable residual. Which axis is
// residual is the developer's choice; see isolation.go (tenant residual) and
// product_isolation.go (product made native, tenant residual).
package orbit

import (
	"context"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Orbit is the support chatbot. It owns one real ADK runner + session service.
type Orbit struct {
	appName  string
	runner   *runner.Runner
	sessions session.Service
}

// New builds Orbit on the real ADK stack with the offline FakeModel.
func New(appName string) (*Orbit, error) { return NewWithModel(appName, NewFakeModel()) }

// NewWithModel builds Orbit on the real ADK stack with a caller-supplied
// model.LLM. This is the seam the cost/observability demo uses to inject a
// metered offline model (synthetic token usage + a call counter) while still
// exercising the real llmagent + runner + session.Service.
func NewWithModel(appName string, m model.LLM) (*Orbit, error) {
	ag, err := llmagent.New(llmagent.Config{
		Name:        "orbit_support",
		Model:       m,
		Description: "Multi-tenant customer-support assistant.",
		Instruction: "You are Orbit, a concise, helpful multi-tenant support agent.",
	})
	if err != nil {
		return nil, err
	}
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             ag,
		SessionService:    svc,
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, err
	}
	return &Orbit{appName: appName, runner: r, sessions: svc}, nil
}

// Reply runs one conversational turn through the REAL ADK runner and returns
// the agent's final text.
//
// NOTE the signature: userID and sessionID are per-request strings the caller
// chooses; appName is NOT here — it was fixed when this Orbit's runner was
// built. That asymmetry is the point: appName is a native, construction-bound
// key slot (un-droppable per request), while any axis folded into userID is
// per-request convention. Binding userID to an authenticated caller is a
// separate middleware concern (ADK callbacks; aisotropy JWT), not key
// construction — and is near-parity between the two.
func (o *Orbit) Reply(ctx context.Context, userID, sessionID, prompt string) (string, error) {
	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText(prompt)}}
	var final string
	for ev, err := range o.runner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		if ev == nil || !ev.IsFinalResponse() || ev.LLMResponse.Content == nil {
			continue
		}
		for _, p := range ev.LLMResponse.Content.Parts {
			if p != nil && p.Text != "" {
				final = p.Text
			}
		}
	}
	return final, nil
}

// Runner exposes the underlying real ADK runner so the cost/observability demo
// can drive turns directly and read per-call usage metadata off the event
// stream (event.UsageMetadata) — the framework's built-in per-call visibility.
func (o *Orbit) Runner() *runner.Runner { return o.runner }

// Sessions exposes the underlying real session.Service so tests can probe the
// framework's raw state model directly (Create/Get with composite keys).
func (o *Orbit) Sessions() session.Service { return o.sessions }

// AppName returns the fixed ADK app name for this runner.
func (o *Orbit) AppName() string { return o.appName }
