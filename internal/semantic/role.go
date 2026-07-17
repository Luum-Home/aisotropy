// e5 query/passage instruction prefixes (ADR-0054). The intfloat/e5 model
// family — the ADR-0049 embedder default, multilingual-e5-small — was trained
// with ASYMMETRIC instruction prefixes: "query: " on the search text and
// "passage: " on the stored documents. Using the right prefix on each side of
// retrieval materially improves recall; using none (or the same prefix on both)
// degrades it. The Rust crate (crates/embed, ADR-0049) already carries an
// optional per-request `prefix` field; this file is the Go-side wiring that
// decides which prefix each call site needs and routes it there — the crate
// never infers query-vs-passage intent, the caller does.
//
// The guard is deliberately model-aware, not unconditional: a NON-e5 model (a
// HashEmbedder, an external OpenAI-compatible endpoint, or a local model not
// trained with these prefixes) MUST NOT be prefixed — a blanket "query: "/
// "passage: " is just noise that hurts it. So the prefix is applied only on an
// e5-aware RoleEmbedder that has prefixing enabled (see EmbedForRole); every
// other Embedder degrades to a plain, unprefixed Embed.

package semantic

import "context"

// EmbedRole identifies how an e5-family embedder should instruction-prefix a
// batch of texts (ADR-0054): search text ("query: ") vs stored documents/facts
// ("passage: "). It is the single vocabulary every role-aware call site speaks.
type EmbedRole int

const (
	// RoleUnspecified applies no instruction prefix. It is the zero value, so a
	// plain Embed call — and any call site with no query/passage intent — is
	// unprefixed, byte-identical to the pre-ADR-0054 behavior.
	RoleUnspecified EmbedRole = iota
	// RoleQuery marks search text: an e5 embedder prefixes it with "query: ".
	RoleQuery
	// RolePassage marks stored documents/facts: an e5 embedder prefixes them
	// with "passage: ".
	RolePassage
)

// e5QueryPrefix / e5PassagePrefix are the exact instruction prefixes the e5
// family expects, trailing space included. They mirror the crate's own
// convention (crates/embed apply_prefix, ADR-0049) so the Go caller and the
// Rust embedder agree byte-for-byte on the prepended string.
const (
	e5QueryPrefix   = "query: "
	e5PassagePrefix = "passage: "
)

// prefix returns the e5 instruction prefix for r, or "" for RoleUnspecified.
func (r EmbedRole) prefix() string {
	switch r {
	case RoleQuery:
		return e5QueryPrefix
	case RolePassage:
		return e5PassagePrefix
	default:
		return ""
	}
}

// String renders the role for logs/tests.
func (r EmbedRole) String() string {
	switch r {
	case RoleQuery:
		return "query"
	case RolePassage:
		return "passage"
	default:
		return "unspecified"
	}
}

// RoleEmbedder is the OPTIONAL capability an Embedder implements when it can
// apply an e5 query:/passage: instruction prefix (ADR-0054). LocalEmbedder and
// DaemonEmbedder implement it — and honor it only when e5 prefixing is enabled
// for their configured model. HashEmbedder and APIEmbedder do NOT implement it,
// so on those EmbedForRole degrades to a plain Embed and the role is a no-op.
type RoleEmbedder interface {
	Embedder
	// EmbedForRole embeds texts, applying role's e5 instruction prefix when the
	// embedder is configured for an e5-family model; otherwise it is identical
	// to Embed. Vectors are returned in input order, one per text.
	EmbedForRole(ctx context.Context, texts []string, role EmbedRole) ([][]float32, error)
}

// EmbedForRole is the single choke point every role-aware call site routes
// through. When emb is a RoleEmbedder it forwards role (so an e5 embedder can
// prefix); for any other Embedder it falls back to emb.Embed, leaving a
// non-e5/external embedder UNAFFECTED. This keeps the query/passage convention
// (ADR-0054) a property of the e5 path only, never a blanket transform applied
// to every model.
func EmbedForRole(ctx context.Context, emb Embedder, texts []string, role EmbedRole) ([][]float32, error) {
	if re, ok := emb.(RoleEmbedder); ok {
		return re.EmbedForRole(ctx, texts, role)
	}
	return emb.Embed(ctx, texts)
}

// EmbedQuery embeds search text with the e5 "query: " prefix (on an e5
// RoleEmbedder). The query-embedding step before a Store.Search MUST use this
// instead of Embed, so a search matches the "passage: "-prefixed corpus. On a
// non-e5/external embedder it is a plain, unprefixed Embed.
func EmbedQuery(ctx context.Context, emb Embedder, texts []string) ([][]float32, error) {
	return EmbedForRole(ctx, emb, texts, RoleQuery)
}

// EmbedPassage embeds stored documents/facts with the e5 "passage: " prefix.
// Ingest (KindDocument) and fact/turn consolidation (KindFact/KindTurn) use it
// so the stored corpus carries the passage prefix an e5 "query: " search is
// trained to match. On a non-e5/external embedder it is a plain Embed.
func EmbedPassage(ctx context.Context, emb Embedder, texts []string) ([][]float32, error) {
	return EmbedForRole(ctx, emb, texts, RolePassage)
}
