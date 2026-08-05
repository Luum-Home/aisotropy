---
type: Research
title: "Barrido de ausencia — ADK Go y el 'separador impuesto por el framework': la afirmación se sostiene, pero mucho más angosta"
description: >-
  Tres lugares del arena de adk-go afirman que ADK Go no impone separador cuando el desarrollador concatena un cuarto eje de scoping dentro de userID. El barrido de cinco puntos que exige .claude/rules/capability-absence-claims.md confirma el núcleo — session.Service son tres strings planos sin validación en v2.0.0 y v2.1.0 — pero refuta la afirmación como propiedad del framework: ADK Go SÍ impone separadores reservados en otras superficies (WithRunID, nombres de artifact), y el tier de plataforma (Vertex AI Memory Bank) expone un scope map[string]string de N claves con match exacto, que elimina la concatenación por completo. Además, el rigor simétrico encuentra que la premisa "aisotropy tiene cuatro ejes" está inflada, ya que product_id no entra en ninguna clave de storage.
tags: [architecture, arena, research, adk-go, google-adk, vertex-ai, multi-tenancy, capability-absence, code-truth]
timestamp: 2026-08-04T00:00:00Z
---

# Barrido de ausencia — ADK Go y el "separador impuesto por el framework"

## 1. Qué afirmación se barrió

Tres lugares del arena dicen esencialmente lo mismo:

| Archivo | Texto |
|---|---|
| `arenas/adk-go/FEATURES.md:68-69` | "exactly one axis has no slot of its own, must be concatenated into `userID` **with no framework-enforced separator**, and is the developer-discipline, drop-vulnerable residual" |
| `arenas/adk-go/README.md:203-205` | "**exactly one axis** has no slot of its own and must be concatenated into `userID` (**no framework-enforced separator**). THAT axis is the developer-discipline, drop-vulnerable residual" |
| `arenas/adk-go/orbit/product_isolation.go:22-25` | "Mapping four axes onto three slots means exactly one axis has no native slot of its own and must be concatenated into one of the three, **with no framework-enforced separator**. THAT axis — and only that one — is developer discipline and drop-vulnerable" |

La afirmación es **más específica de lo que aparenta**, y hay que leerla con precisión antes de juzgarla. No dice "ADK no aísla" (los tres archivos explícitamente corrigen esa versión anterior y la llaman equivocada). Dice tres cosas encadenadas:

1. ADK Go tiene exactamente **tres** slots nativos de clave — `(appName, userID, sessionID)`.
2. Si el integrador necesita un **cuarto** eje, tiene que meterlo por concatenación adentro de uno de los tres.
3. Y en esa concatenación **el framework no impone ningún separador** — no hay constante reservada, no hay validación, la convención es invisible para ADK.

El punto (1) no es una afirmación de ausencia y no lo barro acá (está verificado en el propio adaptador). Los que sí son afirmaciones de ausencia, y por lo tanto disparan la norma, son (2) y (3).

**Alcance del barrido:** `google.golang.org/adk/v2` v2.0.0 (la versión que pinnea `arenas/adk-go/go.mod`) y v2.1.0 (la última publicada), más su ecosistema.

---

## 2. Los cinco puntos

### Punto 1 — Núcleo (`google.golang.org/adk/v2`)

**Verificado contra fuente primaria** (código del módulo, no docs).

Los tres slots son strings planos, sin ninguna validación:

- `session/service.go:22-31` — la interfaz `Service` (`Create/Get/List/Delete/AppendEvent`).
- `session/service.go:41-49` (v2.0.0) / `:43-50` (v2.1.0) — `CreateRequest{AppName, UserID, SessionID string; State map[string]any}`.
- `GetRequest{AppName, UserID, SessionID}`, `ListRequest{AppName, UserID}`, `DeleteRequest{AppName, UserID, SessionID}` — todos `string` desnudo.
- `runner/runner.go:45-46` (`Config.AppName`) y `runner/runner.go:165` (`Run(ctx, userID, sessionID string, …)`) — confirma que `appName` se liga en construcción y nunca es argumento por request.
- `session/database/storage_session.go:32-34` — la PK compuesta en el backend GORM son **tres columnas separadas** (`AppName`, `UserID`, `ID` con `gorm:"primaryKey"`), no un string concatenado.
- `memory/service.go:42-46` — `SearchRequest{Query, UserID, AppName}`.
- `memory/inmemory.go:35-38` — la clave del store es `key{appName, userID string}`.

