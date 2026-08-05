# Spring AI Arena

> **Framework:** [Spring AI](https://github.com/spring-projects/spring-ai) — real dependencies:
> `spring-ai-client-chat` / `spring-ai-model` / `spring-ai-commons` **2.0.0** (Apache-2.0, Java/JVM).
> **Positioning:** The enterprise-JVM entrant, and the JVM's richest, fully self-hostable,
> 100%-permissive agent SDK. Spring-idiomatic `ChatClient` + advisor pipeline, Micrometer
> observability native, Spring Boot autoconfig, a `VectorStore` RAG abstraction, and the new 2.0
> self-correcting structured output. Chat memory is keyed by a single `conversationId` string that
> the developer owns — there is **no framework-enforced identity binding** on it.
> **The honest, narrow point:** isolation here is a convention the developer *constructs* (identity-
> derived `conversationId`), not a default the framework *guarantees* — which matches, not beats,
> aisotropy's isolation-by-default. On the axes Spring AI is built for (JVM ecosystem, maturity,
> self-hostable license) it is genuinely strong and aisotropy does not compete.

<!-- SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md — Spring AI graded **L1**:
     `ChatMemory.CONVERSATION_ID` is required and throws when omitted (loud, not silent), and the
     primitive is single-axis. L1 is precisely "partitions if the caller passes it correctly",
     i.e. the identity binding is the developer's to build — which is what this claim says. -->


## What is real here (read this first)

This is a **REAL integration**, not a source-only analysis. Orbit is compiled and run against the
genuine Spring AI 2.0.0 jars; every isolation and feature claim below was **executed offline with no
provider key**, using a deterministic `EchoChatModel` that implements the real
`org.springframework.ai.chat.model.ChatModel` interface — so `ChatClient`, the memory advisor, the
memory window, and the structured-output converter all run through the actual framework pipeline.

| Piece | Status |
|---|---|
| `spring-ai-client-chat` / `spring-ai-model` / `spring-ai-commons` **2.0.0** | **Real dependency** — resolved from Maven Central, pinned in `pom.xml`, 34-dep tree |
| `ChatClient` + `MessageChatMemoryAdvisor` + `MessageWindowChatMemory` + `InMemoryChatMemoryRepository` | **Real** — Orbit wires the genuine types (`orbit/Orbit.java`) |
| `ChatMemory` / `ChatMemoryRepository` API (the isolation surface) | **Real** — signatures verified with `javap` and exercised in the smoke |
| `BeanOutputConverter<Ticket>` structured output | **Real** — `getFormat()` / `getJsonSchema()` / `convert()` executed |
| A running provider model (OpenAI/Anthropic/etc.) | **NOT run** — replaced by a keyless `EchoChatModel` (real `ChatModel` impl) so the memory/advisor/converter pipeline runs offline |
| Streaming cancellation, Micrometer→OTel bridge | **Source-verified, not executed** — interfaces confirmed via `javap`; labelled as such in `FEATURES.md` |

**Why a fake model.** A real Spring AI chat turn calls out to an LLM provider (needs a key + network).
Everything that makes this arena's point — how memory is keyed, whether a naive key composition
cross-reads and how an identity-derived key closes it, how structured output and the window work —
lives in the **in-process** advisor/memory/converter layer, which runs fully offline. The
`EchoChatModel` echoes back every user message it is handed, so whatever conversation history the
memory advisor injects surfaces verbatim in the reply: a naive read of the wrong `conversationId`
prints the other tenant's secret; an identity-derived read cleanly misses. The heavier Spring Boot
starter path (`spring-ai-starter-model-openai`, autoconfig, a real provider) is out of scope for an
offline keyless run; the core is exercised directly.

## Setup

Requires **JDK 21+** and **Maven 3.9+** (built and run here on **JDK 26** + **Maven 3.9.16**). First
run needs network to fetch the Spring AI jars into `~/.m2`; offline afterward.

```bash
# from arenas/spring-ai/
mvn compile                 # resolves the real Spring AI 2.0.0 tree, compiles Orbit
```

## Run the demos (offline, no key)

```bash
./scripts/run.sh smoke      # tenant/user isolation: 2 postures    -> exit 0
./scripts/run.sh product    # 4th scope axis (product): 2 postures  -> exit 0
./scripts/run.sh features   # memory / structured output            -> exit 0, all checks pass
./scripts/run.sh strengths  # STEELMAN: what Spring AI does well     -> exit 0
./scripts/run.sh all        # all four
./scripts/license_scan.sh   # real dependency license scan          -> exit 0, 100% permissive
```

