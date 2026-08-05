---
type: Process
title: "Barrido de ausencia — tres afirmaciones del arena de Letta (single-org, sin gate pre-spend, exclusividad de sleep-time)"
description: >-
  Barrido de cinco puntos exigido por .claude/rules/capability-absence-claims.md sobre tres afirmaciones que el arena de Letta publica. Una se sostiene sólo si se la reescribe (Letta OSS NO es una sola organización — el core trae Organization como raíz del árbol de objetos, endpoints admin para crearlas y un predicado de acceso por organization_id en toda query; lo que falta es el bind entre credencial y organización). Otra es más angosta de lo que dice (existe CreditVerificationService, un gate pre-step por organización en el core, inerte por defecto y hoy sin capacidad de bloquear). La tercera, la única positiva y por eso la más cara, NO se sostiene como exclusividad: langmem — extensión first-party de LangChain sobre LangGraph, que está en el arena — productiza consolidación diferida por inactividad vía ReflectionExecutor. Incluye rigor simétrico sobre aisotropy (NoAuth, tenant provisto por el embebedor, y nuestro propio sleep-cycle).
tags: [architecture, process, arena, letta, multi-tenancy, isolation, sleep-time, consolidation, cost-governance, code-truth, capability-absence]
timestamp: 2026-08-04T00:00:00Z
---

# Barrido de ausencia — tres afirmaciones del arena de Letta

> **Por qué existe este documento.** `.claude/rules/capability-absence-claims.md` exige que toda
> afirmación de la forma "X no tiene Y" / "sólo nosotros hacemos Z" pase por un barrido de cinco
> puntos (core, extensiones oficiales, plataforma, integraciones oficiales de terceros, docs del
> vendor), con URL o `file:line` por hallazgo, etiqueta de madurez, ubicación en la escalera L0–L3 y
> rigor simétrico sobre aisotropy. Tres afirmaciones del arena de Letta nunca pasaron ese barrido.
> Este documento lo hace. **Ninguna de las tres sobrevive intacta.**

**Alcance y método.** El barrido se hizo el 2026-08-04. Las fuentes primarias son: el código del
servidor OSS `letta-ai/letta` (rama `main`, leído vía `raw.githubusercontent.com`), el SDK
`letta-client==1.12.1` instalado en `arenas/letta/.venv/`, el código de `pydantic-ai` (rama `main`),
PyPI para fechas de release, y las docs del vendor. **No se levantó un servidor Letta**: todo lo que
sigue es lectura de fuente y de docs, no ejecución. Eso está marcado explícitamente donde importa.

**Este documento no modifica los archivos donde viven las afirmaciones.** Es el barrido; la
corrección puntual en `arenas/letta/README.md`, `arenas/letta/FEATURES.md`,
`arenas/letta/orbit/memory.py` y `arenas/letta/scripts/*.py` queda pendiente y se lista al final.

---

## Afirmación A — "Letta OSS es una sola organización, sin binding impuesto por el framework entre el caller autenticado y un tenant"

### Dónde aparece

| Ubicación | Texto |
|---|---|
| `arenas/letta/README.md:154` | "Letta OSS es un **single organization**: there is no framework binding between an authenticated caller and a tenant." |
| `arenas/letta/README.md:233` | "OSS Letta is a **single org** with no framework-enforced bind between an authenticated caller and a tenant." |
| `arenas/letta/FEATURES.md:188` | "CONVENTION (1): #16 — caller→tenant binding (single-org: no framework bind; **org-wide under-scoping leak proven in `smoke.py`**, agent-scoped reads isolate by `agent_id`)." |
| `arenas/letta/orbit/memory.py:28` | "**OSS Letta is a SINGLE ORG with no framework binding between an authenticated caller and a tenant.**" |
| `arenas/letta/scripts/product_isolation_demo.py:29` | "OSS Letta is a SINGLE ORG: the org-wide primitives … carry NO framework binding between the caller and a tenant/product." |

La afirmación tiene **dos mitades independientes** que el texto fusiona en una sola frase, y que el
barrido separa porque **una es falsa y la otra es cierta**:

- **A1 — "es una sola organización"** (afirmación sobre el modelo de datos del framework).
- **A2 — "no hay binding impuesto por el framework entre el caller autenticado y un tenant"**
  (afirmación sobre el plano de autenticación).

### 1. Core — `letta-ai/letta`, código real

**A1 es falsa.** El core OSS tiene un concepto de organización de primera clase, y es la raíz del
árbol de objetos:

- `letta/orm/organization.py` — docstring textual de la clase `Organization`:
  *"The highest level of the object tree. All Entities belong to one and only one Organization."*
  Tiene relaciones `users`, `agents`, `blocks`, `messages`, `source_passages`, `tools`, `archives`,
  `identities`, `runs`, `providers`, `sandbox_configs`… es decir, **toda** entidad cuelga de una
  organización.
  https://github.com/letta-ai/letta/blob/main/letta/orm/organization.py
- `letta/services/organization_manager.py` — `create_organization_async`,
  `get_organization_by_id_async`, `list_organizations_async`, `delete_organization_by_id_async`.
  `create_default_organization_async` crea la org por defecto (`DEFAULT_ORG_ID`), pero es *una* org,
  no *la única posible*.
  https://github.com/letta-ai/letta/blob/main/letta/services/organization_manager.py
