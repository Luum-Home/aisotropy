package llm

import (
	"bytes"
	"encoding/json"

	"github.com/invopop/jsonschema"
	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
)

// SchemaFor derives the JSON Schema for a tool's argument struct T and returns
// it as the raw bytes a ToolSpec.Parameters carries (ADR-0016 lever #1 /
// typed-tool-args SDD, task T2). It is the tool-input arm of Aisotropy's
// "struct as the single contract" thesis (ADR-0002): exactly as a Go struct
// already drives HTTP validation + the OpenAPI schema, here the same struct
// drives the schema the model is shown and, in a later slice, the validation of
// what the model returns before a typed handler runs.
//
// It uses the invopop/jsonschema reflector already in the module graph (MIT,
// the same family Huma builds on), configured for a self-contained, inline
// schema suitable for a provider "function" parameters object:
//
//   - no "$ref"/"$defs" indirection (Anonymous + DoNotReference), so providers
//     that reject external refs accept it verbatim;
//   - no top-level "$id"/"$schema" metadata that some provider tool APIs choke
//     on;
//   - additionalProperties:false so the model cannot smuggle unrequested keys.
//
// A non-struct T (or one that reflects to an empty schema) is valid: the caller
// treats empty Parameters as "no declared argument shape" (permissive object).
func SchemaFor[T any]() (json.RawMessage, error) {
	reflector := jsonschema.Reflector{
		Anonymous:                  true,
		DoNotReference:             true,
		AllowAdditionalProperties:  false,
		RequiredFromJSONSchemaTags: false,
	}
	var zero T
	schema := reflector.Reflect(zero)
	// Strip metadata keys that are noise (or outright rejected) inside a
	// provider function-parameters object; keep the structural schema.
	schema.Version = ""
	schema.ID = ""
	return json.Marshal(schema)
}

// SchemaValidator validates raw JSON tool arguments against a compiled JSON
// Schema. It exists so a typed tool is validated against EXACTLY the schema the
// model was shown (the SchemaFor output stored in tools.Tool.Parameters), not a
// re-derived approximation — so constraint keywords the schema advertises
// (enum / minimum / maximum / required / pattern) are actually enforced, and a
// violation becomes a fail-closed reflection signal rather than a coerced call
// (ADR-0016 lever #1 / typed-tool-args SDD R3, R4).
type SchemaValidator struct {
	sch *jsonschemav6.Schema
}

// CompileSchema compiles a JSON Schema (as produced by SchemaFor) into a
// reusable SchemaValidator. An empty schema returns (nil, nil): a nil validator
// is a no-op that treats every input as valid, so an untyped/no-schema tool is
// unaffected. A schema that fails to compile returns the compile error, letting
// the caller decide to degrade to decode-only validation.
func CompileSchema(schema json.RawMessage) (*SchemaValidator, error) {
	if len(bytes.TrimSpace(schema)) == 0 {
		//nolint:nilnil // documented no-op contract (see doc comment above and
		// TestCompileSchema_EmptyIsNoOp): an empty schema is not an error, it
		// means "no declared argument shape", so (nil, nil) is the correct
		// signal — callers treat a nil *SchemaValidator as a permissive no-op,
		// not an error to unwrap. A sentinel error here would break that
		// contract for existing callers (internal/api/typed_tools.go,
		// internal/api/mcp_tools.go) that rely on err == nil in this case.
		return nil, nil
	}
	doc, err := jsonschemav6.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	c := jsonschemav6.NewCompiler()
	const loc = "mem://aisotropy/tool-args.json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile(loc)
	if err != nil {
		return nil, err
	}
	return &SchemaValidator{sch: sch}, nil
}

// Validate checks raw JSON arguments against the compiled schema. A nil
// validator (or nil schema) is a no-op. It returns a non-nil error describing
// the first constraint violation(s) when the arguments do not satisfy the
// schema, which the caller surfaces as a ToolArgsError for reflection.
func (v *SchemaValidator) Validate(raw json.RawMessage) error {
	if v == nil || v.sch == nil {
		return nil
	}
	inst, err := jsonschemav6.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return v.sch.Validate(inst)
}
