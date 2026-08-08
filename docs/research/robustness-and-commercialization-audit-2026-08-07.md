---
type: research-report
title: "Robustness and Commercialization Audit"
description: "Static, reproducible assessment of Aisotropy's present architecture and its suitable commercialization path."
timestamp: 2026-08-07T00:00:00Z
---

# Robustness and Commercialization Audit

## Executive verdict

**Architecture and core: strong implementation foundation, not a hosted-service certification.** The repository has a serious local-first Go runtime with typed contracts, explicit tenant scoping, meaningful tests, and a deliberate security posture. It is not a production-ready shared multi-tenant SaaS as checked in today.

**Managed or dedicated pilot: conditional GO.** A paid pilot can be credible when each customer receives a dedicated deployment or a customer-owned cloud deployment, with explicit operational ownership and a small support scope.

**Shared multi-tenant SaaS: NO-GO today.** The missing reliability, control-plane, tenancy-administration, and disaster-recovery evidence is material for a self-service shared service.

This is a technical and product assessment, not legal advice. Counsel is needed before a dual-license, contributor-agreement, or trademark decision.

## Scope and evidence

The assessment is static and uses the checkout being reviewed. It does not claim a load test, penetration test, customer validation, or restore drill that has not been run. Re-run the evidence before acting on this report:

```sh
bash scripts/robustness-audit.sh
git rev-parse HEAD
go build ./...
go test ./...
go vet ./...
gofmt -l aisotropy/ cmd/ internal/
```

The script prints HEAD for commit reference plus deterministic input fingerprints. `STATIC_CONTENT_SCOPE` covers the bounded content scan, including untracked source-like files in its four runtime trees and the script itself. `GIT_INDEX` covers the index used by the tracked-file metrics through `git ls-files -s`. `GIT_TAGS` covers tag refs. In targeted mode, `TARGETED_CONTENT_SCOPE` covers the focused package sources and tests, `go.mod`, `go.sum`, the resource guard, and this script. `TARGETED_ENVIRONMENT` covers the forced offline and scratch configuration, the Go environment, and digests of the local module-proxy location and artifacts. `TARGETED_TOOLS` covers the Go executable and version, that environment digest, the focused dependency graph, and resource-guard output. The composite fingerprint identifies this bounded audit without printing local cache paths. HEAD alone does not identify a dirty checkout. The script also prints source and test counts, ADR count, tag count, Apache license digest, static signals, blockers, and informational reviews.

The script performs static checks, not semantic validation. A comment, stub type, or empty file cannot clear a recognized blocker. Every required static source must exist, be nonempty, and match the recognized shape; otherwise the script exits `2`, rather than treating an unknown source as a clean review. Default static mode intentionally exits `1` for a recognized blocker and `2` for an unknown state or because it lacks behavioral validation. Exit `0` is reserved for `--targeted` after no blockers or unknown shapes remain, the resource guard passes, and the focused tests pass. Reviews are informational and do not change the exit status. Static mode is read-only and offline. Targeted mode is opt-in: only after the resource guard passes it creates a `mktemp -d` scratch directory for `GOCACHE`, `GOMODCACHE`, and `GOTMPDIR`, removes it on exit, forces `GOTOOLCHAIN=local`, `GOVCS=off`, `GOWORK=off`, `GOFLAGS=-mod=readonly`, and `CGO_ENABLED=0`, and uses a local-file module proxy with `GOSUMDB=off` and no direct fallback. Its `go env GOMODCACHE` preflight is also run with local toolchain, disabled proxy, disabled checksum database, disabled VCS, workspace off, readonly module mode, CGO off, and Go environment files disabled. It seeds the disposable module cache only from the existing local module cache; a missing artifact exits `2` without a network request. It fingerprints the proxy by location digest and artifact digest, fingerprints the forced environment including local-toolchain and VCS-disabled settings, and compares both the re-enumerated audited-path manifest and the content hashes before and after tests. An added, removed, or modified audited path exits `2`. Targeted output uses labels and fingerprints rather than local paths or Go environment values. The current resource guard blocked targeted mode on this host, so this audit does not certify CGO-disabled test compatibility here.

The Apache license file must hash to `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30`. The exact producer is `bash scripts/robustness-audit.sh`.

