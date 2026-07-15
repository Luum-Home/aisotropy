package guardrails

import (
	"fmt"
	"regexp"
	"strings"
)

// RedactionType names a class of sensitive data the Redactor scrubs. It is a
// string so a RedactionReport can be logged/serialised without leaking the
// underlying values.
type RedactionType string

const (
	RedactEmail       RedactionType = "email"
	RedactCreditCard  RedactionType = "credit_card"
	RedactAPIKey      RedactionType = "api_key"
	RedactBearerToken RedactionType = "bearer_token"
	RedactPrivateKey  RedactionType = "private_key"
	RedactIP          RedactionType = "ip"
	RedactCustom      RedactionType = "custom"
)

// RedactionReport describes WHAT was redacted from a piece of text — the types
// and how many of each — but deliberately never the values themselves. It is
// safe to log. Total is the sum of Counts and is maintained so callers can
// cheaply test "did anything get redacted?" without walking the map.
type RedactionReport struct {
	Counts map[RedactionType]int
	Total  int
}

// Redacted reports whether any sensitive data was scrubbed.
func (r RedactionReport) Redacted() bool { return r.Total > 0 }

// Types returns the redaction types present in the report (unordered).
func (r RedactionReport) Types() []RedactionType {
	out := make([]RedactionType, 0, len(r.Counts))
	for t := range r.Counts {
		out = append(out, t)
	}
	return out
}

func (r *RedactionReport) add(t RedactionType, n int) {
	if n <= 0 {
		return
	}
	if r.Counts == nil {
		r.Counts = make(map[RedactionType]int)
	}
	r.Counts[t] += n
	r.Total += n
}

// CustomPattern is an integrator-supplied detector. Name identifies it in the
// report (under the custom type namespace) and Placeholder is what its matches
// are replaced with. Compile a CustomPattern with NewCustomPattern.
type CustomPattern struct {
	Name        string
	re          *regexp.Regexp
	Placeholder string
}

// NewCustomPattern compiles a custom redaction pattern. The name is used both
// in the redaction report (as "custom:<name>") and, if Placeholder is empty,
// to derive a default placeholder of the form [REDACTED_<NAME>].
func NewCustomPattern(name, pattern, placeholder string) (CustomPattern, error) {
	if strings.TrimSpace(name) == "" {
		return CustomPattern{}, fmt.Errorf("guardrails: custom pattern name must not be empty")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return CustomPattern{}, fmt.Errorf("guardrails: custom pattern %q: %w", name, err)
	}
	if placeholder == "" {
		placeholder = "[REDACTED_" + strings.ToUpper(name) + "]"
	}
	return CustomPattern{Name: name, re: re, Placeholder: placeholder}, nil
}

