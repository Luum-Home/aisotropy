package guardrails

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Identity-PII tier: personal NAMES, NATIONAL-ID numbers, and HEALTH
// information — the three classes redact.go's built-in tiers deliberately left
// uncovered ("they cannot be matched by regex without unacceptable
// false-positive rates and need an NER/classifier pass"). This file closes that
// gap WITHOUT a model or a third-party dependency, and without inheriting the
// false-positive problem, by refusing to do the thing that causes it.
//
// THE DESIGN RULE: never classify a bare token. A capitalised word is not a
// name (every sentence starts with one; so does every product, city, and
// framework). A nine-digit run is not an ID (it is also an order number, a
// timestamp, a hash prefix). Every detector here fires ONLY on an explicit
// contextual ANCHOR — the surrounding text declaring what the value is ("my
// name is", "diagnosed with", "SSN:") — or on a value that carries its own
// structural proof (a checksum/range-valid identifier). That is what keeps
// precision high enough to run in front of a model on real user text.
//
// DEFAULT OFF. Enabled with AISOTROPY_REDACT_IDENTITY_PII=true. Redaction that
// silently starts eating user text is its own kind of incident: an operator
// opts in, and the existing tiers' behavior is byte-identical when they do not.
//
// SCOPE HONESTY (what this is NOT): anchored detection finds PII that announces
// itself. It will NOT find an unannounced name in free prose ("met Sarah at the
// clinic") — that genuinely needs NER, and claiming otherwise would be the
// overclaim this file exists to avoid. It raises the floor; it is not a
// guarantee. The structural-attribute allowlist still holds: this operates on
// message CONTENT only, never on correlation fields (trace_id/tenant_id/
// user_id), which must survive redaction to keep telemetry joinable.

const (
	// RedactPersonName is a personal name captured from an explicit
	// name-declaring anchor ("my name is X", "Patient: X").
	RedactPersonName RedactionType = "person_name"
	// RedactNationalID is a government identity number — validated by
	// structure/range, or captured from an explicit anchor.
	RedactNationalID RedactionType = "national_id"
	// RedactHealthInfo is health/medical information captured from an explicit
	// clinical anchor ("diagnosed with X", "prescribed X").
	RedactHealthInfo RedactionType = "health_info"
)

// identityPIIEnvVar gates the whole tier. Unset/false keeps redaction behavior
// byte-identical to the pre-existing tiers.
const identityPIIEnvVar = "AISOTROPY_REDACT_IDENTITY_PII"

// identityPIIEnabled reports whether the identity tier is switched on. Read per
// call rather than cached at construction so a test (or a runtime reload) can
// flip it without rebuilding every Redactor.
func identityPIIEnabled() bool {
	v := strings.TrimSpace(os.Getenv(identityPIIEnvVar))
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err == nil && on
}

var (
	// A name follows an explicit declaration. One to three capitalised words —
	// enough for "Ada Lovelace" or "Maria del Carmen", bounded so a runaway
	// match cannot swallow a sentence. Deliberately EXCLUDED anchors: "I am"
	// and "this is", which precede far more non-names ("I am Argentine") than
	// names.
	reNameAnchored = regexp.MustCompile(`(?i)\b(my name is|full name\s*[:=]|patient\s*[:=]|customer name\s*[:=]|name\s*[:=])\s*([A-ZÁÉÍÓÚÑ][\p{L}'-]+(?:\s+[A-ZÁÉÍÓÚÑ][\p{L}'-]+){0,2})`)

	// US SSN in canonical dashed form. The pattern is permissive; validity is
	// decided by validSSN below, which encodes the ranges the SSA never issues
	// — the structural proof that separates an SSN from any other 9 digits.
	reSSN = regexp.MustCompile(`\b(\d{3})-(\d{2})-(\d{4})\b`)

	// Any national-ID-ish value that names itself. Covers the long tail
	// (passport, DNI, CUIL/CUIT, NIF, tax id) without a per-country regex zoo:
	// the ANCHOR carries the semantics, so the value can stay a loose token.
	reNationalIDAnchored = regexp.MustCompile(`(?i)\b(ssn|social security(?:\s+number)?|passport(?:\s+(?:no|number|#))?|national id|identity card|dni|nie|cuil|cuit|tax id|tin)\s*(?:no\.?|number|#)?\s*[:=]?\s*([A-Z0-9][A-Z0-9.\-]{5,19})\b`)

	// Health information following a clinical anchor. The captured span is
	// bounded to one clause so a "diagnosed with" never eats the paragraph.
	reHealthAnchored = regexp.MustCompile(`(?i)\b(diagnosed with|diagnosis\s*[:=]|suffers from|prescribed|medication\s*[:=]|medical condition\s*[:=]|treated for)\s*([\p{L}][\p{L}\s'-]{2,48})`)
)

