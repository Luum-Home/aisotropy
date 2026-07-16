# Spring AI Arena

> **Framework:** [Spring AI](https://github.com/spring-projects/spring-ai) — real dependencies:
> `spring-ai-client-chat` / `spring-ai-model` / `spring-ai-commons` **1.1.8** (Apache-2.0, Java/JVM).
> **Positioning:** The enterprise-JVM entrant. Spring-idiomatic `ChatClient` + advisor pipeline,
> Micrometer observability native, Spring Boot autoconfig — but chat memory is a thin layer keyed by
> a single opaque `conversationId` string with **zero tenant/principal binding**.
> **Threat level:** Broad, production-grade generation/advisor ergonomics; isolation is entirely
> developer convention, matching (not beating) aisotropy's surviving differentiator.

## What is real here (read this first)

This is a **REAL integration**, not a source-only analysis. Orbit is compiled and run against the
genuine Spring AI 1.1.8 jars; every isolation and feature claim below was **executed offline with no
provider key**, using a deterministic `EchoChatModel` that implements the real
`org.springframework.ai.chat.model.ChatModel` interface — so `ChatClient`, the memory advisor, the
memory window, and the structured-output converter all run through the actual framework pipeline.

| Piece | Status |
|---|---|
| `spring-ai-client-chat` / `spring-ai-model` / `spring-ai-commons` **1.1.8** | **Real dependency** — resolved from Maven Central, pinned in `pom.xml`, 41-dep tree |
| `ChatClient` + `MessageChatMemoryAdvisor` + `MessageWindowChatMemory` + `InMemoryChatMemoryRepository` | **Real** — Orbit wires the genuine types (`orbit/Orbit.java`) |
| `ChatMemory` / `ChatMemoryRepository` API (the isolation surface) | **Real** — signatures verified with `javap` and exercised in the smoke |
| `BeanOutputConverter<Ticket>` structured output | **Real** — `getFormat()` / `getJsonSchema()` / `convert()` executed |
| A running provider model (OpenAI/Anthropic/etc.) | **NOT run** — replaced by a keyless `EchoChatModel` (real `ChatModel` impl) so the memory/advisor/converter pipeline runs offline |
| Streaming cancellation, Micrometer→OTel bridge | **Source-verified, not executed** — interfaces confirmed via `javap`; labelled as such in `FEATURES.md` |

**Why a fake model.** A real Spring AI chat turn calls out to an LLM provider (needs a key + network).
Everything that makes this arena's point — how memory is keyed, whether a wrong tenant/user/product
leaks, how structured output and the window work — lives in the **in-process** advisor/memory/converter
layer, which runs fully offline. The `EchoChatModel` echoes back every user message it is handed,
so whatever conversation history the memory advisor injects surfaces verbatim in the reply: a
cross-tenant read of the wrong `conversationId` prints the other tenant's secret. The heavier Spring
Boot starter path (`spring-ai-starter-model-openai`, autoconfig, a real provider) is out of scope for
an offline keyless run; the core is exercised directly.

## Setup

Requires **JDK 21+** and **Maven 3.9+** (built and run here on **JDK 26** + **Maven 3.9.9**). First
run needs network to fetch the Spring AI jars into `~/.m2`; offline afterward.

```bash
# from arenas/spring-ai/
mvn compile                 # resolves the real Spring AI 1.1.8 tree, compiles Orbit
```

## Run the demos (offline, no key)

```bash
./scripts/run.sh smoke      # tenant/user isolation smoke   -> exit 0, LEAK CONFIRMED
./scripts/run.sh product    # 4th scope axis (product)      -> exit 0, 3 leaks reproduced
./scripts/run.sh features   # memory / structured output    -> exit 0, all checks pass
./scripts/run.sh all        # all three
./scripts/license_scan.sh   # real dependency license scan  -> exit 0, 100% permissive
```

Each demo is a plain `main` implementing the arena's `leak = exit non-zero` contract: it exits `0`
only when the harness is healthy AND the expected leaks reproduced (positive/honesty controls held).

## Isolation model

**Convention — memory keyed by a single opaque `conversationId` string, bound to no principal.**

Spring AI's entire memory contract (verified via `javap` against the 1.1.8 jars) is:

```java
interface ChatMemory {
    void add(String conversationId, List<Message> messages);
    List<Message> get(String conversationId);          // <- raw string key, no tenant
    void clear(String conversationId);
}
interface ChatMemoryRepository {
    List<String> findConversationIds();                // <- NO filter: every tenant's ids
    List<Message> findByConversationId(String id);     // <- raw lookup, no tenant check
    void saveAll(String conversationId, List<Message> messages);
    void deleteByConversationId(String id);
}
```

There is no tenant, user, product, or authenticated-principal dimension anywhere. `conversationId`
is a developer-chosen string; the `MessageChatMemoryAdvisor` requires it as the
`ChatMemory.CONVERSATION_ID` advisor param and throws if it is absent — but it validates nothing
about *who* is allowed to pass a given value. The correct read and the leaky read are one argument
apart. Three structural leaks, all executed in `SmokeMain` on the real memory plane:

```java
orbit.chat(acmeKey, "hi");                              // any caller with the same string reads acme's memory
orbit.repository().findConversationIds();              // org-wide scan: returns EVERY tenant's convo ids
orbit.repository().findByConversationId(acmeKey);      // raw read, no tenant check at all
```

To scope Orbit by four axes the developer hand-concatenates them into that one string
(`Orbit.convId(tenant,user,product,session) -> "acme|alice|billing|s1"`). That flat, **unescaped**
join *is* the isolation boundary — see `ProductIsolationMain` for the product axis (wrong-product
substitution, org-wide scan, and a delimiter-injection collision where two distinct identities map
to the same key).

## What actually executed (closed loop)

Offline, JDK 26 + Maven 3.9.9, no provider key. Pasted output below is verbatim.

```
$ mvn compile
[INFO] BUILD SUCCESS

$ ./scripts/run.sh smoke
[positive control] acme/alice re-reads own convo -> secret present: true
[LEAK 1: shared conversationId] globex caller on acme's key sees secret: true
[LEAK 2: org-wide scan] findConversationIds() returns all 1 convos incl. acme's: true
[LEAK 2: raw read] findByConversationId(acmeKey) exposes secret: true
[honesty control] correctly-scoped globex key does NOT see acme secret: true
RESULT: exit 0 — harness healthy, LEAK CONFIRMED (convention-based, real memory plane).
exit=0

$ ./scripts/run.sh product
[positive control] exact (acme,alice,productX) reads its own marker: true
[honesty control] exact (acme,alice,productY) does NOT see productX marker: true
[LEAK 1: wrong-product substitution] productY handler using productX string leaks: true
[LEAK 2: org-wide scan] findConversationIds() + raw read crosses to productX: true
[LEAK 3: delimiter-injection collision] distinct identity maps to productX's key: true
RESULT: exit 0 — controls held, all 3 cross-product leaks reproduced on the real memory plane.
exit=0

$ ./scripts/run.sh features
[1 working memory] turn 3 sees turn-1 order id via hydration: true (stored msgs=6)
[2 window eviction] after 6 turns, maxMessages=4 kept 4 msgs (<=4): true
[3 structured output] BeanOutputConverter format+schema present: true; convert() -> Ticket{billing,2}: true
[4 cost probe] model invocations this run (bounded bot): 6 | Usage tokens live on ChatResponse.getMetadata() (source-verified)
RESULT: exit 0 — all feature checks passed on the real Spring AI API.
exit=0

$ ./scripts/license_scan.sh
Total dependencies scanned: 41
[PASS] No strong-copyleft or source-available blocker licenses in the tree.
None — the Spring AI tree is 100% permissive (Apache-2.0 / MIT / BSD / CC0).
Result: PASS — no hard-blocker licenses; tree is fully permissive.
```

**Not executed** (source-verified only, labelled in `FEATURES.md`): a real provider turn
(`OpenAiChatModel` etc.), streaming token cancellation via Reactor `Flux` dispose, and the
Micrometer→OTel span bridge (needs `micrometer-tracing` + an exporter). The interfaces
(`StreamingChatModel.stream(Prompt) : Flux<ChatResponse>`, `ChatClientObservationConvention`,
`ChatResponseMetadata.getUsage()`) were confirmed with `javap` against the shipped jars.

## Version note

Pinned to the mature **1.x GA line (1.1.8)**. Spring AI **2.0.0 GA** also exists; the memory /
advisor / `BeanOutputConverter` API used here is unchanged across both. The one relevant 2.0
addition is **self-correcting structured output** (a provider-native + schema-validation retry) —
noted under feature 2.2 in `FEATURES.md`, since 1.x has no repair loop.

## Deliverables

- [`FEATURES.md`](./FEATURES.md) — all 41 ARENA-SPEC-v2 features classified for Spring AI (ran vs source-verified).
- [`DX-NOTES.md`](./DX-NOTES.md) — honest enterprise-JVM developer-experience assessment.
- `pom.xml`, `src/main/java/orbit/*.java` — the real integration.
- `scripts/run.sh`, `scripts/license_scan.sh` — reproducible run + governance scan.

## Sources

- [Chat Memory :: Spring AI Reference](https://docs.spring.io/spring-ai/reference/api/chat-memory.html)
- [Chat Client API :: Spring AI Reference](https://docs.spring.io/spring-ai/reference/api/chatclient.html)
- [Structured Output Converter :: Spring AI Reference](https://docs.spring.io/spring-ai/reference/api/structured-output-converter.html)
- [Self-Correcting Structured Output in Spring AI 2.0](https://spring.io/blog/2026/06/23/spring-ai-self-correcting-structured-output/)
