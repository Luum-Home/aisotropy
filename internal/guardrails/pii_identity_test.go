package guardrails

import (
	"strings"
	"testing"
)

// The identity tier's whole value proposition is PRECISION: the reason names /
// national IDs / health info were left uncovered is that a naive regex
// over-redacts real text into uselessness. So this file weights the
// false-positive table as heavily as the detection table — a detector that
// catches every SSN and also every order number is not an improvement.

func TestIdentityPII_DetectsAnchoredEntities(t *testing.T) {
	t.Setenv(identityPIIEnvVar, "true")
	r := NewRedactor()

	cases := []struct {
		name     string
		in       string
		wantType RedactionType
		// gone must NOT survive; kept MUST survive (the anchor stays so the
		// sentence still means something to the model).
		gone []string
		kept []string
	}{
		{
			name:     "name after 'my name is'",
			in:       "Hi, my name is Ada Lovelace and I need help.",
			wantType: RedactPersonName, gone: []string{"Ada Lovelace"},
			kept: []string{"my name is", "I need help"},
		},
		{
			name:     "name after a labelled field",
			in:       "Patient: Maria Gonzalez\nReason: follow-up",
			wantType: RedactPersonName, gone: []string{"Maria Gonzalez"},
			kept: []string{"Patient:", "follow-up"},
		},
		{
			name:     "valid SSN redacts on its own structure",
			in:       "My social is 123-45-6789, please verify.",
			wantType: RedactNationalID, gone: []string{"123-45-6789"},
			kept: []string{"please verify"},
		},
		{
			name:     "anchored passport number",
			in:       "Passport number: X1234567 issued in 2021.",
			wantType: RedactNationalID, gone: []string{"X1234567"},
			kept: []string{"Passport number", "issued in 2021"},
		},
		{
			name:     "anchored national id (DNI)",
			in:       "DNI: 30123456 for the account.",
			wantType: RedactNationalID, gone: []string{"30123456"},
			kept: []string{"DNI", "for the account"},
		},
		{
			name:     "health condition after a clinical anchor",
			in:       "The patient was diagnosed with type 2 diabetes last year.",
			wantType: RedactHealthInfo, gone: []string{"type 2 diabetes"},
			kept: []string{"diagnosed with"},
		},
		{
			name:     "prescribed medication",
			in:       "She was prescribed metformin daily.",
			wantType: RedactHealthInfo, gone: []string{"metformin"},
			kept: []string{"prescribed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, rep := r.Redact(tc.in)
			if rep.Counts[tc.wantType] == 0 {
				t.Fatalf("expected a %s redaction; got report %v and output %q", tc.wantType, rep.Counts, out)
			}
			for _, g := range tc.gone {
				if strings.Contains(out, g) {
					t.Errorf("value %q survived redaction: %q", g, out)
				}
			}
			for _, k := range tc.kept {
				if !strings.Contains(out, k) {
					t.Errorf("anchor/context %q was destroyed (the sentence must stay meaningful): %q", k, out)
				}
			}
		})
	}
}

// TestIdentityPII_DoesNotOverRedact is the load-bearing test. Every input here
// LOOKS like PII to a naive matcher and is not. If this table starts failing,
// the tier has become the false-positive machine it was designed not to be.
func TestIdentityPII_DoesNotOverRedact(t *testing.T) {
	t.Setenv(identityPIIEnvVar, "true")
	r := NewRedactor()

	unchanged := []struct{ name, in string }{
		{"bare capitalised words are not names", "Paris is the capital of France."},
		{"sentence-initial capital is not a name", "Docker builds the image locally."},
		{"product names are not personal names", "Qdrant and BadgerDB store the vectors."},
		{"SSN-shaped but invalid area 000", "Order 000-45-6789 shipped."},
		{"SSN-shaped but invalid area 666", "Ticket 666-45-6789 was closed."},
		{"SSN-shaped but invalid area 900+", "Ref 900-45-6789 is pending."},
		{"SSN-shaped but zero group", "Code 123-00-6789 rejected."},
		{"SSN-shaped but zero serial", "Code 123-45-0000 rejected."},
		{"a bare 9-digit run is not an ID", "The build number is 123456789."},
		{"health words without a clinical anchor", "Diabetes research funding increased."},
		{"'name' as an ordinary noun", "The name of the collection is arena_rag."},
	}

	for _, tc := range unchanged {
		t.Run(tc.name, func(t *testing.T) {
			out, rep := r.Redact(tc.in)
			if out != tc.in {
				t.Errorf("FALSE POSITIVE — input was rewritten.\n  in:  %q\n  out: %q\n  report: %v", tc.in, out, rep.Counts)
			}
			for _, ty := range []RedactionType{RedactPersonName, RedactNationalID, RedactHealthInfo} {
				if rep.Counts[ty] != 0 {
					t.Errorf("FALSE POSITIVE — reported a %s on benign text %q", ty, tc.in)
				}
			}
		})
	}
}

// TestIdentityPII_DefaultOffIsByteIdentical proves the opt-in contract: with the
// env var unset, text that the tier WOULD redact passes through untouched, so
// enabling this file changed nothing for existing deployments.
func TestIdentityPII_DefaultOffIsByteIdentical(t *testing.T) {
	t.Setenv(identityPIIEnvVar, "")
	r := NewRedactor()

	in := "My name is Ada Lovelace, SSN 123-45-6789, diagnosed with asthma."
	out, rep := r.Redact(in)
	if out != in {
		t.Errorf("identity tier acted while disabled.\n  in:  %q\n  out: %q", in, out)
	}
	for _, ty := range []RedactionType{RedactPersonName, RedactNationalID, RedactHealthInfo} {
		if rep.Counts[ty] != 0 {
			t.Errorf("identity tier reported %s while disabled", ty)
		}
	}
}

// TestIdentityPII_PreservesCorrelationFields locks the structural-attribute
// allowlist invariant at the content layer: identifiers that telemetry needs to
// join traces (trace_id, tenant_id, user_id) must never be eaten by PII
// redaction — a redactor that breaks correlation trades one incident for
// another.
func TestIdentityPII_PreservesCorrelationFields(t *testing.T) {
	t.Setenv(identityPIIEnvVar, "true")
	r := NewRedactor()

	in := "trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 " +
		"tenant_id=acme user_id=u-1042 request_id=req-77"
	out, rep := r.Redact(in)
	for _, must := range []string{
		"trace_id=4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id=00f067aa0ba902b7",
		"tenant_id=acme",
		"user_id=u-1042",
		"request_id=req-77",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("correlation field %q was redacted — telemetry would stop joining: %q (report %v)", must, out, rep.Counts)
		}
	}
}

// TestIdentityPII_Deterministic — redaction feeds a security suite and a
// prompt; the same input must always produce the same output, or neither the
// gate nor the model behavior is reproducible.
func TestIdentityPII_Deterministic(t *testing.T) {
	t.Setenv(identityPIIEnvVar, "true")
	r := NewRedactor()

	in := "Patient: Ada Lovelace, DNI: 30123456, diagnosed with asthma; contact 123-45-6789."
	first, firstRep := r.Redact(in)
	for i := 0; i < 20; i++ {
		got, rep := r.Redact(in)
		if got != first {
			t.Fatalf("non-deterministic redaction on iteration %d:\n  %q\n  %q", i, first, got)
		}
		if rep.Total != firstRep.Total {
			t.Fatalf("non-deterministic report on iteration %d: %v vs %v", i, firstRep.Counts, rep.Counts)
		}
	}
}
