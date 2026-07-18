package aisotropy

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// TestParseDataEncryptionKey covers every branch of parseDataEncryptionKey: the
// unset (blank) default, a valid hex-encoded key, a valid base64-encoded key, an
// encoding that decodes under neither hex nor base64, and a decoded length that
// is not one of the valid AES key sizes (16/24/32 bytes).
func TestParseDataEncryptionKey(t *testing.T) {
	isolateEnv(t, dataEncryptionKeyEnv)

	t.Run("unset returns not-set, no error", func(t *testing.T) {
		t.Setenv(dataEncryptionKeyEnv, "")
		key, set, err := parseDataEncryptionKey()
		if err != nil || set || key != nil {
			t.Fatalf("parseDataEncryptionKey() = (%v,%v,%v), want (nil,false,nil)", key, set, err)
		}
	})

	t.Run("valid hex 32 bytes (AES-256)", func(t *testing.T) {
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = byte(i)
		}
		t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString(raw))
		key, set, err := parseDataEncryptionKey()
		if err != nil {
			t.Fatalf("parseDataEncryptionKey (hex): %v", err)
		}
		if !set || len(key) != 32 {
			t.Fatalf("parseDataEncryptionKey (hex) = (%d bytes,%v), want (32,true)", len(key), set)
		}
	})

	t.Run("valid base64 16 bytes (AES-128)", func(t *testing.T) {
		raw := make([]byte, 16)
		for i := range raw {
			raw[i] = byte(2 * i)
		}
		// Use a value that is NOT valid hex (base64 alphabet includes letters
		// hex-decode would reject, e.g. '+' or a length hex can't parse) so the
		// hex branch fails over to the base64 branch.
		enc := base64.StdEncoding.EncodeToString(raw)
		t.Setenv(dataEncryptionKeyEnv, enc)
		key, set, err := parseDataEncryptionKey()
		if err != nil {
			t.Fatalf("parseDataEncryptionKey (base64): %v", err)
		}
		if !set || len(key) != 16 {
			t.Fatalf("parseDataEncryptionKey (base64) = (%d bytes,%v), want (16,true)", len(key), set)
		}
	})

	t.Run("neither hex nor base64 errors without echoing the value", func(t *testing.T) {
		t.Setenv(dataEncryptionKeyEnv, "not-valid-hex-or-base64!!! ***")
		key, set, err := parseDataEncryptionKey()
		if err == nil || set || key != nil {
			t.Fatalf("parseDataEncryptionKey(malformed) = (%v,%v,%v), want a decode error", key, set, err)
		}
		if strings.Contains(err.Error(), "not-valid-hex-or-base64") {
			t.Error("parseDataEncryptionKey error must never echo the raw value")
		}
	})

	t.Run("wrong decoded length errors", func(t *testing.T) {
		// 10 raw bytes hex-encodes to 20 hex chars: valid hex, but not a legal
		// AES key length (16/24/32).
		raw := make([]byte, 10)
		t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString(raw))
		key, set, err := parseDataEncryptionKey()
		if err == nil || set || key != nil {
			t.Fatalf("parseDataEncryptionKey(wrong length) = (%v,%v,%v), want a length error", key, set, err)
		}
		if !strings.Contains(err.Error(), "16, 24, or 32 bytes") {
			t.Errorf("error = %q, want it to name the valid AES key sizes", err.Error())
		}
	})
}

// TestNewWorkingMemory_EncryptionBranches covers the encryption-related branches
// newWorkingMemory's own tests (builders_test.go) don't reach: the in-memory
// default warning when a key is set anyway, a malformed on-disk key failing
// boot, and a valid on-disk key enabling BadgerDB AES encryption.
func TestNewWorkingMemory_EncryptionBranches(t *testing.T) {
	isolateEnv(t, "AISOTROPY_DATA_DIR", dataEncryptionKeyEnv)

	t.Run("in-memory default with a key set only warns", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", "")
		raw := make([]byte, 32)
		t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString(raw))
		wm, err := newWorkingMemory(fastStub(), nil)
		if err != nil {
			t.Fatalf("newWorkingMemory (in-memory + key set): %v", err)
		}
		defer func() { _ = wm.Close() }()
	})

	t.Run("on-disk with a malformed key fails boot", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())
		t.Setenv(dataEncryptionKeyEnv, "not-valid-hex-or-base64!!!")
		if _, err := newWorkingMemory(fastStub(), nil); err == nil {
			t.Fatal("newWorkingMemory with a malformed encryption key must fail boot")
		}
	})

	t.Run("on-disk with a valid key opens an encrypted store", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = byte(i + 1)
		}
		t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString(raw))
		wm, err := newWorkingMemory(fastStub(), nil)
		if err != nil {
			t.Fatalf("newWorkingMemory (encrypted on-disk): %v", err)
		}
		defer func() { _ = wm.Close() }()
	})
}
