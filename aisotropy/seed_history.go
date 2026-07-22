// Package aisotropy: the working-memory seed surface — the adoption-surface seam
// that lets an embedding consumer PRELOAD prior conversation turns into a
// session's working memory WITHOUT paying a model call per turn.
//
// The hole this closes (docs/architecture/adoption-surface.md,
// docs/architecture/arena-memory-forensics.md): before this, a third party who
// already held a conversation transcript verbatim — a support handoff, a session
// migration, a "resume this conversation" replay — had no way to put it into the
// runtime's memory except by driving it back through App.Turn, one real model
// generation per assistant turn. That is what forced the arena's memory adapter
// to REGENERATE canned assistant turns through the model (a refusal cascade and
// inflated token counts). SeedHistory writes those turns straight into the L1
// working-memory tier the same way the live chat pipeline does, as pure durable
// Appends — no LLM call — so a subsequent Turn recalls them through the ordinary
// Recent()/Summary() hydration path.
package aisotropy

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// RoleUser, RoleAssistant, and RoleSystem are the message-role values a
// SeededTurn (see SeedHistory's msgs) may carry. They mirror the exact strings
// the live chat pipeline persists (internal/api's roleUser/roleAssistant) so a
// seeded transcript is indistinguishable from one the runtime wrote itself, and
// so the recall path (working-memory hydration) treats seeded turns identically
// to organically produced ones. Re-exported as constants so a consumer names the
// role without guessing the wire string.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
)

// SeedHistory preloads prior conversation turns into the working-memory buffer
// of a single (tenant, user, session) — the adoption-surface primitive for a
// support handoff, a session migration, or any "resume this conversation" flow
// where the caller ALREADY holds the transcript verbatim and must not pay a
// model generation to reconstruct state it already has.
//
// Each turn is a plain, durable Append into the L1 working-memory tier — the
// SAME write the live /chat pipeline performs for every user and assistant turn
// (internal/api/chat.go's wm.Append). It makes NO model call: SeedHistory is a
// sequence of pure BadgerDB writes. A later App.Turn (or the streaming HTTP
// routes) on the same SessionID then recalls the seeded turns through the
// ordinary working-memory hydration (wm.Summary + wm.Recent folded into the
// system prompt) — exactly as it recalls turns the runtime produced itself. The
// turns are appended in the order given and recalled in that order (the buffer
// is sequenced by an insertion counter, not by Message.Timestamp, which stays
// optional provenance metadata — Append stamps it with the current time only
// when it is left zero).
//
// Multi-tenant isolation (constitutional gate, .claude/rules/constitutional-
// gates.md): the write is scoped ENTIRELY to the (tenant, user, session) the
// caller names here. The memory.Key is built from those three components and
// nothing else, Key.Validate rejects an empty or reserved-character-bearing
// identity up front, and every Append reads/writes only under that key's
// on-disk prefix — so a consumer seeding tenant A's session can never reach
// tenant B's buffer. This is the identical trust model App.Turn documents: the
// in-process caller supplies the identity and is inside the trust boundary,
// exactly as the code that mints a JWT for the HTTP path is.
//
// Roles: each message's Role must be one of RoleUser / RoleAssistant /
// RoleSystem and its Content must be non-empty; a bad turn fails fast (before
// the runtime is booted or any write happens) so a typo'd role can never land a
// silently-mis-tagged turn in the buffer. Message.Timestamp is optional.
//
// Honest scope — the buffer window: SeedHistory itself never calls the model,
// but working memory bounds its active buffer (defaultMaxActiveMessages, ~12).
// Seeding MORE turns than that window into one session triggers the SAME
// background condensation the live pipeline uses, and with the runtime's default
// condenser that condensation is an LLM call (an async one, off the seed path).
// A consumer that must stay fully offline while seeding a long transcript should
// register a deterministic condenser via WithSummarizer, or seed within the
// active window. Seeding a handful of recent turns — the typical resume case —
// stays entirely model-call-free.
//
// It boots the App's shared in-process runtime lazily on first call (the same
// runtime App.Turn uses — ensureRuntime), so a SeedHistory immediately followed
// by a Turn on the same SessionID share one working-memory store. Returns the
// number of turns durably appended; on a mid-sequence append error it returns
// the count written so far alongside the error. An empty msgs is a (0, nil)
// no-op.
func (a *App) SeedHistory(ctx context.Context, tenant, user, session string, msgs ...Message) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}

	// Build and validate the isolation key up front — a bad identity is rejected
	// before the runtime is booted or any write happens, never widened into a
	// cross-tenant prefix.
	key := memory.Key{Tenant: tenant, User: user, Session: session}
	if err := key.Validate(); err != nil {
		return 0, fmt.Errorf("aisotropy: seed history: %w", err)
	}

	// Validate every turn before touching the store so a malformed turn fails the
	// whole call fast, rather than leaving a partial, half-tagged buffer.
	for i, m := range msgs {
		if err := validateSeedRole(m.Role); err != nil {
			return 0, fmt.Errorf("aisotropy: seed history: turn %d: %w", i, err)
		}
		if m.Content == "" {
			return 0, fmt.Errorf("aisotropy: seed history: turn %d: content is empty", i)
		}
	}

	rt, err := a.ensureRuntime()
	if err != nil {
		return 0, err
	}

	for i, m := range msgs {
		if err := rt.wm.Append(ctx, key, m); err != nil {
			return i, fmt.Errorf("aisotropy: seed history: append turn %d: %w", i, err)
		}
	}
	return len(msgs), nil
}

// validateSeedRole rejects any role that is not one of the three the live chat
// pipeline persists, so a seeded turn is always tagged with a role the hydration
// path recognizes.
func validateSeedRole(role string) error {
	switch role {
	case RoleUser, RoleAssistant, RoleSystem:
		return nil
	default:
		return fmt.Errorf("role %q must be one of %q, %q, or %q", role, RoleUser, RoleAssistant, RoleSystem)
	}
}
