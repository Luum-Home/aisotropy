package guardrails

import (
	"net/url"
	"strings"
)

// Identity concealment — which vendor/model names the assistant must never
// disclose (the `sg-identity-model` safeguard probe; ADR-0070).
//
// WHY THIS IS DERIVED AND NOT A LIST. The first version was a hardcoded slice:
// {"agnes", "sapiens", "2.0-flash", ...}. That is a defence that degrades
// SILENTLY — point the stack at a different provider and the test keeps passing
// while protecting nothing, because it is still guarding the old vendor's names.
// Nothing fails; the coverage just quietly disappears. Same failure shape as a
// guard that cannot run absolving the thing it cannot check.
//
// So the tokens are DERIVED from the configured provider (the model id and the
// endpoint host) and unioned with a baseline of well-known vendors. Switching
// providers defends the new one automatically, with no list to remember.

// baselineIdentityTokens are vendor/model names a model may volunteer regardless
// of who is actually serving it — a model often "knows" it is a GPT or a Claude
// derivative even when reached through a third-party gateway. These are always
// denied, independently of configuration.
var baselineIdentityTokens = []string{
	"openai", "gpt-4", "gpt-3", "chatgpt",
	"anthropic", "claude",
	"google", "gemini", "palm",
	"meta", "llama",
	"mistral", "mixtral",
	"deepseek", "qwen", "grok", "cohere",
}

// identityStopWords are fragments that appear in model ids but are far too
// generic to deny — banning them would make the assistant refuse ordinary
// sentences, turning a security control into a usability bug.
var identityStopWords = map[string]bool{
	"v1": true, "v2": true, "api": true, "ai": true, "com": true, "net": true,
	"org": true, "io": true, "www": true, "inference": true, "apihub": true,
	"chat": true, "model": true, "models": true, "base": true, "instruct": true,
	"preview": true, "latest": true, "flash": true, "mini": true, "turbo": true,
	"pro": true, "max": true, "small": true, "large": true, "http": true,
	"https": true,
	// NOTE: membership IS the meaning — a key present here is a stop word. A
	// vendor-meaningful term (openai, claude, …) is simply absent, never listed
	// with a false value: an inverted read of that second form is exactly the
	// bug this file's own test caught ("inference" leaking through as deniable).
}

// IdentityTokens returns the lowercase tokens the assistant must never disclose,
// given the configured model id and endpoint base URL. Both may be empty (the
// keyless stub path), in which case only the baseline is returned.
//
// Derivation, deliberately conservative — a false positive here makes the
// assistant refuse legitimate text, so only structurally meaningful fragments
// are taken:
//
//   - model id: split on "/", "-", "_", ":" and "." — so "poolside/laguna-s-2.1"
//     contributes "poolside" and "laguna", and "agnes-2.0-flash" contributes
//     "agnes" (not "flash", a stop word, and not "2" or "0", too short).
//   - endpoint host: the registrable labels, so "inference.poolside.ai"
//     contributes "poolside" ("inference" and "ai" are stop words).
//
// Fragments shorter than 4 characters are dropped: they collide with ordinary
// words far more often than they identify a vendor.
func IdentityTokens(model, baseURL string) []string {
	seen := make(map[string]bool, len(baselineIdentityTokens)+8)
	out := make([]string, 0, len(baselineIdentityTokens)+8)

	add := func(tok string) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if len(tok) < 4 || seen[tok] {
			return
		}
		if identityStopWords[tok] {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}

	for _, t := range baselineIdentityTokens {
		add(t)
	}
	for _, part := range strings.FieldsFunc(model, isIdentitySep) {
		add(part)
	}
	if host := hostOf(baseURL); host != "" {
		for _, label := range strings.Split(host, ".") {
			add(label)
		}
	}
	return out
}

func isIdentitySep(r rune) bool {
	switch r {
	case '/', '-', '_', ':', '.', ' ':
		return true
	}
	return false
}

// hostOf extracts the host from a base URL, tolerating a missing scheme so a
// bare "inference.poolside.ai/v1" still yields a host.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// LeakedIdentityTokens returns which denied tokens appear in a reply, so a
// caller (a test, or an egress check) can report exactly what leaked rather
// than only that something did.
func LeakedIdentityTokens(reply, model, baseURL string) []string {
	lower := strings.ToLower(reply)
	var leaked []string
	for _, tok := range IdentityTokens(model, baseURL) {
		if strings.Contains(lower, tok) {
			leaked = append(leaked, tok)
		}
	}
	return leaked
}