**No existe ninguna función de validación** sobre `appName`/`userID`/`sessionID` en `session/`, `runner/` ni `memory/`: `grep -E 'func .*Validate' ` sobre esos paquetes no devuelve nada aplicable a los slots de identidad.

**La palabra `tenant` no aparece ni una vez** en el código no-test de v2.0.0 ni de v2.1.0 (0 hits).

#### El contrasignal que sí encontré, y que angosta la afirmación

ADK Go **sí impone separadores reservados**, con error tipado — solo que no sobre los slots de identidad:

- `workflow/dynamic_scheduler.go:495-507` — `validateCustomRunID` rechaza IDs vacíos, puramente numéricos, y **los que contienen `/` o `@`**: `fmt.Errorf("%w: %q contains reserved separator (/ or @)", ErrInvalidRunID, id)`. Es exactamente un separador impuesto por el framework, con su propio `ErrInvalidRunID`.
- `artifact/service.go:108-120` — `validateFileName` rechaza `/` y `\` en nombres de artifact: `"invalid name: filename cannot contain path separators"`.
- `session/session.go:255-269` — `KeyPrefixApp = "app:"`, `KeyPrefixUser = "user:"`, `KeyPrefixTemp = "temp:"`: separadores de **namespace de estado** impuestos por el framework, con semántica documentada ("shared across all users and sessions for that application" / "tied to the user_id, shared across all sessions for that user (within the same app_name)").

O sea: el framework conoce el patrón "carácter reservado + validación" y lo aplica en tres superficies distintas. **Lo que no hace es aplicarlo a la identidad.** Eso convierte la afirmación de "ADK no impone separadores" (falso como propiedad del framework) en "ADK no impone separador *en los slots de identidad*" (verdadero y verificado).

Y hay un agravante encontrado en el mismo barrido, que **refuerza** nuestro lado: `artifact/gcsartifact/service.go:71-91` interpola `appName`, `userID` y `sessionID` directo en el path del objeto GCS (`fmt.Sprintf("%s/%s/%s/%s/%d", appName, userID, sessionID, fileName, version)`), validando **solo el `fileName`**. Un `userID` con `/` adentro produce un path ambiguo, sin que nada lo rechace. La asimetría es explícita en el código.

Los prefijos `app:`/`user:` **no agregan un cuarto eje**: `session.InMemoryService()` guarda `appState map[string]stateMap` (por `appName`) y `userState map[string]map[string]stateMap` (por `appName`→`userID`) — `session/service.go:34-38`. Siguen siendo los mismos tres slots, con más granularidad adentro.

### Punto 2 — Extensiones oficiales

**Verificado.** No existe ningún módulo Go first-party fuera del módulo principal: el proxy lista solo `google.golang.org/adk` (v1.x, hasta v1.5.1) y `google.golang.org/adk/v2` (v2.0.0, v2.1.0). No hay `adk-go-contrib`, no hay `adk-ext`.

Dentro del módulo, v2.1.0 agrega dos paquetes top-level que v2.0.0 no tenía: `auth` y `agentregistry`.

- `auth` es **credencial saliente**, no identidad entrante: `auth/credential.go:26-30` define `Credential interface { Apply(h http.Header) error }`, con `APIKeyCredential`, OAuth2, etc. Es para autenticar a ADK **contra** herramientas/MCP. No tiene nada que ver con scoping de tenant. Etiqueta: no aplica.
- `agentregistry` es un cliente del Google Cloud Agent Registry (gobierno de agentes/MCP/endpoints). No es un primitivo de scoping de datos.

El binding de Memory Bank sigue idéntico entre v2.0.0 y v2.1.0 (`grep createUserScope` da los mismos tres hits en ambas).

**Veredicto punto 2: NONE.** Ninguna extensión oficial de ADK Go agrega un eje de scoping ni un separador de identidad.

### Punto 3 — Oferta de plataforma (Vertex AI Agent Engine)

**Acá está el hallazgo que más angosta la afirmación.** Verificado contra fuente primaria: el protobuf generado que ADK Go efectivamente llama (`cloud.google.com/go/aiplatform v1.121.0`, `apiv1beta1/aiplatformpb`).

**Memory Bank tiene un `scope` que es un mapa de N claves, no un string.**

```go
// apiv1beta1/aiplatformpb/memory_bank.pb.go:69-72
// Required. Immutable. The scope of the Memory. Memories are isolated
// within their scope. The scope is defined when creating or generating
// memories. Scope values cannot contain the wildcard character '*'.
Scope map[string]string
```

```go
// apiv1beta1/aiplatformpb/memory_bank_service.pb.go:880-884  (RetrieveMemoriesRequest)
// Required. The scope of the memories to retrieve. A memory must have
// exactly the same scope (`Memory.scope`) as the scope provided here to be
// retrieved (same keys and values). Order does not matter, but it is
// case-sensitive.
Scope map[string]string
```

Lo mismo en `GenerateMemoriesRequest.Scope` (`memory_bank_service.pb.go:650-654`).

Esto es, literalmente, **la solución al problema de aridad sin concatenación**: podés pasar `{"tenant_id": …, "user_id": …, "product_id": …}` como claves separadas, el match es exacto sobre todas las claves y valores, y el orden no importa. No hay separador porque no hay concatenación. La superficie de plataforma **sí** resuelve lo que el núcleo no.

**Pero el binding de ADK Go no la expone.** `memory/vertexai/vertexai_client.go:137`:

```go
// Scope is used to structure the information in MemoryBank. Here we use only the user scope
func createUserScope(userID string) map[string]string {
	return map[string]string{"user_id": userID}
}
```

Usado en `:86` (`AddSessionToMemory`) y `:116` (`SearchMemory`). **Una sola clave, hardcodeada.** Y peor: `memory.SearchRequest` trae un campo `AppName` que el binding **descarta** — nunca entra al scope. O sea, a través de la `memory.Service` de ADK Go, Memory Bank queda scopeado solo por `user_id` (más el `ReasoningEngine` que hace de `parent`); el `appName` no aísla nada en ese tier. Para usar N claves hay que saltearse `memory.Service` y hablarle directo al cliente de aiplatform.

**Vertex AI Sessions también tiene una superficie N-clave que ADK Go no toca:** `Session.Labels map[string]string` (`apiv1beta1/aiplatformpb/session.pb.go:66-72`) y `ListSessionsRequest.Filter` que soporta `labels.key="value"` (`session_service.pb.go:236-243`). Es un **filtro de listado**, no un componente de clave — `GetSession` sigue siendo por resource name — así que es más débil que el scope de Memory Bank. ADK Go no lee ni escribe `Labels` (0 hits en `session/` y `memory/`).

Dato adicional relevante: en el backend Vertex, `appName` no es un string más — `session/vertexai/vertexai_client.go:80` hace `getReasoningEngineID(req.AppName)`, o sea que `appName` resuelve a un **recurso** de Cloud (`projects/…/reasoningEngines/…`). Eso es una frontera de recurso, más fuerte que un slot de string.

**Veredicto punto 3: NATIVE en la plataforma, NO ALCANZABLE desde ADK Go.**

### Punto 4 — Integraciones oficiales con terceros

- **mem0** — integración documentada por el vendor (mem0) para ADK, implementada como `BaseMemoryService`. Expone múltiples claves de scope independientes en una sola llamada: `filters={"AND": [{"user_id": user_id}, {"app_id": app_name}]}`. **Python únicamente** — la doc instala `pip install google-adk mem0ai`; no hay binding Go. La guía usa la Mem0 Platform (API key, cloud); mem0 tiene core open-source auto-hospedable, pero **no verifiqué la licencia exacta ni la fecha del último release** (ver §7).
- **Zep** — aparece en la doc de Zep como framework soportado junto a LangGraph/CrewAI/AutoGen. Python. No verificado en profundidad.
- **OpenMemoryService** (`google/adk-python#3387`) — PR para memoria self-hosted en adk-python. Python. No verificado su estado de merge.
- La doc oficial `adk-docs/docs/sessions/memory.md` lista los memory services por lenguaje: `InMemoryMemoryService` está en Python/TypeScript/Go/Java/Kotlin; `VertexAiMemoryBankService` y `VertexAiRagMemoryService` figuran como Python. Todos operan dentro de las fronteras `app_name` / `user_id`.

