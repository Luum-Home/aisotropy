package aisotropy

import "testing"

// TestResolveListenAddr pins the container-serverless address precedence:
// explicit WithAddr > AISOTROPY_ADDR > $PORT (as ":"+PORT) > ":8080".
func TestResolveListenAddr(t *testing.T) {
	t.Setenv("AISOTROPY_ADDR", ":1111")
	t.Setenv("PORT", "2222")

	// Explicit optAddr (WithAddr) wins over every env source.
	if got := ResolveListenAddr(":9999"); got != ":9999" {
		t.Fatalf("explicit optAddr: got %q, want :9999", got)
	}
	// AISOTROPY_ADDR wins over $PORT when no explicit addr.
	if got := ResolveListenAddr(""); got != ":1111" {
		t.Fatalf("AISOTROPY_ADDR precedence: got %q, want :1111", got)
	}
	// $PORT is used (as ":"+PORT) when AISOTROPY_ADDR is unset — the Cloud Run /
	// Knative / ACA contract.
	t.Setenv("AISOTROPY_ADDR", "")
	if got := ResolveListenAddr(""); got != ":2222" {
		t.Fatalf("$PORT: got %q, want :2222", got)
	}
	// Default when nothing is set.
	t.Setenv("PORT", "")
	if got := ResolveListenAddr(""); got != ":8080" {
		t.Fatalf("default: got %q, want :8080", got)
	}
}
