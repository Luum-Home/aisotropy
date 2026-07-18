package semantic

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// recordingRoleEmbedder is a fake RoleEmbedder that records the roles it was
// asked to embed for, so a test can assert the query/passage prefix wiring
// (ADR-0054) at the Embedder boundary without running the Rust crate.
type recordingRoleEmbedder struct {
	dim   int
	roles []EmbedRole
	texts [][]string
}

func (e *recordingRoleEmbedder) Dimension() int { return e.dim }

func (e *recordingRoleEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.EmbedForRole(ctx, texts, RoleUnspecified)
}

func (e *recordingRoleEmbedder) EmbedForRole(_ context.Context, texts []string, role EmbedRole) ([][]float32, error) {
	e.roles = append(e.roles, role)
	e.texts = append(e.texts, append([]string(nil), texts...))
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, e.dim)
		if e.dim > 0 {
			v[0] = 1
		}
		out[i] = v
	}
	return out, nil
}

// recordingPlainEmbedder is a fake that implements ONLY Embedder (not
// RoleEmbedder) — like HashEmbedder or an external APIEmbedder — so a test can
// prove EmbedForRole degrades to a plain, unprefixed Embed on a non-e5 model.
type recordingPlainEmbedder struct {
	dim         int
	embedCalls  int
	lastTexts   []string
	roleAwareOK bool // stays false: this type must never see a role
}

func (e *recordingPlainEmbedder) Dimension() int { return e.dim }

func (e *recordingPlainEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.embedCalls++
	e.lastTexts = append([]string(nil), texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, e.dim)
	}
	return out, nil
}

func TestEmbedQueryForwardsQueryRole(t *testing.T) {
	emb := &recordingRoleEmbedder{dim: 4}
	if _, err := EmbedQuery(context.Background(), emb, []string{"hola"}); err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(emb.roles) != 1 || emb.roles[0] != RoleQuery {
		t.Fatalf("EmbedQuery role = %v, want [query]", emb.roles)
	}
	if got := RoleQuery.prefix(); got != "query: " {
		t.Fatalf("RoleQuery.prefix() = %q, want %q", got, "query: ")
	}
}

func TestEmbedPassageForwardsPassageRole(t *testing.T) {
	emb := &recordingRoleEmbedder{dim: 4}
	if _, err := EmbedPassage(context.Background(), emb, []string{"documento"}); err != nil {
		t.Fatalf("EmbedPassage: %v", err)
	}
	if len(emb.roles) != 1 || emb.roles[0] != RolePassage {
		t.Fatalf("EmbedPassage role = %v, want [passage]", emb.roles)
	}
	if got := RolePassage.prefix(); got != "passage: " {
		t.Fatalf("RolePassage.prefix() = %q, want %q", got, "passage: ")
	}
}

// TestEmbedForRolePlainEmbedderUnaffected proves that a non-e5/external
// embedder (one that does NOT implement RoleEmbedder) is untouched by the
// query/passage convention: EmbedForRole falls back to plain Embed and the
// role never reaches it.
func TestEmbedForRolePlainEmbedderUnaffected(t *testing.T) {
	emb := &recordingPlainEmbedder{dim: 3}
	if _, err := EmbedForRole(context.Background(), emb, []string{"x"}, RoleQuery); err != nil {
		t.Fatalf("EmbedForRole: %v", err)
	}
	if _, err := EmbedForRole(context.Background(), emb, []string{"y"}, RolePassage); err != nil {
		t.Fatalf("EmbedForRole: %v", err)
	}
	if emb.embedCalls != 2 {
		t.Fatalf("plain embedder Embed calls = %d, want 2 (query+passage both degrade to Embed)", emb.embedCalls)
	}
	if emb.roleAwareOK {
		t.Fatal("plain embedder must never observe a role")
	}
}

// TestIngestUsesPassageRole proves the ingest path (KindDocument) embeds with
// the passage role, so an e5 corpus is stored "passage: "-prefixed.
func TestIngestUsesPassageRole(t *testing.T) {
	ctx := context.Background()
	sc, err := NewScope("tenant-a", "user-1")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	emb := &recordingRoleEmbedder{dim: 4}
	store := NewInMemoryStore(4)
	defer func() { _ = store.Close() }()

	n, err := Ingest(ctx, store, emb, sc, DefaultChunkOptions(), []Document{{ID: "doc-1", Content: "cuerpo del documento"}})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n != 1 {
		t.Fatalf("Ingest wrote %d chunks, want 1", n)
	}
	if len(emb.roles) != 1 || emb.roles[0] != RolePassage {
		t.Fatalf("Ingest embed role = %v, want [passage]", emb.roles)
	}
}