Each demo is a plain `main`. The two isolation demos are **two-sided**: Part A reproduces the
naive-convention bug (a developer key-composition mistake, true of any keyed store), Part B shows the
correct identity-derived construction that closes it. They exit `0` only when the harness is healthy
AND both postures behave as claimed (positive/honesty controls held). `strengths` steelmans the
framework — the real capabilities where Spring AI is strong and aisotropy does not compete.

## Containerized smoke (Docker) — alongside the native run

The same four demos also run **inside a container**, proving the real Spring AI 2.0.0 pipeline
compiles and runs identically in a production-shaped image. Because this arena is a JVM *library*
exercised through `main` methods (not an HTTP server), the containerized smoke is "build with Maven,
run the demos in a slim JRE, assert every one exits 0" — the offline, keyless `EchoChatModel` path —
**not** a server-boot/health-probe test.

```bash
# from arenas/spring-ai/
bash docker/run.sh          # build image -> run container (4 demos) -> assert exit 0 -> cleanup
```

- `docker/Dockerfile` — multi-stage: a **Maven build stage** (`maven:3.9-eclipse-temurin-21`, resolves
  the real Spring AI tree + compiles Orbit to `target/classes`, deps copied to `target/lib`) →
  a **slim JRE runtime stage** (`eclipse-temurin:21-jre`) whose entrypoint runs `SmokeMain`,
  `ProductIsolationMain`, `FeatureDemoMain`, `StrengthsMain` and fails the container if any exits
  non-zero. The classpath (`target/classes` + resolved jars) is exactly what `scripts/run.sh`
  assembles — no fat jar.
- `docker/run.sh` — builds the image, runs the container, reports the **image size** (~123 MB), and
  removes the image on exit. **Skips cleanly (exit 0)** when Docker is absent or the daemon is
  unreachable, so it is safe on hosts/CI without Docker.

**Footprint caveat (honest):** the runtime image is ~**123 MB** — heavier than a Go arena's
distroless/static image (single-digit MB). That is the expected **JVM footprint** (a full JRE +
the Spring AI 2.0.0 jar tree), not a defect; it is the fair cost of the mature JVM ecosystem this
arena is built on. Run-verified: `bash docker/run.sh` builds, runs, and all four demos exit `0`
inside the container.

## Isolation model — two postures, both executed at equal weight

**Spring AI gives you a keyed memory primitive; you supply the identity layer above it.** The
`conversationId` is a developer-owned string with no framework-enforced identity binding. That is the
honest, narrow point — and it cuts both ways: composed naively it cross-reads, composed correctly it
isolates by construction. The demos run *both*.

<!-- SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Spring AI = L1, single-axis
     required `conversationId`; L2 identity binding is what it does NOT reach). -->


Spring AI's memory contract (verified via `javap` against the 2.0.0 jars):

```java
interface ChatMemory {
    void add(String conversationId, List<Message> messages);
    List<Message> get(String conversationId);          // keyed lookup by a developer-owned string
    void clear(String conversationId);
}
interface ChatMemoryRepository {
    List<String> findConversationIds();                // ADMIN/maintenance surface — NOT the request path
    List<Message> findByConversationId(String id);     // keyed lookup
    void saveAll(String conversationId, List<Message> messages);
    void deleteByConversationId(String id);
}
```

**Posture A — naive convention (the developer bug).** If the app lets a caller supply or reuse the
`conversationId` directly, a wrong caller reads the wrong memory. This is the same
under-authorized-key property **any** keyed store has when no identity layer sits above it —
aisotropy's own BadgerDB / rqlite / Qdrant included. It is a developer bug, not a Spring AI defect.
`SmokeMain` Part A reproduces it (globex reusing acme's key reads acme's secret) and labels
`findConversationIds()` for what it is: an **admin/maintenance API, not a per-request read** — a
request handler should never call it with a foreign id.

**Posture B — identity-derived construction (the correct path).** A competent dev derives the
`conversationId` from the **verified principal** (never from caller input) and reads through a facade
that recomputes the key server-side and refuses a mismatched foreign id. `SmokeMain` Part B shows a
globex principal structurally unable to reach acme's memory, and a smuggled foreign key refused. This
is plain application code — the direct analogue of aisotropy's Scope, which ships it by default. The
idiomatic JVM wiring is Spring Security producing the principal and the key derived from it.

