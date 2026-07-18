package llm

import (
	"context"
	"os"
	"strings"
)

// Reasoning-model support (ADR-0061). This file holds the PROVIDER-AGNOSTIC
// half of the design: the opaque carry-forward value threaded through the agent
// loops, the optional capability interface providers may implement, the OFF-by-
// default config flag, and the quota-pressure reasoning-effort ladder. Every
// provider-specific concern (Anthropic signed thinking blocks, OpenAI-compat
// reasoning fields) lives in the respective client file and only ever produces
// or consumes the opaque ReasoningBlock — no provider-ism leaks into this seam
// (ADR-0009 provider-agnostic boundary).
//
// The whole surface is a structural NO-OP unless reasoning is explicitly
// enabled AND the endpoint actually emits reasoning: an empty ReasoningState
// carries nothing, a plain Client is not a ReasoningToolCaller so the loop
// never touches it, and Token.Reasoning stays "".

// reasoningEnvVar toggles reasoning-model support process-wide. Reasoning is
// OFF by default (ADR-0061 "reasoning is OFF by default"): with it unset the
// clients never request thinking, the loops never carry reasoning, and the text
// path is byte-for-byte unchanged.
const reasoningEnvVar = "AISOTROPY_REASONING"

// reasoningEffortEnvVar sets the configured reasoning effort when reasoning is
// enabled: one of "high", "medium", "low", "off". Unset defaults to
// ReasoningEffortMedium. Under quota pressure the effort is stepped DOWN this
// ladder before the cheap-model degrade fires (see ReasoningEffortForPressure).
const reasoningEffortEnvVar = "AISOTROPY_REASONING_EFFORT"

// Reasoning effort levels, highest to lowest. "off" disables reasoning for a
// single call without disabling the feature (the ladder's bottom rung).
const (
	ReasoningEffortHigh   = "high"
	ReasoningEffortMedium = "medium"
	ReasoningEffortLow    = "low"
	ReasoningEffortOff    = "off"
)

// reasoningBlockKind classifies an opaque carried reasoning block. The loop
// never inspects it; only the producing Client interprets its meaning.
type reasoningBlockKind int

const (
	// reasoningKindThinking is a signed Anthropic thinking block (Thinking +
	// Signature), re-attached verbatim as a native `thinking` content block on
	// the next turn so the signature survives (Anthropic's hard requirement).
	reasoningKindThinking reasoningBlockKind = iota
	// reasoningKindRedacted is an Anthropic redacted_thinking block: opaque,
	// encrypted (RedactedData). Passed back verbatim, never decoded or logged.
	reasoningKindRedacted
	// reasoningKindText is a stateless provider's plain reasoning text
	// (OpenAI-compat reasoning / reasoning_content). Optional context on the
	// next turn; there is no signature to preserve.
	reasoningKindText
)

// ReasoningBlock is one opaque unit of carried reasoning. It is deliberately
// OPAQUE to the agent loops (they only shuttle it between steps): only the
// producing/consuming Client reads its fields. Kept as a value with no pointers
// so a ReasoningState copy is a deep copy.
type ReasoningBlock struct {
	kind      reasoningBlockKind
	thinking  string // reasoningKindThinking: the thinking text
	signature string // reasoningKindThinking: the cryptographic signature
	redacted  string // reasoningKindRedacted: the opaque encrypted payload
	text      string // reasoningKindText: plain reasoning text (no signature)
}

// newThinkingBlock builds a signed-thinking carry block (Anthropic).
func newThinkingBlock(thinking, signature string) ReasoningBlock {
	return ReasoningBlock{kind: reasoningKindThinking, thinking: thinking, signature: signature}
}

// newRedactedThinkingBlock builds a redacted-thinking carry block (Anthropic).
func newRedactedThinkingBlock(data string) ReasoningBlock {
	return ReasoningBlock{kind: reasoningKindRedacted, redacted: data}
}

