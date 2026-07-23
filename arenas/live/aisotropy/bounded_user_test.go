package main

import "testing"

// TestBoundedUser pins the HTTP-pattern reconciliation that fixed the recall-only
// 422 (see mode_memory_ext.go): a user_id within aisotropy's ^[a-zA-Z0-9_-]{1,64}$
// path pattern passes through verbatim; one that exceeds 64 chars is mapped to a
// stable, pattern-safe, deterministic surrogate so persist and recall agree.
func TestBoundedUser(t *testing.T) {
	// Short, valid user_ids pass through unchanged (persist/recall use them raw).
	for _, u := range []string{
		"u-memory_cross_session-xsession-01-empid-r1784777956191710000",           // 61 chars
		"u-memory_semantic_recall-semantic-02-brooch-r1784777956191710000-x"[:64], // exactly 64
		"live",
	} {
		if got := boundedUser(u); got != u {
			t.Errorf("boundedUser(%q)=%q; want unchanged (it is within the HTTP pattern)", u, got)
		}
	}

	// Over-long user_ids (the ones that 422'd) map to a bounded, pattern-safe token.
	long1 := "u-memory_semantic_recall-semantic-01-sparekey-r1784777956191710000"    // 66
	long2 := "u-memory_consolidation-consolidation-01-codename-r1784777956191710000" // 69
	for _, u := range []string{long1, long2} {
		got := boundedUser(u)
		if len(got) > 64 {
			t.Errorf("boundedUser(%q) len=%d exceeds the 64-char HTTP cap", u, len(got))
		}
		if !httpUserIDPattern.MatchString(got) {
			t.Errorf("boundedUser(%q)=%q does not satisfy the HTTP user_id pattern", u, got)
		}
	}

	// Deterministic: same input -> same token (persist and recall must agree).
	if boundedUser(long1) != boundedUser(long1) {
		t.Errorf("boundedUser is not deterministic for %q", long1)
	}
	// Isolation: distinct over-long users -> distinct tokens.
	if boundedUser(long1) == boundedUser(long2) {
		t.Errorf("boundedUser aliased two distinct user_ids into one scope — isolation broken")
	}
}
