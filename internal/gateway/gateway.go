package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// Gateway resolves a routing Decision to a concrete llm.Client. It owns a
// name->client registry (the live provider connections) and a Policy (the
// routing logic). Route runs the policy for a Request and returns the client
// its Decision points at; an unknown or empty target falls back to the
// configured default client, logged at Warn (fail-safe: a routing miss must
// never drop a request on the floor).
//
// The registry is separate from internal/llm.Registry on purpose: this is the
// gateway's OWN view of which named targets ("cheap", "premium") map to which
// connections, decoupled from how llm builds its providers. The same
// llm.Client may appear under several gateway names.
type Gateway struct {
	mu          sync.RWMutex
	clients     map[string]llm.Client
	defaultName string
	policy      Policy
	logger      *slog.Logger
}

// New creates a Gateway with the given policy and logger. If logger is nil, a
// no-op logger is used so the fail-safe path never panics. The policy may be
// nil, in which case every request resolves straight to the default client
// (useful as a pass-through).
func New(policy Policy, logger *slog.Logger) *Gateway {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Gateway{
		clients: make(map[string]llm.Client),
		policy:  policy,
		logger:  logger,
	}
}

// Register adds (or replaces) a named target -> client mapping. The first
// target registered becomes the default unless SetDefault is called. A nil
// client is rejected (returns an error) so a misconfiguration surfaces at
// wiring time, not at request time.
func (g *Gateway) Register(name string, c llm.Client) error {
	if name == "" {
		return fmt.Errorf("gateway: cannot register client under empty name")
	}
	if c == nil {
		return fmt.Errorf("gateway: cannot register nil client for %q", name)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clients[name] = c
	if g.defaultName == "" {
		g.defaultName = name
	}
	return nil
}

// SetDefault sets the fallback target used when a Decision.Target is empty or
// unknown. It returns an error if no client is registered under name, so the
// default is guaranteed resolvable.
func (g *Gateway) SetDefault(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.clients[name]; !ok {
		return fmt.Errorf("gateway: cannot set default to unregistered target %q", name)
	}
	g.defaultName = name
	return nil
}

// Targets returns the registered target names, sorted, for diagnostics/tests.
func (g *Gateway) Targets() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.clients))
	for n := range g.clients {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Client resolves a target name to a registered llm.Client. It reports whether
// the returned client is the requested target (ok=true) or the fail-safe
// default (ok=false). When the target is unknown/empty AND no default is set,
// it returns (nil, false).
//
// This is the low-level resolver; most callers want Route, which runs the
// policy first and logs the fallback.
func (g *Gateway) Client(name string) (llm.Client, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if c, ok := g.clients[name]; ok {
		return c, true
	}
	if g.defaultName != "" {
		return g.clients[g.defaultName], false
	}
	return nil, false
}

// Route runs the policy for req, resolves the resulting Decision.Target to a
// client, and returns the chosen client, the Decision that produced it, and an
// error only when the request cannot be served at all (no matching target and
// no default configured — a hard misconfiguration).
//
// On a target miss (empty or unregistered target) it falls back to the default
// client and logs a Warn with the attempted target and the policy's reason.
// The returned Decision always reflects what the policy actually decided (its
// Reason is preserved) so callers/audit see the original intent even when a
// fallback occurred.
func (g *Gateway) Route(ctx context.Context, req Request) (llm.Client, Decision, error) {
	var d Decision
	if g.policy != nil {
		d = g.policy.Select(ctx, req)
	}

	client, exact := g.Client(d.Target)
	if client == nil {
		// No target matched and no default is configured: cannot serve.
		g.logger.WarnContext(ctx, "gateway: no target and no default; cannot route",
			"target", d.Target,
			"reason", d.Reason,
			"tenant", req.Tenant,
		)
		return nil, d, fmt.Errorf("gateway: no client for target %q and no default configured", d.Target)
	}

	if !exact {
		// Fail-safe: the policy asked for a target we don't have (or abstained
		// with an empty target). Serve from the default, but make the miss loud.
		g.logger.WarnContext(ctx, "gateway: unknown/empty target, using default client",
			"attempted_target", d.Target,
			"default_target", g.defaultName,
			"reason", d.Reason,
			"tenant", req.Tenant,
		)
	} else {
		g.logger.DebugContext(ctx, "gateway: routed request",
			"target", d.Target,
			"reason", d.Reason,
			"tenant", req.Tenant,
		)
	}
	return client, d, nil
}
