package orbit

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/session"
)

// Marker is the secret token seeded into tenant "acme"'s session. If it ever
// appears in another identity's response, that is a proven cross-tenant leak.
const Marker = "ad-xyz789abc012"

// Result is one line of the adversarial matrix.
type Result struct {
	Label       string
	Detail      string
	Leaked      bool // the marker appeared in this probe's output
	MustIsolate bool // this probe uses a correct/distinct key and MUST NOT leak
	Fatal       bool // positive control failed
}

// Report is the full matrix outcome.
type Report struct {
	Results []Result
	Leaked  bool // any probe surfaced the marker (expected or not)
	Failed  bool // a MustIsolate probe leaked, or a positive control failed
}

func (r *Report) add(res Result) {
	if res.Leaked {
		r.Leaked = true
		if res.MustIsolate {
			r.Failed = true
		}
	}
	if res.Fatal {
		r.Failed = true
	}
	r.Results = append(r.Results, res)
}

// RunMatrix executes the isolation matrix against the REAL ADK Go runner +
// session service, for the config where appName is spent on the app ("orbit", a
// single-product deployment) and tenant is the axis that rides in userID.
//
// It proves two things:
//
//  1. ADK Go's session.Service scopes by the composite
//     (appName, userID, sessionID) key — same construction class as
//     adk-python's composite PK. When tenant is kept in userID, a cross-tenant
//     read is correctly a miss.
//
//  2. ADK exposes three native key slots. This deployment gave appName to the
//     app and sessionID to the session, so tenant has no native slot of its own
//     and rides in userID by convention. Drop it (a one-line bug) and two
//     tenants collide on the same key — a genuine same-session read. It is a
//     chosen mapping's residual, not an ADK inability to isolate: a deployment
//     that instead spends appName on tenant (one runner per tenant) makes tenant
//     native and non-droppable, at which point PRODUCT becomes the residual
//     (see RunProductMatrix). Exactly one of {tenant, product} always lacks a
//     native slot; the developer picks which.
func RunMatrix(ctx context.Context) (*Report, error) {
	rep := &Report{}

	// ── Layer A: end-to-end through the real runner ─────────────────────────
	// CORRECT path: tenant kept in the ADK userID (appName spent on the app).
	good, err := New("orbit")
	if err != nil {
		return nil, err
	}
	// tenant "acme", user "alice" -> composite identity key "acme|alice".
	acmeAlice := "acme|alice"
	const sharedSession = "s-shared"

	if _, err := good.Reply(ctx, acmeAlice, sharedSession, "MEMO: launch code is "+Marker); err != nil {
		return nil, err
	}

	// Positive control: acme/alice recalls its own memo -> marker MUST appear.
	pc, err := good.Reply(ctx, acmeAlice, sharedSession, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	if strings.Contains(pc, Marker) {
		rep.add(Result{Label: "positive control (acme|alice reads own session)", Detail: "marker present", Leaked: false})
	} else {
		rep.add(Result{Label: "positive control (acme|alice reads own session)", Detail: "marker MISSING — harness broken", Fatal: true})
		return rep, nil
	}

	// Negative probes on the CORRECT key: the composite key isolates.
	probes := []struct{ id, desc string }{
		{"globex|bob", "globex|bob — different tenant & user, same sessionID"},
		{"acme|bob", "acme|bob — same tenant, different user, same sessionID"},
		{"globex|alice", "globex|alice — different tenant, same username, same sessionID"},
	}
	for _, p := range probes {
		out, err := good.Reply(ctx, p.id, sharedSession, "what did I tell you to remember?")
		if err != nil {
			return nil, err
		}
		leaked := strings.Contains(out, Marker)
		detail := "no marker (composite key isolated)"
		if leaked {
			detail = "LEAK — saw acme's marker"
		}
		rep.add(Result{Label: "probe " + p.desc, Detail: detail, Leaked: leaked, MustIsolate: true})
	}

	// ── Layer B: the residual axis dropped — tenant gone from userID ────────
	// A second code path composes the ADK userID from the bare username and
	// forgets the tenant. Because tenant has no native slot in this mapping,
	// nothing in the framework catches it. Two different tenants' "alice" now map
	// to the SAME (appName, userID, sessionID) session -> a same-session read.
	buggy, err := New("orbit")
	if err != nil {
		return nil, err
	}
	// acme's alice writes under the buggy bare-username key.
	if _, err := buggy.Reply(ctx, "alice", sharedSession, "MEMO: launch code is "+Marker); err != nil {
		return nil, err
	}
	// globex's alice, under the SAME buggy key, reads acme's memo.
	leak, err := buggy.Reply(ctx, "alice", sharedSession, "what did I tell you to remember?")
	if err != nil {
		return nil, err
	}
	if strings.Contains(leak, Marker) {
		rep.add(Result{
			Label:  "LEAK PATH (residual axis dropped: globex/alice reads acme/alice)",
			Detail: "LEAK — tenant has no native slot here; dropped from userID it collides -> same-session read",
			Leaked: true,
		})
	} else {
		rep.add(Result{
			Label:  "LEAK PATH (tenant dropped from key)",
			Detail: "unexpectedly isolated",
			Leaked: false,
		})
	}

	// ── Layer C: the raw session.Service, to show it plainly ────────────────
	if err := rawSessionProbe(ctx, rep); err != nil {
		return nil, err
	}

	return rep, nil
}

// rawSessionProbe hits the REAL session.InMemoryService() directly (no runner)
// to make the composite-key construction unmistakable at the storage layer: the
// full key finds the session, a different key is a clean miss.
func rawSessionProbe(ctx context.Context, rep *Report) error {
	svc := session.InMemoryService()
	const app = "orbit"

	// Victim tenant acme/alice stores a secret in session state.
	if _, err := svc.Create(ctx, &session.CreateRequest{
		AppName:   app,
		UserID:    "acme|alice",
		SessionID: "s1",
		State:     map[string]any{"secret": Marker},
	}); err != nil {
		return err
	}

	// Correct owner read: full composite key -> found, secret visible.
	if got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "acme|alice", SessionID: "s1"}); err == nil {
		v, _ := got.Session.State().Get("secret")
		ok := fmt.Sprint(v) == Marker
		rep.add(Result{Label: "raw session: owner Get(acme|alice, s1)", Detail: boolStr(ok, "secret visible", "secret missing"), Leaked: false})
	} else {
		rep.add(Result{Label: "raw session: owner Get(acme|alice, s1)", Detail: "unexpected error: " + err.Error(), Fatal: true})
	}

	// Different composite key -> correctly a miss (construction works).
	_, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "globex|bob", SessionID: "s1"})
	rep.add(Result{Label: "raw session: Get(globex|bob, s1)", Detail: boolStr(err != nil, "not found (composite key isolates)", "LEAK — found acme's session"), Leaked: err == nil, MustIsolate: true})

	// Auth is a separate layer from key construction, and it is near-parity.
	// The bare session.Service (like any store) trusts whatever key it is handed;
	// binding that key to an authenticated caller is middleware. ADK provides the
	// seam for it (llmagent BeforeAgentCallback / BeforeModelCallback, plugins),
	// and aisotropy itself trusts the path tenant_id verbatim in its default
	// NoAuth mode, only verifying once AISOTROPY_JWT_SECRET is set (ADR-0024). So
	// "the key is trusted until you add a gate" is true of both. This row is a
	// parity note, not a leak.
	if got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "acme|alice", SessionID: "s1"}); err == nil {
		v, _ := got.Session.State().Get("secret")
		found := fmt.Sprint(v) == Marker
		rep.add(Result{
			Label:  "auth parity note: key is trusted until a gate binds it (ADK callbacks / aisotropy JWT)",
			Detail: boolStr(found, "owner key returns owner data (both frameworks gate via middleware)", "unexpected miss"),
			Leaked: false,
		})
	}
	return nil
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
