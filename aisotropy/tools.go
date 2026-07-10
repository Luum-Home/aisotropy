package aisotropy

import (
	"context"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// WithTypedTool registers a tool whose arguments are a Go struct T, the typed
// counterpart to WithTool + WithToolExecutor (ADR-0016 lever #1). It is the
// tool-input arm of Aisotropy's "struct as the single contract" thesis
// (ADR-0002): exactly as a Go struct already drives HTTP validation and the
// OpenAPI schema, here the SAME struct T drives three things at once —
//
//   - the JSON Schema the model is shown (derived from T's json/jsonschema tags,
//     advertised via native tool-calling so the model knows the exact shape);
//   - the decoding of the model's returned arguments into a T; and
//   - fail-closed reflection on inputs: if the model's arguments do not decode
//     into T, the runtime surfaces the error back to the model so it corrects
//     itself on the next turn (bounded by the agent's step budget), rather than
//     coercing bad arguments or failing the request.
//
// requiredScope gates the tool exactly like WithTool: the tool is offered to the
// model only when the request's granted scopes include it (empty = always
// available). handler runs the tool; it receives the request's already-scoped
// ctx (read identity via TenantFromContext/UserFromContext — a handler never
// constructs a memory scope, ADR-0016 invariant) and the decoded T.
//
// WithTypedTool accumulates like WithTool; call it once per typed tool. A tool
// registered here and a tool registered via WithTool must not share a name.
//
// Nested and non-string fields survive on the native tool-calling path (a real
// provider). On the structured-output fallback path (a client without native
// tool-calling, e.g. the keyless stub) arguments arrive flattened to strings, so
// only string-shaped fields are guaranteed there — the documented 0.x limitation.
func WithTypedTool[T any](name, description, requiredScope string, handler func(ctx context.Context, args T) (string, error)) Option {
	// Derive the argument schema once at registration; an error here means T is
	// not schema-representable, in which case the tool is advertised without a
	// declared shape (permissive) rather than panicking the builder.
	schema, _ := llm.SchemaFor[T]()

	return func(a *App) {
		a.toolset = append(a.toolset, tools.Tool{
			Name:          name,
			Description:   description,
			RequiredScope: requiredScope,
			Parameters:    schema,
		})
		if a.typedHandlers == nil {
			a.typedHandlers = map[string]api.TypedToolHandler{}
		}
		// The runtime owns validate+decode+dispatch (api.TypedHandlerFor): the
		// args are validated against this exact schema (enum/min/max/required),
		// then decoded into T; a failure reflects instead of coercing (SDD R4).
		a.typedHandlers[name] = api.TypedHandlerFor(name, schema, handler)
	}
}
