---
type: Research
title: "Barrido de ausencia — 'LangGraph literally cannot do' (localidad de estado): la frase no se sostiene"
description: >-
  Dos lugares del adaptador de Cloudflare Agents afirman que LangGraph no puede tener un journal de hechos por keyword adentro del objeto de cómputo — "literally cannot do" y "cannot do at all". El barrido de cinco puntos exigido por .claude/rules/capability-absence-claims.md muestra que LangGraph SÍ corre estado embebido en el mismo proceso (SqliteSaver, SqliteStore, InMemorySaver/InMemoryStore, todos NATIVE), así que la afirmación fuerte es falsa. Lo que sí es cierto es mucho más angosto y de otra naturaleza: LangGraph no ships un índice FTS5 propio, no tiene objeto de cómputo por entidad al cual pegarle el estado, y su tier de plataforma externaliza el estado a Postgres+Redis. Incluye rigor simétrico sobre aisotropy, que en el eje de localidad de estado está más cerca de LangGraph que de Cloudflare.
tags: [architecture, arena, research, langgraph, cloudflare-agents, durable-objects, state-locality, capability-absence, code-truth]
timestamp: 2026-08-04T00:00:00Z
---

# Barrido de ausencia — "LangGraph literally cannot do" (localidad de estado)
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-langgraph-state-locality-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->

> **Por qué existe este documento.** `.claude/rules/capability-absence-claims.md` exige que toda
> afirmación de que un competidor CARECE de una capacidad pase por un barrido de cinco puntos
> (core / extensiones oficiales / plataforma / integraciones oficiales con terceros / docs del
> vendor), con URL o `file:line` por hallazgo, etiqueta de madurez y rigor simétrico sobre nosotros
> mismos. Dos frases del adaptador de Cloudflare Agents afirman algo sobre **LangGraph** —un tercero
> que no es el sujeto de esa carpeta— en la formulación más fuerte disponible del idioma. Nunca se
> les hizo el barrido. Este documento lo hace.
>
> **Aclaración de alcance:** este barrido es sobre **localidad de estado**, NO sobre tenancy.
> `docs/architecture/arena-tenancy-levels.md` ya ubicó a LangGraph OSS en **L1** de la escalera de
> aislamiento (namespace tuples de `BaseStore`, `thread_id` del checkpointer) y ese veredicto queda
> intacto — nada acá lo contradice ni lo reemplaza. Son dos ejes distintos: *quién puede leer qué*
> (tenancy) versus *dónde vive el estado respecto del cómputo* (este documento).

## 1. Qué dice exactamente la afirmación

Las dos apariciones, citadas literales de sus archivos:

**`arenas/cloudflare-agents/DX-NOTES.md:25-29`** (sección "`this.sql` is a joy, and it's a *real* SQLite"):

> Tagged-template SQL with typed rows (`this.sql<User>\`SELECT ...\``) is clean, and the DO's
> SQLite is a full SQLite — my FTS5 probe returned a live match
> (`{"ok":true,"hit":"production uses aws lambda"}`). So you can build a keyword facts journal
> *inside* the object, **which LangGraph literally cannot do**.

**`arenas/cloudflare-agents/README.md:238-240`** (sección "What Cloudflare Agents does genuinely well"):

> **A real in-process SQLite per object, with FTS5.** `this.sql` is a full SQLite embedded in
> the DO (µs, zero network); the FTS5 probe returned a live match — so a keyword facts journal
> lives *inside* the object (**something LangGraph cannot do at all**). **[RAN]**
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-langgraph-state-locality-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->

**La capacidad exacta que se afirma ausente**, descompuesta en sus tres átomos (porque la frase los
funde en uno solo y ahí está el problema):

| # | Átomo | Lo que la frase afirma sobre LangGraph |
|---|---|---|
| **A** | Estado **embebido en el proceso de cómputo** (µs, cero red) | que no puede |
| **B** | Ese estado con **búsqueda por keyword** (FTS5 / BM25) | que no puede |
| **C** | El estado adentro de **un objeto de cómputo por entidad**, direccionable por nombre | que no puede |

El `[RAN]` del README agrava el asunto: marca la línea como resultado ejecutado. Lo ejecutado fue la
probe de FTS5 **de Cloudflare**. La cláusula sobre LangGraph no corrió nada — es una aserción sobre
el producto de otro colgada de la etiqueta de evidencia de una medición propia.

