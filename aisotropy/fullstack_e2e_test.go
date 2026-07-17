package aisotropy

// fullstack_e2e_test.go is the missing REAL full-stack end-to-end test for the
// runtime: a single multi-turn flow that drives the WHOLE request lifecycle over
// the REAL, on-disk in-process backends via the shipped public aisotropy.App
// builder + its HTTP surface — not a stubbed store anywhere except the *model*.
//
// What makes it new (vs. the layered suite and isolation_e2e_test.go):
//   - REAL backends: AISOTROPY_DATA_DIR points at a t.TempDir(), so working
//     memory is a genuine on-disk BadgerDB and facts are a genuine on-disk
//     SQLite+FTS5 file; the semantic tier is the in-process cosine index (no
//     Qdrant, no rqlite, no fakes). The stores are proven real by REOPENING them
//     through the raw tier APIs (facts.OpenSQLite / memory.Open) after the server
//     shuts down and reading the data back — data that survives a fresh handle is
//     not a stub.
//   - The whole chain wired together in ONE flow: identity→ctx→pre-spend quota
//     gate→guardrails(input redaction)→hydration(working memory + facts FTS5 +
//     semantic RAG)→LLM(reflection loop, the keyless stub)→cost meter→
//     transactional outbox→async consolidation into the facts + semantic tiers.
//   - Multi-tenant isolation asserted at EVERY stage: a second tenant B and a
//     cross-user (tenantA/userB) never see tenant A's working memory, facts,
//     vectors, or the ingested knowledge — over the live hydration path AND at
//     the reopened raw-tier level.
//   - Guardrail input-redaction proven to keep a secret out of EVERY tier: the
//     raw secret token is redacted before persistence, so it is absent from A's
//     own later hydration, from the reopened on-disk facts.db, and from the
//     reopened on-disk BadgerDB buffer — while a plain marker written in the same
//     turn IS present, so the absence is redaction, not a dropped turn.
//
// TestFullStackE2E_DeterministicStubRealBackends is the deterministic proof
// (keyless stub model, whose SSE echo folds the hydrated system prompt back into
// the response verbatim — see internal/llm/stub.go buildStubWords "system=%q" —
// so every hydrated fragment is observable). TestFullStackE2E_RealModelAnthropic
// runs the SAME flow against the REAL Anthropic client, gated on
// ANTHROPIC_API_KEY (t.Skip when unset); its assertions are the model-independent
// raw-tier read-backs (consolidation, isolation, redaction), because a real
// model's output is not a verbatim echo.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// The three identities the flow exercises: the writer (tenantA/userA), a foreign
// tenant (tenantB/userB), and a same-tenant-different-user (tenantA/userB) plus a
// same-user-different-tenant (tenantB/userA) — the last two catch per-USER leaks,
// not just per-tenant ones.
const (
	fsTenantA = "fs-tenant-a"
	fsUserA   = "fs-user-a"
	fsTenantB = "fs-tenant-b"
	fsUserB   = "fs-user-b"
)

// fsMarkers are the four distinctive, purely-alphanumeric tokens the flow
// threads through the tiers. Two markers never collide, so a marker found in the
// wrong tenant's response/store is unambiguous proof of a leak, and the secret
// found in ANY tier is unambiguous proof redaction failed.
type fsMarkers struct {
	knowledge string // ingested KindDocument content (semantic RAG surface)
	fact      string // written fact content (facts FTS5 + KindFact dual-index surface)
	codename  string // plain token written in the SAME turn as the secret (must persist)
	secret    string // "sk-…" API-key token (must be redacted → NEVER persisted)
}

// newFSMarkers mints a fresh, collision-free marker set. secret is shaped as an
// OpenAI-style "sk-<20 hex>" token so the built-in redactor scrubs it
// (internal/guardrails redact.go reOpenAIKey) to [REDACTED_API_KEY] before the
// prompt reaches any tier.
func newFSMarkers(t *testing.T) fsMarkers {
	t.Helper()
	return fsMarkers{
		knowledge: marker(t, "kbdoc"),
		fact:      marker(t, "vaultcode"),
		codename:  marker(t, "codename"),
		secret:    marker(t, "sk-"), // marker() appends 20 hex chars → sk-<20hex>, matches the API-key detector
	}
}

