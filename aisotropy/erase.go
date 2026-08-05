package aisotropy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// EraseScopeProcessLocal is the WorkingMemoryScope value: the working-memory
// erase reached the BadgerDB embedded in THIS process and nothing else. It is
// not a configuration choice — BadgerDB takes an exclusive directory lock
// (dirLockGuard: "Cannot acquire directory lock ... Another process is using
// this Badger database"), so two aisotropyd processes structurally cannot share
// one working-memory store, and an erase cannot reach the others.
const EraseScopeProcessLocal = "process-local"

// EraseReport is the structured, per-tier audit record App.EraseSubject returns:
// how many items were removed from each memory tier for the erased (tenant,user)
// subject. It carries counts and identity only — never any erased content — so it
// is safe to log and persist as the erasure's audit trail (GDPR Art.17 asks the
// controller to be able to demonstrate the erasure happened).
//
// Read the counts together with Complete. A count is what THIS process deleted;
// Complete is whether that is the whole story. A receipt that reports "12 items
// deleted" for a fleet where eleven other replicas still hold the subject's
// buffer is worse than no receipt at all — it is documentary evidence of an
// erasure that did not happen.
type EraseReport struct {
	TenantID             string `json:"tenant_id"`
	UserID               string `json:"user_id"`
	FactsDeleted         int    `json:"facts_deleted"`
	SemanticDeleted      int    `json:"semantic_deleted"`
	WorkingMemoryDeleted int    `json:"working_memory_deleted"`

	// WorkingMemoryScope is always EraseScopeProcessLocal — see that constant
	// for why it cannot be anything else.
	WorkingMemoryScope string `json:"working_memory_scope"`
	// DeclaredReplicas is the fleet size this process was told about
	// (AISOTROPY_REPLICAS, default 1 — the same declaration that splits the
	// per-tenant quota). It is DECLARED, never observed: a deployment that scales
	// without updating it will produce receipts that overstate their reach.
	DeclaredReplicas int `json:"declared_replicas"`
	// Complete reports whether this receipt may be read as "the subject's data is
	// gone". It is true only when DeclaredReplicas is 1. With more, the
	// working-memory tier of every OTHER replica still holds the subject's chat
	// buffers and summaries, and this receipt covers one replica out of N.
	Complete bool `json:"complete"`
	// Caveat spells the incompleteness out in prose for whoever reads the audit
	// trail — a compliance reader is not going to infer it from a boolean. Empty
	// when Complete is true.
	Caveat string `json:"caveat,omitempty"`
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
// REACH — read this before treating the receipt as proof of erasure. The
// working-memory tier is an embedded BadgerDB that a single process holds under
// an exclusive directory lock, so this call erases the buffer of the replica that
// served it and NO OTHER. On a fleet of N, the subject's chat buffers survive on
// the N-1 replicas that did not handle the request. The facts and semantic tiers
// behave differently: when they are backed by the networked services (rqlite,
// Qdrant) the erase there IS fleet-wide, because every replica talks to the same
// service; when they run on this binary's in-process defaults they are as local
// as working memory. The report therefore states its reach explicitly
// (WorkingMemoryScope, DeclaredReplicas, Complete, Caveat) rather than letting a
// row of counts imply a completeness it does not have.
//
// The transactional outbox (internal/memory/outbox.go) is NOT a way around this,
// and it was checked before this receipt was written rather than assumed: its
// pending events live under `outbox:pending:` in the SAME per-process BadgerDB
// and are drained by that process's own worker, so an erase event enqueued here
// would be consumed here — by the one replica that has already erased — and never
// seen by the others. It is a local-atomicity mechanism, not a bus. Fanning an
// erase across replicas needs a genuinely shared channel (the networked tier, a
// broker, or a control-plane call to each replica); until one exists, the honest
// move is to say the scope is local, which is what this does.
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

	replicas := rt.replicas
	if replicas < 1 {
		replicas = 1
	}
	report := EraseReport{
		TenantID:           tenantID,
		UserID:             userID,
		WorkingMemoryScope: EraseScopeProcessLocal,
		DeclaredReplicas:   replicas,
		Complete:           replicas == 1,
	}
	if !report.Complete {
		report.Caveat = fmt.Sprintf(
			"PARTIAL ERASURE: working memory is an embedded per-process store, so this erased the buffers of 1 replica out of %d declared (AISOTROPY_REPLICAS); the subject's chat buffers and summaries remain on the other %d until each of them erases the subject too. Facts and semantic vectors are fleet-wide only when backed by the networked rqlite/Qdrant services.",
			replicas, replicas-1)
	}

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

	// Audit trail: identity + per-tier counts only (never erased content). A
	// partial erase is logged at WARN, not Info: an incomplete erasure recorded
	// at the same level as a complete one is how a compliance gap becomes
	// invisible.
	attrs := []any{
		"tenant", tenantID,
		"user", userID,
		"facts_deleted", report.FactsDeleted,
		"semantic_deleted", report.SemanticDeleted,
		"working_memory_deleted", report.WorkingMemoryDeleted,
		"working_memory_scope", report.WorkingMemoryScope,
		"declared_replicas", report.DeclaredReplicas,
		"complete", report.Complete,
	}
	if report.Complete {
		slog.Info("subject erased (GDPR Art.17)", attrs...)
	} else {
		slog.Warn("subject erased on THIS replica only (GDPR Art.17, partial)", append(attrs, "caveat", report.Caveat)...)
	}
	return report, nil
}
