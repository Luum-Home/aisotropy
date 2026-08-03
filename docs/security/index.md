---
type: Section Index
title: Security
description: Aisotropy's security surface — AI-layer guardrails, OWASP LLM/agentic coverage, guardrail i18n hardening, and CVE remediation.
tags: [security, guardrails, owasp, cve]
timestamp: 2026-08-03T00:00:00Z
---

# Security

| Document | What it covers |
|---|---|
| [AI-layer guardrails](ai-layer-guardrails.md) | Stored prompt-injection defense + guardrail defaults at the AI layer. |
| [OWASP Top 10 for LLM Applications](owasp-llm-coverage.md) | Aisotropy's deterministic coverage of the OWASP Top 10 for LLM Applications. |
| [OWASP Top 10 for Agentic Applications](owasp-agentic-coverage.md) | Aisotropy's deterministic coverage of the OWASP Top 10 for Agentic Applications. |
| [Guardrails i18n normalization](guardrails-i18n-normalization.md) | R4 hardening — Unicode normalization + homoglyph/leet/zero-width folding before injection & toxicity matching, and the pluggable Detector seam. |
| [CVE remediation — July 2026](cve-remediation-2026-07.md) | The three reachable Go stdlib advisories (GO-2026-5037/5039/5856) surfaced by the govulncheck pre-push gate and the toolchain-pin remediation. |
