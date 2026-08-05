package orbit

import (
	"sync"

	"github.com/firebase/genkit/go/ai"
)

// SessionStore is the arena's conversation memory. This is the honest heart of
// the isolation story.
//
// Genkit-Go v1.10.0 DOES ship a session/memory primitive — the EXPERIMENTAL
// ai/exp Agents API with a pluggable SessionStore and a shipped in-memory
// localstore.NewInMemorySessionStore (persists+replays Messages across turns).
// But that primitive is keyed by a developer-chosen session id with NO
// tenant/user/product binding (its SessionSnapshot/SessionState carry only a
// SessionID field) — it provides PERSISTENCE, not ISOLATION.
//
// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
//
// This type is a deliberately minimal DIY stand-in with IDENTICAL semantics
// (session-id-keyed, tenant-unaware) so the end-to-end genkit.Generate leak
// path stays small and legible. The leak is NOT an artifact of the stand-in:
// orbit/isolation_test.go's TestRealSessionStoreIsTenantUnaware reproduces the
// exact same cross-tenant read against Genkit's REAL localstore SessionStore.
// Either way, multi-tenancy is developer-passed — you assemble []*ai.Message
// yourself and hand them to ai.WithMessages; the store key is a bare string,
// and nothing in the framework ties it to a tenant, so a wrong/shared key
// silently reads another tenant's history.
//
// (Verified against github.com/firebase/genkit/go v1.10.0: no tenant-scoped
// Session/Memory type exists; the exp SessionStore is session-id-keyed only.)
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string][]*ai.Message
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string][]*ai.Message)}
}

// Append adds a message under the developer-passed key. No tenant check exists
// or is possible at this layer — the key is just a string.
func (s *SessionStore) Append(key string, msgs ...*ai.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[key] = append(s.sessions[key], msgs...)
}

// History returns the conversation for a key. If the caller passes a key that
// belongs to a different tenant, this returns THAT tenant's messages — the
// framework cannot know the difference.
func (s *SessionStore) History(key string) []*ai.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ai.Message, len(s.sessions[key]))
	copy(out, s.sessions[key])
	return out
}
