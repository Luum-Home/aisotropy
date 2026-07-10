package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// --- Strategy A: aisotropy "surgical context" -----------------------------
//
// This drives the REAL internal/memory.WorkingMemory (bounded active window
// + Summarizer condensation) — not a re-implementation — because the whole
// point of this benchmark is to measure the actual code path, not an
// idealized model of it. See README.md "What we replicate vs. import".
//
// We use memory.NoopSummarizer (deterministic, no LLM call) rather than
// memory.LLMSummarizer, because there is no API key in this environment and
// because a real LLM call would make the benchmark non-deterministic and
// non-reproducible offline. NoopSummarizer's condensation is a fixed,
// mechanical text transform (see internal/memory/summarizer.go), so it is an
// honest stand-in for "some condensation happened" while keeping the
// benchmark hermetic.
//
// Important, measured consequence of this choice: NoopSummarizer APPENDS a
// new condensed snippet onto the existing summary every condensation round
// rather than re-compressing to a compact, fixed-size paragraph the way a
// real LLM summarizer is expected to. So Strategy A's system prompt does NOT
// stay flat-sized as the conversation grows — its summary block grows
// roughly linearly too (see TestNoopSummarizerSummaryGrowsUnbounded in
// bench_test.go), just with a much smaller slope than Strategy B's unbounded
// full-history resend. This directly affects the headline reduction
// percentages this benchmark reports and is called out explicitly in
// README.md "Limitations" — it is not a hidden assumption.

// baseSystemPrompt reproduces the literal string that
// internal/api/chat_stream.go's systemPromptFor("") returns. That function
// is unexported, so it cannot be imported and called directly; we mirror its
// output here instead of re-deriving the whole hydrateSystemPrompt algorithm
// from scratch. See README.md "What we replicate vs. import".
const baseSystemPrompt = "You are the Aisotropy agent. Respond briefly and directly."

// roleUser is the memory.Message.Role value for a user turn, shared by every
// strategy that appends conversation turns into a real or simulated
// transcript.
const roleUser = "user"

// recentMessagesWindow mirrors the unexported constant of the same name in
// internal/api/chat_stream.go (value 12 as of this writing — see
// internal/api/chat_stream.go:32). It bounds how many recent active-buffer
// messages are folded into the system prompt alongside the condensed
// summary.
const recentMessagesWindow = 12

// benchKey is the fixed memory.Key used for the single synthetic session
// this benchmark drives through Strategy A. Tenant isolation semantics are
// irrelevant here — there is only ever one session — so a fixed key is
// sufficient and keeps the benchmark deterministic.
var benchKey = memory.Key{Tenant: "bench", User: "synthetic-user", Session: "conversation-1"}

// RequestCost is the token-estimate cost of a single request (turn),
// reported both as the standalone per-request cost and as the running
// cumulative total including every prior turn.
type RequestCost struct {
	Turn       int
	PerRequest TokenEstimate
	Cumulative TokenEstimate
}

// RunStrategyA replays conv through a real, in-memory
// internal/memory.WorkingMemory exactly the way
// internal/api/chat_stream.go's RegisterStream handler does per request:
//
//  1. hydrate the system prompt from the session's condensed Summary plus
//     its Recent(recentMessagesWindow) active-buffer messages (BEFORE
//     appending the new turn's user message — matching the real handler's
//     ordering);
//  2. the "input to the LLM" for that request is that system prompt plus
//     the turn's new user message (matching
//     client.StreamChat(ctx, system, in.Body.Prompt));
//  3. append the user message, then the assistant's reply, to working
//     memory — exactly as the real handler does once a stream completes.
//
// After each Append, this calls wm.Sync() to wait for any background
// condensation goroutine it triggered. The production handler does NOT do
// this — condensation is deliberately asynchronous there (see
// internal/memory/working_memory.go's Append doc comment) so a request is
// never blocked on it. This benchmark waits anyway, purely so its output is
// deterministic: without it, whether a given turn's Recent()/Summary() read
// observes a condensation pass that happened to finish in time would depend
// on goroutine scheduling. This is documented in README.md "Limitations" as
// a deliberate divergence from production timing (steady-state behavior,
// not exact request-by-request timing).
func RunStrategyA(ctx context.Context, conv []Turn) ([]RequestCost, error) {
	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{}) // 0 -> defaultMaxActiveMessages (12)
	if err != nil {
		return nil, fmt.Errorf("strategy A: open working memory: %w", err)
	}
	defer func() { _ = wm.Close() }()

	results := make([]RequestCost, len(conv))
	var cumulative TokenEstimate

	for i, turn := range conv {
		summary, err := wm.Summary(ctx, benchKey)
		if err != nil {
			return nil, fmt.Errorf("strategy A: turn %d: read summary: %w", i+1, err)
		}
		recent, err := wm.Recent(ctx, benchKey, recentMessagesWindow)
		if err != nil {
			return nil, fmt.Errorf("strategy A: turn %d: read recent: %w", i+1, err)
		}

		system := hydrateSystemPromptText(baseSystemPrompt, summary, recent)
		perRequest := Estimate(system).Add(Estimate(turn.User))
		cumulative = cumulative.Add(perRequest)
		results[i] = RequestCost{Turn: i + 1, PerRequest: perRequest, Cumulative: cumulative}

		if err := wm.Append(ctx, benchKey, memory.Message{Role: roleUser, Content: turn.User}); err != nil {
			return nil, fmt.Errorf("strategy A: turn %d: append user: %w", i+1, err)
		}
		wm.Sync()
		if err := wm.Append(ctx, benchKey, memory.Message{Role: "assistant", Content: turn.Assistant}); err != nil {
			return nil, fmt.Errorf("strategy A: turn %d: append assistant: %w", i+1, err)
		}
		wm.Sync()
	}
	return results, nil
}

