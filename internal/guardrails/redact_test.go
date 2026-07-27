package guardrails

import (
	"strings"
	"testing"
)

func TestRedactorPII(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name       string
		in         string
		wantType   RedactionType
		wantCount  int
		wantMasked string // substring that must appear in output (placeholder)
		wantGone   string // substring that must NOT appear in output (the secret)
	}{
		{
			name:       "email single",
			in:         "contact me at alice@example.com please",
			wantType:   RedactEmail,
			wantCount:  1,
			wantMasked: "[REDACTED_EMAIL]",
			wantGone:   "alice@example.com",
		},
		{
			name:      "email two",
			in:        "a@b.co and c@d.org",
			wantType:  RedactEmail,
			wantCount: 2,
		},
		{
			name:       "valid visa credit card with spaces",
			in:         "card 4111 1111 1111 1111 exp",
			wantType:   RedactCreditCard,
			wantCount:  1,
			wantMasked: "[REDACTED_CREDIT_CARD]",
			wantGone:   "4111",
		},
		{
			name:       "valid amex credit card no separators",
			in:         "378282246310005",
			wantType:   RedactCreditCard,
			wantCount:  1,
			wantMasked: "[REDACTED_CREDIT_CARD]",
		},
		{
			name:       "openai key",
			in:         "key=sk-abcdefghijklmnop0123456789",
			wantType:   RedactAPIKey,
			wantCount:  1,
			wantMasked: "[REDACTED_API_KEY]",
			wantGone:   "sk-abcdefghijklmnop",
		},
		{
			name:      "github pat",
			in:        "token ghp_abcdefghijklmnop0123",
			wantType:  RedactAPIKey,
			wantCount: 1,
			wantGone:  "ghp_abcdefghijklmnop0123",
		},
		{
			// Fixture assembled at runtime so no contiguous token literal is
			// written to source (avoids secret-scanner false positives). The
			// value is AWS's public documentation example key, not a real one.
			name:      "aws access key id",
			in:        "AKI" + "A" + "IOSFODNN7EXAMPLE here",
			wantType:  RedactAPIKey,
			wantCount: 1,
			wantGone:  "IOSFODNN7EXAMPLE",
		},
		{
			name:      "slack token",
			in:        "xox" + "b-1234567890-abcdefghij",
			wantType:  RedactAPIKey,
			wantCount: 1,
			wantGone:  "1234567890-abcdefghij",
		},
		{
			name:       "bearer token",
			in:         "Authorization: Bearer abcdef123456ghijkl",
			wantType:   RedactBearerToken,
			wantCount:  1,
			wantMasked: "[REDACTED_BEARER_TOKEN]",
			wantGone:   "abcdef123456ghijkl",
		},
		{
			name:      "aisotropy secret env with value",
			in:        "AISOTROPY_API_KEY=supersecretvalue123",
			wantType:  RedactAPIKey,
			wantCount: 1,
			wantGone:  "supersecretvalue123",
		},
		{
			name:       "ipv4",
			in:         "server at 192.168.1.100 online",
			wantType:   RedactIP,
			wantCount:  1,
			wantMasked: "[REDACTED_IP]",
			wantGone:   "192.168.1.100",
		},
		{
			name:       "phone separated us style",
			in:         "call 555 123 4567 today",
			wantType:   RedactPhone,
			wantCount:  1,
			wantMasked: "[REDACTED_PHONE]",
			wantGone:   "555 123 4567",
		},
		{
			name:       "phone international with plus",
			in:         "reach me on +1 415 555 0132 anytime",
			wantType:   RedactPhone,
			wantCount:  1,
			wantMasked: "[REDACTED_PHONE]",
			wantGone:   "555 0132",
		},
		{
			name:       "postal code keyword anchored",
			in:         "ship to zip code 90210 please",
			wantType:   RedactPostalCode,
			wantCount:  1,
			wantMasked: "[REDACTED_POSTAL_CODE]",
			wantGone:   "90210",
		},
		{
			name:       "street address",
			in:         "deliver to 123 Main Street on Tuesday",
			wantType:   RedactStreetAddress,
			wantCount:  1,
			wantMasked: "[REDACTED_ADDRESS]",
			wantGone:   "123 Main Street",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, rep := r.Redact(tc.in)
			if got := rep.Counts[tc.wantType]; got != tc.wantCount {
				t.Fatalf("count[%s] = %d, want %d (out=%q report=%+v)", tc.wantType, got, tc.wantCount, out, rep.Counts)
			}
			if tc.wantMasked != "" && !strings.Contains(out, tc.wantMasked) {
				t.Errorf("output %q missing placeholder %q", out, tc.wantMasked)
			}
			if tc.wantGone != "" && strings.Contains(out, tc.wantGone) {
				t.Errorf("output %q still contains secret %q", out, tc.wantGone)
			}
		})
	}
}

