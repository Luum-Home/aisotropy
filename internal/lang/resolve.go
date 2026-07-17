package lang

// Resolve applies the detect-vs-remember precedence between the language freshly
// detected for a request and the one previously remembered for that (tenant,user)
// subject (research R1 follow-on, ADR-0053). It is the single place the rule
// lives, so the streaming and non-streaming chat paths reconcile detection and
// memory identically.
//
// The rule:
//
//   - A CONFIDENT detection (anything other than Undetermined) wins and becomes
//     the effective language. persist is true only when it differs from the
//     stored value, so a run of same-language requests writes nothing — the
//     first request in a new language teaches the subject, repeats are free.
//   - An UNDETERMINED detection (empty, too short, or code-mixed input that the
//     detector could not confidently classify) FALLS BACK to the stored language.
//     persist is false: there is nothing newly learned to write, and a low-signal
//     turn must never overwrite a previously learned language with "und".
//   - With no detection AND no stored value the result is Undetermined and
//     persist is false, so consumers keep their English/default behavior. Resolve
//     never invents a language.
//
// The returned Language is ADVISORY: it conditions downstream prose (persona
// selection today, language-conditional guardrails/tokenizer/embedder later),
// never authorization. It carries no tenant/user identity of its own — isolation
// is enforced by the caller keying the stored value strictly by (tenant,user)
// (see internal/memory subject language storage).
func Resolve(detected, stored Language) (effective Language, persist bool) {
	if !detected.IsUndetermined() {
		return detected, detected != stored
	}
	if !stored.IsUndetermined() {
		return stored, false
	}
	return Undetermined, false
}