## 2. El barrido de cinco puntos

### Punto 1 — Core (`langchain-ai/langgraph`)

**Verificado contra fuente primaria (código descargado y grepeado).**

- **`SqliteSaver` — checkpointer SQLite en proceso.** El constructor recibe un
  `sqlite3.Connection` desnudo; `from_conn_string` acepta `":memory:"` o un archivo local:
  `sqlite3.connect(..., check_same_thread=False)` en
  [`libs/checkpoint-sqlite/langgraph/checkpoint/sqlite/__init__.py:99-124`](https://github.com/langchain-ai/langgraph/blob/main/libs/checkpoint-sqlite/langgraph/checkpoint/sqlite/__init__.py).
  Sin proceso aparte, sin socket. **Esto es literalmente estado embebido en el proceso de cómputo.**
- **`SqliteStore` — memoria de largo plazo SQLite en proceso.** Existe
  [`libs/checkpoint-sqlite/langgraph/store/sqlite/base.py`](https://github.com/langchain-ai/langgraph/blob/main/libs/checkpoint-sqlite/langgraph/store/sqlite/base.py)
  (1514 líneas), con docstring `conn = sqlite3.connect(":memory:")` / `SqliteStore.from_conn_string(":memory:")`
  (líneas 788-816). Namespace tuples jerárquicos + búsqueda vectorial opcional (`store_vectors`,
  `vec_distance_cosine`). Mergeado por [PR #3608](https://github.com/langchain-ai/langgraph/pull/3608).
- **`InMemorySaver` / `InMemoryStore`** — estado en RAM del propio proceso, documentado en
  [Persistence](https://docs.langchain.com/oss/python/langgraph/persistence): *"MemorySaver and
  InMemorySaver store checkpoints in RAM. When the process restarts, all checkpoints are lost."*
- **Sobre el átomo B (keyword/FTS5): acá la frase tiene razón parcial.** Grep sobre el fuente de
  `SqliteStore`: **cero ocurrencias de `fts5`** (`grep -c -i fts5 base.py` → `0`). La rama de
  búsqueda no vectorial (`base.py:569-590`) es un `SELECT ... WHERE <ns_condition> AND <filtros JSON>
  ORDER BY updated_at DESC` — filtro estructurado, no ranking por relevancia de texto. **LangGraph
  no ships un journal de hechos por keyword.** Lo que ships es búsqueda semántica (embeddings).
- **Pero el átomo B no está *impedido*, está *ausente*.** El checkpointer y el store toman una
  `sqlite3.Connection` que es tuya; sobre esa misma conexión podés ejecutar SQL arbitrario. Comando
  reproducible, corrido en esta máquina:

  ```
  $ python3 -c "
  import sqlite3
  c=sqlite3.connect(':memory:')
  c.execute('CREATE VIRTUAL TABLE facts USING fts5(body)')
  c.execute(\"INSERT INTO facts(body) VALUES ('production uses aws lambda')\")
  print('FTS5 OK ->', c.execute(\"SELECT body FROM facts WHERE facts MATCH 'lambda'\").fetchall())
  print('sqlite:', sqlite3.sqlite_version)"

  FTS5 OK -> [('production uses aws lambda',)]
  sqlite: 3.53.3
  ```

  Es la misma probe que el adaptador de Cloudflare corrió contra `this.sql`, corrida contra el
  sqlite del proceso donde vive un grafo de LangGraph. Devuelve lo mismo.

**Etiqueta punto 1:** estado embebido en proceso = **NATIVE** (`langgraph-checkpoint-sqlite` 3.1.1,
release 2026-07-30, [PyPI](https://pypi.org/project/langgraph-checkpoint-sqlite/)). Journal por
keyword FTS5 propio del framework = **NONE**, pero construible en pocas líneas sobre la conexión que
el propio framework te entrega.

### Punto 2 — Extensiones oficiales

- `langgraph-checkpoint-sqlite` (Python) **3.1.1** — 2026-07-30. Contiene `checkpoint/sqlite`,
  `store/sqlite` **y** `cache/sqlite` (tres módulos hermanos verificados en el árbol del repo).
  Todos en proceso.
- `@langchain/langgraph-checkpoint-sqlite` (JS) **1.0.3** — publicado 2026-06-10. **Dependencia
  única: `better-sqlite3 ^12.10.0`** (verificado contra el registry de npm). Es un addon nativo
  N-API: **no corre en `workerd`.** Este dato es la base del único reformulado defendible (§4).
- `langmem` (MIT, first-party langchain-ai) — consolidación de memoria sobre `BaseStore`. No aporta
  FTS5.
- **No existe** un checkpointer/store oficial respaldado por Durable Objects.

**Etiqueta:** **OFFICIAL-EXTENSION** para SQLite en proceso (Python, activo). **NONE** para un
backend Durable Object.

### Punto 3 — Oferta de plataforma (LangGraph Platform / LangSmith Deployment)

Acá la afirmación **gana terreno**, y conviene decirlo porque es el único punto donde el espíritu
de la frase se acerca a algo cierto.

[Self-host standalone servers](https://docs.langchain.com/langsmith/deploy-standalone-server) exige
dos servicios de red: *"Postgres will be used to store assistants, threads, runs, persist thread
state and long term memory, and to manage the state of the background task queue"* y *"Redis will be
used as a pub-sub broker to enable streaming real time output from background runs"*. Variables
requeridas: `DATABASE_URI`, `REDIS_URI`.

**En el tier de plataforma, LangGraph externaliza el estado por diseño.** No hay opción de SQLite
embebido en el server (pedido abierto: [langgraphjs#1970](https://github.com/langchain-ai/langgraphjs/issues/1970)).

**Etiqueta:** localidad de estado en la plataforma = **NONE** (arquitectura opuesta, deliberada).

### Punto 4 — Integraciones oficiales con terceros (incluido: ¿LangGraph sobre Durable Objects?)

Este es el punto que el encargo marcó como potencialmente terminal, y el resultado es mixto.

- **LangGraph.js SÍ corre dentro de Cloudflare Workers.**
  [langgraphjs#1692](https://github.com/langchain-ai/langgraphjs/issues/1692) (abierto 2025-09-22,
  cerrado) documenta a un usuario corriendo un grafo en un Worker: el grafo compila y ejecuta; lo
  que se rompe es la escritura de fondo del checkpointer de Postgres, porque el runtime mata el
  request — *"The Workers runtime canceled this request because it detected that your Worker's code
  had hung and would never generate a response"*. Es decir: **el cómputo corre en el edge; el
  problema es el driver TCP del store externo**, exactamente el problema que un store local
  resolvería.
- **El patrón prescripto por LangChain para ese caso** es implementar un `BaseCheckpointSaver`
  propio sobre un backend HTTP (Upstash Redis en la recomendación). El mismo mecanismo permitiría
  respaldarlo con `ctx.storage.sql` de un Durable Object: `BaseCheckpointSaver` es una interfaz
  pública de cuatro métodos (`put`, `putWrites`, `getTuple`, `list`).
- **Integraciones oficiales Cloudflare↔LangChain que SÍ existen:** `@langchain/cloudflare` 1.1.0
  (npm, 2026-06-17) y `langchain-cloudflare` 0.3.6 (PyPI, 2026-07-06), más el
  [changelog de Cloudflare del 2026-07-30](https://developers.cloudflare.com/changelog/post/2026-07-30-ai-search-agent-sdks/)
  que agrega guías para Agents SDK, AI SDK y LangChain. **Ninguna de las tres es un checkpointer de
  Durable Object**: son Workers AI, Vectorize y un retriever de AI Search. Cloudflare integró
  **LangChain**, no LangGraph, y en el eje de retrieval, no en el de estado.
- **No encontré** un checkpointer de Durable Object mantenido por la comunidad. Sí existen
  community checkpointers para Couchbase, Redis, Snowflake — o sea, el ecosistema de backends
  alternativos está vivo, pero nadie publicó el de DO.

**Etiqueta:** LangGraph desplegable sobre Durable Objects = **COMMUNITY-ONLY, y ni siquiera eso**:
el camino existe y está prescripto por el vendor (`BaseCheckpointSaver` custom), pero **no hay
artefacto publicado**. El checkpointer SQLite oficial de JS queda descartado por `better-sqlite3`.

### Punto 5 — Docs del vendor (patrón prescripto)

- [Persistence](https://docs.langchain.com/oss/python/langgraph/persistence) documenta el store
  in-memory y el checkpointer SQLite como caminos de primera clase para desarrollo local y
  *"lightweight deployments"* — **patrón prescripto**, no primitiva forzada.
- [Long-term memory](https://docs.langchain.com/oss/python/langchain/long-term-memory) documenta
  `BaseStore` con namespaces jerárquicos y búsqueda semántica. **Ninguna doc del vendor prescribe un
  journal FTS5.**

## 3. Tabla de madurez

| Capacidad sobre LangGraph | Dónde | Etiqueta | Versión / último release |
|---|---|---|---|
| Checkpointer SQLite embebido en el proceso (`SqliteSaver`) | core/ext oficial | **NATIVE / OFFICIAL-EXTENSION** | `langgraph-checkpoint-sqlite` 3.1.1 — 2026-07-30 |
| Store SQLite embebido (`SqliteStore`, namespaces + vectores) | mismo paquete | **OFFICIAL-EXTENSION** | mismo, `store/sqlite/base.py` |
| Estado en RAM del proceso (`InMemorySaver`, `InMemoryStore`) | core | **NATIVE** | `langgraph` 1.2.9 (pineado en nuestro adaptador) |
| Búsqueda **semántica** sobre store local | ext oficial | **OFFICIAL-EXTENSION** | idem (`store_vectors`, sqlite-vec) |
| Journal de hechos por **keyword / FTS5 / BM25** propio del framework | — | **NONE** | `grep -c fts5 base.py` = 0 |
| Checkpointer SQLite en JS que corra en `workerd` | ext oficial | **NONE** (bloqueado) | `@langchain/langgraph-checkpoint-sqlite` 1.0.3 — 2026-06-10, dep `better-sqlite3` (nativo) |
| Backend Durable Object (checkpointer o store) | — | **NONE** oficial; **no encontrado** en comunidad | — |
| Estado local en el tier de plataforma | LangGraph Platform | **NONE** (Postgres+Redis obligatorios) | docs vigentes 2026 |
| Objeto de cómputo por entidad, direccionable, dueño de su storage | — | **NONE** | no existe la primitiva |

## 4. La distinción que decide el veredicto

Las tres verdades que la frase actual funde en una sola:

| Formulación | ¿Es cierta? | Evidencia |
|---|---|---|
| **"LangGraph no PUEDE tener el estado adentro del cómputo"** (lo que la frase dice) | **FALSA** | `SqliteSaver` sobre `sqlite3.connect(":memory:")`, `SqliteStore`, `InMemorySaver`. Y nuestro propio adaptador lo hace: `arenas/live/langgraph/modes/common.py:302-322` abre un `SqliteSaver` on-disk en proceso; `modes/memory.py:78-103` lo compila en el grafo. |
| **"LangGraph no lo hace POR DEFECTO"** | **Parcialmente falsa** | El default documentado para desarrollo ES en proceso (`InMemorySaver`, SQLite local). Lo que no trae por defecto es el **índice keyword**. |
| **"El DESPLIEGUE TÍPICO de producción de LangGraph no lo hace"** | **CIERTA** | LangGraph Platform / standalone server exige `DATABASE_URI` + `REDIS_URI`; el estado sale del proceso por diseño. |

Y la cuarta, que es la que realmente distingue a Cloudflare y que la frase **no dice**:

| Formulación | ¿Es cierta? | Evidencia |
|---|---|---|
| **"LangGraph no tiene un objeto de cómputo POR ENTIDAD que sea dueño de su propio storage"** | **CIERTA, y es el punto fuerte** | El SQLite en proceso de LangGraph es **por proceso del SO**, no por conversación; nada garantiza que el request N+1 caiga en el mismo proceso. El Durable Object es una instancia global única por nombre, ruteada por el runtime, con su SQLite pegado. Esa primitiva no existe en LangGraph. |

### Escalera de localidad de estado (SL0–SL3)

Análoga a la escalera L0–L3 de tenancy de `arena-tenancy-levels.md`, aplicada a *dónde vive el
estado respecto del cómputo*:

| Nivel | Definición |
|---|---|
| **SL0** | Estado necesariamente externo: la persistencia propia del framework requiere un servicio de red. |
| **SL1** | Estado en proceso posible: el framework ships un store embebido que corre en el mismo proceso, pero es opcional y pensado para desarrollo. |
| **SL2** | Estado en proceso como postura de producción soportada — pero **por proceso/réplica**, sin ruteo por entidad: dos réplicas no lo comparten y los requests no están pineados. |
| **SL3** | Localidad por construcción: el runtime rutea todo request de una entidad a la única instancia de cómputo que es dueña de su storage. Un `SELECT` cruzado es irrepresentable. |

| Sistema | Nivel | Evidencia |
|---|---|---|
| LangGraph OSS (biblioteca) | **SL1** | `SqliteSaver`/`SqliteStore`/`InMemorySaver` corren en proceso; las docs los enmarcan como *local development, testing, lightweight deployments*. |
| LangGraph Platform / standalone server | **SL0** | `DATABASE_URI` + `REDIS_URI` obligatorios. |
| Cloudflare Agents / Durable Objects | **SL3** | Una instancia global por nombre, SQLite embebido propio, ruteo por el runtime. |
| **aisotropy** | **SL2**, degradando a **SL0** cuando se configuran los tiers de red | ver §5 |

## 5. Rigor simétrico — dónde vive NUESTRO estado, y con qué límites

Aplico el mismo estándar, en el mismo documento, como exige la norma.

**Lo que sí tenemos, y es exactamente el átomo B que la frase reclama como diferencial de Cloudflare:**

- **Journal de hechos por keyword FTS5, embebido, en proceso.** `internal/facts/sqlite.go:11` importa
  `modernc.org/sqlite` (driver SQLite puro-Go, BSD-3, **FTS5 habilitado**); `internal/facts/facts.go:4-7`
  define el tier como *"retrieved by keyword (SQLite FTS5)"* y `facts.go:113` documenta el ranking
  **bm25 × Importance × Recency**. O sea: aisotropy tiene el journal por keyword adentro del proceso
  que LangGraph no ships y que Cloudflare habilita a mano. En ese átomo puntual la comparación nos
  favorece **más** de lo que la frase dice.
- **Working memory BadgerDB embebida.** `internal/memory` abre BadgerDB in-process
  (`OpenInMemory` por default, on-disk vía `AISOTROPY_DATA_DIR`). Cero red, cero puerto — el propio
  `.claude/rules/services-config.md` lo lista como *"n/a — embedded in-process, no port"*.

**Lo que relativiza nuestra posición, y va escrito acá porque si no el documento sería marketing:**

- **Nuestra localidad es por PROCESO, no por entidad — la misma limitación de LangGraph.**
  Un `aisotropyd` con BadgerDB embebido y SQLite embebido no comparte ese estado con otra réplica, y
  nada rutea al usuario X siempre al mismo pod. Es SL2, no SL3.
- **El chart lo dice explícito.** `deploy/helm/aisotropy/values.yaml:192-194`:
  *"aisotropyd is stateless in the minimal (in-process, no-persistence) posture, so horizontal
  scaling is safe there. With persistence.enabled the pods share ONE ReadWriteOnce PVC — **do not
  scale past 1 replica in that shape**."* `deploy/k8s/base/deployment.yaml:11` y
  `deploy/helm/aisotropy/templates/deployment.yaml:11` arrancan en `replicas: 1`.
- **Y la postura escalable nos baja a SL0.** Apenas se setean `RQLITE_URL` / `QDRANT_HOST`
  (`internal/facts/factory.go:19`, `internal/semantic/factory.go`), los tiers pasan a ser servicios
  de red — exactamente la arquitectura de LangGraph Platform que criticamos arriba. Nuestro camino a
  escalar horizontalmente **es** externalizar el estado.
- **Conclusión incómoda:** en el eje de localidad de estado, **aisotropy está más cerca de LangGraph
  que de Cloudflare**. El README ya concede *"On raw storage isolation, Cloudflare wins outright"*
  para el aislamiento; la misma concesión corresponde acá, y la frase sobre LangGraph la oscurece al
  sugerir que la brecha es LangGraph-versus-todos cuando es Durable-Objects-versus-todos, nosotros
  incluidos.

**Contra-señal que estaba en nuestro propio repo todo el tiempo** (mismo patrón que el origen de la
norma): `arenas/live/langgraph/requirements.txt:15` pinea `langgraph-checkpoint-sqlite==3.1.0` con el
comentario *"memory mode: real on-disk checkpointer persistence across processes"*, y
`arenas/live/langgraph/modes/common.py:302-322` abre ese SqliteSaver on-disk en proceso. Nuestro
propio adaptador de LangGraph venía corriendo, dimensión tras dimensión, lo que otra carpeta del
mismo repo declaraba imposible.

## 6. Veredicto

**NO SE SOSTIENE** en la formulación actual. Ni "literally cannot do" ni "cannot do at all"
sobreviven al punto 1 del barrido: LangGraph corre estado embebido en el proceso de cómputo de forma
nativa, con tres implementaciones distintas, una de las cuales usamos nosotros mismos en el arena.

Hay una afirmación **MÁS ANGOSTA** que sí se sostiene y que además dice algo más interesante: lo
distintivo del Durable Object no es "estado en proceso" (eso lo tienen LangGraph y aisotropy) sino
**estado por entidad, ruteado por el runtime, dueño de su propio SQLite** — SL3 contra SL1/SL2.

### Frase exacta de reemplazo

Para **`README.md:238-240`** (reemplaza el paréntesis, conserva el `[RAN]` sobre lo que efectivamente
corrió):

> **A real in-process SQLite per object, with FTS5.** `this.sql` is a full SQLite embedded in
> the DO (µs, zero network); the FTS5 probe returned a live match — so a keyword facts journal
> lives *inside the per-entity object*. LangGraph runs in-process SQLite too (`SqliteSaver`,
> `SqliteStore`), but its locality is per-OS-process, not per-conversation, and its server tier
> requires external Postgres+Redis — so it has no addressable object to put the journal *in*.
> **[RAN — the FTS5 probe; the LangGraph comparison is source-verified, not run]**

Para **`DX-NOTES.md:25-29`** (reemplaza la subordinada `which LangGraph literally cannot do`):
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-langgraph-state-locality-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->

> So you can build a keyword facts journal *inside the object* — and the distinctive part is the
> *object*, not the SQLite: LangGraph ships in-process SQLite as well, but per process rather than
> per conversation, and its deployed platform tier externalises state to Postgres+Redis, so there is
> no single, runtime-routed instance that owns its own database.

Ambas son verificables contra las fuentes de §2 y ninguna contradice las filas de LangGraph de
`arena-tenancy-levels.md`.

## 7. Qué no pude verificar

- **Búsqueda exhaustiva de un checkpointer de Durable Object en la comunidad.** Barrí buscadores y
  el registry; no apareció ninguno publicado. Es una afirmación de **ausencia de evidencia**, no una
  enumeración exhaustiva de GitHub — un repo poco indexado la refutaría.
- **`@langchain/langgraph-checkpoint-sqlite` bajo `workerd` no lo probé.** El bloqueo lo infiero de
  su única dependencia (`better-sqlite3`, addon nativo N-API) leída del registry de npm, más el
  hecho conocido de que Workers no carga addons nativos. No corrí el build.
- **`langgraph.store.sqlite` bajo la versión exacta pineada por nuestro adaptador.** Leí `main` del
  repo; el adaptador pinea `langgraph==1.2.9` / `langgraph-checkpoint-sqlite==3.1.0`. La ausencia de
  FTS5 la verifiqué sobre `main`, no sobre ese tag exacto.
- **El estado de `langgraphjs#1692` está cerrado sin resolución visible** en el contenido que
  obtuve; no sé si se cerró por fix, por workaround o por inactividad.
- **Las páginas de docs de LangChain fueron leídas vía WebFetch** (mediado por modelo), salvo las
  citas textuales que reproduzco entre comillas. El código fuente de `SqliteSaver`/`SqliteStore` sí
  fue descargado y grepeado localmente — esa parte es fuente primaria directa.
- **No verifiqué si Cloudflare tiene alguna guía oficial de "correr LangGraph en Agents SDK"** más
  allá del changelog del 2026-07-30 que integra LangChain (no LangGraph) para AI Search.

## Cross-links

- `.claude/rules/capability-absence-claims.md` — la norma que exige este barrido.
- `docs/architecture/arena-tenancy-levels.md` — la escalera L0–L3 de *tenancy* que este documento
  imita en formato y **no** contradice; ahí LangGraph OSS es L1 por otro eje.
- `docs/architecture/arena-memory-extensions.md` — el documento hermano de escalera de capacidades.
- `arenas/cloudflare-agents/README.md`, `arenas/cloudflare-agents/DX-NOTES.md` — los dos archivos que
  contienen las frases barridas. **No fueron modificados por este trabajo.**
- `arenas/live/langgraph/modes/common.py`, `modes/memory.py`, `requirements.txt` — la contra-señal
  in-repo.
- `deploy/helm/aisotropy/values.yaml` — el límite de una réplica que ubica a aisotropy en SL2.
