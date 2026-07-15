package aisotropy

import "testing"

// TestAuthenticatorFromEnv is the RESIDUAL #1 acceptance criterion: the shipped
// binary is securable by config. With AISOTROPY_JWT_SECRET set the selector
// returns a real HS256 JWT authenticator ("jwt-hs256"); unset it returns the
// explicit insecure NoAuth opt-out ("noauth"). The secret value is never part
// of the returned mode label.
func TestAuthenticatorFromEnv(t *testing.T) {
	t.Run("secret set enables jwt-hs256", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", "s3cr3t-do-not-log")

		authr, mode := AuthenticatorFromEnv()
		if mode != "jwt-hs256" {
			t.Errorf("mode = %q, want %q", mode, "jwt-hs256")
		}
		if authr == nil {
			t.Fatal("AuthenticatorFromEnv() returned a nil Authenticator with a secret set")
		}
		if mode == "s3cr3t-do-not-log" {
			t.Error("mode label leaked the secret value")
		}
	})

	t.Run("secret unset defaults to noauth", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", "")

		authr, mode := AuthenticatorFromEnv()
		if mode != "noauth" {
			t.Errorf("mode = %q, want %q", mode, "noauth")
		}
		if authr == nil {
			t.Fatal("AuthenticatorFromEnv() returned a nil Authenticator when unset")
		}
	})
}