- `letta/server/rest_api/app.py:858-861` — el servidor OSS **monta endpoints admin para crear
  organizaciones y usuarios**:
  ```
  # admin/users
  app.include_router(users_router, prefix=ADMIN_PREFIX)
  app.include_router(organizations_router, prefix=ADMIN_PREFIX)
  ```
  `letta/server/rest_api/routers/v1/organizations.py` expone `POST /orgs` (`operation_id:
  create_organization`), `GET /orgs` (`list_orgs`), `DELETE /orgs`. `…/users.py` expone
  `POST /users` (`create_user`), `GET /users` (`list_users`), `PUT /users`.
  https://github.com/letta-ai/letta/blob/main/letta/server/rest_api/routers/v1/organizations.py
- **Y el scoping por organización es obligatorio, no opcional**:
  `letta/orm/sqlalchemy_base.py`, `apply_access_predicate(query, actor, access, access_type=AccessType.ORGANIZATION)`
  agrega un `WHERE` por `actor.organization_id` a **toda** query y **tira `ValueError` si el actor no
  tiene organización**. Comentario textual en el código:
  *"entrypoint for row-level permissions. Defaults to 'same org as the actor, all permissions' at the moment"*.
  https://github.com/letta-ai/letta/blob/main/letta/orm/sqlalchemy_base.py

  Esto es **exactamente lo contrario** de "single org": es un filtro de fila por organización
  aplicado por el framework en cada lectura. Una `client.blocks.list()` sin filtros **no** devuelve
  "EVERY tenant's block"; devuelve los blocks *de la organización del actor*.

**A2 es cierta, y la fuente lo confirma.** El actor sale de un header sin verificar:

- `letta/server/rest_api/dependencies.py`, `get_headers(...)`:
  `actor_id: Optional[str] = Header(None, alias="user_id")`. Lo único que se valida es el
  **formato** (`user-<uuid4>` vía `PRIMITIVE_ID_PATTERNS`), no la **procedencia**. Quien pueda
  llamar a la API elige qué actor —y por lo tanto qué organización— usar.
  https://github.com/letta-ai/letta/blob/main/letta/server/rest_api/dependencies.py
- `letta/server/rest_api/app.py:165-172, 797-799` — el único mecanismo de auth del OSS es
  `CheckPasswordMiddleware` con **una sola contraseña compartida**
  (`LETTA_SERVER_PASSWORD`, o una aleatoria si no se setea; se activa con `LETTA_SERVER_SECURE=true`
  o `--secure`). Comentario textual: *"middleware that only allows requests to pass through if user
  provides a password thats randomly generated and stored in memory"*.
  Una contraseña ⇒ acceso a **todas** las organizaciones vía el header `user_id`.

**Lectura correcta del core:** Letta OSS es **multi-organización con aislamiento por fila a nivel de
organización (L2 dado el actor), pero con procedencia de identidad L1**: la credencial no está atada
a ninguna organización; el actor —y con él la org— es un parámetro provisto por el caller.

### 2. Extensiones oficiales

- **`Identity` / `identities`** — sigue existiendo en el core (`letta/orm/identity.py`, router
  `identities_router` montado en `/v1`), pero **las docs del vendor lo declaran deprecado** y
  prescriben el mapeo a nivel aplicación (ver punto 5). Etiqueta: **NATIVE pero DEPRECADO**.
- No se encontró ningún paquete first-party fuera del core que agregue bind credencial→org al OSS.
  Etiqueta para "bind caller↔tenant vía extensión oficial": **NONE** (barrido no exhaustivo — ver
  "Qué no pude verificar").

### 3. Oferta de plataforma — Letta Cloud

Acá está el punto donde la precisión del "OSS" en la afirmación **importa y se sostiene**:

- **Client-side access tokens — Cloud-only, y son un bind real.** El SDK
  `letta-client==1.12.1` expone `client.access_tokens.create(...)` con una política tipada:
  `arenas/letta/.venv/lib/python3.13/site-packages/letta_client/types/access_token_create_params.py`
  ```python
  class Policy(TypedDict, total=False):
      id: Required[str]
      access: Required[List[Literal["read_messages","write_messages","read_agent","write_agent"]]]
      type: Required[Literal["agent"]]
  ```
  Es decir: un token acotado a **agentes concretos** con permisos concretos y expiración
  (default 5 minutos). Eso **sí** es un binding impuesto por el framework entre una credencial y un
  subconjunto de datos. La doc lo titula literalmente **"Create token (Cloud-only)"**:
  https://docs.letta.com/api-reference/client-side-access-tokens/create
- **Proyectos** — el cliente manda `X-Project-Id` / `X-Project`
  (`letta_client/_client.py:319-320`), y el servidor OSS los lee
  (`dependencies.py`, `project_id: Header(None, alias="X-Project-Id")`). En Letta Cloud el workspace
  se organiza en proyectos (agentes van al "Default Project"):
  https://docs.letta.com/guides/cloud/quickstart
