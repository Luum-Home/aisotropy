package orbit

import (
	"sync"

	"github.com/firebase/genkit/go/ai"
)

// SessionStore is the arena's conversation memory. This is the honest heart of
// the isolation story: stable Genkit Go ships NO session/memory/thread
// primitive. Multi-tenancy is entirely developer-passed — you assemble
// []*ai.Message yourself and hand them to ai.WithMessages. The store key is a
// bare string chosen by the developer; nothing in the framework ties that key
// to a tenant, so a wrong key silently reads another tenant's history.
//
// (Verified against github.com/firebase/genkit/go v1.10.0: the genkit and ai
// packages expose Generate/Flows/Tools/Prompts but no Session type.)
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