// The retrieval phrasing shared by the positive control and every negative
// probe: it overlaps the stored facts/knowledge on real words ("alpha vault
// access code", "codename", "operations reference") but carries NONE of the
// markers, so a passing negative assertion is never vacuous — the paired
// positive control proves the same phrasing DOES surface the markers when the
// scope legitimately matches.
const fsRetrieval = "what is the alpha vault access code and the alpha vault codename and the operations reference"

// newOnDiskFullStackApp builds an App wired to REAL on-disk backends: BadgerDB +
// SQLite live under a t.TempDir() (AISOTROPY_DATA_DIR), with rqlite/Qdrant
// explicitly unset so the facts/semantic tiers fall back to their local on-disk /
// in-process defaults (never a networked fake). It runs NoAuth (these probes
// exercise hydration isolation + redaction, orthogonal to authentication) and an
// effectively unlimited rate limit (throttling would only mask hydration). The
// returned dataDir is where the raw-tier read-back reopens the stores.
func newOnDiskFullStackApp(t *testing.T, client LLMClient) (app *App, base, dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	t.Setenv("AISOTROPY_DATA_DIR", dataDir)
	t.Setenv("RQLITE_URL", "")  // facts → on-disk SQLite under dataDir, not networked rqlite
	t.Setenv("QDRANT_HOST", "") // semantic → in-process cosine, not networked Qdrant
	addr := freeAddr(t)
	app = New(
		WithAddr(addr),
		WithLLM(client),
		WithRateLimit(1e9, 1e9),
		WithAuthenticator(NoAuth()),
	)
	return app, "http://" + addr, dataDir
}

// bootFullStack starts App.Run on its own goroutine and returns a stop() that
// cancels the server and BLOCKS until Run returns — i.e. until graceful shutdown
// has flushed and closed every on-disk store. The raw-tier read-back must run
// AFTER stop() so it reopens files no longer held by the server.
func bootFullStack(t *testing.T, app *App, base string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	waitForServer(t, base+"/healthz")

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error on shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not return after ctx cancellation")
		}
	}
}

// seedTenantAWrites drives the two writer turns for tenant A over the shipped
// non-streaming chat route (which validates identity, runs the quota gate + input
// guardrail, and persists the REDACTED user turn to working memory + the
// consolidation outbox in one BadgerDB transaction):
//
//	(b) a fact turn — "Remember that <fact> is the alpha vault access code" — the
//	    heuristic extractor distils this into a durable fact.
//	(c) a secret turn — a plain <codename> AND an "sk-…" <secret> in one message;
//	    the secret is redacted before persistence, the codename is not.
func seedTenantAWrites(t *testing.T, base string, m fsMarkers) {
	t.Helper()
	if st, _ := postChat(t, base, fsTenantA, fsUserA, "fs-seed-fact",
		"Remember that "+m.fact+" is the alpha vault access code"); st != 200 {
		t.Fatalf("seed fact turn for tenantA: status %d", st)
	}
	if st, _ := postChat(t, base, fsTenantA, fsUserA, "fs-seed-secret",
		"Remember that the alpha vault codename is "+m.codename+" and the api key is "+m.secret); st != 200 {
		t.Fatalf("seed secret turn for tenantA: status %d", st)
	}
}

// fsCrossIdentities are the non-writer identities that must NEVER see tenant A's
// data: a foreign tenant, a same-tenant-different-user, and a
// same-user-different-tenant.
var fsCrossIdentities = []struct{ tenant, user string }{
	{fsTenantB, fsUserB},
	{fsTenantA, fsUserB},
	{fsTenantB, fsUserA},
}