- **Contexto de billing por org** — `X-Billing-Plan-Type` / `X-Billing-Cost-Source` /
  `X-Billing-Customer-Id` → `BillingContext(plan_type, cost_source, customer_id)`
  (`letta/schemas/provider_trace.py:13-18`). Headers presentes en el core OSS, poblados por la
  infraestructura de Letta Cloud.

**Consecuencia:** la afirmación hace bien en decir "OSS". Si dijera "Letta" a secas, sería falsa
por los access tokens de Cloud. Pero la precisión que hoy tiene es *accidental*: el texto acierta en
el calificador y falla en el sustantivo ("single organization").

### 4. Integraciones oficiales con terceros

- **Supabase + Next.js** (cookbook oficial de Letta): el patrón documentado es autenticar en
  Supabase, y que el middleware de la app adjunte el usuario a los headers del request, mapeando
  usuario → `agent_id` **en la capa de aplicación**.
  https://docs.letta.com/cookbooks/integrations/supabase/
  Etiqueta: **OFFICIAL-THIRD-PARTY-INTEGRATION**, self-hosteable (Supabase tiene OSS), pero es un
  **patrón prescrito**, no un primitivo: confirma A2 en vez de refutarla.

### 5. Docs del vendor

- **`docs.letta.com/guides/agents/multi-user`** — el patrón prescrito. Históricamente: crear un
  `Identity` con el `identifier_key` del usuario y asociar el agente. **La página hoy declara la
  feature de Identities deprecada** y dice: *"manage user-to-agent mappings in your own application
  layer using Letta's unique identifiers (e.g., `agent_id`)"*. Etiqueta: **prescribed-pattern**,
  explícitamente app-level.
- **Issue `letta-ai/letta#2887`, "Multi-Tenant support for self-hosting deployment"** — abierto por
  un miembro de la comunidad (Colstuwjx, 2025-09-15), pide "multiple api-keys for different users",
  y observa: *"Otherwise we have to setup multiple letta deployment for each user?"*. **Cerrado como
  stale, sin respuesta de mantenedor visible.** https://github.com/letta-ai/letta/issues/2887
  Es una señal fuerte a favor de A2 — pero **no es evidencia de A1**: pedir API keys por usuario es
  pedir el *bind*, no la *existencia de orgs*.
- **Self-hosting** — auth = `SECURE=true` + `LETTA_SERVER_PASSWORD`, Bearer con esa contraseña.
  https://docs.letta.com/letta-code/selfhosting/

### La parte del "leak proven" — `FEATURES.md:188`