At the audited checkout, the same script measured zero Git tags with `git tag --list | wc -l`. [`CONTRIBUTING.md`](../../CONTRIBUTING.md) states that there are no production users yet. The external-build reference is located with `rg -n '^# Adoption learnings|concept demo' docs/roadmap/adoption-learnings.md`; it describes a real domain application but explicitly a concept demo, not a production system. The public package is 0.x and its v1 intent waits for two to three real external consumers, stated in `rg -n '2–3 real external consumers' docs/guides/api-versioning-policy.md`. See [`docs/guides/api-versioning-policy.md`](../guides/api-versioning-policy.md).

A bounded content scan of `aisotropy`, `internal`, `cmd`, and `deploy` is reported by the script as INFO. It is not universal proof that a billing or control plane does not exist. `internal/costmeter` is a best-effort usage aggregate and explicitly not billing. This is a scope finding, not a claim that billing could not be integrated by a consumer.

## Robustness dimensions

| Dimension | Assessment | Evidence and bound |
|---|---|---|
| Architecture | Strong core | Typed public builder and sealed engine, but `startRuntime` and `Run` concentrate most composition in [`aisotropy/app.go`](../../aisotropy/app.go). |
| Correctness and concurrency | Verified single-writer contract, unverified same-session risk | The store declares `WithDetectConflicts(false)` under a single-writer-per-key contract. The current test suite does not establish a lost-write reproduction for same-session concurrent writers. See [`internal/memory/working_memory.go`](../../internal/memory/working_memory.go). |
| Security and isolation | Strong floor, incomplete hosted posture | Context-scoped identity and JWKS authentication are real. The JWKS cache retains old keys after refresh errors in [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go). A hosted deployment must configure a non-`NoAuth` authenticator and verify the must-agree bind. |
| Operations and DR | Pilot level | Helm and runbooks exist. Badger and SQLite round trips are tested, but scheduled backup, point-in-time recovery, cross-region replication, and measured service RPO and RTO are not built into the runtime. |
| Tests | Broad automated unit coverage, limited production evidence | `go test ./...`, race and manual lanes are documented. Networked stores and external providers are opt-in or manual rather than a continuous production-equivalent test lane. |
| Product maturity | Framework and hostable runtime, pre-product | The public package declares 0.x semantics in [`aisotropy/CHANGELOG.md`](../../aisotropy/CHANGELOG.md). The repository describes no production users and treats real consumer adoption as a v1 input, not a completed milestone. |

## Verified strengths

- The contract is unusually explicit. Go structs drive validation, OpenAPI, and model-facing contracts rather than hiding behavior in configuration.
- Tenant and user identity travels through context and stores validate scoped keys. The public architecture and `scopecheck` rules make accidental arbitrary scope creation harder.
- The LLM path has useful failure controls. The non-streaming route reflects structured output up to a bounded retry count and returns an error instead of an unstructured fallback. See [`internal/api/chat.go`](../../internal/api/chat.go).
- JWKS parsing pins RS256 and limits a JWKS response to 1 MiB (`rg -n 'maxJWKSResponseBytes|1 MiB' internal/auth/jwt_rs256.go`). A first fetch failure is rejected. See [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go).
- The working-memory user turn and consolidation event share a Badger transaction on the main paths. See [`internal/api/chat.go`](../../internal/api/chat.go) and [`internal/memory/working_memory.go`](../../internal/memory/working_memory.go).
- The repository includes a realistic deployment warning. The Helm chart rejects durable multi-replica use of one Badger volume, rather than presenting it as horizontally scalable. See [`deploy/helm/aisotropy/templates/_helpers.tpl`](../../deploy/helm/aisotropy/templates/_helpers.tpl).
- The licensing posture is checkable. The audit script verifies the exact Apache-2.0 license digest and the existing license gate can inspect dependency licenses.

## Classification and prioritized blockers

**BLOCKER** means the current source or an authoritative ADR directly prevents the named hosted-service claim. **RISK** means a material concern that needs a test or operational decision, but is not proven as a current defect. **CAVEAT** means an architectural tradeoff that informs deployment and product design without itself blocking a pilot.

### P0 before a shared SaaS launch

1. **BLOCKER: hosted authentication policy and must-agree proof.** The stock binary fails closed when no authenticator is configured. `NoAuth` is available only through explicit opt-in, and a non-loopback bind needs a second override. That local-development escape hatch is not a hosted deployment default. Any hosted pilot must prohibit `NoAuth`, configure real authentication, and demonstrate valid, invalid, and tenant-mismatched requests through a must-agree end-to-end test. See [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go), [`aisotropy/app.go`](../../aisotropy/app.go), and [`internal/api/authn.go`](../../internal/api/authn.go).