// Precompiled detectors, in the order they are applied. Ordering matters:
// multi-line PEM blocks and "Bearer <tok>" phrases are matched before the
// bare prefix-token patterns so the more specific form wins, and structured
// identifiers (emails, IPs) are matched before the greedy credit-card scan so
// their digits are already replaced by non-numeric placeholders.
var (
	// PEM private-key block: -----BEGIN ... PRIVATE KEY----- ... -----END ...
	rePrivateKey = regexp.MustCompile(`(?s)-----BEGIN[^-]*PRIVATE KEY-----.*?-----END[^-]*PRIVATE KEY-----`)

	// Authorization: Bearer <token>. Case-insensitive on the keyword.
	reBearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-]{8,}=*`)

	// Internal Aisotropy secret env vars, with or without an assigned value.
	reAisotropy = regexp.MustCompile(`\bAISOTROPY_[A-Z0-9_]+\b(?:\s*[:=]\s*\S+)?`)

	// Common provider token prefixes.
	reOpenAIKey = regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}\b`)
	reGitHubPAT = regexp.MustCompile(`\bghp_[A-Za-z0-9]{16,}\b`)
	reAWSKeyID  = regexp.MustCompile(`\bAKIA[0-9A-Z]{12,}\b`)
	reSlackTok  = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{8,}\b`)

	reEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)

	// IPv4 candidate; octet ranges are validated in code before redacting.
	reIPv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

	// Credit-card candidate: 13-19 digits, optionally grouped by single spaces
	// or dashes. Luhn-validated in code before redacting.
	reCardCandidate = regexp.MustCompile(`\b\d(?:[ \-]?\d){12,18}\b`)
)

// prefixKeyDetector pairs a provider-token regex with its report type.
type prefixKeyDetector struct {
	re *regexp.Regexp
	t  RedactionType
}

var prefixKeyDetectors = []prefixKeyDetector{
	{reOpenAIKey, RedactAPIKey},
	{reGitHubPAT, RedactAPIKey},
	{reAWSKeyID, RedactAPIKey},
	{reSlackTok, RedactAPIKey},
}

// Redactor scrubs sensitive data from text before it reaches the model. The
// zero value is not usable; construct one with NewRedactor. A Redactor is
// safe for concurrent use: it holds only immutable compiled patterns.
type Redactor struct {
	custom       []CustomPattern
	placeholders map[RedactionType]string
}

// NewRedactor returns a Redactor loaded with the built-in detectors plus any
// supplied custom patterns.
func NewRedactor(custom ...CustomPattern) *Redactor {
	return &Redactor{
		custom: append([]CustomPattern(nil), custom...),
		placeholders: map[RedactionType]string{
			RedactEmail:       "[REDACTED_EMAIL]",
			RedactCreditCard:  "[REDACTED_CREDIT_CARD]",
			RedactAPIKey:      "[REDACTED_API_KEY]",
			RedactBearerToken: "[REDACTED_BEARER_TOKEN]",
			RedactPrivateKey:  "[REDACTED_PRIVATE_KEY]",
			RedactIP:          "[REDACTED_IP]",
		},
	}
}

func (r *Redactor) placeholder(t RedactionType) string {
	if p, ok := r.placeholders[t]; ok {
		return p
	}
	return "[REDACTED_" + strings.ToUpper(string(t)) + "]"
}

// Redact returns the scrubbed text and a report of what was removed. It is
// fail-open on the text: the returned string is always a valid message (never
// dropped), and a nil/empty input yields an empty report. The report never
// contains the redacted values.
func (r *Redactor) Redact(text string) (string, RedactionReport) {
	report := RedactionReport{}
	if text == "" {
		return "", report
	}

	// 1. Multi-line PEM private-key blocks.
	text = r.replaceAll(text, rePrivateKey, RedactPrivateKey, &report)

	// 2. Bearer tokens (before bare prefix tokens so the whole phrase wins).
	text = r.replaceAll(text, reBearer, RedactBearerToken, &report)

	// 3. Internal Aisotropy secret env vars (name, optionally with value).
	text = r.replaceAll(text, reAisotropy, RedactAPIKey, &report)

	// 4. Provider token prefixes.
	for _, d := range prefixKeyDetectors {
		text = r.replaceAll(text, d.re, d.t, &report)
	}

	// 5. Emails (before the numeric scans).
	text = r.replaceAll(text, reEmail, RedactEmail, &report)

	// 6. Credit cards — greedy candidate scan, Luhn-gated.
	text = reCardCandidate.ReplaceAllStringFunc(text, func(m string) string {
		digits := stripSeparators(m)
		if luhnValid(digits) {
			report.add(RedactCreditCard, 1)
			return r.placeholder(RedactCreditCard)
		}
		return m
	})

	// 7. IPv4 addresses — validate octet ranges to cut false positives.
	text = reIPv4.ReplaceAllStringFunc(text, func(m string) string {
		if validIPv4(m) {
			report.add(RedactIP, 1)
			return r.placeholder(RedactIP)
		}
		return m
	})

	// 8. Integrator-supplied custom patterns, last so they run on what remains.
	//    Count matches before substituting so the report stays accurate.
	for _, c := range r.custom {
		n := len(c.re.FindAllStringIndex(text, -1))
		if n == 0 {
			continue
		}
		report.add(RedactCustom, n)
		text = c.re.ReplaceAllString(text, c.Placeholder)
	}

	return text, report
}

// replaceAll substitutes every match of re with the type's placeholder and
// tallies the count into report.
func (r *Redactor) replaceAll(text string, re *regexp.Regexp, t RedactionType, report *RedactionReport) string {
	n := len(re.FindAllStringIndex(text, -1))
	if n == 0 {
		return text
	}
	report.add(t, n)
	return re.ReplaceAllString(text, r.placeholder(t))
}

// stripSeparators removes spaces and dashes, leaving only the digits of a
// credit-card candidate.
func stripSeparators(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// luhnValid reports whether an all-digit string of length 13..19 passes the
// Luhn checksum used by payment-card numbers.
func luhnValid(num string) bool {
	if len(num) < 13 || len(num) > 19 {
		return false
	}
	sum := 0
	alt := false
	for i := len(num) - 1; i >= 0; i-- {
		c := num[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// validIPv4 reports whether every dotted octet is in 0..255.
func validIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
		n := 0
		for i := 0; i < len(p); i++ {
			c := p[i]
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}