// validSSN applies the Social Security Administration's never-issued ranges.
// Without this, "123-45-6789"-shaped strings in test data, invoices, and
// tracking numbers would all redact — the false-positive rate that made a bare
// regex unacceptable in the first place.
func validSSN(area, group, serial string) bool {
	a, err := strconv.Atoi(area)
	if err != nil || a == 0 || a == 666 || a >= 900 {
		return false
	}
	if g, err := strconv.Atoi(group); err != nil || g == 0 {
		return false
	}
	if s, err := strconv.Atoi(serial); err != nil || s == 0 {
		return false
	}
	return true
}

// redactIdentityPII applies the identity tier. It is a no-op (returning the
// input unchanged, report untouched) when the tier is disabled, so callers can
// invoke it unconditionally.
//
// Anchored detectors replace only the CAPTURED VALUE and keep the anchor text.
// Leaving "my name is [REDACTED_PERSON_NAME]" intact preserves the sentence's
// meaning for the model — the point is to remove the identifier, not to shred
// the utterance into something the assistant can no longer reason about.
func (r *Redactor) redactIdentityPII(text string, report *RedactionReport) string {
	if !identityPIIEnabled() || text == "" {
		return text
	}

	// Validated structural IDs first: an SSN inside "SSN: 123-45-6789" should be
	// caught by its own checksum-style rule, not merely by the anchor.
	text = replaceValidated(text, reSSN, func(groups []string) bool {
		return validSSN(groups[1], groups[2], groups[3])
	}, r.placeholder(RedactNationalID), RedactNationalID, report)

	text = replaceCapture(text, reNationalIDAnchored, r.placeholder(RedactNationalID), RedactNationalID, report)
	text = replaceCapture(text, reNameAnchored, r.placeholder(RedactPersonName), RedactPersonName, report)
	text = replaceCapture(text, reHealthAnchored, r.placeholder(RedactHealthInfo), RedactHealthInfo, report)
	return text
}

// replaceCapture replaces submatch 2 (the value) while preserving submatch 1
// (the anchor). Patterns passed here MUST have exactly that shape.
func replaceCapture(text string, re *regexp.Regexp, placeholder string, t RedactionType, report *RedactionReport) string {
	n := 0
	out := re.ReplaceAllStringFunc(text, func(m string) string {
		g := re.FindStringSubmatch(m)
		if len(g) < 3 {
			return m
		}
		n++
		// Preserve the exact anchor bytes and the separator run that followed
		// it, so only the identifier itself is replaced.
		anchorEnd := strings.Index(m, g[2])
		if anchorEnd < 0 {
			return placeholder
		}
		return m[:anchorEnd] + placeholder
	})
	if n > 0 {
		report.add(t, n)
	}
	return out
}

// replaceValidated replaces a match only when `ok` accepts its submatches — the
// pattern proposes, the validator disposes. This is how a detector can be
// permissive about SHAPE without paying for it in false positives.
func replaceValidated(text string, re *regexp.Regexp, ok func(groups []string) bool, placeholder string, t RedactionType, report *RedactionReport) string {
	n := 0
	out := re.ReplaceAllStringFunc(text, func(m string) string {
		g := re.FindStringSubmatch(m)
		if !ok(g) {
			return m
		}
		n++
		return placeholder
	})
	if n > 0 {
		report.add(t, n)
	}
	return out
}