// TestFullStackE2E_DeterministicStubRealBackends is the deterministic full-stack
// E2E: the keyless stub model, REAL on-disk BadgerDB + SQLite, in-process cosine
// semantic. One flow ingests knowledge, writes a fact, writes a secret-bearing
// turn, hydrates all three tiers through the streaming route, drives the outbox →
// consolidation path, asserts tenant B (and cross-user) isolation at every stage,
// and asserts input-redaction kept the secret out of every tier — then reopens
// the raw on-disk tiers and reads the data back to prove the stores are real.
func TestFullStackE2E_DeterministicStubRealBackends(t *testing.T) {
	m := newFSMarkers(t)
	app, base, dataDir := newOnDiskFullStackApp(t, fastStub())

	// (a) Ingest knowledge into the shared in-process semantic store BEFORE the
	// server boots. IngestKnowledge and Run share the same App-memoized semantic
	// index (ensureSemantic), so this KindDocument is exactly what the streaming
	// RAG hydration below reads.
	ctx := context.Background()
	n, err := app.IngestKnowledge(ctx, fsTenantA, fsUserA, Document{
		ID:      "fs-kb-1",
		Content: "Alpha vault operations reference " + m.knowledge + ": alpha vault access procedures, alpha vault codes, and the alpha vault codename registry.",
	})
	if err != nil {
		t.Fatalf("ingest knowledge: %v", err)
	}
	if n == 0 {
		t.Fatal("ingest knowledge wrote 0 chunks; the semantic RAG surface would be empty")
	}

	stop := bootFullStack(t, app, base)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	// (b) + (c): write the fact and the secret-bearing turn.
	seedTenantAWrites(t, base, m)

	// (d) POSITIVE CONTROL: poll the streaming route in a FRESH session until
	// tenant A retrieves its own consolidated fact. A fresh session means the hit
	// comes from the facts/semantic tiers (populated asynchronously by the outbox
	// worker), NOT the live working-memory buffer — so success proves the whole
	// chain: outbox → consolidation → facts FTS5 + KindFact vector, plus the
	// ingested KindDocument, all hydrated back into the system prompt (which the
	// stub echoes verbatim).
	var aBody string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, body := streamChat(t, base, fsTenantA, fsUserA, "fs-fresh-read-A", fsRetrieval)
		if st == 200 && strings.Contains(body, m.fact) {
			aBody = body
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if aBody == "" {
		t.Fatalf("positive control failed: tenantA never retrieved its own consolidated fact %q via facts/semantic hydration within the deadline; the chain is not wired", m.fact)
	}
	// A's own hydration must surface: the consolidated fact, the codename fact,
	// AND the ingested knowledge (semantic RAG) — proving all three tiers feed the
	// prompt. The secret must be ABSENT even here (redaction happens before
	// persistence, so A itself can never retrieve the raw secret).
	for _, want := range []struct{ label, tok string }{
		{"consolidated fact (facts FTS5 / KindFact RAG)", m.fact},
		{"codename fact", m.codename},
		{"ingested knowledge (KindDocument RAG)", m.knowledge},
	} {
		if !strings.Contains(aBody, want.tok) {
			t.Errorf("tenantA hydration missing %s (%q); that tier is not being exercised in-flow", want.label, want.tok)
		}
	}
	if strings.Contains(aBody, m.secret) {
		t.Errorf("REDACTION FAILED (live): raw secret %q surfaced in tenantA's OWN hydration — it should have been redacted before persistence", m.secret)
	}

	// ISOLATION (live): no other identity may retrieve ANY of tenant A's markers
	// or the secret, in any session, using the same retrieval phrasing.
	for _, id := range fsCrossIdentities {
		st, body := streamChat(t, base, id.tenant, id.user, "fs-probe", fsRetrieval)
		if st != 200 {
			t.Errorf("stream for %s/%s: status %d", id.tenant, id.user, st)
			continue
		}
		for _, leaked := range []struct{ label, tok string }{
			{"working-memory/facts fact marker", m.fact},
			{"facts codename marker", m.codename},
			{"ingested knowledge marker", m.knowledge},
			{"redacted secret", m.secret},
		} {
			if strings.Contains(body, leaked.tok) {
				t.Errorf("CROSS-TENANT LEAK: %s/%s saw tenantA's %s %q via hydration", id.tenant, id.user, leaked.label, leaked.tok)
			}
		}
	}

	// Shut the server down so BadgerDB + SQLite are flushed and closed, THEN read
	// the data back through the raw tier APIs to prove the stores are real
	// on-disk (not stubbed) and that isolation + redaction hold at the tier level.
	stop()
	stopped = true
	assertRawTiersReadBack(t, dataDir, m)
}

// TestFullStackE2E_RealModelAnthropic runs the SAME full-stack flow against the
// REAL Anthropic client, so a dev/CI with a key gets a real-model full-stack
// proof. It is GATED on ANTHROPIC_API_KEY (t.Skip when unset — the key is never
// faked). A real model does not echo the system prompt verbatim, so its
// assertions are the model-INDEPENDENT raw-tier read-backs (consolidation,
// isolation, redaction) — identical to the deterministic test's, exercised over
// the identical pipeline, just with a real client driving the reflection loop and
// the streaming call.
func TestFullStackE2E_RealModelAnthropic(t *testing.T) {
	key := envOrSkip(t, "ANTHROPIC_API_KEY")
	m := newFSMarkers(t)
	// The REAL Anthropic client explicitly (not llm.FromEnv, whose precedence
	// could pick OpenRouter/OpenAI if those keys were also present). Empty model →
	// llm.DefaultAnthropicModel.
	app, base, dataDir := newOnDiskFullStackApp(t, llm.NewAnthropicClient(key, ""))

	ctx := context.Background()
	n, err := app.IngestKnowledge(ctx, fsTenantA, fsUserA, Document{
		ID:      "fs-kb-1",
		Content: "Alpha vault operations reference " + m.knowledge + ": alpha vault access procedures, alpha vault codes, and the alpha vault codename registry.",
	})
	if err != nil {
		t.Fatalf("ingest knowledge: %v", err)
	}
	if n == 0 {
		t.Fatal("ingest knowledge wrote 0 chunks")
	}

	stop := bootFullStack(t, app, base)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	// Same writer turns — real LLM call runs inside ChatCore's reflection loop,
	// but persistence + consolidation are model-independent.
	seedTenantAWrites(t, base, m)

	// Exercise the streaming path against the real model too (real StreamChat over
	// the whole hydration chain); assert only that it completes with a 200 and a
	// non-empty body — the content is the model's, not a verbatim echo.
	if st, body := streamChat(t, base, fsTenantA, fsUserA, "fs-fresh-read-A", fsRetrieval); st != 200 || strings.TrimSpace(body) == "" {
		t.Fatalf("real-model streaming read: status=%d empty=%v", st, strings.TrimSpace(body) == "")
	}

	// Give the async outbox worker (poll interval 1s) time to consolidate the
	// writer turns into the facts + semantic tiers before shutdown. Bounded, and
	// well above the poll interval.
	time.Sleep(4 * time.Second)

	stop()
	stopped = true
	// Model-independent proof: the real-model flow consolidated tenant A's data
	// into the real on-disk tiers, isolation holds, and the secret was redacted.
	assertRawTiersReadBack(t, dataDir, m)
}

// assertRawTiersReadBack reopens the on-disk facts (SQLite+FTS5) and working
// memory (BadgerDB) through their RAW tier APIs — the definitive proof the stores
// are real (data survives a fresh handle after the server closed them) — and
// asserts, at the tier level:
//   - tenant A's consolidated fact + codename are present under A's scope,
//   - the raw secret is present in NEITHER tier (redaction held), while the
//     codename (written in the same turn) IS present (so absence == redaction),
//   - no cross identity's scope can read A's fact/codename (isolation).
func assertRawTiersReadBack(t *testing.T, dataDir string, m fsMarkers) {
	t.Helper()
	ctx := context.Background()

	// --- Facts tier: real on-disk SQLite + FTS5 ---
	fs, err := facts.OpenSQLite(filepath.Join(dataDir, "facts.db"))
	if err != nil {
		t.Fatalf("reopen on-disk facts.db: %v", err)
	}
	defer func() { _ = fs.Close() }()

	scA, err := facts.NewScope(fsTenantA, fsUserA)
	if err != nil {
		t.Fatalf("facts scope A: %v", err)
	}
	aFacts, err := fs.Search(ctx, scA, fsRetrieval, 50)
	if err != nil {
		t.Fatalf("facts search (A): %v", err)
	}
	aFactContent := joinFactContent(aFacts)
	if !strings.Contains(aFactContent, m.fact) {
		t.Errorf("reopened on-disk facts.db is missing tenantA's consolidated fact %q (consolidation did not reach the real SQLite tier); got: %q", m.fact, aFactContent)
	}
	if !strings.Contains(aFactContent, m.codename) {
		t.Errorf("reopened on-disk facts.db is missing tenantA's codename %q (the paired non-secret token that proves the turn was stored); got: %q", m.codename, aFactContent)
	}
	if strings.Contains(aFactContent, m.secret) {
		t.Errorf("REDACTION FAILED: raw secret %q persisted to the on-disk facts.db", m.secret)
	}

	// Isolation at the raw facts tier: no cross identity's scope returns A's data.
	for _, id := range fsCrossIdentities {
		sc, serr := facts.NewScope(id.tenant, id.user)
		if serr != nil {
			t.Fatalf("facts scope %s/%s: %v", id.tenant, id.user, serr)
		}
		r, ferr := fs.Search(ctx, sc, fsRetrieval, 50)
		if ferr != nil {
			t.Fatalf("facts search (%s/%s): %v", id.tenant, id.user, ferr)
		}
		if c := joinFactContent(r); strings.Contains(c, m.fact) || strings.Contains(c, m.codename) {
			t.Errorf("RAW-TIER FACTS ISOLATION BREACH: %s/%s's scope read tenantA's fact/codename; got: %q", id.tenant, id.user, c)
		}
	}

	// --- Working-memory tier: real on-disk BadgerDB ---
	wm, err := memory.Open(dataDir, 0, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("reopen on-disk BadgerDB working memory: %v", err)
	}
	defer func() { _ = wm.Close() }()

	aRecent, err := wm.Recent(ctx, memory.Key{Tenant: fsTenantA, User: fsUserA, Session: "fs-seed-secret"}, 50)
	if err != nil {
		t.Fatalf("working-memory Recent (A): %v", err)
	}
	aWMContent := joinMessageContent(aRecent)
	if !strings.Contains(aWMContent, m.codename) {
		t.Errorf("reopened on-disk BadgerDB buffer is missing tenantA's codename %q; the secret turn did not persist to the real working-memory tier; got: %q", m.codename, aWMContent)
	}
	if strings.Contains(aWMContent, m.secret) {
		t.Errorf("REDACTION FAILED: raw secret %q persisted to the on-disk BadgerDB working-memory buffer", m.secret)
	}

	// Isolation at the raw working-memory tier: the same session id under a
	// different tenant/user holds none of A's content.
	for _, id := range fsCrossIdentities {
		r, rerr := wm.Recent(ctx, memory.Key{Tenant: id.tenant, User: id.user, Session: "fs-seed-secret"}, 50)
		if rerr != nil {
			t.Fatalf("working-memory Recent (%s/%s): %v", id.tenant, id.user, rerr)
		}
		if c := joinMessageContent(r); strings.Contains(c, m.codename) || strings.Contains(c, m.fact) {
			t.Errorf("RAW-TIER WORKING-MEMORY ISOLATION BREACH: %s/%s's buffer held tenantA's content; got: %q", id.tenant, id.user, c)
		}
	}
}

// joinFactContent concatenates the content of scored facts for substring
// assertions.
func joinFactContent(fs []facts.ScoredFact) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// joinMessageContent concatenates the content of working-memory messages for
// substring assertions.
func joinMessageContent(msgs []memory.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// envOrSkip returns the named env var's value, or skips the test when it is
// unset/empty. Used to gate the real-model variant on ANTHROPIC_API_KEY without
// ever faking a credential.
func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Skipf("%s not set; skipping the real-model full-stack E2E (deterministic stub variant covers the flow)", name)
	}
	return v
}