// TestLocalEmbedderPrefixForModelAware proves the model-aware guard: an e5
// embedder emits query:/passage: prefixes, while a non-e5 embedder emits none
// for the same roles (a blanket prefix must not hurt a non-e5 model).
func TestLocalEmbedderPrefixForModelAware(t *testing.T) {
	e5, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "x", ModelDir: "y", Dimension: 4, E5Prefixes: true})
	if err != nil {
		t.Fatalf("NewLocalEmbedder(e5): %v", err)
	}
	if got := e5.prefixFor(RoleQuery); got != "query: " {
		t.Errorf("e5 prefixFor(query) = %q, want %q", got, "query: ")
	}
	if got := e5.prefixFor(RolePassage); got != "passage: " {
		t.Errorf("e5 prefixFor(passage) = %q, want %q", got, "passage: ")
	}
	if got := e5.prefixFor(RoleUnspecified); got != "" {
		t.Errorf("e5 prefixFor(unspecified) = %q, want empty", got)
	}

	nonE5, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "x", ModelDir: "y", Dimension: 4, E5Prefixes: false})
	if err != nil {
		t.Fatalf("NewLocalEmbedder(non-e5): %v", err)
	}
	for _, role := range []EmbedRole{RoleQuery, RolePassage, RoleUnspecified} {
		if got := nonE5.prefixFor(role); got != "" {
			t.Errorf("non-e5 prefixFor(%v) = %q, want empty (unaffected)", role, got)
		}
	}
}

// TestLocalEmbedderWriteRequestCarriesPrefix asserts on the exact NDJSON
// request the crate reads: an e5 embedder writes {"prefix":"query: "} on each
// request line, and a non-e5 embedder omits the field entirely (byte-identical
// to the pre-ADR-0054 request). This is the strongest evidence — the literal
// bytes sent to the embedder subprocess — and needs no built binary.
func TestLocalEmbedderWriteRequestCarriesPrefix(t *testing.T) {
	e5, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "x", ModelDir: "y", Dimension: 4, E5Prefixes: true})
	if err != nil {
		t.Fatalf("NewLocalEmbedder: %v", err)
	}

	// e5 + query role -> every line carries the query: prefix.
	inPath, _, cleanup, err := e5.writeRequest([]string{"a", "b"}, e5.prefixFor(RoleQuery))
	if err != nil {
		t.Fatalf("writeRequest: %v", err)
	}
	defer cleanup()
	for _, req := range readRequests(t, inPath) {
		if req.Prefix != "query: " {
			t.Errorf("e5 query request %q prefix = %q, want %q", req.ID, req.Prefix, "query: ")
		}
	}

	// e5 + passage role -> passage: prefix.
	inPath2, _, cleanup2, err := e5.writeRequest([]string{"a"}, e5.prefixFor(RolePassage))
	if err != nil {
		t.Fatalf("writeRequest: %v", err)
	}
	defer cleanup2()
	if got := readRequests(t, inPath2)[0].Prefix; got != "passage: " {
		t.Errorf("e5 passage prefix = %q, want %q", got, "passage: ")
	}

	// non-e5 -> the raw JSON must NOT contain a "prefix" field at all.
	nonE5, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "x", ModelDir: "y", Dimension: 4, E5Prefixes: false})
	if err != nil {
		t.Fatalf("NewLocalEmbedder(non-e5): %v", err)
	}
	inPath3, _, cleanup3, err := nonE5.writeRequest([]string{"a"}, nonE5.prefixFor(RoleQuery))
	if err != nil {
		t.Fatalf("writeRequest: %v", err)
	}
	defer cleanup3()
	raw, err := os.ReadFile(inPath3)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	if strings.Contains(string(raw), "prefix") {
		t.Errorf("non-e5 request must omit the prefix field, got: %s", strings.TrimSpace(string(raw)))
	}
}

