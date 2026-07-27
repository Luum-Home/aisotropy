package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"regexp"
	"strings"

	"github.com/firebase/genkit/go/ai"
)

// ---- ARENA_STUB toggle ----
//
// ARENA_STUB=1 swaps every piece of real infrastructure this adapter talks
// to for a deterministic, in-process fake so EVERY mode runs fully offline
// (no OPENAI_API_KEY/OPENAI_BASE_URL, no EMBED_URL, no QDRANT_URL, no
// RQLITE_URL — no network call of any kind):
//
//   - chat model: a locally genkit.DefineModel'd ECHO model (stubModelFunc
//     below), registered with the SAME name (OPENAI_MODEL, default
//     "agnes-2.0-flash") the real oai.DefineModel would have used, so every
//     mode's ai.WithModel(rc.model) call is unchanged — see main.go's run().
//   - embeddings (embed_client.go's embed): stubEmbedding — a deterministic
//     384-dim pseudo-vector seeded from an FNV hash of the input text (same
//     input -> same vector, every run, no HTTP call).
//   - vector search (qdrant_client.go's searchQdrant): stubQdrantSearch — a
//     small canned, in-process "corpus" of Nimbus-Rail-themed docs (the same
//     corpus theme the real arena_rag collection uses), NOT a real semantic
//     search over stubEmbedding's vectors (those are pseudo-random, not
//     semantically meaningful) — this is an honest limitation, not a
//     simulation of relevance ranking.
//   - durable memory (rqlite_client.go's rqliteClient): backed by
//     package-level in-memory maps instead of real HTTP calls to rqlite —
//     see rqlite_client.go's "ARENA_STUB in-memory backing" comment for the
//     documented cross-process-persistence limitation this implies.
//
// stubMode is read once at process start (an env var does not change during
// a single invocation) and consulted by every one of the fakes above.
var stubMode = os.Getenv("ARENA_STUB") == "1"

// stubModelMode: the MODEL is a fake, but the stores may still be real.
//
// stubMode substitutes the model AND every store at once, which makes it
// structurally blind to a broken store connection — precisely where the defects
// that invalidated the 2026-07-27 board lived (no Qdrant credential, an orphaned
// rqlite Raft volume, credentials the HTTP client silently drops). The
// integration lane (ARENA_STUB_MODEL=1) keeps the stores REAL and mocks only the
// model, so the infrastructure is verified at zero model cost — and therefore
// independently of which model happens to be configured.
//
// Full stub implies a stubbed model; the reverse is deliberately NOT true. Use
// this for anything model-shaped (the model definition, the reported model id,
// the credential requirement) and plain stubMode for anything store-shaped (the
// embedder, rqlite).
var stubModelMode = stubMode || os.Getenv("ARENA_STUB_MODEL") == "1"

// stubModelSupports mirrors compat_oai.BasicText (Multiturn/Tools/SystemRole,
// no native Media) — the stub never receives real media, and setting
// Constrained=false (the zero value) means Genkit falls back to
// instructions-in-prompt for ai.WithOutputType instead of requesting native
// constrained decoding, which is exactly what stubModelFunc below is built
// to satisfy (it reads req.Output.Schema itself and emits matching JSON).
var stubModelSupports = &ai.ModelSupports{
	Multiturn:  true,
	Tools:      true,
	SystemRole: true,
}

