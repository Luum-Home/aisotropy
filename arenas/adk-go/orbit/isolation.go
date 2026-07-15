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

// RunMatrix executes the adversarial isolation matrix against the REAL ADK Go
// runner + session service. It proves two things at once:
//
//  1. ADK Go's session.Service DOES scope by the composite
//     (appName, userID, sessionID) key — same construction class as
//     adk-python's composite PK. When the developer folds tenant into the key,
//     a cross-tenant read is correctly a miss.
//
//  2. There is NO tenant primitive and NO binding of that key to an
//     authenticated caller. The moment the developer drops the tenant
//     component (a one-line bug), two tenants collide on the same key and one
//     silently reads the other's memory. That is the leaky path — and it is
//     exactly as cheap to write as the correct one.
func RunMatrix(ctx context.Context) (*Report, error) {
	rep := &Report{}

	// ── Layer A: end-to-end through the real runner ─────────────────────────
	// CORRECT convention: the developer folds tenant into the ADK userID.
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

	// ── Layer B: the developer bug — tenant DROPPED from the key ────────────
	// A second, buggy code path composes the ADK userID from the bare username
	// and forgets the tenant. ADK has no tenant field to catch this. Two
	// different tenants' "alice" now map to the SAME (appName, userID,
	// sessionID) session -> silent cross-tenant read.
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
			Label:  "LEAK PATH (tenant dropped from key: globex/alice reads acme/alice)",
			Detail: "LEAK — bare-username key collides across tenants; ADK has no tenant binding to prevent it",
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
// to make the "developer-passed key, trusted verbatim" property unmistakable.
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

	// The catch: nothing binds UserID to an authenticated caller. Code that
	// passes the victim's exact identity string (attacker-controlled, replayed,
	// or a dropped-tenant bug) gets the victim's session. The framework trusts
	// the string verbatim.
	got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: "acme|alice", SessionID: "s1"})
	if err == nil {
		v, _ := got.Session.State().Get("secret")
		leaked := fmt.Sprint(v) == Marker
		rep.add(Result{
			Label:  "raw session: attacker/replay passes victim key (acme|alice, s1)",
			Detail: boolStr(leaked, "LEAK — key is trusted verbatim, no identity binding", "isolated"),
			Leaked: leaked,
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