For the product axis (`ProductIsolationMain`): Part A reproduces two **key-composition bugs** — a
dropped/mis-passed product segment, and a **delimiter forgery** where `Orbit.convId(...)`'s flat,
unescaped `"|"` join lets two distinct identities serialize to the same string. That forgery is an
artifact of the **flat-key format the developer chose**, not a Spring AI flaw: Part B switches to a
structured/escaped, context-derived key and both bugs vanish (`productY` cannot read `productX`, and
no identity can forge a collision). aisotropy binds `product_id` in an unforgeable `Scope` value
object by default; here the developer owns that one-line discipline.

## What actually executed (closed loop)

Offline, JDK 26 + Maven 3.9.16, no provider key. Pasted output below is verbatim.

```
$ mvn clean compile
[INFO] BUILD SUCCESS

$ ./scripts/run.sh smoke
---- PART A: naive convention (caller-controlled conversationId) ----
[positive control] acme/alice re-reads own convo -> secret present: true
[naive cross-read] globex caller on acme's key sees secret: true  <- developer bug: any keyed store leaks under an unauthorized key
[admin API, not request path] repository.findConversationIds() lists 1 id(s) — an admin/maintenance surface, not a per-request read
[honesty control] a correctly-composed globex key does NOT see acme secret: true
---- PART B: identity-derived conversationId (correct construction) ----
[positive control] acme principal reads its own derived-key memory: true
[by construction] globex principal cannot reach acme's memory (derived key differs): true
[by construction] a smuggled foreign conversationId is refused server-side: true
RESULT: exit 0 — harness healthy, both postures behaved as claimed (real memory plane).
exit=0

$ ./scripts/run.sh product
---- PART A: naive flat key (unescaped join, caller-composed) ----
[positive control] exact (acme,alice,productX) reads its own marker: true
[honesty control] exact (acme,alice,productY) does NOT see productX marker: true
[bug 1: wrong-product substitution] productY handler using productX string reads X: true  <- dropped/mis-passed segment, a key-composition bug
[bug 2: delimiter forgery] distinct identity serializes to productX's key: true  <- artifact of a flat UNESCAPED key format the developer chose
---- PART B: structured, escaped, identity-derived key ----
[positive control] structured (acme,alice,productX) reads its own marker: true
[by construction] productY cannot read productX (product bound from context): true
[by construction] structured key blocks delimiter forgery (no collision): true
RESULT: exit 0 — Part A reproduced the naive bugs, Part B closed them (real memory plane).
exit=0

$ ./scripts/run.sh features
[1 working memory] turn 3 sees turn-1 order id via hydration: true (stored msgs=6)
[2 window eviction] after 6 turns, maxMessages=4 kept 4 msgs (<=4): true
[3 structured output] BeanOutputConverter format+schema present: true; convert() -> Ticket{billing,2}: true
[4 cost probe] model invocations this run (bounded bot): 6 | Usage tokens live on ChatResponse.getMetadata() (source-verified)
RESULT: exit 0 — all feature checks passed on the real Spring AI API.
exit=0

$ ./scripts/run.sh strengths
[S1 fluent ChatClient + advisor] turn 2 auto-hydrated turn-1 order id: true
[S2 fluent structured output] call().entity(Ticket.class) -> Ticket{billing,2}: true
[S3 bounded window] after 6 turns kept 3 msgs (<=4): true; seeded SystemMessage retained across eviction: true
[S4 2.0 self-correcting seam] StructuredOutputChatOptions carries the JSON schema to the model layer: true (full provider-native repair loop = SOURCE)
RESULT: exit 0 — all executable strengths ran against the real Spring AI API.
exit=0

$ ./scripts/license_scan.sh
Total dependencies scanned: 34
[PASS] No strong-copyleft or source-available blocker licenses in the tree.
None — the Spring AI tree is 100% permissive (Apache-2.0 / MIT / BSD / CC0).
Result: PASS — no hard-blocker licenses; tree is fully permissive.
```

**Not executed** (source-verified only, labelled in `FEATURES.md`): a real provider turn
(`OpenAiChatModel` etc.), streaming token cancellation via Reactor `Flux` dispose, and the
Micrometer→OTel span bridge (needs `micrometer-tracing` + an exporter). The interfaces
(`StreamingChatModel.stream(Prompt) : Flux<ChatResponse>`, `ChatClientObservationConvention`,
`ChatResponseMetadata.getUsage()`) were confirmed with `javap` against the shipped jars.

## What Spring AI does genuinely well — RUN-VERIFIED via `StrengthsMain`

Isolation-by-default is the one narrow axis where aisotropy differs. On the axis Spring AI is built
for — the JVM's richest, fully self-hostable, 100%-permissive agent SDK — Spring AI is genuinely
strong and aisotropy is **not trying to compete**. Saying so plainly is what makes the rest of this
arena credible. `./scripts/run.sh strengths` (exit 0, offline, no key) exercises four of them against
the pinned 2.0.0 API and prints the observed result:

