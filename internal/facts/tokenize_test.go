package facts

import (
	"context"
	"testing"
)

// TestSearchAccentFoldingLatin proves the ADR-0051 tokenizer change
// (`unicode61 remove_diacritics 2`) accent-folds Latin text on BOTH sides of
// the match: a stored accented term is found by an unaccented query and vice
// versa. This is the recall win that replaces English-only porter stemming for
// the ES/PT/DE/FR Latin-script case.
func TestSearchAccentFoldingLatin(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{tenant: "acme", user: "alice"}

	// Spanish + French facts with diacritics.
	facts := []Fact{
		{ID: "es", TenantID: "acme", UserID: "alice",
			Content: "la canción de la reunión anual", Importance: 0.5},
		{ID: "fr", TenantID: "acme", UserID: "alice",
			Content: "réunion budgétaire à Genève", Importance: 0.5},
	}
	for _, f := range facts {
		if err := st.Save(context.Background(), sc, f); err != nil {
			t.Fatalf("Save(%s): %v", f.ID, err)
		}
	}

	cases := []struct {
		name  string
		query string
		want  string // fact ID expected in results
	}{
		{"accented stored, unaccented query", "cancion", "es"},
		{"unaccented stored form via accented query", "canción", "es"},
		{"french accented term, unaccented query", "reunion", "fr"},
		{"french accented query matches", "budgétaire", "fr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.Search(context.Background(), sc, tc.query, 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", tc.query, err)
			}
			if !containsID(got, tc.want) {
				t.Fatalf("Search(%q) = %v, want to contain %q", tc.query, ids(got), tc.want)
			}
		})
	}
}

// TestSearchCJKBigramPath proves the bigram shim makes space-less CJK text
// searchable — the exact case unicode61 alone returns 0 rows for (a run with no
// interior spaces collapses to one token). Japanese and Chinese phrases are
// saved, then found by a shorter substring of the same run.
func TestSearchCJKBigramPath(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{tenant: "acme", user: "alice"}

	facts := []Fact{
		// Japanese: "the user is refactoring the billing database"
		{ID: "ja", TenantID: "acme", UserID: "alice",
			Content: "ユーザーは請求データベースをリファクタリングしています", Importance: 0.5},
		// Chinese: "user preference: dark theme"
		{ID: "zh", TenantID: "acme", UserID: "alice",
			Content: "用户偏好深色主题", Importance: 0.5},
	}
	for _, f := range facts {
		if err := st.Save(context.Background(), sc, f); err != nil {
			t.Fatalf("Save(%s): %v", f.ID, err)
		}
	}

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"japanese substring 'database'", "データベース", "ja"},
		{"japanese two-char substring", "請求", "ja"},
		{"chinese substring 'dark theme'", "深色主题", "zh"},
		{"chinese two-char substring", "偏好", "zh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.Search(context.Background(), sc, tc.query, 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", tc.query, err)
			}
			if !containsID(got, tc.want) {
				t.Fatalf("Search(%q) = %v, want to contain %q", tc.query, ids(got), tc.want)
			}
		})
	}

	// Cross-fact isolation of the bigram path: a Chinese query must not surface
	// the Japanese fact and vice versa (different bigrams, no accidental match).
	got, err := st.Search(context.Background(), sc, "深色主题", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if containsID(got, "ja") {
		t.Fatalf("Chinese query leaked the Japanese fact: %v", ids(got))
	}
}

// TestFtsIndexTextBigrams is a pure unit test of the index-time transform: CJK
// runs become space-separated overlapping bigrams; Latin text passes through
// unchanged so unicode61 tokenizes it as before.
func TestFtsIndexTextBigrams(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello world", "hello world"}, // pure Latin: verbatim
		{"café", "café"},               // diacritics left to unicode61
		{"数据库", " 数据 据库 "},             // 3-char CJK -> 2 bigrams
		{"标", " 标 "},                   // lone ideograph -> itself
		{"db数据", "db 数据 "},             // mixed Latin+CJK boundary
		{"数据db", " 数据 db"},             // CJK then Latin
	}
	for _, tc := range cases {
		if got := ftsIndexText(tc.in); got != tc.want {
			t.Errorf("ftsIndexText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFtsQueryBigrams pins the query-time transform mirrors the index side.
func TestFtsQueryBigrams(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"a", ""},                  // single Latin char dropped as noise
		{"database", `"database"`}, // whole Latin word, one quoted term
		{"数据库", `"数据" OR "据库"`},    // CJK -> quoted bigram terms
		{"标", `"标"`},               // lone ideograph -> single term
		{"db 数据", `"db" OR "数据"`},  // Latin term OR one CJK bigram
	}
	for _, tc := range cases {
		if got := ftsQuery(tc.in); got != tc.want {
			t.Errorf("ftsQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func containsID(fs []ScoredFact, id string) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

func ids(fs []ScoredFact) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.ID
	}
	return out
}
