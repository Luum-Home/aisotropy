package llm

import (
	"encoding/json"
	"testing"
)

// weatherArgs is a representative typed-tool argument struct: a required string,
// an enum, and a number — the shapes map[string]string cannot express.
type weatherArgs struct {
	City  string  `json:"city" jsonschema:"description=the city to look up"`
	Units string  `json:"units" jsonschema:"enum=c,enum=f"`
	Days  int     `json:"days" jsonschema:"minimum=1,maximum=14"`
	Lat   float64 `json:"lat,omitempty"`
}

// TestSchemaFor_DerivesInlineObjectSchema verifies SchemaFor produces a
// self-contained (no $ref/$defs), metadata-stripped, additionalProperties:false
// object schema whose properties mirror the struct's json tags — the tool-input
// arm of struct-as-contract (ADR-0016 / typed-tool-args SDD R2, R8).
func TestSchemaFor_DerivesInlineObjectSchema(t *testing.T) {
	raw, err := SchemaFor[weatherArgs]()
	if err != nil {
		t.Fatalf("SchemaFor error: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v\nraw: %s", err, raw)
	}

	if got := schema["type"]; got != jsonSchemaTypeObject {
		t.Errorf("schema type = %v, want object", got)
	}
	// No external-reference machinery: providers that reject $ref must accept it.
	if _, ok := schema["$defs"]; ok {
		t.Errorf("schema carries $defs; want inlined (DoNotReference)")
	}
	if _, ok := schema["$ref"]; ok {
		t.Errorf("schema carries $ref; want inlined")
	}
	// Metadata that some provider function APIs reject must be stripped.
	if _, ok := schema["$schema"]; ok {
		t.Errorf("schema carries $schema metadata; want stripped")
	}
	if _, ok := schema["$id"]; ok {
		t.Errorf("schema carries $id metadata; want stripped")
	}
	if add, ok := schema["additionalProperties"]; !ok || add != false {
		t.Errorf("additionalProperties = %v (present=%v), want false", add, ok)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties object: %s", raw)
	}
	for _, want := range []string{"city", "units", "days"} {
		if _, ok := props[want]; !ok {
			t.Errorf("properties missing %q; got keys %v", want, keysOf(props))
		}
	}
}

// TestSchemaFor_EmptyStructIsValid verifies a no-arg tool (empty struct) still
// yields valid, marshalable schema bytes — the caller treats it as "no declared
// argument shape".
func TestSchemaFor_EmptyStructIsValid(t *testing.T) {
	type noArgs struct{}
	raw, err := SchemaFor[noArgs]()
	if err != nil {
		t.Fatalf("SchemaFor[noArgs] error: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("empty-struct schema not valid JSON: %v", err)
	}
	if schema["type"] != jsonSchemaTypeObject {
		t.Errorf("empty-struct schema type = %v, want object", schema["type"])
	}
}

// TestCompileSchema_EnforcesConstraints verifies the validator enforces the
// generated schema's constraint keywords (enum / minimum / maximum / required),
// not just structural decode — the mechanism behind fail-closed reflection on
// tool inputs (SDD R3).
func TestCompileSchema_EnforcesConstraints(t *testing.T) {
	schema, err := SchemaFor[weatherArgs]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	v, err := CompileSchema(schema)
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	if v == nil {
		t.Fatal("CompileSchema returned a nil validator for a non-empty schema")
	}

	cases := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"valid", `{"city":"Paris","units":"c","days":3}`, false},
		{"days above maximum", `{"city":"Paris","units":"c","days":99}`, true},
		{"days below minimum", `{"city":"Paris","units":"c","days":0}`, true},
		{"units outside enum", `{"city":"Paris","units":"kelvin","days":3}`, true},
		{"wrong type for days", `{"city":"Paris","units":"c","days":"three"}`, true},
		{"missing required city", `{"units":"c","days":3}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(json.RawMessage(tc.args))
			if tc.wantErr && err == nil {
				t.Fatalf("Validate(%s) = nil, want a constraint error", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate(%s) = %v, want nil", tc.args, err)
			}
		})
	}
}

// TestCompileSchema_ErrorBranches drives each of CompileSchema's three
// distinct error returns — malformed JSON (UnmarshalJSON), a document that
// fails meta-schema validation (AddResource), and an unresolvable $ref
// (Compile) — so a schema degrade-to-decode-only caller (internal/api's
// typed_tools.go/mcp_tools.go) is proven to actually see an error on each of
// these malformed-schema shapes, not just the happy path.
func TestCompileSchema_ErrorBranches(t *testing.T) {
	t.Run("malformed JSON fails to unmarshal", func(t *testing.T) {
		_, err := CompileSchema(json.RawMessage(`{not valid json`))
		if err == nil {
			t.Fatal("expected an UnmarshalJSON error for malformed JSON")
		}
	})
	t.Run("document fails meta-schema validation at AddResource", func(t *testing.T) {
		// "type" must be a string (or array of strings); a number fails the
		// JSON Schema meta-schema itself, which AddResource validates against
		// before Compile ever runs.
		_, err := CompileSchema(json.RawMessage(`{"type": 123}`))
		if err == nil {
			t.Fatal("expected an AddResource meta-schema validation error")
		}
	})
	t.Run("unresolvable $ref fails at Compile", func(t *testing.T) {
		_, err := CompileSchema(json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/nonexistent"}}}`))
		if err == nil {
			t.Fatal("expected a Compile error for an unresolvable $ref")
		}
	})
}

// TestSchemaValidator_ValidateMalformedJSON proves Validate itself returns an
// error (rather than panicking or silently accepting) when handed raw
// arguments that are not valid JSON at all — distinct from a well-formed but
// schema-violating payload (covered by TestCompileSchema_EnforcesConstraints).
func TestSchemaValidator_ValidateMalformedJSON(t *testing.T) {
	schema, err := SchemaFor[weatherArgs]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	v, err := CompileSchema(schema)
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	if err := v.Validate(json.RawMessage(`{not valid json`)); err == nil {
		t.Fatal("expected Validate to error on malformed JSON arguments")
	}
}

// TestCompileSchema_EmptyIsNoOp verifies an empty schema yields a nil validator
// whose Validate is a no-op (an untyped tool is unaffected).
func TestCompileSchema_EmptyIsNoOp(t *testing.T) {
	v, err := CompileSchema(nil)
	if err != nil {
		t.Fatalf("CompileSchema(nil): %v", err)
	}
	if v != nil {
		t.Fatalf("CompileSchema(nil) = %v, want nil validator", v)
	}
	if err := v.Validate(json.RawMessage(`{"anything":true}`)); err != nil {
		t.Fatalf("nil validator Validate = %v, want no-op nil", err)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
