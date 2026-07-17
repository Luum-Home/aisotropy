package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// GetSubjectLanguage returns the language code previously remembered for a single
// (tenant,user) subject, and whether one is present. It is the read half of the
// per-subject language persistence added for the R1 follow-on (ADR-0053): the
// chat handlers call it to fall back to a subject's known language when the
// current request's detection is Undetermined.
//
// Isolation is enforced by construction: the key is built by subjectLangKey,
// which validates both identity components with the same reserved-character rule
// every other working-memory operation uses, so a blank or malformed tenant/user
// is rejected here rather than producing a wildcard read that could span
// subjects. A key that has never been written returns ("", false, nil).
func (w *WorkingMemory) GetSubjectLanguage(ctx context.Context, tenant, user string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	key, err := subjectLangKey(tenant, user)
	if err != nil {
		return "", false, err
	}

	var (
		code  string
		found bool
	)
	err = w.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			code = string(val)
			return nil
		})
	})
	if err != nil {
		return "", false, fmt.Errorf("memory: get subject language: %w", err)
	}
	return code, found, nil
}

// PutSubjectLanguage stores code as the remembered language for a single
// (tenant,user) subject, overwriting any previous value. It is the write half of
// per-subject language persistence (ADR-0053) and is called only when the current
// request produced a confident, changed detection, so the store is written at
// most once per genuine language change per subject — not on every turn.
//
// The value is advisory metadata, never an authorization input. Isolation is
// enforced exactly as for GetSubjectLanguage: the key is subject-scoped and both
// identity components are validated up front, so a blank or malformed identity is
// rejected rather than widened into a wildcard write. An empty or
// reserved-character-bearing code is rejected — the stored value is a plain
// ISO 639-1 tag (e.g. "es"), never free-form text.
func (w *WorkingMemory) PutSubjectLanguage(ctx context.Context, tenant, user, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if code == "" || strings.ContainsAny(code, reservedKeyChars) {
		return fmt.Errorf("memory: subject language code %q is empty or contains a reserved character", code)
	}
	key, err := subjectLangKey(tenant, user)
	if err != nil {
		return err
	}
	if err := w.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, []byte(code))
	}); err != nil {
		return fmt.Errorf("memory: put subject language: %w", err)
	}
	return nil
}
