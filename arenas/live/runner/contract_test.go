package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestWireContract_EveryDeclaredFieldIsPopulated — a contract field nobody fills
// is a lie in the contract, and the adapter that TRUSTS it is the one that looks
// broken.
//
// THE BUG THIS PINS. `adapterInput` declared a `Tools` field from the day the
// wire contract was written, and no code path ever set it. Eight adapters coped
// by hardcoding their own `calculate` tool; spring-ai read the field it had been
// promised, found it empty, and errored on every tools cell of every board. Seven
// errors per board, attributed to the framework for as long as the board has
// existed — and my first diagnosis invented a rationale for it ("the runner
// deliberately omits tools so each adapter registers its own, a fairness
// principle"). That rationale was fiction. types.go:33 declares the field;
// nothing filled it.
//
// The lesson generalises past this one field: when a struct is a PUBLIC CONTRACT
// that other codebases parse, an unpopulated field is not dead code, it is a
// promise nobody keeps. This test walks every work item the builders produce and
// asserts that each contract field is populated by at least one of them — or is
// listed below with a written reason why the runner legitimately never sends it.
func TestWireContract_EveryDeclaredFieldIsPopulated(t *testing.T) {
	// Fields the RUNNER legitimately never populates, each with the reason.
	// Adding a name here is a deliberate act: it says "no adapter should expect
	// this from us". Anything not listed must be sent by some builder.
	neverSentByRunner := map[string]string{}

	// Opt-in paths count as populated, but ONLY when the test actually exercises
	// them — otherwise "it is sent when a flag is on" becomes an excuse that
	// hides a genuinely dead field. ARENA_STRICT_STRUCTURED=1 is what makes
	// buildQAStyle attach Schema/SchemaName, so it is switched on here rather
	// than waved through in the exemption list below.
	t.Setenv("ARENA_STRICT_STRUCTURED", "1")

	tasks, rag := contractFixtureWork(t)

	seen := map[string]bool{}
	modes := []string{
		"qa", "rag", "tools", "memory", "structured", "stream", "safeguard",
		"memory_cross_session", "memory_semantic_recall", "memory_consolidation",
		"isolation_forgetful",
	}
	for _, mode := range modes {
		for _, item := range buildWork("contract-probe", mode, tasks, rag, 0) {
			for _, inv := range item.Invocations {
				v := reflect.ValueOf(inv)
				typ := v.Type()
				for i := 0; i < typ.NumField(); i++ {
					if !v.Field(i).IsZero() {
						seen[typ.Field(i).Name] = true
					}
				}
			}
		}
	}

	var input adapterInput
	typ := reflect.TypeOf(input)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if seen[name] {
			continue
		}
		if reason, ok := neverSentByRunner[name]; ok {
			t.Logf("%s: never sent by the runner — %s", name, reason)
			continue
		}
		t.Errorf("adapterInput.%s is DECLARED in the wire contract (json tag %q) but no "+
			"builder in tasks.go ever populates it.\n"+
			"  An adapter reading this field gets nothing, forever, and looks broken for "+
			"trusting the contract — which is exactly what happened to spring-ai's tools mode.\n"+
			"  Either populate it in the relevant builder, or add it to neverSentByRunner "+
			"WITH a reason so the omission is a decision instead of an oversight.",
			name, typ.Field(i).Tag.Get("json"))
	}
}

// TestWireContract_ToolsCarriesAUsableDefinition — populating the field is not
// enough; what travels must be something an adapter can actually register with
// its own framework's tool primitive. A syntactically present but semantically
// empty tool definition would satisfy the test above while leaving spring-ai
// exactly as broken.
func TestWireContract_ToolsCarriesAUsableDefinition(t *testing.T) {
	tasks, rag := contractFixtureWork(t)
	items := buildWork("contract-probe", "tools", tasks, rag, 0)
	if len(items) == 0 {
		t.Fatal("tools mode produced no work items")
	}
	tools := items[0].Invocations[0].Tools
	if len(tools) == 0 {
		t.Fatal("tools invocation carries no tool definitions")
	}
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("a tool definition has no name — nothing can register it")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description; a model cannot decide when to call it", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
			t.Errorf("tool %q parameters are not valid JSON Schema: %v", tool.Name, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q parameters must be a JSON Schema object, got %v", tool.Name, schema["type"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("tool %q parameters declare no properties", tool.Name)
		}
	}
}

// contractFixtureWork loads the REAL suite and rag questions rather than a
// fixture. A fixture would keep these tests passing while the shipped suite
// drifted, which is the failure mode the arena has hit repeatedly.
func contractFixtureWork(t *testing.T) ([]task, []ragQuestion) {
	t.Helper()
	tasks, err := loadSuite("../tasks/suite.jsonl")
	if err != nil {
		t.Fatalf("load suite: %v", err)
	}
	rag, err := loadRagQuestions("../_shared/rag_questions.jsonl")
	if err != nil {
		t.Fatalf("load rag questions: %v", err)
	}
	return tasks, rag
}