// TestLocalEmbedRequestOmitsEmptyPrefix pins the byte-compatibility contract:
// an empty Prefix is omitted from the JSON (the crate's `prefix` is
// #[serde(default)], so absence == unprefixed), while a non-empty Prefix is
// present.
func TestLocalEmbedRequestOmitsEmptyPrefix(t *testing.T) {
	empty, err := json.Marshal(localEmbedRequest{ID: "r0", Text: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), "prefix") {
		t.Errorf("empty prefix must be omitted, got %s", empty)
	}
	set, err := json.Marshal(localEmbedRequest{ID: "r0", Text: "hi", Prefix: "query: "})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(set), `"prefix":"query: "`) {
		t.Errorf("non-empty prefix must be present, got %s", set)
	}
}

func TestDaemonEmbedderPrefixForModelAware(t *testing.T) {
	e5, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: "x", ModelDir: "y", Dimension: 4, E5Prefixes: true})
	if err != nil {
		t.Fatalf("NewDaemonEmbedder: %v", err)
	}
	if got := e5.prefixFor(RoleQuery); got != "query: " {
		t.Errorf("e5 daemon prefixFor(query) = %q, want %q", got, "query: ")
	}
	if got := e5.prefixFor(RolePassage); got != "passage: " {
		t.Errorf("e5 daemon prefixFor(passage) = %q, want %q", got, "passage: ")
	}
	nonE5, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: "x", ModelDir: "y", Dimension: 4})
	if err != nil {
		t.Fatalf("NewDaemonEmbedder(non-e5): %v", err)
	}
	if got := nonE5.prefixFor(RoleQuery); got != "" {
		t.Errorf("non-e5 daemon prefixFor(query) = %q, want empty", got)
	}
}

// TestIsE5Model checks the model-aware autodetect: e5-family paths enable
// prefixing, unrelated model names do not (and are NOT false-positive matched).
func TestIsE5Model(t *testing.T) {
	e5Paths := []string{
		"/models/multilingual-e5-small",
		"/opt/e5-base-v2",
		"/var/lib/E5-Large",
		"models/intfloat_multilingual-e5-small",
	}
	for _, p := range e5Paths {
		if !isE5Model(p) {
			t.Errorf("isE5Model(%q) = false, want true", p)
		}
	}
	nonE5Paths := []string{
		"/models/bge-small-en",
		"/models/all-MiniLM-L6-v2",
		"/models/base5-experimental", // must NOT false-match a bare "e5" substring
		"/models/nomic-embed-text",
	}
	for _, p := range nonE5Paths {
		if isE5Model(p) {
			t.Errorf("isE5Model(%q) = true, want false", p)
		}
	}
}

// TestE5PrefixesEnabledEnvOverride proves AISOTROPY_EMBED_E5_PREFIXES is
// authoritative over the model-dir autodetect (both directions).
func TestE5PrefixesEnabledEnvOverride(t *testing.T) {
	t.Setenv("AISOTROPY_EMBED_E5_PREFIXES", "false")
	if e5PrefixesEnabled("/models/multilingual-e5-small") {
		t.Error("explicit false must disable prefixes even for an e5 model dir")
	}
	t.Setenv("AISOTROPY_EMBED_E5_PREFIXES", "true")
	if !e5PrefixesEnabled("/models/bge-small") {
		t.Error("explicit true must enable prefixes even for a non-e5 model dir")
	}
	t.Setenv("AISOTROPY_EMBED_E5_PREFIXES", "")
	if !e5PrefixesEnabled("/models/multilingual-e5-small") {
		t.Error("unset env must autodetect e5 from the model dir")
	}
	if e5PrefixesEnabled("/models/bge-small") {
		t.Error("unset env must leave a non-e5 model unprefixed")
	}
}

// TestEmbedRoleString covers EmbedRole.String's three named cases plus the
// default fallback for any other value (a defensive branch for a future role
// added without updating String).
func TestEmbedRoleString(t *testing.T) {
	cases := []struct {
		r    EmbedRole
		want string
	}{
		{RoleQuery, "query"},
		{RolePassage, "passage"},
		{RoleUnspecified, "unspecified"},
		{EmbedRole(99), "unspecified"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("EmbedRole(%d).String() = %q, want %q", c.r, got, c.want)
		}
	}
}

// readRequests parses an NDJSON request file into localEmbedRequest rows.
func readRequests(t *testing.T, path string) []localEmbedRequest {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open request: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []localEmbedRequest
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req localEmbedRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("decode request line %q: %v", line, err)
		}
		out = append(out, req)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan requests: %v", err)
	}
	return out
}