- **S1 — Fluent `ChatClient` + advisor pipeline (RAN).** `ChatClient.builder(model).defaultSystem(…)
  .defaultAdvisors(MessageChatMemoryAdvisor.builder(memory).build())` wires memory once; a multi-turn
  conversation auto-hydrates prior history. This is the ergonomic core, and it is excellent.
- **S2 — Structured output the fluent way (RAN).** `call().entity(Ticket.class)` runs the real
  `BeanOutputConverter` end-to-end: schema-into-prompt → model JSON → typed Java `record` back.
- **S3 — Bounded working memory (RAN).** `MessageWindowChatMemory` turn-aware eviction keeps the
  window at `maxMessages` and — unlike naive FIFO — retains a `SystemMessage` in the store across
  evictions (the demo seeds one and observes it survive 6 overflowing turns).
- **S4 — The NEW Spring AI 2.0 self-correcting-output seam (RAN API / SOURCE loop).**
  `StructuredOutputChatOptions` (2.0-only, confirmed on the shipped classpath) carries the JSON schema
  down to the model layer for provider-native + schema-validated repair. The demo builds the real
  options object and reads the schema back; the **full retry loop is provider-native** (needs a real
  provider — SOURCE, see `FEATURES.md` 2.2 and the [2.0 blog](https://spring.io/blog/2026/06/23/spring-ai-self-correcting-structured-output/)).
- **VectorStore RAG abstraction (SOURCE).** A first-class Spring AI strength (`QuestionAnswerAdvisor`,
  `FilterExpressionBuilder`), but it ships in the separate `spring-ai-vector-store` module not on this
  minimal chat classpath — covered source-verified in `FEATURES.md` 1.4, not executed here.

**Where aisotropy differs vs. where Spring AI is strong.** aisotropy differs on isolation-by-default
(all four scope axes bound from `context.Context` as an unforgeable `Scope`), per-tenant pre-spend
governance, and a Go single-binary footprint. Spring AI wins the **mature JVM ecosystem** (Spring
Security, Actuator, Micrometer, `@Transactional` all one dependency away), is **fully permissive and
self-hostable** with no SaaS gate anywhere, and — uniquely among the arenas — its transaction model
lets you *build* atomic consolidation the Python arenas cannot. Different tools for different jobs.

## Version note

Pinned to the current **2.0.0 GA** line (bumped from 1.1.8). The memory / advisor /
`BeanOutputConverter` API Orbit uses is unchanged across the 1.x→2.0 jump — the bump recompiled
and re-ran clean with **zero source changes** (verified: `mvn clean compile` + `./scripts/run.sh all`,
all three demos exit 0). 2.0 runs on **Spring Framework 7** and **Jackson 3.x** (core/databind), which
shrinks the resolved tree from 41 to 34 deps. The one relevant 2.0 addition is **self-correcting
structured output** (a provider-native + schema-validation retry) — noted under feature 2.2 in
`FEATURES.md`; Orbit still exercises the plain converter, so that retry loop is available but not
wired here.

## Deliverables

- [`FEATURES.md`](./FEATURES.md) — all 41 ARENA-SPEC-v2 features classified for Spring AI (ran vs source-verified).
- [`DX-NOTES.md`](./DX-NOTES.md) — honest enterprise-JVM developer-experience assessment.
- `pom.xml`, `src/main/java/orbit/*.java` — the real integration. `SmokeMain` / `ProductIsolationMain`
  are two-sided (naive-convention Part A + correct identity-derived Part B); `StrengthsMain` steelmans
  the framework; `FeatureDemoMain` exercises the memory / structured-output primitives.
- `scripts/run.sh` (`smoke|product|features|strengths|all`), `scripts/license_scan.sh` — reproducible
  run + governance scan.
- `docker/Dockerfile` + `docker/run.sh` — containerized smoke (multi-stage Maven→JRE) that runs the
  four demos inside a container and asserts exit 0; skips cleanly without Docker.

## Sources

- [Chat Memory :: Spring AI Reference](https://docs.spring.io/spring-ai/reference/api/chat-memory.html)
- [Chat Client API :: Spring AI Reference](https://docs.spring.io/spring-ai/reference/api/chatclient.html)
- [Structured Output Converter :: Spring AI Reference](https://docs.spring.io/spring-ai/reference/api/structured-output-converter.html)
- [Self-Correcting Structured Output in Spring AI 2.0](https://spring.io/blog/2026/06/23/spring-ai-self-correcting-structured-output/)
