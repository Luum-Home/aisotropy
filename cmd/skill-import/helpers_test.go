package main

import "testing"

// TestGoType covers every branch of goType, including the fallback default for
// unknown/unspecified declared arg types.
func TestGoType(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"bool", "bool"},
		{"int", "int"},
		{"number", "float64"},
		{"string", "string"},
		{"", "string"},
		{"something-else", "string"},
	}
	for _, tc := range cases {
		if got := goType(tc.in); got != tc.want {
			t.Errorf("goType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestJsonschemaTag covers both the required and not-required branches.
func TestJsonschemaTag(t *testing.T) {
	req := jsonschemaTag(argSpec{Description: "a  field", Required: true})
	if req != "description=a field,required" {
		t.Errorf("required tag = %q", req)
	}
	notReq := jsonschemaTag(argSpec{Description: "b field", Required: false})
	if notReq != "description=b field" {
		t.Errorf("non-required tag = %q", notReq)
	}
}

// TestExportedCamel covers multi-part, single-part, and empty-string inputs.
func TestExportedCamel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fill_pdf_form", "FillPdfForm"},
		{"run-thing", "RunThing"},
		{"solo", "Solo"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := exportedCamel(tc.in); got != tc.want {
			t.Errorf("exportedCamel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStructNameAndHandlerName(t *testing.T) {
	if got := structName("fill_pdf_form"); got != "FillPdfFormArgs" {
		t.Errorf("structName = %q", got)
	}
	if got := handlerName("fill_pdf_form"); got != "fillPdfFormHandler" {
		t.Errorf("handlerName = %q", got)
	}
}

func TestGoFieldName(t *testing.T) {
	if got := goFieldName("source_path"); got != "SourcePath" {
		t.Errorf("goFieldName = %q", got)
	}
}

func TestLowerFirst(t *testing.T) {
	if got := lowerFirst(""); got != "" {
		t.Errorf("lowerFirst(\"\") = %q, want empty", got)
	}
	if got := lowerFirst("FillPdfForm"); got != "fillPdfForm" {
		t.Errorf("lowerFirst = %q", got)
	}
}

func TestOrDashOrDefault(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(\"\") = %q", got)
	}
	if got := orDash("MIT"); got != "MIT" {
		t.Errorf("orDash(MIT) = %q", got)
	}
	if got := orDefault("", "python3"); got != "python3" {
		t.Errorf("orDefault fallback = %q", got)
	}
	if got := orDefault("bash", "python3"); got != "bash" {
		t.Errorf("orDefault given = %q", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n  b   c\t d"); got != "a b c d" {
		t.Errorf("oneLine = %q", got)
	}
}

func TestToolNameFromPath(t *testing.T) {
	if got := toolNameFromPath("scripts/run-thing.sh"); got != "run_thing" {
		t.Errorf("toolNameFromPath = %q", got)
	}
}

func TestSanitizeModuleSegment(t *testing.T) {
	if got := sanitizeModuleSegment("PDF Form Filler!"); got != "pdf-form-filler" {
		t.Errorf("sanitizeModuleSegment = %q", got)
	}
	// A name with nothing but invalid characters falls back to "skill".
	if got := sanitizeModuleSegment("###"); got != "skill" {
		t.Errorf("sanitizeModuleSegment(all-invalid) = %q, want %q", got, "skill")
	}
}

func TestModuleRoot(t *testing.T) {
	if got := moduleRoot("github.com/Luum-Home/aisotropy/aisotropy"); got != "github.com/Luum-Home/aisotropy" {
		t.Errorf("moduleRoot = %q", got)
	}
}

// TestGoRawString covers both the raw-backtick-literal branch and the
// strconv.Quote fallback used when the body itself contains a backtick.
func TestGoRawString(t *testing.T) {
	if got := goRawString("plain body"); got != "`plain body`" {
		t.Errorf("goRawString(plain) = %q", got)
	}
	got := goRawString("has a `code` span")
	if got == "`has a `code` span`" {
		t.Errorf("goRawString(backtick) should not emit an unescaped raw literal, got %q", got)
	}
	if got != `"has a `+"`"+`code`+"`"+` span"` {
		t.Errorf("goRawString(backtick) = %q", got)
	}
}

func TestInjectsSilently(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"Always load this on start.", true},
		{"auto-load this skill.", true},
		{"automatically inject context.", true},
		{"always inject the persona.", true},
		{"Apply on every request.", true},
		{"load on every request.", true},
		{"Just follow this procedure step by step.", false},
	}
	for _, tc := range cases {
		if got := injectsSilently(tc.body); got != tc.want {
			t.Errorf("injectsSilently(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
