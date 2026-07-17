package aisotropy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// EraseReport is the structured, per-tier audit record App.EraseSubject returns:
// how many items were removed from each memory tier for the erased (tenant,user)
// subject. It carries counts and identity only — never any erased content — so it
// is safe to log and persist as the erasure's audit trail (GDPR Art.17 asks the
// controller to be able to demonstrate the erasure happened).
type EraseReport struct {
	TenantID             string `json:"tenant_id"`
	UserID               string `json:"user_id"`
	FactsDeleted         int    `json:"facts_deleted"`
	SemanticDeleted      int    `json:"semantic_deleted"`
	WorkingMemoryDeleted int    `json:"working_memory_deleted"`
}

// EraseSubject erases ALL data for a single (tenant,user) subject across every
// memory tier — the facts journal (internal/facts), the semantic vectors
// (internal/semantic: KindFact, KindTurn, and the subject's own KindDocument
// points), and the working-memory chat buffers/summaries (internal/memory) —
// implementing GDPR Art.17 ("right to erasure") at the runtime level.
//
// Scope safety (hard invariant). tenant/user are validated by the same blessed
// path every read/write uses (memory.ValidateIdentityComponent, then
// facts.NewScope / semantic.NewScope). A blank or malformed identity returns an
// error and erases NOTHING — there is no wildcard-delete path. Every tier
// confines its delete to the exact (tenant,user), so a subject erase can never
// remove another tenant's or another user's data.
//
// Tenant-SHARED knowledge is deliberately preserved. A tenant-wide shared
// document (a KindDocument point with an empty user, from a tenant-level share)
// is NOT one user's personal data, so a per-user erase leaves it in place. A
// separate tenant-level erase (a documented follow-on) is the tool for removing
// tenant-shared knowledge.
//
// Idempotent. Erasing a subject that has no data returns a zero-count report and
// no error; erasing twice is safe (the second call reports zeros). The returned
// EraseReport is the caller's audit record.
//
// Store support is REQUIRED, never silently skipped. Each tier's store must
// implement its ScopeEraser capability; a configured store that does not returns
// an error rather than leaving that subject's data behind. Every built-in store
// (in-memory, SQLite, rqlite, Qdrant, the routing stores, and the sealed
// community-backend stores) implements it.
//
// Like Turn, it boots the App's shared runtime on first use, so the tiers it
// erases are the exact stores the chat pipeline reads and writes. Safe to call
// concurrently with chat turns; the underlying stores are concurrency-safe.
func (a *App) EraseSubject(ctx context.Context, tenantID, userID string) (EraseReport, error) {
	// Validate identity up front through the shared rule so a blank/malformed
	// subject is rejected before any tier is touched — never a wildcard delete.
	if err := memory.ValidateIdentityComponent("tenant", tenantID); err != nil {
		return EraseReport{}, err
	}
	if err := memory.ValidateIdentityComponent("user", userID); err != nil {
		return EraseReport{}, err
	}

	rt, err := a.ensureRuntime()
	if err != nil {
		return EraseReport{}, err
	}

	report := EraseReport{TenantID: tenantID, UserID: userID}

	// Facts tier (always present). Erase every row — current and superseded — for
	// the subject. This also removes the fact side of the engram fact+vector
	// pairing; the vector side (KindFact points) is removed by the semantic erase
	// below, so the paired deletion is covered end to end.
	factsScope, err := facts.NewScope(tenantID, userID)
	if err != nil {
		return report, err
	}
	factsEraser, ok := rt.factsStore.(facts.ScopeEraser)
	if !ok {
		return report, fmt.Errorf("aisotropy: facts store %T does not support subject erasure", rt.factsStore)
	}
	if report.FactsDeleted, err = factsEraser.EraseScope(ctx, factsScope); err != nil {
		return report, fmt.Errorf("aisotropy: erase facts for subject: %w", err)
	}

	// Semantic tier (present unless the App was built without one). Erases the
	// subject's KindFact, KindTurn, and user-scoped KindDocument points in one
	// scoped sweep; tenant-shared documents (empty user) are left intact.
	if rt.semanticStore != nil {
		semScope, serr := semantic.NewScope(tenantID, userID)
		if serr != nil {
			return report, serr
		}
		semEraser, sok := rt.semanticStore.(semantic.ScopeEraser)
		if !sok {
			return report, fmt.Errorf("aisotropy: semantic store %T does not support subject erasure", rt.semanticStore)
		}
		if report.SemanticDeleted, serr = semEraser.EraseScope(ctx, semScope); serr != nil {
			return report, fmt.Errorf("aisotropy: erase semantic vectors for subject: %w", serr)
		}
	}

	// Working-memory tier (always present): every session's chat buffer, summary,
	// and sequence counter under the (tenant,user) prefix.
	if report.WorkingMemoryDeleted, err = rt.wm.EraseSubject(ctx, tenantID, userID); err != nil {
		return report, fmt.Errorf("aisotropy: erase working memory for subject: %w", err)
	}

	// Audit trail: identity + per-tier counts only (never erased content).
	slog.Info("subject erased (GDPR Art.17)",
		"tenant", tenantID,
		"user", userID,
		"facts_deleted", report.FactsDeleted,
		"semantic_deleted", report.SemanticDeleted,
		"working_memory_deleted", report.WorkingMemoryDeleted,
	)
	return report, nil
}
