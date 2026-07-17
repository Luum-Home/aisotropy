package aisotropy

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// dataEncryptionKeyEnv is the environment variable that opts working memory into
// BadgerDB at-rest encryption (Gap 5): when it is set AND AISOTROPY_DATA_DIR
// points at an on-disk store, the working-memory BadgerDB is opened with AES
// encryption. Its VALUE is key material and is NEVER logged (see
// parseDataEncryptionKey, which keeps the raw value out of every error).
const dataEncryptionKeyEnv = "AISOTROPY_DATA_ENCRYPTION_KEY"

// dataEncryptionKeySet reports whether AISOTROPY_DATA_ENCRYPTION_KEY carries a
// non-empty value, without decoding or exposing it. Used to warn when a key is
// supplied but the store is in-memory (so encryption cannot apply).
func dataEncryptionKeySet() bool {
	return strings.TrimSpace(os.Getenv(dataEncryptionKeyEnv)) != ""
}

// parseDataEncryptionKey decodes AISOTROPY_DATA_ENCRYPTION_KEY into AES key
// material for BadgerDB at-rest encryption. The value may be HEX or standard
// base64 (hex is tried first; a value that decodes under neither is rejected),
// and the decoded length MUST be 16, 24, or 32 bytes (AES-128, AES-192, or
// AES-256). It returns:
//
//	(key, true, nil)   when the env var is set and valid,
//	(nil, false, nil)  when it is unset/blank (encryption stays off), and
//	(nil, false, err)  when it is set but malformed or the wrong length.
//
// The raw env value and the decoded key are NEVER placed in the returned error
// or logged anywhere — only the byte length is ever surfaced, so key material
// cannot leak through diagnostics.
func parseDataEncryptionKey() (key []byte, set bool, err error) {
	raw := strings.TrimSpace(os.Getenv(dataEncryptionKeyEnv))
	if raw == "" {
		return nil, false, nil
	}
	var decoded []byte
	if b, herr := hex.DecodeString(raw); herr == nil {
		decoded = b
	} else if b, berr := base64.StdEncoding.DecodeString(raw); berr == nil {
		decoded = b
	} else {
		// Deliberately does NOT echo the value: it is secret key material.
		return nil, false, fmt.Errorf("%s is not valid hex or base64", dataEncryptionKeyEnv)
	}
	switch len(decoded) {
	case 16, 24, 32:
		return decoded, true, nil
	default:
		return nil, false, fmt.Errorf("%s must decode to 16, 24, or 32 bytes (AES-128/192/256), got %d", dataEncryptionKeyEnv, len(decoded))
	}
}