func TestRedactorPrivateKeyBlock(t *testing.T) {
	r := NewRedactor()
	// PEM markers assembled from fragments so no contiguous key-header literal
	// is written to source (avoids secret-scanner false positives). This is a
	// synthetic block, not a real key.
	dash := "-----"
	begin := dash + "BEGIN RSA PRIVATE" + " KEY" + dash
	end := dash + "END RSA PRIVATE" + " KEY" + dash
	in := "before\n" + begin + "\nMIIEowIBAAKCAQEA...\nabc123==\n" + end + "\nafter"
	out, rep := r.Redact(in)
	if rep.Counts[RedactPrivateKey] != 1 {
		t.Fatalf("private key count = %d, want 1", rep.Counts[RedactPrivateKey])
	}
	if strings.Contains(out, "MIIEowIBAAKCAQEA") {
		t.Errorf("private key body leaked in output: %q", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("surrounding text lost: %q", out)
	}
}

// TestRedactorFalsePositives makes sure non-secret text is left untouched,
// especially Luhn-invalid numbers and out-of-range IP octets.
func TestRedactorFalsePositives(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name string
		in   string
	}{
		{"luhn invalid 16 digits", "order number 1234567890123456 shipped"},
		{"invalid ip octet", "version 999.999.1.1 build"},
		{"plain prose", "The quick brown fox jumps over the lazy dog."},
		{"short digit run", "pin is 1234"},
		// A lowercase phrase that ends in a street-type word must NOT be read as an
		// address (the name words are matched case-sensitively).
		{"lowercase street-word prose", "it is only a 5 minutes drive from here"},
		// A bare grouped number without phone separators/plus or a zip keyword.
		{"bare number no context", "the total came to 12345 units"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, rep := r.Redact(tc.in)
			if rep.Total != 0 {
				t.Errorf("expected no redactions, got %d (%+v), out=%q", rep.Total, rep.Counts, out)
			}
			if out != tc.in {
				t.Errorf("text mutated: got %q want %q", out, tc.in)
			}
		})
	}
}

func TestRedactorFailOpenEmpty(t *testing.T) {
	r := NewRedactor()
	out, rep := r.Redact("")
	if out != "" || rep.Total != 0 || rep.Redacted() {
		t.Fatalf("empty input should yield empty output and empty report, got out=%q rep=%+v", out, rep)
	}
}

func TestRedactorCustomPattern(t *testing.T) {
	cp, err := NewCustomPattern("employee_id", `\bEMP-\d{5}\b`, "")
	if err != nil {
		t.Fatalf("NewCustomPattern: %v", err)
	}
	r := NewRedactor(cp)
	out, rep := r.Redact("user EMP-12345 and EMP-67890 flagged")
	if rep.Counts[RedactCustom] != 2 {
		t.Fatalf("custom count = %d, want 2", rep.Counts[RedactCustom])
	}
	if strings.Contains(out, "EMP-12345") || strings.Contains(out, "EMP-67890") {
		t.Errorf("custom secret leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED_EMPLOYEE_ID]") {
		t.Errorf("default custom placeholder missing: %q", out)
	}
}

func TestNewCustomPatternValidation(t *testing.T) {
	if _, err := NewCustomPattern("", `x`, ""); err == nil {
		t.Error("empty name should error")
	}
	if _, err := NewCustomPattern("bad", `(unclosed`, ""); err == nil {
		t.Error("invalid regex should error")
	}
}

func TestRedactorMultipleTypesReported(t *testing.T) {
	r := NewRedactor()
	in := "email bob@example.com card 4111111111111111 ip 10.0.0.1 key sk-abcdefghijklmnop0123456789"
	_, rep := r.Redact(in)
	for _, want := range []RedactionType{RedactEmail, RedactCreditCard, RedactIP, RedactAPIKey} {
		if rep.Counts[want] < 1 {
			t.Errorf("expected at least one %s redaction, report=%+v", want, rep.Counts)
		}
	}
	if rep.Total < 4 {
		t.Errorf("expected total >= 4, got %d", rep.Total)
	}
}

func TestLuhnValid(t *testing.T) {
	tests := []struct {
		num  string
		want bool
	}{
		{"4111111111111111", true},  // visa test
		{"378282246310005", true},   // amex test
		{"6011111111111117", true},  // discover test
		{"1234567890123456", false}, // fails luhn
		{"", false},
		{"123", false},                  // too short
		{"12345678901234567890", false}, // too long (>19)
		{"411111111111111a", false},     // non-digit
	}
	for _, tc := range tests {
		if got := luhnValid(tc.num); got != tc.want {
			t.Errorf("luhnValid(%q) = %v, want %v", tc.num, got, tc.want)
		}
	}
}

func TestValidIPv4(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"192.168.1.1", true},
		{"0.0.0.0", true},
		{"255.255.255.255", true},
		{"256.1.1.1", false},
		{"1.2.3", false},
		{"1.2.3.4.5", false},
		{"999.1.1.1", false},
	}
	for _, tc := range tests {
		if got := validIPv4(tc.ip); got != tc.want {
			t.Errorf("validIPv4(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}