2. **BLOCKER: JWKS stale-cache acceptance.** `keyForKid` attempts a refresh after TTL expiry but only rejects a refresh failure when `s.keys == nil`. `refreshLocked` deliberately leaves the old key set intact on error. A still-valid token signed by a removed key can therefore be accepted for an unbounded outage of the JWKS endpoint after the configured TTL. See [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go), `keyForKid` and `refreshLocked`. Decide and test a maximum stale lifetime, key revocation behavior, outage telemetry, and an emergency deny path.

3. **BLOCKER: semantic re-ingestion is destructive before replacement succeeds.** `semantic.Ingest` calls `DeleteBySource` before embedding and before `Upsert`. An embedding or upsert failure leaves the prior document absent. See [`internal/semantic/ingest.go`](../../internal/semantic/ingest.go), `Ingest`. Use a versioned generation, write-new then atomically switch an active generation, or preserve the old generation until the new one is searchable.

4. **BLOCKER: outbox retry has no terminal poison-record policy in the audited implementation.** The transactional outbox provides atomic local enqueue and leaves an event pending after a failed sink call. The bounded source review finds no attempt field or terminal action on that path. This is not a universal absence proof, so the script labels its code evidence as static signals and asks for review if the source shape changes. The worker and recovery hooks are in [`internal/memory/outbox.go`](../../internal/memory/outbox.go). Define bounded retry, idempotency, quarantine, alerting, and a replay workflow before a hosted service promises durable memory.

5. **BLOCKER: no shared working-memory and coordination service.** ADR-0081 says, "Decided, not implemented. Zero lines of the port are written." The runtime also reports working-memory erasure as process-local. See [`docs/decisions/0081-shared-working-memory-tier-and-local-quota.md`](../decisions/0081-shared-working-memory-tier-and-local-quota.md) and [`aisotropy/erase.go`](../../aisotropy/erase.go). A shared SaaS needs an explicit consistency and failover model, not session affinity as its main correctness mechanism.

6. **BLOCKER: tenant administration is not a service authorization model.** JWT identity binding and verified tool scopes are useful, but a tenant-wide document scope does not establish roles such as tenant owner, administrator, billing operator, support operator, or auditor. See [`internal/api/authn.go`](../../internal/api/authn.go), [`internal/semantic/semantic.go`](../../internal/semantic/semantic.go), and ADR-0019. Build role and authorization policy separately from memory scope.

7. **BLOCKER: persistence failures can return a successful response.** `chatPersistUserTurn` and `chatRecordTurn` log append errors and keep the request successful. This can be an acceptable availability choice for a framework, but it conflicts with an implicit durable-chat promise. See [`internal/api/chat.go`](../../internal/api/chat.go), `chatPersistUserTurn` and `chatRecordTurn`. Expose the durability outcome, choose an explicit acknowledgment policy, and meter failures.

8. **BLOCKER: HTTP and MCP service limits need a hosted-service contract.** There are bounded JWKS responses and scoped MCP tools, but no consolidated request-body, per-tenant stream, connection, tool-call, queue, and time budget policy for a public service. The stdio MCP integration is also documented as a 0.x limitation with no in-repo end-to-end spawned-server test. See [`aisotropy/CHANGELOG.md`](../../aisotropy/CHANGELOG.md) and [`aisotropy/mcpserve.go`](../../aisotropy/mcpserve.go).

### P1 before scaling managed pilots

9. **RISK: metrics are partly dormant.** Several advanced paths are optional, manual, or documented as code-only. The reality ledger is useful for this distinction, except that its authentication row is stale: it says the stock binary opts into `NoAuth`, while the current boot behavior in [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) fails closed absent explicit opt-in and applies a non-loopback guard. For authentication, `main.go` wins. Do not sell observed reliability or performance until the signal is enabled, retained, alerted, and exercised in the chosen deployment. See [`docs/architecture/reality-ledger.md`](../architecture/reality-ledger.md).