// stubModelFunc is the ECHO model function passed to genkit.DefineModel in
// stub mode (main.go's run()). It is a genuine ai.ModelFunc — same signature
// a real provider plugin implements — so every mode's genkit.Generate /
// ai.WithMiddleware / ai.WithStreaming / ai.WithOutputType call path runs
// completely unchanged; only what answers the request differs.
//
// Behavior, in priority order:
//  1. If the request carries a JSON-Schema output config (ai.WithOutputType,
//     e.g. "structured" mode), emit JSON matching that schema
//     (stubJSONForSchema) instead of an echo — Genkit's own format handler
//     (ai/format_json.go) parses and validates it exactly as it would a real
//     model's structured reply.
//  2. Else if the request offers tools AND the conversation has not already
//     received a tool response for this turn, emit a single native
//     ToolRequest for the first offered tool with synthesized arguments
//     (stubToolArgs) — this drives Genkit's REAL tool-execution loop
//     (ai/generate.go's handleToolRequests) exactly like a real model
//     deciding to call a tool, so "tools"/"observability" modes exercise
//     the same code path offline.
//  3. Else, echo: "[stub-echo] " + the last user message text, prefixed with
//     the ARENA_MW sentinel note if middleware already appended it (so
//     "middleware" mode's inbound transform is visibly reflected in the
//     reply, proving the model itself received the mutated request).
func stubModelFunc(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	lastUser := lastRequestUserText(req)

	// req.Output.Schema is the NATIVE-constrained-decoding schema, which
	// Genkit's own GenerateWithRequest (ai/generate.go) deliberately clears
	// to nil whenever native constraining is unavailable (the stub model's
	// ModelSupports.Constrained is the zero value, so it always is) — the
	// schema is instead embedded as prompt instructions (a message Part with
	// Metadata["purpose"]=="output", ai/formatter.go's injectInstructions).
	// requestOutputSchema below checks both, so the stub honors
	// ai.WithOutputType regardless of which path a given model/config takes.
	schema := requestOutputSchema(req)

	var reply string
	switch {
	case len(schema) > 0:
		reply = stubJSONForSchema(schema)
	case len(req.Tools) > 0 && !requestHasToolResponse(req):
		part, err := stubToolRequestPart(req.Tools[0], lastUser)
		if err != nil {
			return nil, fmt.Errorf("stub model: build tool request: %w", err)
		}
		msg := &ai.Message{Role: ai.RoleModel, Content: []*ai.Part{part}}
		if cb != nil {
			if err := cb(ctx, &ai.ModelResponseChunk{Role: ai.RoleModel, Index: 0, Content: []*ai.Part{part}}); err != nil {
				return nil, err
			}
		}
		return &ai.ModelResponse{
			Message:      msg,
			FinishReason: ai.FinishReasonStop,
			Usage:        approxUsage(lastUser, ""),
		}, nil
	default:
		reply = "[stub-echo] " + lastUser
	}

	msg := &ai.Message{Role: ai.RoleModel, Content: []*ai.Part{ai.NewTextPart(reply)}}
	if cb != nil {
		if err := cb(ctx, &ai.ModelResponseChunk{Role: ai.RoleModel, Index: 0, Content: []*ai.Part{ai.NewTextPart(reply)}}); err != nil {
			return nil, err
		}
	}
	return &ai.ModelResponse{
		Message:      msg,
		FinishReason: ai.FinishReasonStop,
		Usage:        approxUsage(lastUser, reply),
	}, nil
}

// lastRequestUserText returns the concatenated text of the final user-role
// message in the request, or "" if there is none.
func lastRequestUserText(req *ai.ModelRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != ai.RoleUser {
			continue
		}
		var b strings.Builder
		for _, p := range m.Content {
			if p.IsText() {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// requestOutputSchema returns the JSON Schema ai.WithOutputType asked for,
// checking both the places it can show up: the native-constrained-decoding
// config (req.Output.Schema, populated only when the model advertises
// ModelSupports.Constrained) and the prompt-instructions fallback every
// other model (including this stub) actually gets — a message Part with
// Metadata["purpose"]=="output" whose text embeds the schema as
// `Output should be in JSON format and conform to the following schema:
//
//	```<json>```
//
// ` (ai/formatter.go's jsonFormatter.Handler). Returns nil if neither is
// present (an unstructured request).
func requestOutputSchema(req *ai.ModelRequest) map[string]any {
	if req.Output != nil && len(req.Output.Schema) > 0 {
		return req.Output.Schema
	}
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if !p.IsText() || p.Metadata == nil || p.Metadata["purpose"] != "output" {
				continue
			}
			start := strings.Index(p.Text, "```")
			if start == -1 {
				continue
			}
			rest := p.Text[start+3:]
			end := strings.Index(rest, "```")
			if end == -1 {
				continue
			}
			var schema map[string]any
			if err := json.Unmarshal([]byte(rest[:end]), &schema); err == nil {
				return schema
			}
		}
	}
	return nil
}

// requestHasToolResponse reports whether any message in the request already
// carries a tool-response part — i.e. this is a SECOND (or later) iteration
// of Genkit's tool loop, after a tool already ran, so the stub should
// finalize with text instead of requesting another tool call.
func requestHasToolResponse(req *ai.ModelRequest) bool {
	for _, m := range req.Messages {
		if m.Role != ai.RoleTool {
			continue
		}
		for _, p := range m.Content {
			if p.IsToolResponse() {
				return true
			}
		}
	}
	return false
}

// stubToolRequestPart builds a native ToolRequest part for the given tool
// definition, synthesizing arguments generically from its InputSchema so it
// works for any caller-supplied tool shape, not just the two this adapter's
// own tools.go recognizes by name.
func stubToolRequestPart(def *ai.ToolDefinition, userText string) (*ai.Part, error) {
	args := stubArgsForSchema(def.InputSchema, userText)
	return ai.NewToolRequestPart(&ai.ToolRequest{Name: def.Name, Input: args}), nil
}

// stubArgsForSchema fills a JSON-Schema "object" shape with deterministic
// placeholder values good enough to drive a real tool body (e.g. tools.go's
// calculator/corpus_lookup/clock-fallback dispatch, which matches on
// argument key names and types, not their exact values). userText is the
// current user question, used to derive a real arithmetic expression for an
// expression-shaped argument (see stubPlaceholderFor).
func stubArgsForSchema(schema map[string]any, userText string) map[string]any {
	out := map[string]any{}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		propSchema, _ := raw.(map[string]any)
		out[name] = stubPlaceholderFor(name, propSchema, userText)
	}
	return out
}