**Veredicto punto 4: OFFICIAL-THIRD-PARTY-INTEGRATION, pero solo Python.** Ninguna alcanza a ADK **Go**, que es el sujeto de la afirmación.

### Punto 5 — Docs del vendor

**Verificado contra fuente primaria** (fetch de la página, no búsqueda).

Google **sí** publica una guía de multi-tenancy para agentes, y su prescripción **no es** concatenar en `user_id`. Es **un proyecto de Google Cloud por tenant**:

> "Each tenant project is a dedicated Google Cloud project for each business unit. Individual tenant projects are isolated environments."
> — `https://docs.cloud.google.com/architecture/multi-tenant-agentic-ai-system`

El portal frontend "extrae la identidad del usuario, por ejemplo su unidad de negocio o tenant ID" y usa un registry para rutear al proyecto correcto. El aislamiento lo dan tres capas: frontera de proyecto, **PAB Policy** (limita a qué recursos puede acceder la identidad del agente) y **VPC Service Controls** a nivel organización. La guía rechaza explícitamente mezclar datos de tenants en una sola aplicación.

Eso es una **respuesta real a la pregunta de aridad**, etiquetada como corresponde: **patrón prescrito, no primitivo**. Y es una respuesta que nuestros tres archivos no mencionan en absoluto — presentan la concatenación a mano como si fuera el único camino para componer un cuarto eje.

