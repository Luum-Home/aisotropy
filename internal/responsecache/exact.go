package responsecache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Scope is the (tenant, user) identity a cache entry is confined to. It is a
// small comparable value used directly as the semantic partition-map key and
// mixed into every exact-cache key, so isolation is by construction: two
// distinct (tenant, user) pairs can never collide on a key or share a
// partition.
//
// Callers MUST derive Scope from the request's validated identity — the
// tenant_id/user_id path params internal/tenancy validates at the handler
// boundary — never from the request body/header. This package deliberately
// keeps Scope a plain struct (rather than importing tenancy.Scope, whose
// unexported fields make it unconstructable outside its package) so it stays a
// standalone, testable unit; the caller does the tenancy validation before
// constructing it.
type Scope struct {
	Tenant string
	User   string
}

// normalizePrompt canonicalizes a prompt so trivially-different phrasings map
// to the same exact-cache key. It trims leading/trailing whitespace and
// collapses every run of internal whitespace (spaces, tabs, newlines) to a
// single space via strings.Fields. When lowercase is true it also lowercases,
// so case-only differences collide; case-sensitivity is a documented,
// caller-chosen policy (NewExactCache's lowercase flag) because for some
// prompts case is semantically significant.
func normalizePrompt(prompt string, lowercase bool) string {
	normalized := strings.Join(strings.Fields(prompt), " ")
	if lowercase {
		normalized = strings.ToLower(normalized)
	}
	return normalized
}

// exactKey derives the exact-cache key: sha256(tenant \0 user \0 normalized).
// The two NUL separators are unambiguous because a validated tenant/user can
// never itself contain NUL (see internal/memory's identity validation), so no
// pair of distinct (tenant, user, prompt) triples can alias to the same byte
// sequence — the structural guarantee behind cross-tenant isolation on the
// exact path.
func exactKey(scope Scope, normalized string) string {
	h := sha256.New()
	h.Write([]byte(scope.Tenant))
	h.Write([]byte{0})
	h.Write([]byte(scope.User))
	h.Write([]byte{0})
	h.Write([]byte(normalized))
	return hex.EncodeToString(h.Sum(nil))
}

// Store is the backing key/value engine for the exact cache. Implementations
// enforce a TTL and a size bound. Keys are already tenant/user-scoped opaque
// hashes (see exactKey), so a Store implementation need not — and must not — be
// scope-aware itself; isolation lives entirely in the key.
type Store interface {
	// Get returns the stored value for key and whether it was present and
	// unexpired. An expired entry reports (\"\", false) and may be evicted.
	Get(key string) (string, bool)
	// Set stores value under key with the given ttl. ttl <= 0 means the entry
	// never expires by time (it can still be evicted by the size bound).
	Set(key, value string, ttl time.Duration)
	// Len reports the number of live entries currently held.
	Len() int
}

// lruEntry is one node in the LRU list.
type lruEntry struct {
	key       string
	value     string
	expiresAt time.Time // zero => no time-based expiry
}

// lruStore is the default Store: an in-memory LRU with a fixed capacity and
// per-entry TTL. It is safe for concurrent use.
type lruStore struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List               // front = most-recently-used
	items    map[string]*list.Element // key -> element holding *lruEntry
	now      func() time.Time         // injectable clock (tests set this)
}

// NewLRUStore returns the default exact-cache Store: an in-memory LRU bounded
// to capacity entries. A capacity <= 0 is treated as 1 (a zero-capacity cache
// would store nothing, which is almost never intended).
func NewLRUStore(capacity int) Store {
	if capacity <= 0 {
		capacity = 1
	}
	return &lruStore{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element, capacity),
		now:      time.Now,
	}
}

// expired reports whether e has a TTL that has elapsed as of t.
func (e *lruEntry) expired(t time.Time) bool {
	return !e.expiresAt.IsZero() && t.After(e.expiresAt)
}

func (s *lruStore) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	el, ok := s.items[key]
	if !ok {
		return "", false
	}
	entry := el.Value.(*lruEntry)
	if entry.expired(s.now()) {
		// Lazily evict expired entries on read.
		s.removeElement(el)
		return "", false
	}
	s.ll.MoveToFront(el)
	return entry.value, true
}

func (s *lruStore) Set(key, value string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = s.now().Add(ttl)
	}

	if el, ok := s.items[key]; ok {
		entry := el.Value.(*lruEntry)
		entry.value = value
		entry.expiresAt = expiresAt
		s.ll.MoveToFront(el)
		return
	}

	el := s.ll.PushFront(&lruEntry{key: key, value: value, expiresAt: expiresAt})
	s.items[key] = el

	if s.ll.Len() > s.capacity {
		s.evictOldest()
	}
}

func (s *lruStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// evictOldest removes the least-recently-used entry. Caller holds s.mu.
func (s *lruStore) evictOldest() {
	el := s.ll.Back()
	if el != nil {
		s.removeElement(el)
	}
}

// removeElement unlinks el from both the list and the index. Caller holds s.mu.
func (s *lruStore) removeElement(el *list.Element) {
	s.ll.Remove(el)
	delete(s.items, el.Value.(*lruEntry).key)
}

// ExactCache is the exact (hash) response cache: it answers a Get only when the
// normalized prompt is byte-for-byte identical to a stored one under the same
// (tenant, user) scope. It is the cheap first layer of ResponseCache.
type ExactCache struct {
	store     Store
	ttl       time.Duration
	lowercase bool
}

// NewExactCache returns an ExactCache over store with the given per-entry ttl
// and case policy. A nil store defaults to an LRU of 1024 entries. ttl <= 0
// disables time-based expiry. When lowercase is true, prompts differing only in
// case collide (see normalizePrompt).
func NewExactCache(store Store, ttl time.Duration, lowercase bool) *ExactCache {
	if store == nil {
		store = NewLRUStore(1024)
	}
	return &ExactCache{store: store, ttl: ttl, lowercase: lowercase}
}

// Get returns the cached response for (scope, prompt) if one exists and has not
// expired. The scope is baked into the key, so a match under a different
// tenant/user is structurally impossible.
func (c *ExactCache) Get(scope Scope, prompt string) (string, bool) {
	key := exactKey(scope, normalizePrompt(prompt, c.lowercase))
	return c.store.Get(key)
}

// Set stores response for (scope, prompt) under the scoped key.
func (c *ExactCache) Set(scope Scope, prompt, response string) {
	key := exactKey(scope, normalizePrompt(prompt, c.lowercase))
	c.store.Set(key, response, c.ttl)
}