10. **RISK: backup and restore are a foundation, not an online service capability.** The repository contains [`docs/operations/backup-restore.md`](../operations/backup-restore.md) and [`docs/operations/disaster-recovery.md`](../operations/disaster-recovery.md). Badger and SQLite round trips are tested in CI, while the runbook explicitly says there is no built-in scheduled backup, point-in-time recovery, or cross-region replication. A shared SaaS still needs recurring restore drills, a managed backup owner, and measured service objectives.

11. **RISK: supply-chain controls need an operating cadence.** `scripts/license-audit.sh` and `scripts/supply-chain-guard.sh` are good foundations. They do not replace signed release provenance, dependency update ownership, vulnerability response objectives, image scanning, and a customer-facing security disclosure process.

12. **RISK: same-session concurrency is an unverified contract, not a reproduced P0 loss.** The implementation states a single-writer-per-key invariant and recomputes condensation work to avoid stale snapshots. The audit did not find a cheap existing test that reproduces a same-session lost write. Treat this as a concurrency proof and adversarial-test requirement before broadening concurrency, not as a demonstrated current data-loss defect. See [`internal/memory/working_memory.go`](../../internal/memory/working_memory.go) and [`internal/memory/isolation_test.go`](../../internal/memory/isolation_test.go).

## Architectural caveats

**CAVEAT: concentrated composition.** `startRuntime` and `Run` form a concentrated composition root in [`aisotropy/app.go`](../../aisotropy/app.go). That keeps the integration visible, but changes there have a high blast radius.

**CAVEAT: process-global configuration.** `RuntimeConfig` materializes values with `os.Setenv` in [`aisotropy/runtimeconfig.go`](../../aisotropy/runtimeconfig.go), which is process-global state and complicates embedding more than one independently configured runtime. It is not, by itself, a hosted-SaaS blocker.

**CAVEAT: schema compilation policy.** `WithTypedTool` intentionally ignores a `SchemaFor` error and advertises a permissive shape in [`aisotropy/tools.go`](../../aisotropy/tools.go). That is a schema-compilation fail-open policy. It needs a hosted-service decision, error metric, and likely a strict production mode. It is not counted as a SaaS blocker by this audit.

The public facade has become broad because it offers the builder, hosting, task execution, graph execution, ingestion, compatibility endpoints, and tool seams. This is useful adoption surface, but every exported convenience becomes a compatibility promise while the package is still 0.x.

The reality ledger should be treated as a product boundary, not marketing copy. It distinguishes REAL from CODE-ONLY, dormant, partial, and aspirational capabilities. Use that matrix during sales qualification and contracts, except for authentication: its `NoAuth` wording is stale and [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) is the controlling evidence for current binary boot behavior. See [`docs/architecture/reality-ledger.md`](../architecture/reality-ledger.md).

## Market comparison

The comparison is directional. It describes documented surfaces, not a feature score or a procurement recommendation. Sources were accessed on 2026-08-07.