// hydrateSystemPromptText reproduces the assembly algorithm of
// internal/api/chat_stream.go's hydrateSystemPrompt (also unexported):
// base prompt, optionally followed by the condensed summary, optionally
// followed by the recent-messages block. See README.md "What we replicate
// vs. import".
func hydrateSystemPromptText(base, summary string, recent []memory.Message) string {
	if summary == "" && len(recent) == 0 {
		return base
	}
	var sb strings.Builder
	sb.WriteString(base)
	if summary != "" {
		sb.WriteString("\n\nSummary of the conversation so far: ")
		sb.WriteString(summary)
	}
	if len(recent) > 0 {
		sb.WriteString("\n\nMost recent messages in this session:\n")
		for _, m := range recent {
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
		}
	}
	return sb.String()
}

// --- Strategy B: classic LangChain-style baseline --------------------------
//
// ConversationBufferMemory-style prompting: every prior turn is re-sent
// verbatim on every request (unbounded growth), plus a fixed per-request
// prompt-template overhead. See README.md "Strategy B template" for exactly
// which LangChain templates this approximates and why.

// langchainTemplateOverhead is the fixed per-request scaffold text
// prepended to every Strategy B request, ahead of the resent conversation
// history and the new user turn. It combines:
//
//   - the preamble of LangChain's ConversationChain default prompt
//     (langchain.chains.conversation.prompt.DEFAULT_TEMPLATE, the canonical
//     ConversationBufferMemory-style prompt this task asked us to baseline
//     against), and
//   - the Thought/Action/Action Input/Observation reasoning-format section
//     of LangChain's classic "hwchase17/react" hub prompt (the best-known
//     default ReAct agent template), MINUS its {tools}/{tool_names}
//     sections.
//
// The {tools}/{tool_names} sections are deliberately omitted: this
// benchmark's synthetic scenario has no tools on either side, and including
// a tool-listing block here (which Strategy A also does not pay for) would
// strawman Strategy B with overhead it would not actually carry in an
// apples-to-apples comparison. What remains is the reasoning-format
// scaffolding a production LangChain agent commonly carries even without
// tools registered. This is a deliberate choice toward the conservative
// (pro-B, anti-A) end of "how much overhead does a realistic LangChain
// deployment carry" — see README.md "Strategy B template" for the exact
// measured token count of this text.
const langchainTemplateOverhead = `The following is a friendly conversation between a human and an AI assistant. The assistant is talkative and provides lots of specific details from its context. If the assistant does not know the answer to a question, it truthfully says it does not know.

Use the following format when reasoning about the request:

Thought: you should always think about what to do next
Action: the action to take, if any action is needed
Action Input: the input to that action
Observation: the result of taking that action
... (this Thought/Action/Action Input/Observation sequence can repeat)
Thought: I now know the final answer
Final Answer: the final answer to the original input

Current conversation:
`

// RunStrategyB replays conv as a classic full-history chat prompt: on turn
// i, the request is langchainTemplateOverhead followed by every prior turn
// (1..i-1) re-sent verbatim as "Human: ...\nAssistant: ...\n" lines,
// followed by the new turn's "Human: {user}\nAssistant:" continuation
// prompt. This is unbounded: turn i's cost grows with the entire prior
// transcript, unlike Strategy A's fixed-size window.
func RunStrategyB(conv []Turn) []RequestCost {
	results := make([]RequestCost, len(conv))
	var history strings.Builder
	var cumulative TokenEstimate
	templateTokens := Estimate(langchainTemplateOverhead)

	for i, turn := range conv {
		newTurnText := fmt.Sprintf("Human: %s\nAssistant:", turn.User)
		perRequest := templateTokens.
			Add(Estimate(history.String())).
			Add(Estimate(newTurnText))
		cumulative = cumulative.Add(perRequest)
		results[i] = RequestCost{Turn: i + 1, PerRequest: perRequest, Cumulative: cumulative}

		// ConversationBufferMemory semantics: append this full turn verbatim
		// so the NEXT request resends it too.
		fmt.Fprintf(&history, "Human: %s\nAssistant: %s\n", turn.User, turn.Assistant)
	}
	return results
}