Esa línea no afirma una ausencia: **atribuye un defecto** ("org-wide under-scoping leak proven in
`smoke.py`"). El estándar de evidencia es más alto y **no se cumple**:

`arenas/letta/scripts/smoke.py` y `product_isolation_demo.py` corren **OFFLINE contra
`arenas/letta/orbit/memory.py`**, un store propio del arena que —según su propio docstring— *"mirror
the real server's two surfaces … exactly"*. Es decir: lo que está "proven" es el comportamiento de
**nuestra reimplementación de la semántica de Letta**, no el de Letta. El servidor real nunca se
levantó (el propio `README.md:226-229` lo dice: *"Not executed (needs a live server + DB + provider
key)"*).

Y la semántica que el mirror modela es **incorrecta en un punto clave**: modela un
`blocks.list()` sin filtro como un escaneo que cruza tenants, cuando el servidor real le aplica
`apply_access_predicate` por `organization_id`. El "leak" sólo existe si **todos los tenants viven
en la misma organización** — que es la convención de deployment por defecto, no una propiedad del
framework. Un `smoke.py` no puede probar una propiedad del servidor sin correr el servidor.

### Escalera L0–L3

| Sistema | Nivel | Evidencia |
|---|---|---|
| **Letta OSS — plano de datos, dado el actor** | **L2** | `apply_access_predicate` inyecta `WHERE organization_id = actor.organization_id` en toda query y falla si el actor no tiene org (`letta/orm/sqlalchemy_base.py`). El downstream no puede escapar del binding sin esfuerzo deliberado. |
| **Letta OSS — procedencia de identidad (el edge)** | **L1** | El actor sale del header `user_id`, validado sólo por formato (`dependencies.py`); la única auth es una contraseña compartida (`app.py:797-799`). Poner el `user_id` de otro es un parámetro de distancia. |
| **Letta OSS — efectivo, extremo a extremo** | **L1** | El eslabón más débil manda: un L2 downstream colgado de un L1 upstream es L1. |
| **Letta Cloud** | **L2** | Access tokens acotados por política a agentes concretos con expiración (Cloud-only, docs citadas); proyectos + org gestionadas por la plataforma. No L3: la política se declara al emitir el token, no está impuesta por construcción en la API. |
| **aisotropy** | ver § rigor simétrico | — |

### Veredicto A — **NO SE SOSTIENE COMO ESTÁ / MÁS ANGOSTA**

A1 ("single organization") es **falsa contra el core**. A2 ("no framework binding entre caller
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-letta-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->
autenticado y tenant") es **cierta y verificada en fuente**. El problema es que el texto las fusiona,
y la mitad falsa es la que hace el trabajo retórico.

**Frase exacta de reemplazo** (para `README.md:154`, `README.md:233`, `orbit/memory.py:28`,
`product_isolation_demo.py:29`, adaptando el envoltorio de cada lugar):

> Letta OSS **sí** tiene organizaciones de primera clase — `Organization` es la raíz del árbol de
> objetos, el servidor monta endpoints admin para crearlas (`POST /v1/admin/orgs`, `POST
> /v1/admin/users`) y `apply_access_predicate` agrega un filtro por `organization_id` a **toda**
> query (`letta/orm/sqlalchemy_base.py`). Lo que **no** hay es un binding impuesto por el framework
> entre la credencial autenticada y esa organización: el actor se elige con el header `user_id`,
> validado sólo por formato (`letta/server/rest_api/dependencies.py`), y la única auth del OSS es una
> contraseña compartida (`LETTA_SERVER_PASSWORD`, `app.py:797-799`). El aislamiento por fila es
> **L2 dado el actor**, pero la procedencia del actor es **L1**, así que el resultado extremo a
> extremo es L1. El bind credencial→datos existe sólo en Letta Cloud (client-side access tokens
> acotados por política a agentes concretos, documentados como Cloud-only). El deployment de un solo
> org es la **convención por defecto**, no una limitación del framework.
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-letta-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->

**Frase exacta de reemplazo para `FEATURES.md:188`:**

> CONVENTION (1): #16 — caller→tenant binding (el core trae orgs y filtra por `organization_id` en
> toda query, pero el actor viene de un header sin verificar y la auth OSS es una contraseña
> compartida; el mapeo caller→`agent_id` es app-level, patrón prescrito por el vendor. El escenario
> de under-scoping org-wide está **modelado offline** en `smoke.py` contra `orbit/memory.py`, no
> ejecutado contra un servidor Letta).

Nótese el cambio de **"proven"** a **"modelado offline"**: es la corrección más importante de las
tres, porque es la única que hoy atribuye un defecto a un producto ajeno sobre evidencia que no lo
toca.

---

## Afirmación B — "Letta has NO equivalent of Pydantic AI's `UsageLimits.check_before_request`"

### Dónde aparece

`arenas/letta/scripts/cost_obs_demo.py:192-195`, en el docstring de `diy_pre_spend_check`:

> *"Letta has NO equivalent of Pydantic AI's `UsageLimits.check_before_request` — its `Usage` type
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-letta-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->
> only exists AFTER the call, and `max_steps` caps the agent LOOP server-side, it is not a
> token/cost budget. So this pre-call check is entirely the developer's."*

La capacidad comparada es: **un gate que rechaza ANTES de gastar tokens, con presupuesto de
tokens/costo**.

### El lado de Pydantic AI — verificado en fuente

`pydantic_ai/usage.py:492-512`:
```python
def check_before_request(self, usage: RunUsage) -> None:
    """Raises a `UsageLimitExceeded` exception if the next request would exceed any of the limits."""
```
Chequea `request_limit`, `input_tokens_limit`, `total_tokens_limit` y **`cost_limit`**.
https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/usage.py

La referencia del arena es **exacta**.

### 1. Core — Letta OSS

Acá el barrido encuentra algo que la afirmación no contempla: **`CreditVerificationService`**.

`letta/services/credit_verification_service.py` —
https://github.com/letta-ai/letta/blob/main/letta/services/credit_verification_service.py
```python
class CreditVerificationService:
    """Service for verifying organization credit balance before agent execution."""

    async def verify_credits(self, organization_id: str, agent_id: str) -> bool:
```
Y **está cableado en el loop del agente**: `letta/agents/letta_agent_v2.py:61` (import), `:110`
(`self.credit_verification_service = CreditVerificationService()`), `:232-242` (espera el
`credit_task` de la iteración previa antes de correr el próximo step y, si da falso, corta con
`StopReasonType.insufficient_credits`), `:270` (dispara el chequeo en paralelo con el overhead del
loop).

O sea: **existe un gate pre-step por ORGANIZACIÓN —es decir, por tenant— en el core OSS.** Eso es
más de lo que la afirmación concede. Pero tiene tres limitaciones, las tres leídas en la fuente:

1. **Inerte por defecto.** `if not self.endpoint or not self.auth_key: return True`. Requiere
   `STEP_ORCHESTRATOR_ENDPOINT` y `STEP_COMPLETE_KEY`, que apuntan a un servicio interno de Letta
   no documentado ni provisto por el OSS.
2. **Hoy no puede bloquear.** **Todas** las ramas retornan `True`, incluida la de crédito
   insuficiente, que sólo loguea *"Insufficient credits would have fired for organization …"*, con un
   comentario en el código: *"We need to test why this is firing in production."* Fail-open por
   diseño actual.
3. **No es un presupuesto de tokens/costo.** Es un booleano `hasMoreCredits` contra un servicio
   remoto. No hay `input_tokens_limit`, `total_tokens_limit` ni `cost_limit` como en pydantic-ai.

Lo demás del core confirma la afirmación:
- `letta/schemas/usage.py` — `LettaUsageStatistics` es un objeto de **resultado**
  (`completion_tokens`, `prompt_tokens`, `total_tokens`, `step_count`), poblado después de la
  interacción.
- `letta/settings.py` (648 líneas) — un grep por `budget|token_limit|max_tokens|quota|rate_limit|spend|cost`
  devuelve **tres** hits, ninguno un presupuesto de gasto:
  `gemini_force_minimum_thinking_budget` (:217), un flag de traces a ClickHouse (:393) y
  `archival_memory_token_limit` (:463, límite de memoria archival, no de gasto).
- `max_steps` — confirmado como tope de **iteraciones del loop**:
  `letta/agents/letta_agent_v2.py:233` `for i in range(max_steps)`, con
  `StopReasonType.max_steps` al agotarse (:408-409). Expuesto en el SDK
  (`letta_client/resources/agents/agents.py:887`). La lectura del arena es correcta.

### 2. Extensiones oficiales
Ninguna encontrada que agregue presupuesto de tokens/costo pre-call. Etiqueta: **NONE**
(no exhaustivo).

### 3. Plataforma — Letta Cloud
`BillingContext(plan_type, cost_source: 'quota'|'credits', customer_id)`
(`letta/schemas/provider_trace.py:13-18`) más el `CreditVerificationService` de arriba son
**la mitad OSS de un sistema de cuota que vive en Cloud**. La doc de pricing
(https://docs.letta.com/letta-agent/pricing) describe cuota y créditos, pero al agotarse el plan
personal *"you can continue at pay-as-you-go API rates"* y en el plan API *"you'll be notified by
email when approaching your spending limit"* — o sea, **notificación y overage, no rechazo
pre-request**. No pude confirmar que Cloud rechace un request antes de correrlo.

### 4. Integraciones oficiales con terceros
No se encontró ninguna documentada para presupuesto pre-call. Etiqueta: **NONE**.

### 5. Docs del vendor
No se encontró guía de presupuesto/cuota para self-hosted. El único control de gasto documentado
para OSS es BYOK (traés tu propia key del proveedor) — es decir, el control lo hace el proveedor,
no Letta.

### Veredicto B — **MÁS ANGOSTA**

La comparación con pydantic-ai es correcta en su sustancia (no hay presupuesto de tokens/costo
pre-call), pero **"NO equivalent" es demasiado fuerte**: hay un gate pre-step por organización
cableado en el loop del agente. Que sea inerte por defecto y fail-open no lo vuelve inexistente —
lo vuelve *no utilizable*, que es otra cosa y hay que decirla como tal.

**Frase exacta de reemplazo** para el docstring de `cost_obs_demo.py:192-195`:

> DIY gate. Letta no tiene un presupuesto de tokens/costo pre-call como el
> `UsageLimits.check_before_request` de Pydantic AI (`pydantic_ai/usage.py:492`, que chequea
> `request_limit`/`input_tokens_limit`/`total_tokens_limit`/`cost_limit`): su `LettaUsageStatistics`
> es un objeto de resultado y `max_steps` topea el LOOP del agente
> (`letta/agents/letta_agent_v2.py:233`), no el gasto. Lo más cercano en el core es
> `CreditVerificationService.verify_credits(organization_id, agent_id)`
> (`letta/services/credit_verification_service.py`), un chequeo booleano de créditos por
> organización cableado antes de cada step (`letta_agent_v2.py:232-242`) — pero es **inerte sin
> `STEP_ORCHESTRATOR_ENDPOINT`/`STEP_COMPLETE_KEY`** (un servicio interno de Letta, no provisto por
> el OSS) y **hoy retorna `True` en todas sus ramas**, incluida la de crédito insuficiente, que sólo
> loguea. Así que en la práctica el chequeo pre-call sigue siendo del desarrollador.

---

## Afirmación C — "it's the only framework that has productized sleep-time consolidation"
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-letta-2026-08-04.md — this document IS that sweep; the line above is the claim it examines. -->

### Dónde aparece

`arenas/letta/FEATURES.md:322`, en el Summary.

**Es una afirmación de exclusividad.** Por eso el punto 1 del barrido no es Letta: es **cada uno de
los otros frameworks** y sus ecosistemas. Y es la más frágil: alcanza **un solo** contraejemplo.
El universo relevante son los 12 frameworks del arena (`arenas/README.md`): Pydantic AI, Mastra,
LangGraph, Eino, ADK Go, Genkit Go, Letta, Dify, Cloudflare Agents, Spring AI, Flue, eve.

### El lado de Letta — verificado, y es sólido

Sleep-time es **NATIVE en el core OSS**, no sólo en Cloud:
- `letta/schemas/group.py:15-16, 72-82, 119-123` — `ManagerType.sleeptime` y
  `ManagerType.voice_sleeptime`; `class SleeptimeManager(ManagerConfig)` con
  `manager_agent_id` y `sleeptime_agent_frequency`.
  https://github.com/letta-ai/letta/blob/main/letta/schemas/group.py
- SDK `letta-client==1.12.1`: `AgentType` incluye `"sleeptime_agent"` y `"voice_sleeptime_agent"`
  (`letta_client/types/agent_type.py`); `agent_create_params.py:117-118`
  `enable_sleeptime: Optional[bool]` — *"If set to True, memory management will move to a background
  agent thread."*

Nadie discute que Letta lo tiene y lo productizó. La pregunta es si es **el único**.

### 1. "Core" — los otros frameworks y sus ecosistemas

**El contraejemplo que rompe la afirmación: `langmem`.**

- **Qué es:** paquete first-party de LangChain (org `langchain-ai`), MIT, construido sobre el store
  de LangGraph — y **LangGraph está en el arena**.
  https://github.com/langchain-ai/langmem
- **Background memory manager:** `create_memory_store_manager`. La doc dice que las memorias se
  extraen *"'subconsciously' … automatically from conversations"* y que *"the agent will continue as
  normal while memories are processed in the background"*.
  https://langchain-ai.github.io/langmem/background_quickstart/
- **Y explícitamente diferido por inactividad —o sea, literalmente sleep-time:**
  *"Instead of processing each message immediately for long-term memory management, you can wait for
  conversation activity to settle."* El mecanismo es `ReflectionExecutor` con
  `executor.submit(to_process, after_seconds=delay)` y un comentario en el propio ejemplo:
  `delay = 0.5 # In practice would choose longer (30-60 min)`. Cancela tareas viejas cuando llegan
  mensajes nuevos (debounce) y procesa sólo tras el delay.
  https://langchain-ai.github.io/langmem/guides/delayed_processing/
- **Y hace consolidación, no sólo extracción:** la rutina de fondo *"merges similar memories,
  summarizes old conversations, and deletes flagged entries"*, escribiéndolos de vuelta al store.
  https://langchain-ai.github.io/langmem/concepts/conceptual_guide/

Extracción en background + diferimiento hasta que la conversación se aquieta + merge/summarize/prune
de memorias es la misma capacidad que Letta llama sleep-time consolidation, con una API dedicada.
**Eso es productizado.**

**Contraejemplo secundario, más débil: `mem0`.** `async_mode` es **default `True`** en la
plataforma: las adiciones de memoria se encolan para ejecución en background y devuelven un event id.
https://docs.mem0.ai/platform/features/async-mode-default-change ·
https://github.com/mem0ai/mem0/blob/main/docs/open-source/features/async-memory.mdx
Es consolidación en background real (mem0 decide ADD/UPDATE/DELETE con LLM), pero disparada **por la
escritura**, no por la inactividad. Cuenta como background, no exactamente como *sleep-time*.

**Zep / Graphiti:** el paper describe consolidación (comunidades vía label propagation, aristas
bitemporales con `valid_at`/`expired_at`/`invalid_at`) — https://arxiv.org/html/2501.13956v1 — pero
**no pude confirmar en fuente primaria** que corra en una ventana de inactividad. Lo dejo como **no
verificado**, no como contraejemplo.

### 2. Extensiones oficiales
`langmem` **es** el hallazgo del punto 2 (extensión first-party de LangChain sobre LangGraph).
Etiqueta: **OFFICIAL-EXTENSION**.

### 3. Plataforma
LangGraph Platform incluye el long-term memory store por defecto según la doc de langmem. mem0
Platform es cloud (mem0 OSS también trae async memory). No profundicé más: la afirmación ya cayó.

### 4. Integraciones oficiales con terceros
mem0 y Zep son integraciones documentadas de varios frameworks del arena. mem0 es self-hosteable
(OSS) además de cloud.

### 5. Docs del vendor
Las páginas citadas de langmem son docs oficiales de LangChain; el patrón está **prescrito**, con API
propia — no es un patrón sugerido sin primitivo.

### Tabla de madurez (con fechas verificadas en PyPI, 2026-08-04)

| Candidato | Capacidad | Etiqueta | Versión / último release |
|---|---|---|---|
| **Letta** (`letta`) | `SleeptimeManager`, `enable_sleeptime`, `sleeptime_agent` | **NATIVE** | `0.16.8` — **2026-05-14** |
| **langmem** (LangChain / LangGraph) | background memory manager + `ReflectionExecutor(after_seconds)` con debounce por inactividad + merge/summarize/prune | **OFFICIAL-EXTENSION** | `0.0.30` — **2025-10-27** (≈9 meses sin release; pre-1.0 — ver caveat) |
| **mem0** (`mem0ai`) | `async_mode=True` por default, consolidación LLM en background | **OFFICIAL-THIRD-PARTY-INTEGRATION** | `2.0.16` — **2026-08-04** (hoy) |
| **Zep / Graphiti** (`graphiti-core`) | consolidación de grafo temporal; ventana de inactividad **no verificada** | **NO VERIFICADO** | `0.29.3` — **2026-07-27** |

**Caveat de madurez, dicho de frente:** la norma dice que una extensión abandonada no cuenta como
"tiene". langmem está en `0.0.30` y sin release desde 2025-10-27. **No la considero abandonada** —
nueve meses no es abandono para un paquete pre-1.0, la doc sigue publicada y es first-party — pero
alguien podría argumentar que un `0.0.x` estancado no es "productizado" al nivel de Letta. Ese es el
argumento más fuerte que le queda a la afirmación original, y por eso la reescritura que propongo
**no** dice "Letta no es el único": dice exactamente qué se chequeó y con qué madurez.

### Veredicto C — **NO SE SOSTIENE**

La exclusividad cae. Y cae dos veces:

1. Por langmem (y, con matices, mem0): la capacidad existe fuera de Letta, productizada, en el
   ecosistema oficial de un framework que **está en el arena**.
2. **Por nosotros mismos.** El documento donde vive la frase compara contra aisotropy, y aisotropy
   tiene consolidación de fondo: `internal/sleepcycle/{coordinator,worker,record}.go`,
   `internal/consolidation/sink.go` y el worker Rust `crates/sleep-cycle/src/main.rs` (DataFusion,
   ADR-0005). Escribir "el único framework" en un doc comparativo cuyo otro sujeto tiene un paquete
   llamado literalmente `sleepcycle` es un error de rigor simétrico antes que un error de barrido.

**Frase exacta de reemplazo** para `FEATURES.md:322`:

> …y es el único de los 12 frameworks del arena cuyo **core** trae sleep-time consolidation como
> primitivo de primera clase (`SleeptimeManager` / `enable_sleeptime`,
> `letta/schemas/group.py:119`). No es el único que la tiene productizada en su ecosistema: langmem
> —extensión first-party de LangChain sobre LangGraph, MIT, `0.0.30` del 2025-10-27— expone un
> background memory manager con diferimiento explícito hasta que la conversación se aquieta
> (`ReflectionExecutor(after_seconds=…)`, con debounce) y merge/summarize/prune de memorias; y mem0
> (`2.0.16`) procesa adiciones de memoria en background por default. aisotropy también consolida
> fuera del camino del request (`internal/sleepcycle/`, `crates/sleep-cycle/`), con la diferencia
> de que la nuestra es determinista y la de Letta la maneja un agente LLM.

---

## Rigor simétrico — aisotropy bajo el mismo estándar

No negociable por la norma, y en este barrido hay dos frentes.

### Aislamiento — dónde está aisotropy en la escalera, con los bordes puestos

Esta parte ya está establecida y auditada en
[`docs/architecture/arena-tenancy-levels.md`](arena-tenancy-levels.md) § "aisotropy's honest
placement"; la repito acá porque la norma exige que el estándar aplicado a Letta viva **en el mismo
documento**:

- **Almacenamiento / tiers downstream: L3.** `internal/memory/key.go:96` — `Key.Validate()` rechaza
  componentes vacíos o con caracteres reservados antes de cualquier escritura; el prefijo
  `tenant:{t}:user:{u}:session:{s}` es compuesto, así que nombrar el scope de otro es
  irrepresentable. `internal/semantic/isolation_test.go` es un meta-test que rompe el build si el
  filtro de Qdrant deja de ser `Must:` in-scan.
- **Edge HTTP: L2 con auth configurada, L1 en NoAuth.** `aisotropy/authenv.go:106-108` exige
  `AISOTROPY_ALLOW_NOAUTH=1` explícito, y `AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1` como segunda guarda
  para bind no-loopback (`authenv.go:29-37`). **Pero en NoAuth el edge degrada a L1**: la identidad
  es lo que el caller ponga en la URL — **exactamente el mismo modo de falla que le señalamos a
  Letta con el header `user_id`**. La diferencia real no es de clase: es que Letta lo hace por
  defecto y nosotros exigimos dos opt-ins explícitos para llegar ahí.
- **Fachada de librería: scope obligatorio pero confiado.** `App.Turn` / `App.SeedHistory` exigen
  la terna `(tenant, user, session)` y la validan, así que no se puede *omitir* — pero **aceptan el
  string de tenant que el embebedor pase**. La procedencia de la identidad en esa capa es del
  embebedor, igual que en todos los L1 de enfrente.
- **La autodescripción honesta:** aisotropy tiene disciplina L3 de almacenamiento **río abajo de**
  una identidad que es L2 verificada en el edge HTTP (con auth configurada) y confiada en la
  fachada de librería. "Por construcción" es cierto del almacenamiento; **no** del edge, salvo que
  se diga explícitamente.

**Comparación honesta con el hallazgo de este barrido:** Letta OSS y aisotropy-en-NoAuth tienen el
mismo defecto estructural (identidad provista por el caller sin verificar). Lo que difiere es el
default y la cantidad de puertas que hay que abrir a propósito para llegar ahí. Ese es el argumento
que se puede defender; "ellos no tienen concepto de organización" no lo es, y además es falso.

### Sleep-time — nuestra propia exclusividad, medida con la misma vara

Si "productizado" fuera el criterio, aisotropy **no** califica: `internal/sleepcycle` corre siempre
(worker de outbox), pero el consolidador DataFusion (`crates/sleep-cycle`) está **dormido salvo que
se setee `AISOTROPY_SLEEPCYCLE_BIN`** — está en `.claude/rules/services-config.md` con esas palabras.
Un camino que hay que activar con una variable de entorno no es más "producto" que el
`CreditVerificationService` de Letta que critiqué arriba por exactamente eso. La diferencia
defendible es de **naturaleza** (dedup determinista sobre Parquet vs. un agente LLM reescribiendo
bloques de memoria), no de grado de productización.

---

## Qué no pude verificar

- **No levanté un servidor Letta.** Todo lo de arriba es lectura de fuente (`main` de
  `letta-ai/letta`, SDK 1.12.1 instalado) y de docs. En particular, **no ejecuté** un
  `client.blocks.list()` contra dos organizaciones reales para confirmar empíricamente que
  `apply_access_predicate` corta el cruce. La lectura del código es inequívoca, pero es lectura.
- **No verifiqué qué versión del servidor corresponde a `letta-client==1.12.1`.** Leí `main` del
  repo; el SDK pineado en el arena puede apuntar a un servidor anterior. Los hallazgos estructurales
  (Organization como raíz, header `user_id`, `apply_access_predicate`) son de larga data, pero la
  correspondencia exacta de versiones no está confirmada.
- **No barrí exhaustivamente los 12 frameworks del arena para sleep-time.** Encontré el
  contraexemplo que tira la afirmación (langmem) y paré. No puedo afirmar que Mastra, Dify,
  Cloudflare Agents, Flue o eve **no** lo tengan — y esa afirmación de ausencia requeriría su propio
  barrido, que es precisamente el error que este documento corrige.
- **Zep/Graphiti quedó sin resolver** para "consolidación en ventana de inactividad": la
  arquitectura de consolidación está documentada, el disparo por idle no.
- **No confirmé si Letta Cloud rechaza un request antes de correrlo** al agotarse la cuota. La doc
  de pricing describe overage y notificación por email, lo que sugiere que no, pero no encontré
  documentación de enforcement pre-request.
- **Los access tokens Cloud-only no los probé.** La etiqueta "Cloud-only" viene del título de la
  página de referencia del vendor; no verifiqué en el código del servidor OSS que la ruta
  `/v1/client-side-access-tokens` esté ausente.
- **`letta-ai/letta#2887` lo leí vía fetch de la página del issue**, que no mostró comentarios de
  mantenedores. No puedo distinguir "no hubo respuesta" de "el fetch no la trajo".
- **No barrí extensiones first-party de Letta** más allá del core y el SDK; puede haber paquetes
  `letta-*` que agreguen bind de identidad y no aparecieron en las búsquedas.

## Verificado contra fuente primaria vs. inferido

**Verificado contra fuente primaria (código leído, `file:line` citado):**
`Organization` como raíz del árbol de objetos; `create_organization_async`; routers admin de orgs y
users montados en el servidor OSS; `apply_access_predicate` filtrando por `actor.organization_id`;
el header `user_id` como fuente del actor con validación sólo de formato; `CheckPasswordMiddleware`
con contraseña única; `CreditVerificationService` y su cableado en `letta_agent_v2.py`, incluidos
sus retornos `True` universales; ausencia de settings de presupuesto en `letta/settings.py`;
`LettaUsageStatistics` como objeto de resultado; `max_steps` como tope de loop;
`SleeptimeManager` en el core; `AgentType`/`enable_sleeptime` en el SDK 1.12.1;
`Policy` de access tokens en el SDK; headers `X-Project-Id`/`X-Billing-*`;
`check_before_request` de pydantic-ai con `cost_limit`.

**Verificado contra docs del vendor (no código):**
access tokens etiquetados Cloud-only; deprecación de Identities y el patrón app-level;
cookbook de Supabase; semántica de cuota/overage de Letta Cloud; el background memory manager,
`ReflectionExecutor` y la consolidación (merge/summarize/prune) de langmem; `async_mode` de mem0.

**Inferido (no verificado directamente):**
Que la convención de deployment de un solo org es lo que hace que el escenario de under-scoping del
arena parezca una propiedad del framework; que langmem cuenta como "productizado" al mismo nivel que
el sleep-time de Letta (juicio, no hecho — el caveat de madurez está escrito arriba); que Letta Cloud
no hace enforcement pre-request.

## Correcciones pendientes (este documento no las aplicó)

| Archivo:línea | Acción |
|---|---|
| `arenas/letta/README.md:154` | reemplazar por la frase A |
| `arenas/letta/README.md:233` | reemplazar por la frase A |
| `arenas/letta/FEATURES.md:188` | reemplazar por la frase A para `FEATURES.md` (incluye "proven" → "modelado offline") |
| `arenas/letta/FEATURES.md:322` | reemplazar por la frase C |
| `arenas/letta/orbit/memory.py:28` | reemplazar por la frase A; además el docstring apunta a `docs.letta.com/guides/agents/multi-user` citando Identities, que el vendor ya deprecó |
| `arenas/letta/scripts/product_isolation_demo.py:29` | reemplazar por la frase A |
| `arenas/letta/scripts/cost_obs_demo.py:192-195` | reemplazar por la frase B |
| `arenas/README.md` (fila 1, columna Letta) | dice "CONVENTION org-wide"; con este barrido la lectura correcta es "L2 dado el actor / L1 por procedencia" |

## Cross-links

- `.claude/rules/capability-absence-claims.md` — la norma que exige este barrido.
- `docs/architecture/arena-tenancy-levels.md` — la escalera L0–L3 y la ubicación honesta de
  aisotropy que esta pieza reutiliza; este documento es el mismo ejercicio aplicado a Letta, que
  aquella tabla no cubría.
- `docs/architecture/arena-memory-extensions.md` — el documento hermano sobre claims de unicidad en
  memoria; la afirmación C es exactamente esa clase de error.
- `.claude/rules/causal-claims-about-runs.md` — el "proven in smoke.py" de `FEATURES.md:188` es un
  primo de la familia que esa regla cubre: una conclusión sobre lo que un run demostró, formada sin
  chequear qué tocó realmente el run.