| Project or product | Documented current emphasis | Implication for Aisotropy |
|---|---|---|
| [Mastra](https://mastra.ai/docs) | TypeScript agents with workflows, memory, server, Studio, traces, metrics, evaluations, and deployment. | Aisotropy can differentiate through Go, sovereign deployment, and typed visible contracts. It should not claim a comparable managed platform until its operational plane exists. |
| [Letta](https://docs.letta.com/) | Stateful agents, persistent memory, managed cloud or self-hosted server, and shared memory blocks. | Letta makes shared state and agent coordination a product primitive. Aisotropy needs an explicit shared-memory path before competing in that category. |
| [LangGraph](https://docs.langchain.com/oss/python/langgraph/overview) and [LangSmith](https://docs.langchain.com/langsmith/evaluation) | Durable execution, persistence, human oversight, tracing, offline and online evaluation, plus managed and self-hosted operating modes. | Aisotropy has strong runtime primitives, but lacks the integrated orchestration and observable service lifecycle expected by this category. |
| [Qdrant](https://qdrant.tech/documentation/) | Vector search with collection, storage, filtering, and multitenancy documentation. | Qdrant is an infrastructure dependency option, not a substitute for tenant administration, service recovery, or application-level authorization. |
| [Temporal](https://docs.temporal.io/) | Crash-proof durable workflow execution that resumes after failures and outages. | The outbox and long-running work should borrow Temporal-like durability properties or use a durable workflow engine where customer commitments require them. |
| [Apache License FAQ](https://apache.org/foundation/license-faq.html) | Apache-2.0 is reusable and does not distinguish commercial use from other use. | An Apache runtime and a paid hosted service are compatible commercial choices. This does not answer trademark, CLA, or dual-license questions. |

## Commercial recommendation

Keep the complete core and public SDK under Apache-2.0. Validate a paid managed service through dedicated-instance or bring-your-own-cloud pilots. Do not choose open-core or closed source now.

Apache-2.0 does not prevent selling operations, security hardening, deployment automation, support, SLOs, managed upgrades, observability, or dedicated capacity. Moving the code behind a commercial boundary before paid demand is demonstrated would add licensing and community cost without solving the readiness blockers. If the future strategy includes dual licensing, CLAs, or trademark controls, involve qualified counsel first.

## 30, 60, and 90 day commercial validation plan

All thresholds below are proposals, not measured facts.

| Period | Work | Proposed validation threshold |
|---|---|---|
| Days 1 to 30 | Interview design partners, pick one regulated or sovereignty-sensitive workflow, deploy one single-tenant reference environment, add product telemetry and a durable incident log. | Proposal: five qualified conversations, two written pilot problem statements, and one customer-owned success metric. |
| Days 31 to 60 | Run one or two paid dedicated or BYOC pilots. Execute backup and restore drills, outbox failure drills, JWKS outage behavior, load bounds, and support playbooks. | Proposal: at least one paid pilot, one successful restore per pilot environment, no unresolved P0 event, and a named operator for every service dependency. |
| Days 61 to 90 | Test willingness to renew and the operational cost of service delivery. Decide whether to scale dedicated deployments, invest in a shared control plane, or stop. | Proposal: renewal intent from at least one paid pilot, gross-margin model that includes support, and evidence that a restore and incident response can meet the sold service objective. |

## Production exit criteria

A general shared SaaS should remain blocked until all of the following are demonstrated in the selected production topology:

- Bounded JWKS stale-key policy with alerts and a tested revocation path.
- Same-session concurrent write semantics backed by adversarial tests.
- Non-destructive semantic document replacement with rollback behavior.
- Outbox capacity, poison event quarantine, replay, and backlog observability.
- Shared working memory or an explicit single-writer and routing architecture that preserves session correctness under failure.
- Tenant roles and audited authorization for administration, support, billing, and data access.
- Explicit durable-write response semantics and customer-visible service health.
- Enforced HTTP, SSE, MCP, tool execution, and cost limits per tenant.
- Online metrics, alerts, on-call ownership, dependency update policy, and release provenance.
- Automated backup, restore, and disaster-recovery drills with measured objectives.
- At least two production-like consumers and a stable v1 compatibility decision for the public facade.

## Limitations and no-go items

This report is not a legal opinion, security certification, penetration test, load test, or market study. It cannot establish availability, latency, recovery time, or customer demand without operating evidence.

Do not launch a self-service shared multi-tenant SaaS from this checkout. Do not imply that the local Badger state is horizontally scalable. Do not market dormant or manual lanes as continuously proven. Do not promise durable conversational history while append failures are hidden behind a successful response. Do not switch to open-core or close the code merely to compensate for missing product validation.

## Reproducibility

The durable evidence is [`scripts/robustness-audit.sh`](../../scripts/robustness-audit.sh). Its default static mode is read-only and offline. Its opt-in targeted mode is also offline, but uses disposable local scratch while focused tests run and removes it on exit.

```sh
bash scripts/robustness-audit.sh
printf 'exit=%s\n' "$?"
bash -n scripts/robustness-audit.sh
git diff --check -- docs/research/robustness-and-commercialization-audit-2026-08-07.md scripts/robustness-audit.sh
```

The following non-mutating fixture proves that a required source cannot be silently treated as clean. It simulates truncation only for the check, then asserts exit `2`:

```sh
bash scripts/robustness-audit.sh --fixture-truncate-source internal/auth/jwt_rs256.go
test "$?" -eq 2
```

Expected result at the audited checkout is a nonzero exit. Interpret output with the printed audit fingerprint and static-signal scope, not only with HEAD. Exit `0` becomes reachable only after the recognized blockers have been remediated, the changed implementation has corresponding recognized evidence rather than an unknown source shape, and `--targeted` passes on a host accepted by the resource guard. The script cannot certify production readiness or prove a universal absence from a bounded source scan.