La doc de estado (`adk-docs/docs/sessions/state.md`, reflejada en `adk.dev/sessions/state/`) confirma el punto 1: los prefijos `user:`/`app:`/`temp:` scopean dentro de `user_id` y `app_name`, sin agregar ejes.

---

## 3. Tabla de madurez

| Hallazgo | Etiqueta | Último release verificado |
|---|---|---|
| `session.Service` = 3 slots string sin validación ni separador | **NATIVE** (y es la ausencia que la afirmación describe) | v2.1.0 — **2026-07-23** (proxy: `Time:"2026-07-23T15:32:19Z"`) |
| Prefijos de estado `app:` / `user:` / `temp:` | **NATIVE** — separador impuesto para namespaces de *estado*, no de identidad | v2.1.0 — 2026-07-23 |
| `validateCustomRunID` (rechaza `/` y `@`), `validateFileName` (rechaza `/` y `\`) | **NATIVE** — separador impuesto, en superficies que no son identidad | v2.1.0 — 2026-07-23 |
| Paquete `auth` (credencial saliente) | **NATIVE** (nuevo en v2.1.0) — no es scoping | v2.1.0 — 2026-07-23 |
| Módulos Go first-party de extensión | **NONE** — el proxy solo lista `adk` y `adk/v2` | — |
| Memory Bank `scope map[string]string`, N claves, match exacto | **NATIVE en la plataforma** (Vertex AI Agent Engine) | `cloud.google.com/go/aiplatform` v1.121.0 (API `v1beta1`) |
| Binding ADK Go → Memory Bank: 1 sola clave `user_id`, descarta `appName` | **NATIVE** (limitación del binding, no de la API) | v2.1.0 — 2026-07-23 |
| Vertex AI Sessions `Labels` + `Filter labels.key=` | **NATIVE en la plataforma**, no expuesto por ADK Go | aiplatform v1.121.0 |
| mem0 / Zep como `BaseMemoryService` con múltiples claves de filtro | **OFFICIAL-THIRD-PARTY-INTEGRATION — Python únicamente** | fecha **no verificada** (§7) |
| "Multi-tenant agentic AI system": proyecto de Cloud por tenant + PAB + VPC-SC | **PATRÓN PRESCRITO por el vendor** (no primitivo) | fecha de publicación **no verificada** (§7) |

La versión pinneada por el arena es **v2.0.0 (2026-06-30)**. Verifiqué que **v2.1.0 no cambia nada relevante**: `session/service.go` y `memory/service.go` tienen las mismas estructuras, sigue habiendo 0 apariciones de `tenant`, y `createUserScope` sigue devolviendo una sola clave. El pin no está ocultando una mejora.

---

## 4. El nivel en la escalera L0–L3

ADK Go no tiene un único nivel: tiene tres superficies con niveles distintos, y decir "ADK es Lx" a secas sería el mismo error grueso que la norma prohíbe.

| Superficie | Nivel | Justificación |
|---|---|---|
| `session.Service` (el data plane que el arena ejercita) | **L1, con un slot no-dropeable** | Tres identificadores de primera clase que particionan **si** el llamador los pasa bien. Omitir `userID` no da error: da otra partición, en silencio — el modo de falla canónico de L1. La excepción parcial: `appName` se liga en construcción del runner (`runner/runner.go:45,165`) y **no es argumento por request**, así que ese eje sí es no-dropeable per-request. Eso lo acerca a L2 *para ese eje* en el sentido de "ligado una vez, downstream no lo puede escapar" — pero no en el sentido de seguridad, porque nada valida que el llamador tenga derecho a ese `appName`. |
| `memory.Service` → Memory Bank vía ADK Go | **L1, y más débil que el tier de sesión** | El scope se arma con una sola clave (`user_id`) y `appName` se descarta (`vertexai_client.go:116`). Dos "productos" con runners distintos pero el mismo `userID` comparten memorias, salvo que uses ReasoningEngines distintos. |
| Memory Bank llamado directo (plataforma) | **L1 de aridad ilimitada** | El `scope map[string]string` elimina la concatenación, pero sigue siendo un parámetro que hay que pasar bien: como el match es exacto sobre todas las claves, olvidar una clave no da error — da otro scope vacío. Es "partición silenciosa", el modo de falla de L1, no L2. **No confundir "sin concatenación" con "impuesto".** |
| Patrón prescrito: proyecto por tenant + PAB + VPC-SC | **L2/L3, pero en infraestructura, no en el framework** | Con PAB Policy y VPC-SC, la identidad del agente de un tenant no puede alcanzar el proyecto de otro: es imposible por construcción, no por disciplina. Ahora bien, la escalera de `arena-tenancy-levels.md` mide **el primitivo del framework**, no lo que un integrador cuidadoso arma encima. Esto es infraestructura, y hay que decirlo así. |

**Nivel a reportar para ADK Go en el arena: L1.** No L0 (hay tres identificadores de primera clase, real composite key, y la corrección anterior de "no scoping" ya está bien hecha en los tres archivos). No L2 (nada valida, nada liga la identidad a un llamador probado, la omisión es silenciosa).

---

## 5. Rigor simétrico — aisotropy bajo la misma lupa

El estándar que le apliqué a ADK me obliga a mirar nuestro lado del mismo modo. Encontré algo que **no es cosmético**.

### 5.1 La premisa de los "cuatro ejes" está inflada

Los tres archivos construyen la comparación sobre esta frase (`FEATURES.md:63-65`, `README.md:187-189`, `product_isolation.go:18-20`):

> "aisotropy binds four axes (`tenant_id, user_id, session_id, product_id`) from `context.Context`" · "aisotropy scopes memory by the composite `(tenant_id, user_id, session_id, product_id)`"

**El código dice tres, no cuatro.**

- `internal/memory/key.go:96-107` — `Key.Validate()` valida exactamente **tres** componentes: `tenant`, `user`, `session`. La estructura `Key` no tiene campo de producto.
- `internal/memory/key.go:117-119` — el prefijo en disco es `tenant:%s:user:%s:session:%s`. Tres.
- `internal/tenancy/scope.go:72-76` — `Scope{tenant, user, hasUser}`. **Dos.** Es el scope que usan los tiers de facts y semantic.
- `product_id` **nunca entra en una clave de storage ni en un filtro**. Es un selector de persona y un atributo de telemetría: `aisotropy/app.go:311-315` (`WithPersona(productID, p)` registra un prompt base por producto), `internal/api/bitrate_hydrator.go:43-49` (pasa `productID` a `hydrateSystemPrompt` solo para elegir el base prompt; la clave de memoria que arma en la línea de al lado es `memory.Key{Tenant, User, Session}`), `internal/api/core.go:110` y `internal/api/stream_core.go:192` (lo loguean). Y `internal/llm/otel.go:238` lo dice con todas las letras: *"product_id is not yet threaded through this seam"*.

O sea: **aisotropy scopea memoria por tres ejes, igual aridad que los tres slots de ADK.** La comparación "4 ejes contra 3 slots" que estructura los tres archivos sobrestima nuestro lado, y con eso infla el tamaño del residual que le atribuimos a ADK. El `TestResidualAxisDropLeaks` sigue midiendo algo real —qué pasa si se dropea un componente hand-folded— pero la narrativa de "nosotros tenemos un eje más que ellos no pueden cubrir" no la sostiene el código.

**Lo que sí sobrevive**, y es lo que la comparación debería decir: no es la **cantidad** de ejes, es la **calidad** de los tres. Los nuestros están validados — `ValidateIdentityComponent` (`internal/memory/key.go:34-42`) rechaza vacío y los caracteres reservados `":\x00\n"`, con el comentario que explica exactamente por qué (`key.go:17-24`: *"If a caller could smuggle a colon into, say, a session id, it could forge a prefix collision between tenants"*). Los de ADK son strings sin validar. **Ese** es el delta verdadero, y es un delta de calidad de slot, no de aridad.

### 5.2 Nuestros propios bordes, con el mismo detalle

- **NoAuth.** `aisotropy/app.go:1153-1157` rechaza todo `/v1` con 401 salvo que haya authenticator o NoAuth explícito, y `aisotropy/authenv.go` exige `AISOTROPY_ALLOW_NOAUTH=1` más un segundo guard para bind no-loopback. Pero en modo NoAuth **la identidad del path se cree verbatim** — el borde degrada a L1, exactamente el mismo modo de falla que le señalamos a ADK. El propio `README.md:230-234` del adaptador ya lo admite ("aisotropy itself trusts the path `tenant_id` verbatim in its default NoAuth mode"), lo cual está bien y hay que mantenerlo.
- **La fachada de librería** acepta el string de tenant que el embebedor le pase. Nuestra validación previene forjar una colisión de prefijo; no previene que el embebedor mande el tenant equivocado. Provenance es problema del embebedor, igual que en cualquier framework L1.
- **"Separador impuesto" no es "identidad probada".** Es la distinción que hay que cuidar al escribir la frase de reemplazo: nuestro `reservedKeyChars` es una defensa contra **forja de prefijo**, no contra **identidad falsa**. Vender lo primero como si fuera lo segundo sería exactamente el tipo de deslizamiento que esta norma existe para frenar.

---

## 6. Veredicto y frase de reemplazo

### Veredicto: **SE SOSTIENE PERO MÁS ANGOSTA**

**Por qué no "se sostiene" tal cual:**

1. Como propiedad del framework, "no framework-enforced separator" es **falso**. ADK Go impone separadores reservados con error tipado en al menos dos lugares (`validateCustomRunID` rechaza `/` y `@`; `validateFileName` rechaza `/` y `\`) y define constantes de separador de namespace (`KeyPrefixApp/User/Temp`). Lo que falta es específicamente en los slots de identidad.
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-adk-go-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->
2. Las tres frases presentan la concatenación a mano como **el** camino para componer un cuarto eje. El barrido encontró dos caminos que no lo son: el `scope map[string]string` de N claves de Memory Bank (match exacto, sin concatenación) y el patrón prescrito por Google de un proyecto de Cloud por tenant. Que ninguno sea alcanzable desde `session.Service` es cierto y es lo que hay que decir — pero decirlo, no omitirlo.
3. Rigor simétrico: la premisa de "cuatro ejes" de nuestro lado no la sostiene el código (§5.1).

**Por qué no "no se sostiene":** para la superficie de la que la afirmación realmente habla — la `session.Service` que el arena ejercita — el barrido de cinco puntos **confirma** la ausencia, en las dos versiones publicadas, sin excepciones: tres strings planos, cero validación, cero constante de separador, cero concepto de tenant, y ninguna extensión oficial Go que lo cambie.

### Frase exacta de reemplazo

**Versión larga** (para `orbit/product_isolation.go:22-25`, donde el comentario tiene lugar):

```
// Mapping four axes onto three slots means exactly one axis has no native slot
// of its own and must be concatenated into one of the three. Inside ADK Go's
// session.Service the framework enforces nothing about that fold: appName,
// userID and sessionID are plain strings with no validation and no reserved
// character — unlike WithRunID, which rejects the reserved separators '/' and
// '@' with ErrInvalidRunID (workflow/dynamic_scheduler.go:495-507), or artifact
// filenames, which reject path separators (artifact/service.go:115-120). ADK
// knows the pattern; it just does not apply it to identity. Outside
// session.Service the fold is NOT the only option: Vertex AI Memory Bank takes
// an N-key `scope map[string]string` with exact-match retrieval over all keys
// (aiplatformpb RetrieveMemoriesRequest.Scope), and Google's own prescribed
// multi-tenancy answer is one Cloud project per tenant with PAB Policy and
// VPC-SC, not string concatenation. Neither is reachable through ADK Go, whose
// Memory Bank binding hardcodes a single {"user_id": ...} key and drops appName
// entirely (memory/vertexai/vertexai_client.go:137, used at :86 and :116).
// SWEPT: 2026-08-04 — docs/architecture/arena-sweep-adk-go-2026-08-04.md
```

**Versión corta** (para `FEATURES.md:68-69` y `README.md:203-205`):

> exactly one axis has no slot of its own and must be concatenated into `userID` — a slot ADK Go stores as a plain, unvalidated string. The framework enforces reserved separators elsewhere (`WithRunID` rejects `/` and `@`; artifact filenames reject path separators) but not on the identity slots, so the fold-in convention is invisible to it. Outside `session.Service` the fold is not the only option — Vertex AI Memory Bank takes an N-key `scope map[string]string` with exact-match retrieval, and Google's prescribed multi-tenancy pattern is one Cloud project per tenant — but neither is reachable through ADK Go's own `session.Service`/`memory.Service`. SWEPT: 2026-08-04 (`docs/architecture/arena-sweep-adk-go-2026-08-04.md`)

### Corrección adicional requerida (fuera del alcance de este documento)

La premisa de los cuatro ejes (§5.1) aparece en los mismos tres archivos y en varias frases más de `FEATURES.md` §1/§1b y `README.md`. **No la toqué** — este documento es solo el barrido. Pero la frase de reemplazo de arriba mantiene "four axes" únicamente porque cambiar eso es una corrección distinta, más grande, y hay que hacerla completa o no hacerla. Queda anotado como deuda explícita, no como comentario suelto.

---

## 7. Qué no pude verificar, y por qué

**Verificado contra fuente primaria** (código descargado y leído, o página fetcheada):

- Todo el punto 1: código de `google.golang.org/adk/v2` v2.0.0 y v2.1.0 en el module cache.
- Todo el punto 3: protobuf generado de `cloud.google.com/go/aiplatform` v1.121.0.
- Fechas de release de ADK Go: `proxy.golang.org` (`.info`), que es autoritativo.
- El patrón prescrito del punto 5: fetch directo de `docs.cloud.google.com/architecture/multi-tenant-agentic-ai-system`.
- Todo §5 (aisotropy): código del repo.

**Inferido o no verificado:**

1. **Fechas de release de mem0 y Zep.** No las busqué. La norma pide fecha de último release para no contar como "lo tiene" a una extensión abandonada; acá no la tengo, así que la fila de la tabla queda con la fecha en blanco a propósito. Lo que sí es firme es que **son Python** y por lo tanto no alcanzan al sujeto de la afirmación.
2. **Licencia exacta de mem0 y si el `BaseMemoryService` documentado es self-hosteable o solo cloud.** La doc de la integración usa una API key de Mem0 Platform; el core es open-source pero no confirmé licencia ni que el path documentado funcione self-hosted.
3. **Estado del PR `google/adk-python#3387` (OpenMemoryService).** Apareció en la búsqueda; no verifiqué si está mergeado.
4. **La página `docs.cloud.google.com/agent-builder/agent-engine/memory-bank/overview` no me devolvió el campo `scope`** — el fetch trajo contenido de "Gemini Enterprise Agent Platform" sin la sección de API. Por eso la evidencia del punto 3 sale del **protobuf generado**, que es mejor fuente igual: es exactamente lo que ADK Go compila y llama.
5. **La fecha de publicación de la guía de multi-tenancy de Google Cloud.** La página no la expone de forma verificable.
6. **La lectura de fechas de `github.com/google/adk-go/releases` vía WebFetch salió con año 2024**, contradiciendo al module proxy (2026). Confío en el **proxy** — es un timestamp de servicio, no una lectura mediada por modelo. Dejo constancia de la discrepancia en vez de esconderla.
7. **No corrí el adaptador.** El barrido es de superficie de API y de ecosistema; no ejecuté `orbit/product_isolation_test.go` ni verifiqué de nuevo sus asserts. Los tests que los tres archivos citan los tomo como dados por el trabajo previo, no como verificados acá.
8. **`agentregistry` lo inspeccioné solo por nombre y descripción**, no leí su código completo. Si escondiera un primitivo de scoping, se me pasó — aunque un registry de gobierno de agentes es una superficie improbable para eso.

---

## 8. Evidencia ejecutable

Los comandos que produjeron los hallazgos, para que se puedan volver a correr:

```bash
# Traer el código de las dos versiones al module cache
cd arenas/adk-go
go mod download google.golang.org/adk/v2@v2.0.0
go mod download google.golang.org/adk/v2@v2.1.0
git checkout -- go.sum   # go mod download toca go.sum: restaurarlo

A=$(go env GOMODCACHE)/google.golang.org/adk/v2@v2.1.0

# Punto 1 — los tres slots, sin validación
grep -n -A8 'type CreateRequest\|type GetRequest\|type ListRequest' "$A/session/service.go"
grep -rn --include='*.go' 'func .*Validate' "$A/session/" "$A/runner/" "$A/memory/"   # vacío

# Punto 1 — cero concepto de tenant (esperado: 0)
grep -rn --include='*.go' -i 'tenant' "$A" | grep -v _test.go | wc -l

# Punto 1 — el contrasignal: separadores SÍ impuestos, en otras superficies
sed -n '495,507p' "$A/workflow/dynamic_scheduler.go"
sed -n '115,120p' "$A/artifact/service.go"
sed -n '255,269p' "$A/session/session.go"
sed -n '71,91p'   "$A/artifact/gcsartifact/service.go"   # interpola identidad sin validar

# Punto 3 — el scope de N claves de Memory Bank
P=$(go list -m -f '{{.Dir}}' cloud.google.com/go/aiplatform)
grep -n -B6 'Scope map\[string\]string' "$P/apiv1beta1/aiplatformpb/memory_bank.pb.go"
grep -n -B6 'Scope map\[string\]string' "$P/apiv1beta1/aiplatformpb/memory_bank_service.pb.go"
sed -n '236,243p' "$P/apiv1beta1/aiplatformpb/session_service.pb.go"   # filtro labels.key=

# Punto 3 — el binding de ADK Go usa una sola clave y descarta appName
sed -n '105,140p' "$A/memory/vertexai/vertexai_client.go"

# Punto 2 — no hay módulos Go first-party de extensión
curl -s https://proxy.golang.org/google.golang.org/adk/v2/@v/list
curl -s https://proxy.golang.org/google.golang.org/adk/v2/@v/v2.1.0.info

# §5.1 — aisotropy scopea por TRES ejes, no cuatro
sed -n '96,119p' internal/memory/key.go
sed -n '72,76p'  internal/tenancy/scope.go
grep -rn --include='*.go' -iE 'productID|product_id' internal/ aisotropy/ \
  | grep -v _test.go | grep -viE 'persona|production'
sed -n '43,49p' internal/api/bitrate_hydrator.go
```

---

## 9. Enlaces cruzados

- `.claude/rules/capability-absence-claims.md` — la norma que este barrido ejecuta (cinco puntos, etiquetas de madurez, escalera L0–L3, rigor simétrico).
- `docs/architecture/arena-tenancy-levels.md` — la escalera L0–L3 y su aplicación al resto de los frameworks; este documento agrega ADK Go a esa grilla y usa su formato.
- `docs/architecture/arena-sweep-langgraph-state-locality-2026-08-04.md` — barrido hermano del mismo día, misma norma, otra afirmación.
- `docs/architecture/arena-memory-extensions.md` — el barrido de ecosistema de memoria, del que este hereda la disciplina de "no alcanza leer el core".
- `scripts/absence-claim-guard.py` — el guard que exige el marcador `SWEPT: YYYY-MM-DD` junto a cada afirmación de ausencia, con vencimiento a 180 días.
- `arenas/adk-go/README.md`, `arenas/adk-go/FEATURES.md`, `arenas/adk-go/orbit/product_isolation.go` — los tres lugares que hay que anotar con la frase de reemplazo y el marcador.

## Fuentes web

- [Multi-tenant agentic AI system — Google Cloud Architecture Center](https://docs.cloud.google.com/architecture/multi-tenant-agentic-ai-system)
- [Vertex AI Agent Engine Memory Bank overview](https://docs.cloud.google.com/agent-builder/agent-engine/memory-bank/overview)
- [ADK — State (prefijos `app:` / `user:` / `temp:`)](https://adk.dev/sessions/state/)
- [adk-docs — Memory: Long-Term Knowledge with `MemoryService`](https://github.com/google/adk-docs/blob/main/docs/sessions/memory.md)
- [Mem0 — Google ADK integration](https://docs.mem0.ai/integrations/google-ai-adk)
- [google/adk-go — releases](https://github.com/google/adk-go/releases)
- [pkg.go.dev — `google.golang.org/adk/v2`](https://pkg.go.dev/google.golang.org/adk/v2)
