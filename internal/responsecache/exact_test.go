package responsecache

import (
	"testing"
	"time"
)

// testClock is a mutable clock for deterministic TTL tests.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newClockedLRU returns an lruStore whose clock is driven by clk, for
// deterministic TTL assertions.
func newClockedLRU(t *testing.T, capacity int, clk *testClock) *lruStore {
	t.Helper()
	s, ok := NewLRUStore(capacity).(*lruStore)
	if !ok {
		t.Fatalf("NewLRUStore did not return *lruStore")
	}
	s.now = clk.now
	return s
}

func TestNormalizePrompt(t *testing.T) {
	cases := []struct {
		in        string
		lowercase bool
		want      string
	}{
		{"  hello   world  ", false, "hello world"},
		{"hello\t\nworld", false, "hello world"},
		{"Hello World", false, "Hello World"},
		{"Hello World", true, "hello world"},
		{"  MiXeD\n Case ", true, "mixed case"},
	}
	for _, tc := range cases {
		if got := normalizePrompt(tc.in, tc.lowercase); got != tc.want {
			t.Errorf("normalizePrompt(%q, %v) = %q, want %q", tc.in, tc.lowercase, got, tc.want)
		}
	}
}

func TestExactCacheHitAfterNormalization(t *testing.T) {
	ec := NewExactCache(nil, time.Minute, false)
	sc := Scope{Tenant: "acme", User: "alice"}

	ec.Set(sc, "What is the capital of France?", "Paris")

	// Same prompt with different surrounding whitespace normalizes to the same
	// key and hits.
	got, ok := ec.Get(sc, "  What is   the capital of France?  ")
	if !ok {
		t.Fatalf("expected exact hit after normalization, got miss")
	}
	if got != "Paris" {
		t.Fatalf("got %q, want %q", got, "Paris")
	}
}

func TestExactCacheMiss(t *testing.T) {
	ec := NewExactCache(nil, time.Minute, false)
	sc := Scope{Tenant: "acme", User: "alice"}
	ec.Set(sc, "prompt one", "answer one")

	if _, ok := ec.Get(sc, "a totally different prompt"); ok {
		t.Fatalf("expected miss for an unseen prompt")
	}
}

func TestExactCaseSensitivityPolicy(t *testing.T) {
	sc := Scope{Tenant: "acme", User: "alice"}

	// Case-sensitive (default): different case => miss.
	cs := NewExactCache(nil, time.Minute, false)
	cs.Set(sc, "Hello", "hi")
	if _, ok := cs.Get(sc, "hello"); ok {
		t.Fatalf("case-sensitive cache should miss on case-only difference")
	}

	// Case-insensitive: different case => hit.
	ci := NewExactCache(nil, time.Minute, true)
	ci.Set(sc, "Hello", "hi")
	if _, ok := ci.Get(sc, "hello"); !ok {
		t.Fatalf("case-insensitive cache should hit on case-only difference")
	}
}

func TestExactCacheTTLExpiry(t *testing.T) {
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	store := newClockedLRU(t, 16, clk)
	ec := NewExactCache(store, 30*time.Second, false)
	sc := Scope{Tenant: "acme", User: "alice"}

	ec.Set(sc, "ephemeral", "value")

	// Within TTL: hit.
	clk.advance(29 * time.Second)
	if _, ok := ec.Get(sc, "ephemeral"); !ok {
		t.Fatalf("expected hit within TTL")
	}

	// Past TTL: miss, and the entry is evicted.
	clk.advance(2 * time.Second)
	if _, ok := ec.Get(sc, "ephemeral"); ok {
		t.Fatalf("expected miss after TTL expiry")
	}
	if store.Len() != 0 {
		t.Fatalf("expected expired entry to be evicted, Len=%d", store.Len())
	}
}

func TestLRUEviction(t *testing.T) {
	store := NewLRUStore(2)
	ec := NewExactCache(store, 0, false) // no TTL
	sc := Scope{Tenant: "acme", User: "alice"}

	ec.Set(sc, "one", "1")
	ec.Set(sc, "two", "2")
	ec.Set(sc, "three", "3") // evicts "one" (LRU)

	if _, ok := ec.Get(sc, "one"); ok {
		t.Fatalf("expected 'one' to be evicted by LRU")
	}
	if _, ok := ec.Get(sc, "two"); !ok {
		t.Fatalf("expected 'two' to still be present")
	}
	if _, ok := ec.Get(sc, "three"); !ok {
		t.Fatalf("expected 'three' to be present")
	}
	if store.Len() != 2 {
		t.Fatalf("expected Len=2 under capacity bound, got %d", store.Len())
	}
}

func TestLRURecencyOnGet(t *testing.T) {
	store := NewLRUStore(2)
	ec := NewExactCache(store, 0, false)
	sc := Scope{Tenant: "acme", User: "alice"}

	ec.Set(sc, "one", "1")
	ec.Set(sc, "two", "2")
	// Touch "one" so it becomes most-recently-used.
	if _, ok := ec.Get(sc, "one"); !ok {
		t.Fatalf("expected 'one' present before eviction")
	}
	ec.Set(sc, "three", "3") // should evict "two", not "one"

	if _, ok := ec.Get(sc, "two"); ok {
		t.Fatalf("expected 'two' to be evicted as least-recently-used")
	}
	if _, ok := ec.Get(sc, "one"); !ok {
		t.Fatalf("expected 'one' to survive as recently-used")
	}
}
