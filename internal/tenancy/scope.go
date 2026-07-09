package tenancy

import (
	"context"
	"errors"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// tenantCtxKey / userCtxKey are unexported so only WithTenant/WithUser can
// set them and only TenantFromContext/UserFromContext/FromContext can read
// them — mirroring the package-local ctx-carrier pattern
// internal/llm/context.go already established (which itself mirrors
// internal/api/chat.go's own tenantCtxKey/userCtxKey). internal/tenancy
// cannot reuse internal/api's unexported keys directly (they are private to
// that package and importing internal/api from here would invert the
// dependency direction), so it owns its own carrier, set explicitly by
// whoever wires identity into ctx for the facts tier — the same way
// chat_stream.go explicitly calls llm.WithTenant/llm.WithUser alongside its
// own withIdentity today.
type tenantCtxKey struct{}
type userCtxKey struct{}

// WithTenant returns a copy of ctx carrying tenant for this package's own
// FromContext. A no-op (returns ctx unchanged) when tenant is empty, so
// FromContext still reports a clear "no tenant in context" error instead of
// silently accepting an empty string.
func WithTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantCtxKey{}, tenant)
}

// WithUser returns a copy of ctx carrying user, mirroring WithTenant. User
// identity is optional: not every facts-tier table is user-scoped (see
// Table.ScopedByUser), so a ctx carrying only a tenant is valid.
func WithUser(ctx context.Context, user string) context.Context {
	if user == "" {
		return ctx
	}
	return context.WithValue(ctx, userCtxKey{}, user)
}

// TenantFromContext retrieves the tenant set by WithTenant, if any.
func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantCtxKey{}).(string)
	return v, ok
}

// UserFromContext retrieves the user set by WithUser, if any.
func UserFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(userCtxKey{}).(string)
	return v, ok
}

// errNoTenantInContext / errZeroValueScope are the two ways a caller can end
// up trying to use an unscoped identity: either FromContext found nothing to
// read (returned immediately, never producing a Scope at all), or a Scope's
// zero value was used directly instead of going through FromContext (Go
// cannot prevent `var s tenancy.Scope` from compiling — unexported fields
// still zero-initialize — so Scope.Query taints the resulting Builder with
// this error instead, and every statement's Render/Exec/Query surfaces it).
var (
	errNoTenantInContext = errors.New("tenancy: context carries no tenant identity (call WithTenant before FromContext)")
	errZeroValueScope    = errors.New("tenancy: zero-value Scope has no tenant identity; obtain a Scope via tenancy.FromContext(ctx), never a bare tenancy.Scope{}")
)

// Scope carries the tenant (and, optionally, user) identity a statement is
// built against. Its fields are unexported: the only way to obtain a Scope
// whose tenant is guaranteed non-empty and validated is FromContext.
type Scope struct {
	tenant  string
	user    string
	hasUser bool
}

// FromContext is the ONLY constructor for a valid Scope. It reads the
// identity WithTenant/WithUser injected into ctx and re-validates each
// component with memory.ValidateIdentityComponent — the same non-empty /
// no-reserved-character rule internal/memory/key.go enforces for the
// working-memory tier, reused here rather than duplicated, so the two tiers
// can never silently drift on what counts as a valid tenant/user string.
//
// Returns an error — never a partially-valid Scope — if ctx carries no
// tenant, or if the tenant/user string fails validation.
func FromContext(ctx context.Context) (Scope, error) {
	tenant, ok := TenantFromContext(ctx)
	if !ok {
		return Scope{}, errNoTenantInContext
	}
	if err := memory.ValidateIdentityComponent("tenant", tenant); err != nil {
		return Scope{}, err
	}

	scope := Scope{tenant: tenant}
	if user, ok := UserFromContext(ctx); ok {
		if err := memory.ValidateIdentityComponent("user", user); err != nil {
			return Scope{}, err
		}
		scope.user = user
		scope.hasUser = true
	}
	return scope, nil
}

// Tenant returns the scope's tenant identity.
func (s Scope) Tenant() string { return s.tenant }

// User returns the scope's user identity and whether one was set. Not every
// Scope carries a user — only tables registered via Table.ScopedByUser
// require one at render time.
func (s Scope) User() (string, bool) { return s.user, s.hasUser }

// valid reports whether s was actually produced by FromContext (or is at
// least non-zero-valued) rather than being a bare tenancy.Scope{} literal.
func (s Scope) valid() error {
	if s.tenant == "" {
		return errZeroValueScope
	}
	return nil
}

// Query is the ONLY entry point into the statement builder. There is no
// package-level function that builds a Builder from a Table alone — every
// path to Select/Insert/Update/Delete goes through a Scope obtained from
// FromContext first. If s is a forged/zero-value Scope, the returned
// Builder is tainted with an error that every statement's Render/Exec/Query
// method surfaces, rather than silently emitting an unscoped statement.
func (s Scope) Query(t Table) Builder {
	return Builder{scope: s, table: t, err: s.valid()}
}
