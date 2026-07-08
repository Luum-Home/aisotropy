// Tenant/user identity propagation for internal/llm's own consumers.
//
// internal/llm must not import internal/api (see otel.go's package doc and
// docs/architecture/architecture layering), so it cannot reuse
// internal/api's tenantCtxKey/userCtxKey (chat.go) to read the identity Huma
// injects. This file gives internal/llm its own, package-local ctx
// propagation: internal/api sets it explicitly (see chat_stream.go, right
// after withIdentity) alongside its own injection, so the WithOTel decorator
// (otel.go) can read tenant/user back out of ctx without a cross-package
// import — exactly the "read those values back out of ctx here" follow-up
// ADR-0010 flags under "Observability decision: two LLM call paths".
package llm

import "context"

// tenantCtxKey / userCtxKey are unexported so only WithTenant/WithUser can
// set them and only TenantFromContext/UserFromContext can read them —
// mirroring the pattern internal/api/chat.go already uses for its own,
// separate tenantCtxKey/userCtxKey.
type tenantCtxKey struct{}
type userCtxKey struct{}

// WithTenant returns a copy of ctx carrying tenant for internal/llm's own
// consumers (currently: WithOTel's span attributes). A no-op (returns ctx
// unchanged) when tenant is empty, so callers can pass it through
// unconditionally without an extra branch.
func WithTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantCtxKey{}, tenant)
}

// WithUser returns a copy of ctx carrying user, mirroring WithTenant.
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