// stubPlaceholderFor returns a schema-appropriate placeholder value. Field
// names containing "expr"/"expression"/"equation" get the arithmetic
// expression DERIVED from the actual user question (deriveArithmetic), so a
// calculator-shaped tool returns the CORRECT number offline (e.g. "What is 47
// multiplied by 89?" -> "47*89" -> 4183) instead of a fixed constant — the same
// higher bar the reference aisotropy stub holds itself to
// (arenas/live/aisotropy/tool_stub.go's deriveExpression). Everything else gets
// a generic placeholder per JSON-Schema type.
func stubPlaceholderFor(name string, schema map[string]any, userText string) any {
	lname := strings.ToLower(name)
	t, _ := schema["type"].(string)
	switch {
	case strings.Contains(lname, "expr") || strings.Contains(lname, "equation"):
		return deriveArithmetic(userText)
	case t == "number" || t == "integer":
		return 1
	case t == "boolean":
		return true
	case t == "array":
		return []any{}
	case t == "object":
		return map[string]any{}
	default:
		return "arena-stub"
	}
}

// stubWordArithmeticOps maps the English arithmetic phrases the arena's built-in
// arithmeticTasks use (arenas/live/runner/tasks.go: "multiplied by", "plus",
// ...) to their symbols, longest/most-specific first so "multiplied by" is
// consumed whole before a shorter match could interfere. Parallels the
// reference aisotropy stub's wordOperators (arenas/live/aisotropy/tool_stub.go).
var stubWordArithmeticOps = []struct {
	re  *regexp.Regexp
	sym string
}{
	{regexp.MustCompile(`(?i)multiplied\s+by`), "*"},
	{regexp.MustCompile(`(?i)divided\s+by`), "/"},
	{regexp.MustCompile(`(?i)\btimes\b`), "*"},
	{regexp.MustCompile(`(?i)\bplus\b`), "+"},
	{regexp.MustCompile(`(?i)\bminus\b`), "-"},
}