// newTextReasoningBlock builds a plain-text reasoning carry block (stateless
// providers with no signature to preserve).
func newTextReasoningBlock(text string) ReasoningBlock {
	return ReasoningBlock{kind: reasoningKindText, text: text}
}

// ReasoningState is the intra-task carry-forward for reasoning across tool-call
// steps (ADR-0061 Piece 2). It is a VALUE carried on the request goroutine's
// call stack — exactly like agentloop's `current` local — NEVER a shared map,
// NEVER keyed by tenant. Two tenants' loops each hold their own ReasoningState
// on their own goroutine, so they are structurally incapable of sharing a
// reasoning byte (the tenant-isolation guarantee). It is garbage-collected when
// the loop returns, so reasoning never outlives the task and never reaches
// persistence.
type ReasoningState struct {
	// Blocks is the provider-native carry for the NEXT step, opaque to the loop.
	Blocks []ReasoningBlock
}

// IsEmpty reports whether the state carries nothing — the steady state on a
// non-reasoning endpoint and with reasoning off.
func (s ReasoningState) IsEmpty() bool { return len(s.Blocks) == 0 }

// Clone returns an independent deep copy of the state. Callers thread a cloned
// value between steps so no two steps ever alias the same backing slice — a
// belt-and-suspenders reinforcement of the value semantics (the blocks are
// value types with no pointers, so copying the slice is a full copy).
func (s ReasoningState) Clone() ReasoningState {
	if len(s.Blocks) == 0 {
		return ReasoningState{}
	}
	blocks := make([]ReasoningBlock, len(s.Blocks))
	copy(blocks, s.Blocks)
	return ReasoningState{Blocks: blocks}
}

// ReasoningText returns the human-readable reasoning carried by the state — the
// concatenation of every block's thinking/text — for callers (e.g. the
// StreamChat-only agentloop) that reinject reasoning as plain leading context
// where there is no signed block to preserve. Redacted (encrypted) blocks
// contribute nothing: they are opaque and MUST NOT be surfaced as text. Returns
// "" for an empty state.
func (s ReasoningState) ReasoningText() string {
	var sb strings.Builder
	for _, b := range s.Blocks {
		switch b.kind {
		case reasoningKindThinking:
			if b.thinking != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.thinking)
			}
		case reasoningKindText:
			if b.text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.text)
			}
		}
	}
	return sb.String()
}

// NewTextReasoningState builds a ReasoningState carrying a single plain-text
// reasoning block (no signature) — the shape a StreamChat-only capture path
// produces, where only the streamed reasoning TEXT is available. Empty text
// yields an empty state, so a non-reasoning stream produces nothing.
func NewTextReasoningState(text string) ReasoningState {
	if text == "" {
		return ReasoningState{}
	}
	return ReasoningState{Blocks: []ReasoningBlock{newTextReasoningBlock(text)}}
}

// ReasoningToolCaller is an OPTIONAL capability a ToolCaller MAY additionally
// implement when its provider can carry reasoning across tool-call steps. It is
// discovered by type assertion exactly like ToolCaller (rtc, ok :=
// client.(ReasoningToolCaller)); a ToolCaller that does not implement it means
// reasoning is simply not carried between steps — graceful degradation: the
// loop still runs, it just loses cross-step chain-of-thought.
//
// It embeds ToolCaller (hence Client), so a ReasoningToolCaller is always a
// usable streaming Client and a usable tool caller. The SAME security invariant
// applies: the tools passed in MUST already be scope-filtered and the returned
// ToolCall.Name MUST STILL be re-validated by the caller before execution.
type ReasoningToolCaller interface {
	ToolCaller

	// CallToolsReasoning is CallTools plus a prior ReasoningState IN and a fresh
	// ReasoningState OUT. The producing Client re-attaches prior.Blocks in its
	// provider-native form (Anthropic: verbatim signed `thinking` content
	// blocks; OpenAI-compat: optional reasoning-summary context) and returns
	// whatever reasoning it produced this turn. Implementations MUST honor ctx
	// cancellation. prior may be empty (first step, or a non-reasoning endpoint)
	// — an empty prior means "carry nothing", identical to plain CallTools.
	CallToolsReasoning(ctx context.Context, system, prompt string, tools []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error)
}

