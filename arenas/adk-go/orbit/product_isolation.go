package orbit

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/session"
)

// ProductMarker is the secret seeded into product "X"'s session. If it appears
// in a DIFFERENT product's response, that is a cross-PRODUCT read.
const ProductMarker = "px-secret-4417"

// RunProductMatrix maps aisotropy's 4 scope axes onto ADK Go's 3 native key
// slots and shows both the STRONGEST honest ADK config and the one residual
// that survives it.
//
// aisotropy scopes memory by the composite (tenant_id, user_id, session_id,
// product_id) — four axes, all bound from context.Context. ADK Go's
// session.Service exposes exactly THREE native key slots: (appName, userID,
// sessionID). Mapping four axes onto three slots means exactly one axis has no
// native slot of its own and must be concatenated into one of the three, with
// no framework-enforced separator. THAT axis — and only that one — is developer
// discipline and drop-vulnerable.
//
// SWEPT: 2026-08-04 docs/architecture/arena-sweep-adk-go-2026-08-04.md — verdict HOLDS,
// NARROWER. session.Service is three plain unvalidated strings in v2.0.0 and v2.1.0 (no
// Validate on the identity slots, zero occurrences of "tenant"), and no first-party Go
// extension adds an axis or a separator. Narrower on two counts: ADK DOES enforce reserved
// separators elsewhere — validateCustomRunID rejects '/' and '@' with ErrInvalidRunID
// (workflow/dynamic_scheduler.go:495-507), validateFileName rejects path separators
// (artifact/service.go:108-120) — so the absence is specific to identity; and outside
// session.Service the hand-fold is not the only route, since Vertex AI Memory Bank takes an
// N-key `scope map[string]string` with exact-match retrieval over all keys, which ADK Go's
// binding collapses to a single {"user_id": …} (memory/vertexai/vertexai_client.go:137).
//
// This matrix shows the strongest choice for the PRODUCT axis:
//
//  1. STRONGEST config — one runner per product with appName = product. ADK
//     binds appName at runner CONSTRUCTION (runner.Config.AppName is captured as
//     r.appName; Run(ctx, userID, sessionID, …) never takes an appName). So the
//     product axis becomes a NATIVE, non-droppable key component: a cross-product
//     read is a storage miss by construction, and no per-request code path can
//     "forget" it. A competent developer prevents the product-axis leak this way.
//
//  2. The RESIDUAL — with appName spent on product and sessionID on session, the
//     userID slot must carry BOTH tenant and user. Tenant is now the axis with no
//     native slot: it is concatenated into userID and the framework binds nothing
//     to that convention. Drop it (a one-line bug) and two tenants' "alice"
//     collide on the SAME (appName, userID, sessionID) session — a genuine
//     same-session read. The leak is a chosen-weak-config collision, not an ADK
//     inability to isolate.
//
// Symmetric point: had the developer spent appName on TENANT instead (see
// RunMatrix), PRODUCT would be the residual. Exactly one of {tenant, product}
// always lacks a native slot; the developer chooses which.
//
// SWEPT: 2026-08-04 docs/architecture/arena-sweep-adk-go-2026-08-04.md — the arity holds for
// ADK Go (three slots in v2.0.0 and v2.1.0; the module proxy lists only `adk` and `adk/v2`,
// so no first-party extension adds a fourth). The surface that removes the fold entirely —
// Memory Bank's N-key scope map — sits in the platform tier and is not reachable through
// ADK Go's session.Service/memory.Service.
func RunProductMatrix(ctx context.Context) (*Report, error) {
	rep := &Report{}

	// ── Layer A: STRONGEST config — appName = product (one runner per product) ──
	// tenant+user share the userID slot; product owns its own runner's appName.
	const tenantUser = "acme|alice"
	const sess = "s1"

	runnerX, err := New("productX") // appName = productX (bound at construction)
	if err != nil {
		return nil, err
	}
	runnerY, err := New("productY") // appName = productY, SAME userID + sessionID
	if err != nil {
		return nil, err
	}

	if _, err := runnerX.Reply(ctx, tenantUser, sess, "MEMO: launch code is "+ProductMarker); err != nil {
		return nil, err
	}

	// Positive control: productX's runner reads its own product session.
	pc, err := runnerX.Reply(ctx, tenantUser, sess, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	if strings.Contains(pc, ProductMarker) {
		rep.add(Result{Label: "positive control (productX runner reads its own product session)", Detail: "marker present", Leaked: false})
	} else {
		rep.add(Result{Label: "positive control (productX runner reads its own product session)", Detail: "marker MISSING — harness broken", Fatal: true})
		return rep, nil
	}

	// Cross-product read through productY's runner: identical userID + sessionID,
	// but a DIFFERENT appName bound at construction -> storage miss BY CONSTRUCTION.
	out, err := runnerY.Reply(ctx, tenantUser, sess, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	leaked := strings.Contains(out, ProductMarker)
	detail := "no marker (appName=product is a native, non-droppable slot -> isolated by construction)"
	if leaked {
		detail = "LEAK — saw productX's marker"
	}
	rep.add(Result{Label: "strongest config: productY runner reads SAME userID+sessionID (appName differs)", Detail: detail, Leaked: leaked, MustIsolate: true})

	// ── Layer B: the RESIDUAL — tenant folded into userID (no native slot left) ──
	// appName is spent on product, so tenant must ride in userID. When tenant is
	// kept in the key, different tenants isolate on userID; drop it and they collide.
	resid, err := New("productX") // product still native via appName
	if err != nil {
		return nil, err
	}
	const residSess = "s-shared"
	// Correct path: tenant kept in userID -> a different tenant isolates.
	if _, err := resid.Reply(ctx, "acme|alice", residSess, "MEMO: launch code is "+ProductMarker); err != nil {
		return nil, err
	}
	probe, err := resid.Reply(ctx, "globex|alice", residSess, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	rleak := strings.Contains(probe, ProductMarker)
	rdetail := "no marker (tenant kept in userID -> isolated)"
	if rleak {
		rdetail = "LEAK — saw acme's marker"
	}
	rep.add(Result{Label: "residual axis (tenant in userID): globex|alice, same product+session", Detail: rdetail, Leaked: rleak, MustIsolate: true})

	// Leaky path: drop the residual axis (tenant) from userID -> same-session read.
	buggy, err := New("productX")
	if err != nil {
		return nil, err
	}
	if _, err := buggy.Reply(ctx, "alice", residSess, "MEMO: launch code is "+ProductMarker); err != nil { // acme's alice
		return nil, err
	}
	leak, err := buggy.Reply(ctx, "alice", residSess, "what did I tell you to remember?") // globex's alice
	if err != nil {
		return nil, err
	}
	if strings.Contains(leak, ProductMarker) {
		rep.add(Result{
			Label:  "LEAK PATH (residual axis dropped: bare userID collapses two tenants)",
			Detail: "LEAK — tenant has no native slot; dropped from userID it collides -> genuine same-session read",
			Leaked: true,
		})
	} else {
		rep.add(Result{
			Label:  "LEAK PATH (residual axis dropped from userID)",
			Detail: "unexpectedly isolated",
			Leaked: false,
		})
	}

	// ── Layer C: raw session.Service — appName is a real key component ───────
	if err := rawProductProbe(ctx, rep); err != nil {
		return nil, err
	}

	return rep, nil
}

// rawProductProbe hits the REAL session.InMemoryService() directly to make the
// "appName is a genuine, construction-bound key component" property unmistakable
// at the storage layer: the same userID + sessionID under a different appName is
// a clean miss.
func rawProductProbe(ctx context.Context, rep *Report) error {
	svc := session.InMemoryService()
	const user, sess = "acme|alice", "s1"

	// productX stores a secret under appName=productX.
	if _, err := svc.Create(ctx, &session.CreateRequest{
		AppName:   "productX",
		UserID:    user,
		SessionID: sess,
		State:     map[string]any{"secret": ProductMarker},
	}); err != nil {
		return err
	}

	// Owner read: same appName -> secret visible.
	if got, err := svc.Get(ctx, &session.GetRequest{AppName: "productX", UserID: user, SessionID: sess}); err == nil {
		v, _ := got.Session.State().Get("secret")
		ok := fmt.Sprint(v) == ProductMarker
		rep.add(Result{Label: "raw session: owner Get(appName=productX, acme|alice, s1)", Detail: boolStr(ok, "secret visible", "secret missing"), Leaked: false})
	} else {
		rep.add(Result{Label: "raw session: owner Get(appName=productX, acme|alice, s1)", Detail: "unexpected error: " + err.Error(), Fatal: true})
	}

	// Different appName, SAME userID + sessionID -> construction miss.
	_, err := svc.Get(ctx, &session.GetRequest{AppName: "productY", UserID: user, SessionID: sess})
	rep.add(Result{Label: "raw session: Get(appName=productY, acme|alice, s1) — same user+session, other product", Detail: boolStr(err != nil, "not found (appName isolates by construction)", "LEAK — found productX's session"), Leaked: err == nil, MustIsolate: true})

	return nil
}
