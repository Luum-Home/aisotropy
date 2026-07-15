package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler is the wrapped operator handler. It records whether the gate let a
// request through and answers 200, so tests can assert reach vs. block.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

// TestOpsAuthGateDisabledReturns503 proves the default-deny posture: an empty
// token CLOSES the endpoint, and every request — with or without a header —
// gets 503 with the operator-guidance body, never reaching the handler.
func TestOpsAuthGateDisabledReturns503(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"bearer header present", "Bearer whatever"},
		{"malformed header", "garbage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			gate := OpsAuthGate("")(okHandler(&reached))

			req := httptest.NewRequest(http.MethodGet, "/costs", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if reached {
				t.Fatal("handler was reached with the gate closed")
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
			}
			if !strings.Contains(body.Error, "AISOTROPY_OPS_TOKEN") {
				t.Fatalf("error body %q does not mention the env var to set", body.Error)
			}
		})
	}
}

// TestOpsAuthGateMissingHeaderReturns401 proves a configured gate rejects a
// request that carries no Authorization header with a generic 401.
func TestOpsAuthGateMissingHeaderReturns401(t *testing.T) {
	reached := false
	gate := OpsAuthGate("s3cr3t")(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/costs", nil)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if reached {
		t.Fatal("handler was reached without a credential")
	}
	assertGenericUnauthorized(t, rec)
}

// TestOpsAuthGateWrongTokenReturns401 proves a mismatched token is rejected,
// and — via a deliberately wrong-LENGTH token — that the ConstantTimeCompare
// path handles unequal lengths without panicking (length-safe).
func TestOpsAuthGateWrongTokenReturns401(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"same length wrong value", "s3cr3X"},
		{"shorter token", "s3c"},
		{"longer token", "s3cr3t-plus-extra-bytes"},
		{"empty bearer value", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			gate := OpsAuthGate("s3cr3t")(okHandler(&reached))

			req := httptest.NewRequest(http.MethodGet, "/costs", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if reached {
				t.Fatal("handler was reached with a wrong token")
			}
			assertGenericUnauthorized(t, rec)
		})
	}
}

// TestOpsAuthGateCorrectBearerReachesHandler proves the happy path: the exact
// token in a well-formed Bearer header reaches the wrapped handler with 200.
func TestOpsAuthGateCorrectBearerReachesHandler(t *testing.T) {
	reached := false
	gate := OpsAuthGate("s3cr3t")(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/costs", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t")
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !reached {
		t.Fatal("handler was NOT reached with the correct token")
	}
}

// TestOpsAuthGateBearerSchemeCaseInsensitive proves the RFC 7235 scheme match
// is case-insensitive, so "bearer <token>" is accepted.
func TestOpsAuthGateBearerSchemeCaseInsensitive(t *testing.T) {
	reached := false
	gate := OpsAuthGate("s3cr3t")(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/costs", nil)
	req.Header.Set("Authorization", "bearer s3cr3t")
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !reached {
		t.Fatal("handler was NOT reached with a lowercase bearer scheme")
	}
}

// TestOpsAuthGateNonBearerSchemeReturns401 proves a non-Bearer credential
// (e.g. Basic) is rejected without reaching the handler.
func TestOpsAuthGateNonBearerSchemeReturns401(t *testing.T) {
	reached := false
	gate := OpsAuthGate("s3cr3t")(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/costs", nil)
	req.Header.Set("Authorization", "Basic s3cr3t")
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if reached {
		t.Fatal("handler was reached with a non-Bearer scheme")
	}
}

// TestOpsTokenFromEnv proves OpsTokenFromEnv reads AISOTROPY_OPS_TOKEN and is
// the single source of truth for the env var name.
func TestOpsTokenFromEnv(t *testing.T) {
	if OpsEnvVar != "AISOTROPY_OPS_TOKEN" {
		t.Fatalf("OpsEnvVar = %q, want AISOTROPY_OPS_TOKEN", OpsEnvVar)
	}
	t.Setenv(OpsEnvVar, "from-env-token")
	if got := OpsTokenFromEnv(); got != "from-env-token" {
		t.Fatalf("OpsTokenFromEnv() = %q, want from-env-token", got)
	}
}

// assertGenericUnauthorized checks the 401 body is the generic JSON error that
// reveals nothing about WHY the request was rejected.
func assertGenericUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Error != "unauthorized" {
		t.Fatalf("error body = %q, want generic \"unauthorized\"", body.Error)
	}
}