// ReasoningEnabledFromEnv reports whether reasoning-model support is enabled via
// AISOTROPY_REASONING. Recognized truthy values: "1", "true", "on", "yes"
// (case-insensitive). Anything else — including unset — is OFF (the default).
func ReasoningEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(reasoningEnvVar))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// ReasoningEffortFromEnv reads the configured reasoning effort from
// AISOTROPY_REASONING_EFFORT, normalizing to one of the four levels. Unset or
// unrecognized defaults to ReasoningEffortMedium.
func ReasoningEffortFromEnv() string {
	return normalizeReasoningEffort(os.Getenv(reasoningEffortEnvVar))
}

// normalizeReasoningEffort maps an arbitrary string to a valid effort level,
// defaulting to medium.
func normalizeReasoningEffort(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ReasoningEffortHigh:
		return ReasoningEffortHigh
	case ReasoningEffortLow:
		return ReasoningEffortLow
	case ReasoningEffortOff:
		return ReasoningEffortOff
	default:
		return ReasoningEffortMedium
	}
}

// reasoningEffortRung is the ordered ladder used to step effort DOWN under
// pressure: index 0 is the highest, the last is off.
var reasoningEffortRung = []string{
	ReasoningEffortHigh,
	ReasoningEffortMedium,
	ReasoningEffortLow,
	ReasoningEffortOff,
}

// Reasoning-effort ladder thresholds (ADR-0061 Piece 3). Reasoning is the most
// expensive and most compressible lever, so it is shed FIRST — before the
// cheap-model DegradeAt (DefaultDegradeThreshold = 0.95) fires.
const (
	// reasoningEffortStepThreshold: at/above this pressure the configured effort
	// is stepped down ONE rung (high→medium→low→off).
	reasoningEffortStepThreshold = 0.80
	// reasoningEffortOffThreshold: at/above this pressure reasoning is off
	// entirely — coinciding with DefaultDegradeThreshold, where DegradeAt also
	// routes to the cheap model.
	reasoningEffortOffThreshold = DefaultDegradeThreshold
)

// ReasoningEffortForPressure returns the reasoning effort to use for the next
// call given the configured effort and the tenant's current quota pressure
// (memory.Limiter.Pressure, a fraction in [0,1] where 1.0 = bucket exhausted).
//
// Ladder (ADR-0061 Piece 3):
//
//	pressure < 0.80        -> the configured effort, unchanged
//	0.80 <= pressure < 0.95 -> the configured effort stepped down ONE rung
//	pressure >= 0.95        -> off (reasoning shed; DegradeAt drops to cheap too)
//
// It never steps UP: "off" and "low" stay put or go lower, never higher. This
// is a pure function (no I/O, no state), mirroring DegradeAt, so callers wire it
// in with a pressure value and it is trivially testable.
func ReasoningEffortForPressure(configured string, pressure float64) string {
	configured = normalizeReasoningEffort(configured)
	if pressure >= reasoningEffortOffThreshold {
		return ReasoningEffortOff
	}
	if pressure < reasoningEffortStepThreshold {
		return configured
	}
	return stepDownReasoningEffort(configured)
}

// stepDownReasoningEffort returns the next-lower rung on the effort ladder, or
// the same level if already at the bottom (off).
func stepDownReasoningEffort(effort string) string {
	for i, level := range reasoningEffortRung {
		if level == effort {
			if i+1 < len(reasoningEffortRung) {
				return reasoningEffortRung[i+1]
			}
			return ReasoningEffortOff
		}
	}
	return effort
}
