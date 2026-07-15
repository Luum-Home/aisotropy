package auth

import (
	"context"
	"crypto/tls"
	"testing"
)

// fakeRequest is a canned auth.Request for driving Authenticators in isolation.
type fakeRequest struct {
	tenant, user string
	headers      map[string]string
	tls          *tls.ConnectionState
}

func (r fakeRequest) PathTenant() string { return r.tenant }
func (r fakeRequest) PathUser() string   { return r.user }
func (r fakeRequest) Header(name string) string {
	if r.headers == nil {
		return ""
	}
	return r.headers[name]
}
func (r fakeRequest) TLS() *tls.ConnectionState { return r.tls }

func TestNoAuth_TrustsPathIdentityVerbatim(t *testing.T) {
	p, err := NoAuth().Authenticate(context.Background(), fakeRequest{tenant: "acme", user: "alice"})
	if err != nil {
		t.Fatalf("NoAuth returned error: %v", err)
	}
	if p.TenantID != "acme" || p.UserID != "alice" {
		t.Fatalf("NoAuth principal = %+v, want tenant=acme user=alice", p)
	}
	if len(p.Scopes) != 0 {
		t.Errorf("NoAuth should carry no scopes, got %v", p.Scopes)
	}
}
