---
title: "Prompt-Injection Defense — 2026 State of the Art, and the honest path for aisotropy"
type: research
date: 2026-07-17
scope: security / guardrails / prompt-injection
status: research-report (read-only; no code changed)
grounds:
  - internal/guardrails/injection.go
  - internal/guardrails/scanner.go
  - internal/guardrails/output.go
  - internal/guardrails/guardrails.go
  - internal/api/chat_stream.go (hydrateSystemPrompt / untrusted-context fence)
  - internal/api/knowledge.go (scan-on-ingest)
  - internal/tools/tools.go (scope gate)
  - internal/agentloop/agentloop.go (double-gate)
  - internal/llm/reflect.go (fail-closed structured output)
  - docs/decisions/0052-multilingual-guardrail-model.md
  - docs/decisions/0005-datafusion-go-rust-boundary.md
---

# Prompt-Injection Defense — 2026 State of the Art, and the honest path for aisotropy

## 0. The claim under audit

The fatalistic claim was: *"the guardrails are the weakest layer and will STAY superficial
because the LLM can't distinguish data from instructions."*

Verdict up front: **the claim is right about one thing and wrong about the conclusion it draws
from it.** It is true that today's LLMs cannot, at the token level, reliably separate "data" from
"instructions" — that premise is well supported. But the conclusion ("therefore guardrails stay
superficial / it's a dead end") is **false**, because it silently assumes that *guardrails = a
detector that reads text and guesses whether it's an attack*. The 2025–2026 literature is nearly
unanimous that detection is the weak lane and will stay weak — but that **architecture** (change the
system so that even a *successful* injection cannot cause a consequential action) is a different,
much stronger lane, and aisotropy is already standing in it without the operator having named it.

The rest of this report proves both halves with citations, then lays out a concrete, license-clean,
self-hostable roadmap.

---

## 1. The "data vs instructions" problem: unsolvable at the token level, defeatable at the system level

### 1.1 Why detection alone is a losing lane (the fatalism's true half)

- Microsoft's own **Spotlighting** paper — the canonical delimiting/datamarking/encoding
  work — reports that encoding can drive attack-success-rate (ASR) to ~0% on summarization/Q&A in
  the *static* setting, **but** the same evaluation notes that when 12 defenses were tested against
  *adaptive* adversaries who tuned attacks per-defense, **every single defense was bypassed at >90%
  ASR**. Delimiting/datamarking are real hardening, not a wall. ([arXiv 2403.14720](https://arxiv.org/pdf/2403.14720))
- The **LLMail-Inject** challenge (IEEE SaTML 2025, Dec 2024–Feb 2025) ran a realistic email-agent
  with layered defenses and **839 participants produced 208,095 unique attack submissions**; the
  organizers' framing is that defenses were repeatedly evaded and the dataset exists precisely
  because indirect injection is not solved. ([arXiv 2506.09956](https://arxiv.org/abs/2506.09956),
  [Microsoft MSRC winners post](https://www.microsoft.com/en-us/msrc/blog/2025/03/announcing-the-winners-of-the-adaptive-prompt-injection-challenge-llmail-inject/))
- A 2026 paper title says the quiet part out loud: **"Defenses Against Prompt Attacks Learn Surface
  Heuristics"** — trained detectors latch onto surface features and generalize poorly to novel
  phrasings. ([arXiv 2601.07185](https://arxiv.org/pdf/2601.07185))
- **"Adaptive Attacks Break Defenses Against Indirect Prompt Injection"** (2025) is the direct
  statement that static-benchmark wins evaporate under an adaptive attacker.
  ([arXiv 2503.00061](https://arxiv.org/pdf/2503.00061))

So: a regex layer (what aisotropy's `internal/guardrails/injection.go` is) or even a fine-tuned
classifier is a **speed bump**, not a gate. The operator's instinct that "this layer stays
superficial" is correct **for this layer**.

### 1.2 The design-time defenses that actually change the game (the fatalism's false half)

The 2025–2026 SoA moved the problem out of the model's head and into the *system around it*. The
principle, stated by DeepMind/ETH and by Willison's design-patterns survey, is:

> Once an LLM ingests untrusted input, it must become **impossible for that input to trigger any
> consequential action**. ([Design Patterns for Securing LLM Agents, arXiv 2506.08837](https://arxiv.org/pdf/2506.08837);
> [Willison summary](https://simonwillison.net/2025/Jun/13/prompt-injection-design-patterns/))

The concrete techniques, and — critically — whether each is **architectural** (injection can't
*act*) or **detection** (catch the payload):

| Technique | Class | What it does | Does it actually work? |
|---|---|---|---|
| **CaMeL** (DeepMind/ETH 2025) | **Architectural** | Privileged-LLM writes a plan from the *trusted* query only; Quarantined-LLM reads untrusted data with **no tool access**; a custom interpreter tracks data provenance/**taint** and enforces **capability** policies before every tool call. Untrusted bytes literally cannot reach the control flow. | **Yes, provably.** Solved **77% of AgentDojo tasks with *provable* security** (vs 84% undefended utility) in the v2 paper. ([arXiv 2503.18813](https://arxiv.org/abs/2503.18813); [Willison](https://simonwillison.net/2025/Apr/11/camel/)) |
| **Dual-LLM** (Willison 2023) | **Architectural** | Privileged LLM holds tools but never sees untrusted text; Quarantined LLM sees untrusted text but holds no tools and returns only symbolic refs (`$VAR1`) an orchestrator substitutes. Breaks the path injected instructions need to reach the actor. | **Yes**, at a utility cost (symbolic indirection). CaMeL is its generalization. ([Willison/CaMeL](https://simonwillison.net/2025/Apr/11/camel/); [dual-LLM pattern](https://github.com/nibzard/awesome-agentic-patterns/blob/main/patterns/dual-llm-pattern.md)) |
| **Plan-Then-Execute / Action-Selector / Map-Reduce / Code-Then-Execute / Context-Minimization** | **Architectural** | Six patterns that all trade agent autonomy for the guarantee that tool *feedback* can't re-route *tool selection*. Code-Then-Execute adds full taint-tracking in a sandboxed DSL (the CaMeL direction). | Work **to the degree you accept reduced autonomy** — the explicit stated tradeoff. ([arXiv 2506.08837](https://arxiv.org/pdf/2506.08837)) |
| **Spotlighting / delimiting / datamarking** (Microsoft) | Detection-adjacent (hardening) | Fence untrusted content in randomized markers / per-token marks / encoding so the model treats it as opaque data. | Helps in static settings, **bypassed under adaptive attack**. Cheap and worth doing as defense-in-depth, never as the wall. ([arXiv 2403.14720](https://arxiv.org/pdf/2403.14720)) |
| **StruQ** (structured queries) | Training-time architectural | Reserved delimiters + fine-tune the model to privilege the instruction field over the data field. | Drops **optimization-free attacks to ~0% ASR**; weaker vs optimization-based attacks. ([BAIR blog](https://bair.berkeley.edu/blog/2025/04/11/prompt-injection-defense/); [SecAlign paper](https://arxiv.org/html/2410.05451v3)) |
| **SecAlign** (preference optimization) | Training-time architectural | DPO-style preference tuning to prefer the "answer the real instruction, ignore the injection" output. | Reduces StruQ's residual ASR to **~8%**, >4x better vs optimization-based attacks, **no measured utility loss** (AlpacaEval2 WinRate held). ([SecAlign, arXiv 2410.05451](https://arxiv.org/html/2410.05451v3); [ACM CCS 2025](https://dl.acm.org/doi/10.1145/3719027.3744836)) |
| **OpenAI Instruction Hierarchy** | Training-time (model-side) | Fine-tune the model to rank system > user > tool/data and ignore lower-privilege overrides. | **+63% robustness** to injected-instruction attacks with minimal capability loss — but the authors explicitly say the model **is still vulnerable to powerful adversarial attacks**. ([arXiv 2404.13208](https://arxiv.org/html/2404.13208v1); [OpenAI](https://openai.com/index/the-instruction-hierarchy/)) |

The through-line: **detection is bypassable; capability-limiting + information-flow/taint is the
only lane with *provable* wins** (CaMeL), and it wins by making the model's confusion *irrelevant*
rather than by curing it.

---

## 2. Where aisotropy already sits (surprisingly, not zero)

The operator framed guardrails as "the weakest layer." That is true of `internal/guardrails/*`
**as a detector**. But aisotropy already implements **three of the four architectural lanes above**,
in different files, under different names. Mapping the code to the taxonomy:

### 2.1 Capability-limiting = "governance by absence" (this is the CaMeL *capability* half)
- `internal/tools/tools.go`: a tool is injected into the prompt **only** when the caller's granted
  scopes include its `RequiredScope`; `Permitted(scopes)` omits everything else entirely
  (`tools.go:41–71`). The model is never even *shown* a tool it lacks scope for.
- `internal/agentloop/agentloop.go`: a **double gate** — `SelectTool` "never returns a Decision
  whose Tool is outside the set `Registry.Permitted(scopes)` computes" (`agentloop.go:72–108`).
  Even a fully hijacked model that *names* a forbidden tool cannot get it executed.
- This is exactly CaMeL's/Willison's core claim in miniature: **a successful injection can only
  ever invoke capabilities that were already granted.** An email-exfil tool the tenant never
  granted is not "defended against" — it *does not exist* for that request.

### 2.2 Data fencing + provenance separation (the Spotlighting/dual-LLM *data* half)
- `internal/api/chat_stream.go` `hydrateSystemPrompt`: trusted persona/system instructions stay
  **above** the fence; **all** retrieved content (facts, semantic fragments, summary, recent turns)
  is assembled into a single block wrapped in `<untrusted_context>…</untrusted_context>` with a
  standing notice that the block is *data, not instructions* (`chat_stream.go:46–373`). This is
  **spotlighting-by-delimiting**, done right (trusted content never enters the fence).
- `sanitizeUntrusted` neutralizes any literal fence sentinel inside retrieved content so a crafted
  document can't smuggle a `</untrusted_context>` close-tag to break out (`chat_stream.go:77–86`).
  This closes the classic delimiter-escape that breaks naive spotlighting.
- The injection detector is run **only on the untrusted block, never on the trusted persona**
  (`chat_stream.go:354–361`) — a correct provenance decision.

### 2.3 Scan-on-ingest for stored/indirect injection (RAG hardening)
- `internal/api/knowledge.go` `scanDocumentsForInjection`: every ingested document is scanned and a
  flagged doc **rejects the whole ingest with a 400** (`knowledge.go:88–131, 211–229`) — so a
  poisoned document is caught *before* it can be embedded into tenant/shared memory. This is the
  "secure the knowledge database before use, no runtime overhead" defense the RAG-security
  literature recommends. ([Nexsage RAG hardening](https://nexsage.in/blog/indirect-prompt-injection-rag-pipeline-security/))

### 2.4 Output-constraint = fail-closed structured output (the StruQ *shape* half, at inference)
- `internal/llm/reflect.go`: the reflection loop forces structured output and re-generates on
  schema violation (`reflect.go:8–82`). Combined with Huma's struct-as-contract JSON-Schema
  enforcement, an injected instruction that tries to make the model emit free-form
  attacker-controlled text has to survive a schema gate. This is not StruQ (no fine-tune) but it is
  the *same idea* — constrain the output channel so a hijack has less room to express itself.

### 2.5 Tenant isolation the agent cannot cross (blast-radius cap)
- `context.Context`-propagated `tenant_id` (`constitutional-gates.md`, `internal/api/chat.go`)
  means even a hijacked agent operating on behalf of tenant A cannot read/write tenant B's memory —
  the isolation is enforced below the model, in every store query. Injection can misbehave *within*
  a tenant's granted scope; it cannot escalate *across* tenants.

### 2.6 The multilingual detector PoC (ADR-0052) — the one honest *detection* upgrade already staged
- `internal/guardrails/scanner.go` + `crates/guardrail-model`: an **opt-in**, env-gated
  (`AISOTROPY_GUARDRAIL_MODEL_BIN`), **fail-open**, additive mDeBERTa zero-shot-NLI worker behind
  the `InputScanner` seam, wired the ADR-0005 subprocess way (no new Go dep, tenant text stays in
  the process tree). Default model `MoritzLaurer/mDeBERTa-v3-base-mnli-xnli` is **MIT**, ~100
  languages (`docs/decisions/0052-multilingual-guardrail-model.md`). It correctly **BLOCKS** Llama
  Prompt Guard 2 on license grounds (see §3). Status: *PoC, not yet benchmarked* — the seam is
  CI-verified, the real-model multilingual accuracy is unmeasured (ADR-0052 "PoC vs Production").

**Honest scorecard.** On the 2026 defense spectrum, aisotropy has: capability-limiting ✅ (strong),
data fencing ✅ (good), provenance separation ✅, ingest scanning ✅, output-constraint ✅ (partial),
cross-tenant blast cap ✅. It is **missing**: (a) a strong self-hostable *input detector* actually
turned on, (b) *trust-tagging with graded provenance* on retrieved chunks (today it's binary
trusted/untrusted, no per-source trust levels), (c) a *dual-LLM/quarantine* step for constructing
tool arguments from untrusted content, and (d) any *taint/IFC* enforcement tying an untrusted
source to a restricted capability set (CaMeL's interpreter). It is **not** a superficial-only
posture; it is a solid architectural floor with a soft detection ceiling and no taint layer yet.

---

## 3. Detection-model landscape (2026), ranked for a sovereign, permissive-license runtime

The constitutional gate is hard: **MIT/Apache/BSD only; AGPL/SSPL/BSL/ELv2/Llama-Community are
BLOCKERS** (`.claude/rules/cos/license-policy.md`). That single rule eliminates the best-benchmarked
model on the market.

| Model / tool | Multilingual? | Self-hostable? | License | vs aisotropy's regex | FP cost |
|---|---|---|---|---|---|
| **Meta Llama Prompt Guard 2 (86M/22M)** | 86M yes (mDeBERTa base), 22M limited | Yes (weights) | ❌ **Llama 4 Community License, gated** — BSL-family field-of-use restriction = **constitutional BLOCKER** | 99.8% AUC, 97.5% jailbreak-detect @1% FPR — best in class | Low (tuned) — but license makes it unusable here. ([HF 86M card](https://huggingface.co/meta-llama/Llama-Prompt-Guard-2-86M); [PurpleLlama model card](https://github.com/meta-llama/PurpleLlama/blob/main/Llama-Prompt-Guard-2/86M/MODEL_CARD.md)) |
| **ProtectAI `deberta-v3-base-prompt-injection-v2`** | ❌ **English-only**, no jailbreak class | Yes | ✅ **Apache-2.0** | Much better than regex on English injection | Vendor warns it **false-positives on system prompts** — do not scan trusted text with it. ([HF model card](https://huggingface.co/protectai/deberta-v3-base-prompt-injection-v2)) |
| **mDeBERTa-v3 NLI** (`MoritzLaurer/mDeBERTa-v3-base-mnli-xnli`) — *aisotropy's ADR-0052 PoC* | ✅ ~100 langs (cross-lingual NLI) | Yes (candle subprocess) | ✅ **MIT** | Closes the non-English gap regex can't touch (ADR-0052) | Tunable threshold; accuracy **unbenchmarked** in-repo. ([ADR-0052](../decisions/0052-multilingual-guardrail-model.md)) |
| **NVIDIA NeMo Guardrails** | Via configured models | **Yes**, runs wherever Python runs; no vendor-hosted lock-in | ✅ **Apache-2.0** | Programmable input/dialog/retrieval/output/execution rails (Colang) — a framework, not just a classifier | Python runtime dep — diverges from aisotropy's Rust-worker/Go precedent. ([GitHub](https://github.com/NVIDIA-NeMo/Guardrails); [comparison](https://aisecurityandsafety.org/en/compare/nemo-guardrails-vs-lakera-guard/)) |
| **Lakera Guard** | Yes | SaaS **and** self-hosted, but **proprietary/enterprise** | ❌ Commercial (custom pricing) | Battle-tested injection/PII classifiers | **SaaS default sends tenant text off-box — violates the sovereignty thesis**; self-host is paid+closed. Out for a sovereign OSS runtime. ([comparison](https://aisecurityandsafety.org/en/compare/nemo-guardrails-vs-lakera-guard/)) |
| **Llama Guard / IBM Granite Guardian / Vijil** | Varies | Yes (weights) | ⚠️ Llama Guard = Llama Community (**BLOCKER**); Granite Guardian = Apache-2.0 (**OK**); Vijil = commercial | Content-safety oriented (toxicity/policy) more than injection | Verify per-model; Granite Guardian (Apache) is the license-clean option in this row. |

**Takeaways for a sovereign build:**
- The single best injection model (**Prompt Guard 2**) is **off the table on license** — ADR-0052
  already made this call correctly. Do not relitigate it without an ADR + human sign-off.
- The license-clean *English* detector is **ProtectAI v2 (Apache)**; the license-clean
  *multilingual* detector is **mDeBERTa-v3 NLI (MIT)** — already the ADR-0052 default. A production
  posture can run **both** (English specialist + multilingual generalist) behind the existing
  `Detector.WithScanner(...)` seam, which merges verdicts (max score, union of signals).
- SaaS options (**Lakera**) are architecturally incompatible with "tenant text never leaves the
  process tree" — they are excluded, not deferred.

---

## 4. Indirect / stored injection via RAG — the hardest case, and the graded-trust gap

RAG is where aisotropy is most exposed and where the literature is clearest that binary
trusted/untrusted is not enough:

- **The trust paradox** (the core finding): user queries are treated as untrusted, but retrieved
  context is *implicitly trusted*, even though both land in the same context window and the model
  has **no native mechanism to apply different rules to content of different provenance**.
  ([Witness.ai RAG security](https://witness.ai/blog/rag-security/);
  [Nexsage](https://nexsage.in/blog/indirect-prompt-injection-rag-pipeline-security/))
- **Prompt Flow Integrity (PFI)** formalizes the fix as a security property: identify untrusted
  data, enforce **least privilege on what that data can cause the model to do**, and **validate data
  flows before they cause consequential actions** — i.e., CaMeL's taint idea applied to RAG.
  ([discussed in the RAG-defense literature, 2025](https://witness.ai/blog/rag-security/))
- **Immutable provenance per chunk** (who added it, when, from what source, through what pipeline)
  so an anomalous action traces back to the injected document; and **per-source trust levels** so a
  first-party curated doc and a scraped third-party page are not equal.
  ([Nexsage](https://nexsage.in/blog/indirect-prompt-injection-rag-pipeline-security/))
- **Knowledge-DB sanitation before use** (detect/remove malicious docs at ingest, zero runtime
  overhead) — **aisotropy already does this** in `knowledge.go`. New 2026 work like **CleanBase**
  (detecting malicious docs in RAG DBs) and stateful trust-inference for agentic RAG confirm this is
  an active, tractable line. ([CleanBase, arXiv 2605.00460](https://arxiv.org/pdf/2605.00460);
  [Stateful Trust Inference, arXiv 2602.21447](https://arxiv.org/pdf/2602.21447))
- **Honest limit of detection-at-ingest:** it catches *known-shaped* payloads (regex/classifier);
  a clean semantic injection with no attack vocabulary passes ingest and is caught only by the
  fence + capability limits at *use* time. Ingest scanning raises the floor; it is not the ceiling.

The gap: aisotropy's fence is **binary** (trusted-above / untrusted-below). It does **not** carry a
per-source **trust tag** on each retrieved chunk, and it does not tie "this tool argument was
derived from an untrusted chunk" to a restricted capability set. That is the single highest-value
architectural upgrade available (see Tier 2/3).

---

## 5. Roadmap for aisotropy — tiered, concrete, license-checked

Every tier reuses the seams that already exist (`InputScanner`, `OutputCheck`, the fence, the scope
gate, the subprocess pattern), so none of this is a rewrite.

### Tier 1 — Cheap + high value (weeks; hardening the floor you already have)
1. **Turn on a license-clean input detector in production posture.** ADR-0052's mDeBERTa NLI worker
   is built and env-gated — add the warm `--serve` daemon form (as `crates/embed` did) to kill the
   per-call spawn+load cost (ADR-0052 lists this as the documented follow-on), then benchmark
   multilingual precision/recall on a cross-lingual injection corpus (the explicit "not yet
   benchmarked" gap). **Optionally** add **ProtectAI v2 (Apache)** as a *second* English-specialist
   `InputScanner` behind `Detector.WithScanner` — but per its own card, **never point it at the
   trusted persona** (system-prompt FP), only at the untrusted block. *Buys:* real coverage of the
   non-English + obfuscated injections regex misses. *Cost:* one Rust worker + weights provisioning.
   *License:* MIT (mDeBERTa) / Apache (ProtectAI) — both pass the gate.
2. **Trust-tag retrieved chunks (metadata only, no behavior change yet).** Extend the hydration
   assembly in `chat_stream.go` so each item in the untrusted block carries an inline provenance tag
   (source id + trust level: `first_party_curated` | `tenant_ingested` | `third_party`). *Buys:* the
   audit trail the RAG literature demands, and the substrate Tier 2/3 need. *Cost:* low — it's a
   string-assembly + metadata change. *License:* n/a.
3. **Tighten the fence with datamarking (not just delimiting).** Add Microsoft's per-token
   datamarking as an option inside the fence for high-risk sources. *Buys:* marginal static-ASR
   reduction, defense-in-depth. *Cost:* low. *Honest caveat:* bypassable under adaptive attack — do
   it as a layer, not a wall. ([arXiv 2403.14720](https://arxiv.org/pdf/2403.14720))

### Tier 2 — Medium (quarter; a real quarantine step for the sharp edge)
4. **Dual-LLM / quarantine for tool-argument construction from untrusted content.** When the agent
   must build a tool call whose arguments derive from an untrusted chunk, route that extraction
   through a **quarantined** model call that has **no tool access** and returns **symbolic
   references** the orchestrator substitutes — the privileged path that actually invokes the tool
   never reads the raw untrusted bytes. aisotropy already has the two halves this needs: the
   `agentloop` scope gate (the privileged actor) and the fence (the untrusted data boundary). *Buys:*
   removes the exact path indirect injection uses to reach a granted tool with attacker-chosen args.
   *Cost:* an extra model call on tool-arg paths (latency/tokens) + orchestrator plumbing. *License:*
   n/a (pattern, not a dependency). ([dual-LLM](https://simonwillison.net/2025/Apr/11/camel/),
   [design patterns](https://arxiv.org/pdf/2506.08837))
5. **Graded-trust → capability restriction.** Wire the Tier-1 trust tags into the scope gate: a tool
   call whose arguments were derived from a `third_party` chunk is denied any scope beyond a
   read-only allow-list. *Buys:* PFI-style "least privilege on what untrusted data can cause."
   *Cost:* medium — a policy layer over `Registry.Permitted`. *License:* n/a.

### Tier 3 — Architectural / hard (the CaMeL question)
6. **A CaMeL-style capability + information-flow-control (taint) layer, leveraging the existing
   scope gate.** This is *reachable* precisely because the two hardest CaMeL pieces already exist in
   spirit: (a) **capabilities** = the scope gate (`tools.go` / `agentloop.go`), and (b) a
   **structured control plane** = struct-as-contract + reflection loop (`internal/llm/reflect.go`).
   What is missing is the **taint interpreter**: a component that tracks provenance through the
   agent's plan and blocks a tool call whose arguments are tainted by an untrusted source unless a
   policy explicitly allows it. CaMeL builds this as a custom interpreter over a privileged-LLM plan
   + quarantined-LLM data extraction. *Buys:* the only lane with **provable** security (CaMeL:
   77% of AgentDojo tasks *provably* safe). *Cost:* **high** — a real interpreter/DSL, the largest
   item here, an SDD-scale change. *License:* n/a (CaMeL is a design, MIT-spirit; no restricted dep).
   ([arXiv 2503.18813](https://arxiv.org/abs/2503.18813))

**Recommended sequencing:** Tier 1 now (it's mostly already built and dark), Tier 2 next quarter
(the dual-LLM quarantine is the highest security-per-effort item after detection), Tier 3 as a
deliberate SDD initiative only if the threat model justifies a full taint interpreter.

---

## 6. The honest ceiling — what remains unsolved even with all of it

- **No defense is 100%.** CaMeL — the best architectural result — is explicitly *not* complete: it
  provably secures **77%** of AgentDojo tasks, not 100%, and trades ~7 points of utility to do it;
  the remaining tasks either can't be expressed under its constraints or leak via side channels.
  ([arXiv 2503.18813](https://arxiv.org/abs/2503.18813))
- **Adaptive attackers beat detection every time.** Spotlighting's own eval: 12 defenses, all
  bypassed >90% ASR under adaptive attack ([arXiv 2403.14720](https://arxiv.org/pdf/2403.14720));
  "Adaptive Attacks Break Defenses" ([arXiv 2503.00061](https://arxiv.org/pdf/2503.00061)); and
  detectors "learn surface heuristics" ([arXiv 2601.07185](https://arxiv.org/pdf/2601.07185)). Any
  detector aisotropy ships (regex, ProtectAI, mDeBERTa) will have false negatives on novel phrasing
  — that's why detection must stay *additive* (as ADR-0052 correctly makes it) and never the gate.
- **Model-side hierarchy is partial.** Even OpenAI's instruction-hierarchy fine-tune admits the
  model "is likely still vulnerable to powerful adversarial attacks"
  ([arXiv 2404.13208](https://arxiv.org/html/2404.13208v1)); StruQ+SecAlign gets residual ASR to
  ~8%, not 0 ([SecAlign](https://arxiv.org/html/2410.05451v3)). aisotropy doesn't control the
  hosted model's training, so it can't rely on this lane anyway.
- **The utility/security tradeoff is real and permanent.** Every architectural pattern buys safety
  by removing agent autonomy ([arXiv 2506.08837](https://arxiv.org/pdf/2506.08837)). Tier 2/3 will
  make some legitimate agent behaviors harder. That is the price of provable safety, and it should
  be a conscious product decision, not a surprise.
- **Residual risk lives *inside* the granted scope.** aisotropy's scope gate guarantees an injection
  can't invoke an *ungranted* capability — but it *can* misuse a *granted* one (e.g., if a tenant
  granted a "send summary" tool, an injection can influence *what* gets summarized). Narrowing scope
  grants and Tier-2 graded-trust are the mitigations; the risk never reaches zero.

---

## 7. Bottom line

The fatalism confused **one lane for the whole road**. "Detection guardrails stay superficial" is
correct and well-cited. "Therefore it's a dead end" is wrong, because aisotropy already stands on
the *architectural* lane — capability-limiting scope gates, a correct untrusted-context fence,
provenance-separated ingest scanning, and output-constraint — which is the same lane CaMeL uses to
get the field's only *provable* wins. The right move is not to polish the detector into a wall (it
can't be one); it's to (1) enable the license-clean detectors already built, additively; (2) add
graded trust-tags + a dual-LLM quarantine for tool-arg construction; and (3) treat a full CaMeL-style
taint interpreter as a reachable — because the scope gate and struct-contract are already here —
but high-cost architectural goal.

---

## TRUST_REPORT

TRUST_REPORT: SCORE=82 STATUS=MEDIUM EVIDENCE=6 UNCERTAINTIES=5
---
Score: 82/100

WHAT I VERIFIED:
  - Read the actual guardrail code, not docs about it: `injection.go` (regex+weights+`InputScanner`
    seam), `scanner.go` (ADR-0052 mDeBERTa subprocess, fail-open, additive), `output.go`/
    `guardrails.go` (OutputCheck seam), `chat_stream.go:46–373` (the untrusted-context fence +
    `sanitizeUntrusted` + detector-on-untrusted-only), `knowledge.go:88–131` (scan-on-ingest rejects
    the whole batch), `tools.go:41–71` + `agentloop.go:72–108` (the scope double-gate),
    `reflect.go:8–82` (fail-closed structured output). Every aisotropy claim cites a file+lines.
  - Confirmed ADR-0052's license posture in the ADR text: default mDeBERTa = MIT; Llama Prompt
    Guard 2 = explicit BLOCKER — cross-checked against Meta's own model card (Llama 4 Community,
    gated) and the license-policy gate.
  - CaMeL's AgentDojo numbers (77% provable / 84% undefended) taken from the paper + Willison, not
    memory.
  - Spotlighting's "all 12 defenses bypassed >90% under adaptive attack" and the "defenses learn
    surface heuristics" / "adaptive attacks break defenses" honest-ceiling claims each cite a paper.
  - Detection-model license/multilingual/self-host facts cite the specific HF model cards / vendor
    comparisons (ProtectAI Apache + English-only + system-prompt-FP; NeMo Apache self-host; Lakera
    SaaS/proprietary; Prompt Guard 2 gated license).
  - Report is a single new file under `docs/research/`; no code touched, nothing committed.

UNSURE ABOUT:
  - AgentDojo baseline "no-defense" ASR: the search results did not surface a clean single number
    for undefended ASR (they gave utility-under-attack drops, e.g. GPT-4 69%→50%). I cited what was
    verifiable and did not invent a baseline ASR figure.
  - Prompt Guard 2's exact license label: sources say "Llama 4 Community License, gated"; I treated
    that as a BSL-family field-of-use BLOCKER consistent with ADR-0052 and the repo gate. A lawyer,
    not this report, is the authority on whether a specific deployment qualifies.
  - Whether ProtectAI v2 is genuinely Apache-2.0 for the *weights* vs just the repo: the model card
    states Apache-2.0; I did not independently audit the weight redistribution terms.
  - Tier 3 (CaMeL taint layer) reachability is an architectural judgment, not a verified build — I
    graded it "reachable but high-cost" from the code seams present, not from a prototype.
  - I read the fence and scope-gate code but did not run the agent end-to-end against a live
    injection corpus, so the "where aisotropy sits" scorecard is code-grounded, not attack-tested.

HUMAN SHOULD CHECK:
  - Before adopting ProtectAI v2, confirm the weight license and remember its own card forbids
    scanning system prompts (FP) — only wire it to the untrusted block.
  - Any Prompt Guard 2 reconsideration must go through an ADR + explicit human license sign-off; do
    not point `AISOTROPY_GUARDRAIL_MODEL_DIR` at it.
  - Validate the Tier-1 mDeBERTa benchmark (ADR-0052's open "accuracy not yet measured" item) before
    calling multilingual detection "done."

---

**One-line honest answer to "can something real be done here, or is it a dead end?"**
Not a dead end — the detector lane is a bounded speed bump, but aisotropy already stands on the
*architectural* lane (scope-gated capabilities + untrusted-context fence + fail-closed output), which
is the same design lane that produces the field's only provable prompt-injection defenses (CaMeL), so
there is a real, incremental, license-clean path from "solid floor" to "near-provable" — it just runs
through capability/taint design, not through a better regex.
