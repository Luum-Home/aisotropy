package orbit

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/session"
)

// ProductMarker is the secret seeded into product "X"'s session. If it appears
// in a DIFFERENT product's response for the same tenant+user, that is a proven
// cross-PRODUCT leak.
const ProductMarker = "px-secret-4417"

// RunProductMatrix proves the THIRD isolation axis aisotropy scopes on but ADK
// Go does not: product_id.
//
// aisotropy scopes memory by the composite (tenant_id, user_id, session_id,
// product_id). ADK Go's session.Service scopes by (appName, userID, sessionID)
// only — there is NO product field and NO binding of any key component to an
// authenticated caller. product_id is therefore, at best, a fourth string the
// developer must hand-fold into the ADK key themselves. Whatever they pass is
// trusted verbatim.
//
// This matrix shows both faces:
//
//  1. WHEN the developer folds product into the key (userID = tenant|user|product),
//     the composite key isolates: a different product for the same tenant+user
//     is correctly a miss. (positive control + correct-path negative probe)
//
//  2. product_id is just another developer-passed key component the framework
//     never binds to anything. Drop it from the key (one line — and the natural
//     bug, since ADK has no product slot to remind you it exists) and two
//     products for the SAME tenant+user collide on one session: the wrong
//     product silently reads product X's marker. (the LEAK)
func RunProductMatrix(ctx context.Context) (*Report, error) {
	rep := &Report{}

	// ── Layer A: end-to-end through the real runner ─────────────────────────
	// CORRECT convention: developer folds tenant|user|PRODUCT into the ADK userID.
	good, err := New("orbit")
	if err != nil {
		return nil, err
	}
	const sess = "s-shared"
	acmeAliceX := "acme|alice|productX" // tenant acme, user alice, product X
	acmeAliceY := "acme|alice|productY" // SAME tenant+user, product Y

	if _, err := good.Reply(ctx, acmeAliceX, sess, "MEMO: launch code is "+ProductMarker); err != nil {
		return nil, err
	}

	// Positive control: productX recalls its own marker -> MUST appear.
	pc, err := good.Reply(ctx, acmeAliceX, sess, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	if strings.Contains(pc, ProductMarker) {
		rep.add(Result{Label: "positive control (acme|alice|productX reads own product session)", Detail: "marker present", Leaked: false})
	} else {
		rep.add(Result{Label: "positive control (acme|alice|productX reads own product session)", Detail: "marker MISSING — harness broken", Fatal: true})
		return rep, nil
	}

	// Correct-path negative probe: product folded into key -> different product isolates.
	out, err := good.Reply(ctx, acmeAliceY, sess, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	leaked := strings.Contains(out, ProductMarker)
	detail := "no marker (product folded into composite key -> isolated)"
	if leaked {
		detail = "LEAK — saw productX's marker"
	}
	rep.add(Result{Label: "probe acme|alice|productY (product IN key: same tenant+user, different product)", Detail: detail, Leaked: leaked, MustIsolate: true})

	// ── Layer B: the developer bug — product DROPPED from the key ───────────
	// A second code path composes the ADK userID from tenant|user only and
	// forgets product_id — the natural mistake, because ADK has no product slot
	// to bind. Two products for the same tenant+user now map to the SAME
	// (appName, userID, sessionID) session -> silent cross-product read.
	buggy, err := New("orbit")
	if err != nil {
		return nil, err
	}
	tenantUserOnly := "acme|alice" // product_id never folded in
	// productX writes its secret under the product-less key.
	if _, err := buggy.Reply(ctx, tenantUserOnly, sess, "MEMO: launch code is "+ProductMarker); err != nil {
		return nil, err
	}
	// productY, under the SAME product-less key, reads productX's memo.
	leak, err := buggy.Reply(ctx, tenantUserOnly, sess, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	if strings.Contains(leak, ProductMarker) {
		rep.add(Result{
			Label:  "LEAK PATH (product dropped from key: productY reads productX for same tenant+user)",
			Detail: "LEAK — product-less key collides across products; ADK has no product binding to prevent it",
			Leaked: true,
		})
	} else {
		rep.add(Result{
			Label:  "LEAK PATH (product dropped from key)",
			Detail: "unexpectedly isolated",
			Leaked: false,
		})
	}

	// ── Layer C: raw session.Service — product_id is trusted verbatim ───────
	if err := rawProductProbe(ctx, rep); err != nil {
		return nil, err
	}

	return rep, nil
}

// rawProductProbe hits the REAL session.InMemoryService() directly to make the
// "product_id is just a developer-passed string, bound to nothing" property
// unmistakable at the storage layer.
func rawProductProbe(ctx context.Context, rep *Report) error {
	svc := session.InMemoryService()
	const app = "orbit"

	// productX stores a secret under the fully-folded key.
	if _, err := svc.Create(ctx, &session.CreateRequest{
		AppName:   app,
		UserID:    "acme|alice|productX",
		SessionID: "s1",
		State:     map[string]any{"secret": ProductMarker},
	}); err != nil {
		return err
	}

	// Correct owner read: full key incl. product -> secret visible.
	if got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "acme|alice|productX", SessionID: "s1"}); err == nil {
		v, _ := got.Session.State().Get("secret")
		ok := fmt.Sprint(v) == ProductMarker
		rep.add(Result{Label: "raw session: owner Get(acme|alice|productX, s1)", Detail: boolStr(ok, "secret visible", "secret missing"), Leaked: false})
	} else {
		rep.add(Result{Label: "raw session: owner Get(acme|alice|productX, s1)", Detail: "unexpected error: " + err.Error(), Fatal: true})
	}

	// Different product folded in -> correctly a miss (construction works).
	_, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "acme|alice|productY", SessionID: "s1"})
	rep.add(Result{Label: "raw session: Get(acme|alice|productY, s1)", Detail: boolStr(err != nil, "not found (product in key isolates)", "LEAK — found productX's session"), Leaked: err == nil, MustIsolate: true})

	// The catch: nothing binds the product component (or any component) to an
	// authenticated caller. Any code that names productX's exact key — an
	// attacker replaying it, or a peer product that guessed/derived it — gets
	// productX's session. product_id is a convention label, not a boundary.
	got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "acme|alice|productX", SessionID: "s1"})
	if err == nil {
		v, _ := got.Session.State().Get("secret")
		leaked := fmt.Sprint(v) == ProductMarker
		rep.add(Result{
			Label:  "raw session: caller passes productX key verbatim (acme|alice|productX, s1)",
			Detail: boolStr(leaked, "LEAK — product_id trusted verbatim, no identity binding", "isolated"),
			Leaked: leaked,
		})
	}
	return nil
}
