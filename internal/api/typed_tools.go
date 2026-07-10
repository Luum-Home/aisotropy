package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// ToolArgsError marks a typed tool's arguments as invalid (they did not decode
// or validate against the tool's argument struct). It is the signal the agent
// loop uses to REFLECT on inputs: rather than coercing bad arguments or failing
// the request, the loop surfaces this error back to the model as the tool's
// result so the model can correct itself on the next turn, bounded by the run's
// step budget (ADR-0016 lever #1 / typed-tool-args SDD R4). A tool's own runtime
// failure is a plain error, NOT a ToolArgsError, so the two are distinguishable.
type ToolArgsError struct {
	Tool string
	Err  error
}

func (e *ToolArgsError) Error() string {
	return fmt.Sprintf("invalid arguments for tool %q: %v", e.Tool, e.Err)
}

func (e *ToolArgsError) Unwrap() error { return e.Err }

// TypedToolHandler runs a typed tool from the model's raw JSON arguments. The
// façade's WithTypedTool[T] builds one per tool (via TypedHandlerFor): it
// validates+decodes rawArgs into the tool's argument struct T (returning a
// *ToolArgsError on failure) and invokes the consumer's typed handler. The
// handler is type-erased here so the runtime dispatches every typed tool
// uniformly without knowing T.
type TypedToolHandler func(ctx context.Context, rawArgs json.RawMessage) (string, error)

// TypedHandlerFor builds the type-erased TypedToolHandler for a typed tool: it
// (1) validates the model's raw arguments against the tool's JSON Schema (the
// exact SchemaFor[T] output the model was shown, so enum/min/max/required are
// enforced), then (2) decodes them into T, and (3) invokes handler. A failure at
// step 1 or 2 returns a *ToolArgsError — the fail-closed reflection signal — so
// the agent loop asks the model to correct itself rather than running handler
// with coerced or out-of-constraint data (ADR-0016 lever #1 / SDD R3, R4).
//
// schema is the SchemaFor[T] output. If it does not compile, the tool degrades
// to decode-only validation (structure/type still enforced by json.Unmarshal);
// the tool is never disabled by a schema-compilation problem.
func TypedHandlerFor[T any](name string, schema json.RawMessage, handler func(ctx context.Context, args T) (string, error)) TypedToolHandler {
	validator, _ := llm.CompileSchema(schema) // nil on empty/uncompilable -> decode-only
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		payload := rawArgs
		if len(bytes.TrimSpace(payload)) == 0 {
			payload = json.RawMessage("{}") // validate "no args" so required fields are caught
		}
		if err := validator.Validate(payload); err != nil {
			return "", &ToolArgsError{Tool: name, Err: err}
		}
		var args T
		if err := json.Unmarshal(payload, &args); err != nil {
			return "", &ToolArgsError{Tool: name, Err: err}
		}
		return handler(ctx, args)
	}
}

// TypedToolExecutor dispatches typed tools by name from raw JSON args. It is
// built by the public builder from WithTypedTool registrations and consulted by
// both agent paths BEFORE the map-based ToolExecutor: a tool it owns is run with
// the model's exact JSON (preserving nested/typed fields), a tool it does not
// own is left to the map executor. It never constructs a memory Scope — a typed
// handler receives the request's already-scoped ctx and reads identity via
// TenantFromContext (ADR-0016 invariant).
type TypedToolExecutor struct {
	handlers map[string]TypedToolHandler
}

// NewTypedToolExecutor builds a TypedToolExecutor from a name→handler map. A nil
// or empty map yields an executor that owns no tools (Has always false), so the
// runtime behaves exactly as before typed tools existed.
func NewTypedToolExecutor(handlers map[string]TypedToolHandler) *TypedToolExecutor {
	cp := make(map[string]TypedToolHandler, len(handlers))
	for k, v := range handlers {
		cp[k] = v
	}
	return &TypedToolExecutor{handlers: cp}
}

// Has reports whether name is a registered typed tool.
func (t *TypedToolExecutor) Has(name string) bool {
	if t == nil {
		return false
	}
	_, ok := t.handlers[name]
	return ok
}

// Run dispatches the named typed tool with the model's raw JSON args. It returns
// a *ToolArgsError when the args are invalid (the caller reflects), the handler's
// result on success, or the handler's own error on a runtime failure.
func (t *TypedToolExecutor) Run(ctx context.Context, name string, rawArgs json.RawMessage) (string, error) {
	h, ok := t.handlers[name]
	if !ok {
		return "", fmt.Errorf("typed tool %q not registered", name)
	}
	return h(ctx, rawArgs)
}

// combinedMapExecutor adapts the typed executor onto the map[string]string
// ToolExecutor seam for the structured-output fallback path (clients without
// native tool-calling, e.g. the keyless stub). A typed tool's flattened string
// args are re-marshalled to JSON and dispatched to the typed handler
// (best-effort — nested/typed fidelity is only guaranteed on the native path,
// the documented 0.x limitation, SDD R7); an untyped tool goes straight to the
// wrapped fallback executor (preserving its friction wrapping). A *ToolArgsError
// is surfaced as the tool RESULT so the fallback loop reflects rather than fails.
type combinedMapExecutor struct {
	typed    *TypedToolExecutor
	fallback agentloop.ToolExecutor
}

func (c combinedMapExecutor) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	if c.typed.Has(name) {
		raw, err := json.Marshal(args)
		if err != nil {
			raw = []byte("{}")
		}
		res, rerr := c.typed.Run(ctx, name, raw)
		var ae *ToolArgsError
		if errors.As(rerr, &ae) {
			return "invalid arguments: " + ae.Err.Error() + " — please correct and retry.", nil
		}
		return res, rerr
	}
	return c.fallback.Execute(ctx, name, args)
}

// decodeStringArgs flattens the model's raw JSON tool arguments into the
// string-keyed map the map-based ToolExecutor consumes (untyped tools on the
// native path). It mirrors internal/llm's decodeToolArgs: string scalars pass
// through, other values are stringified, and empty/invalid JSON yields an empty
// map so an untyped tool still runs.
func decodeStringArgs(argsJSON string) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return out
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
			continue
		}
		b, _ := json.Marshal(v)
		out[k] = string(b)
	}
	return out
}
