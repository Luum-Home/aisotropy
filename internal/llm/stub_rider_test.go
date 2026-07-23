package llm

import (
	"strings"
	"testing"
)

// TestStubStructuredReply_SchemaInSystemRegion locks the ADR-0068 (A1)
// consequential fix: after the schema rider was relocated from the prompt body
// into the SYSTEM region (gollem.go buildGollemPrompt), the stub must still emit
// a schema-satisfying JSON object when the marker is carried by `system`. Before
// the fix the stub scanned only `prompt`, silently dropped to a free-text echo,
// and the chat reflection loop burned its full retry budget (~18s per test).
func TestStubStructuredReply_SchemaInSystemRegion(t *testing.T) {
	system := "You are the agent.\n" + schemaInstructionMarker +
		" this JSON Schema:\n" + `{"type":"object","properties":{"reply":{"type":"string"}}}`
	prompt := "What model are you?"

	got, ok := stubStructuredReply(system, prompt)
	if !ok {
		t.Fatalf("stub must recognize a schema rider in the SYSTEM region")
	}
	if !strings.Contains(got, `"reply"`) {
		t.Fatalf("stub structured reply must satisfy the schema's properties; got %q", got)
	}
	// The user's actual prompt is folded into the string field, not the system.
	if !strings.Contains(got, "What model are you?") {
		t.Fatalf("reply must be derived from the user prompt; got %q", got)
	}
}