// stubArithmeticTokenRe pulls the arithmetic-shaped tokens (numbers, parens,
// + - * /) out of arbitrary surrounding prose, after the word operators above
// have been turned into their symbols.
var stubArithmeticTokenRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?|[()+\-*/]`)

// deriveArithmetic extracts a genuinely-evaluable arithmetic expression from a
// natural-language arithmetic question (e.g. "What is 47 multiplied by 89?" ->
// "47*89"), so the stub-driven calculate tool computes the CORRECT answer
// offline. Falls back to a fixed "2 + 2" when the text carries no evaluable
// arithmetic (a non-arithmetic tool shape hitting an expression-named arg), so
// the tool is never handed empty/malformed input. Validated with the same
// evalArithmetic the tool body uses (tools.go). Mirrors
// arenas/live/aisotropy/tool_stub.go's deriveExpression.
func deriveArithmetic(text string) string {
	for _, w := range stubWordArithmeticOps {
		text = w.re.ReplaceAllString(text, " "+w.sym+" ")
	}
	toks := stubArithmeticTokenRe.FindAllString(text, -1)
	if len(toks) == 0 {
		return "2 + 2"
	}
	expr := strings.Join(toks, "")
	if _, err := evalArithmetic(expr); err != nil {
		return "2 + 2"
	}
	return expr
}

// stubJSONForSchema renders a minimal, schema-conforming JSON document for
// req.Output.Schema (ai.WithOutputType) by walking "properties" and filling
// each field with a type-appropriate placeholder — generic, not special-
// cased to structuredAnswer (structured.go), so any struct callers pass to
// ai.WithOutputType round-trips through the stub without a schema-violation
// error from Genkit's own JSON-format handler (ai/format_json.go).
func stubJSONForSchema(schema map[string]any) string {
	val := stubValueForSchema(schema)
	b, err := json.Marshal(val)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func stubValueForSchema(schema map[string]any) any {
	if schema == nil {
		return map[string]any{}
	}
	t, _ := schema["type"].(string)
	switch t {
	case "object", "":
		out := map[string]any{}
		props, _ := schema["properties"].(map[string]any)
		for name, raw := range props {
			propSchema, _ := raw.(map[string]any)
			out[name] = stubValueForNamedSchema(name, propSchema)
		}
		return out
	case "array":
		items, _ := schema["items"].(map[string]any)
		return []any{stubValueForSchema(items)}
	case "string":
		return "stub"
	case "number":
		return 0.5
	case "integer":
		return 1
	case "boolean":
		return true
	default:
		return "stub"
	}
}

// stubValueForNamedSchema special-cases a couple of common field-name
// conventions (a "confidence"-named number reads more plausibly in [0,1];
// nothing behavioral depends on this beyond readability of the stub output).
func stubValueForNamedSchema(name string, schema map[string]any) any {
	t, _ := schema["type"].(string)
	if strings.Contains(strings.ToLower(name), "confidence") && (t == "number" || t == "integer") {
		return 0.5
	}
	return stubValueForSchema(schema)
}

// approxUsage reports a deterministic, non-negative token estimate (whole
// words in±out) so applyUsage (main.go) has real, non-"-1" numbers to
// report even offline — clearly an approximation, not provider-reported
// usage, which is the only thing arenas/live/SPEC.md's contract rule
// requires when the model is real.
func approxUsage(in, out string) *ai.GenerationUsage {
	return &ai.GenerationUsage{
		InputTokens:  len(strings.Fields(in)),
		OutputTokens: len(strings.Fields(out)),
	}
}

// ---- stub embeddings (embed_client.go's embed) ----

// stubEmbedding returns a deterministic 384-dim unit-ish vector seeded from
// an FNV-1a hash of text, matching the real e5 server's dimensionality so
// callers that assume a fixed vector length (none currently do) still work.
// It is NOT a semantic embedding — same input always yields the same
// output, but unrelated inputs do not cluster meaningfully. See
// stubQdrantSearch for the corresponding honest limitation on the search
// side.
func stubEmbedding(text string) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	src := rand.New(rand.NewSource(int64(h.Sum64())))
	const dim = 384
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = src.Float32()*2 - 1
	}
	return vec
}

// ---- stub vector search (qdrant_client.go's searchQdrant) ----

// stubCorpus is a small, canned, in-process stand-in for the real Qdrant
// "arena_rag" collection (arenas/live/_shared/populate-qdrant.py's Nimbus
// Rail corpus) — same theme, far fewer docs, fixed content and scores. It
// exists so "rag"/"tools"(corpus_lookup)/"observability" modes have
// SOMETHING deterministic to retrieve offline; it does not rank by actual
// similarity to the query (stubEmbedding's vectors are pseudo-random, so a
// real cosine ranking over them would be meaningless theater, not a fake
// search) — every call returns the same docs in the same order.
var stubCorpus = []retrievedDoc{
	{DocID: "stub-01-vireo-t7.md", Title: "Vireo T7 Trainset (stub)", Text: "The Vireo T7 has a service top speed of 400 km/h and seats 480 passengers.", Score: 0.91},
	{DocID: "stub-02-blue-guarantee.md", Title: "Blue Guarantee (stub)", Text: "Nimbus Rail issues an automatic full refund if a trainset arrives more than 10 minutes late.", Score: 0.87},
	{DocID: "stub-03-lumen-membership.md", Title: "Lumen Membership (stub)", Text: "Lumen loyalty membership grants priority boarding and lounge access; pricing is not published in this corpus.", Score: 0.72},
}

func stubQdrantSearch(limit int) []retrievedDoc {
	if limit <= 0 || limit > len(stubCorpus) {
		limit = len(stubCorpus)
	}
	out := make([]retrievedDoc, limit)
	copy(out, stubCorpus[:limit])
	return out
}
