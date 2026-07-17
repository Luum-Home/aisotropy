// Package memory implements Aisotropy's working-memory tier (Phase 2 of the
// roadmap, see docs/architecture/memory-tiers.md -> "Working memory"): the
// active chat buffer plus its condensed summary, embedded in-process via
// BadgerDB (see docs/components/badgerdb-working-memory.md).
//
// This package deliberately does not implement the Transactional Outbox
// described in docs/architecture/consistency.md — that lands in Phase 5. The
// key layout below reserves the "tenant:...:chat/summary/seq" namespaces so
// an "outbox:pending:*" namespace can be added later without collisions.
package memory

import (
	"fmt"
	"strings"
)

// reservedKeyChars are characters that must never appear inside a key
// component: they are the separators used by the on-disk key encoding. If a
// caller could smuggle a colon into, say, a session id, it could forge a
// prefix collision between tenants — allowing one tenant to read another's
// buffer via a crafted key. Rejecting these characters outright is the
// enforcement point for tenant/user isolation at the key level (see the
// "Data model / keys" section of the component doc).
const reservedKeyChars = ":\x00\n"

// ValidateIdentityComponent applies the exact non-empty / no-reserved-
// character rule Key.Validate applies to each of its three components. It is
// exported so identity carriers outside this package that are not
// BadgerDB-keyed (e.g. internal/tenancy's Scope, built for the rqlite facts
// tier) can reuse the same rule instead of re-implementing a parallel
// character-class check that would have to be kept in lockstep with
// reservedKeyChars by hand. field is used only to build a readable error
// message (e.g. "tenant", "user").
func ValidateIdentityComponent(field, value string) error {
	if value == "" {
		return fmt.Errorf("memory: %s requires a non-empty value", field)
	}
	if strings.ContainsAny(value, reservedKeyChars) {
		return fmt.Errorf("memory: %s %q contains a reserved character", field, value)
	}
	return nil
}

// SubjectPrefix returns the BadgerDB key prefix that bounds EVERY
// working-memory key for a single (tenant,user) subject across ALL of that
// subject's sessions: "tenant:{tenant}:user:{user}:". It is the primitive
// behind subject-level erasure (GDPR Art.17, see WorkingMemory.EraseSubject):
// both components are validated by the same reserved-character rule
// Key.Validate applies, so a blank or malformed identity is rejected here
// rather than producing a wildcard prefix that could span subjects. The
// trailing ":" delimiter makes the prefix exact — user "a" can never match
// user "ab", because the reserved ":" cannot appear inside a component, so
// "tenant:t:user:a:" is not a prefix of "tenant:t:user:ab:".
func SubjectPrefix(tenant, user string) ([]byte, error) {
	if err := ValidateIdentityComponent("tenant", tenant); err != nil {
		return nil, err
	}
	if err := ValidateIdentityComponent("user", user); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("tenant:%s:user:%s:", tenant, user)), nil
}

// subjectLangKey returns the BadgerDB key holding the remembered language code
// for a single (tenant,user) subject: "tenant:{tenant}:user:{user}:lang". It is
// deliberately built off SubjectPrefix so the language value shares the exact
// subject scope — and the exact reserved-character validation — as every other
// working-memory read/write: a blank or malformed identity is rejected here
// rather than widened into a wildcard key, and the value lives UNDER the subject
// prefix so EraseSubject (GDPR Art.17) removes it along with the rest of the
// subject's data. The "lang" leaf cannot collide with a session's namespace
// ("...:session:{session}:...") because the reserved ":" can never appear inside
// a component, so no session id can spell "lang".
func subjectLangKey(tenant, user string) ([]byte, error) {
	prefix, err := SubjectPrefix(tenant, user)
	if err != nil {
		return nil, err
	}
	return append(prefix, "lang"...), nil
}

// Key identifies a single chat session's working-memory buffer, scoped by
// tenant and user. All three components are mandatory and explicit by
// design: a Key must never be built by ad hoc string concatenation
// elsewhere in the codebase, since that is exactly the free-form pattern
// that risks cross-tenant leakage.
type Key struct {
	Tenant  string
	User    string
	Session string
}

// Validate rejects empty components and components containing a reserved
// character. Every WorkingMemory operation calls this before touching
// BadgerDB, so an invalid Key can never reach the on-disk key space.
func (k Key) Validate() error {
	if err := ValidateIdentityComponent("tenant", k.Tenant); err != nil {
		return err
	}
	if err := ValidateIdentityComponent("user", k.User); err != nil {
		return err
	}
	if err := ValidateIdentityComponent("session", k.Session); err != nil {
		return err
	}
	return nil
}

// prefix returns the hierarchical, human-readable session prefix that all of
// this key's on-disk sub-keys share:
// "tenant:{tenant}:user:{user}:session:{session}".
//
// BadgerDB's LSM-tree keeps keys lexicographically sorted, so an iterator
// bounded by this prefix reads only this session's data even with many
// tenants coexisting in the same process — the property the component doc
// calls out as key to keeping reads in microseconds.
func (k Key) prefix() string {
	return fmt.Sprintf("tenant:%s:user:%s:session:%s", k.Tenant, k.User, k.Session)
}

// chatPrefix is the sub-namespace holding the active buffer's messages, one
// key per message (see chatMessageKey).
func (k Key) chatPrefix() []byte {
	return []byte(k.prefix() + ":chat:")
}

// chatMessageKey builds the key for a single active message. seq is a
// per-session monotonic counter (see seqKey); zero-padding it to 20 digits
// (enough for the full uint64 range) makes lexicographic order match
// numeric order, so a prefix iterator yields messages in insertion order.
func (k Key) chatMessageKey(seq uint64) []byte {
	return []byte(fmt.Sprintf("%s%020d", k.chatPrefix(), seq))
}

// summaryKey is the single key holding the condensed "session state"
// summary buffer, rewritten every time older messages are condensed.
func (k Key) summaryKey() []byte {
	return []byte(k.prefix() + ":summary")
}

// seqKey is the per-session monotonic sequence counter used to allocate
// ordered chat message keys. It never decreases, even as older messages are
// pruned from the active buffer, so newly appended messages always sort
// after already-pruned ones.
func (k Key) seqKey() []byte {
	return []byte(k.prefix() + ":seq")
}
