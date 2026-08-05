---
type: Research
title: "Barrido de ausencia — las 32 afirmaciones del arena de Dify, agrupadas en cinco"
description: >-
  Dify aportaba 32 entradas al ledger de scripts/absence-claim-guard.py y nunca había tenido barrido de ningún tipo, porque hasta el 2026-08-04 su nombre no estaba en la lista COMPETITORS del guard y sus claims de ausencia eran invisibles. Las 32 líneas son cinco afirmaciones distintas. El barrido de cinco puntos que exige .claude/rules/capability-absence-claims.md sostiene tres (licencia, no-hay-librería-embebible, shared-schema sin RLS), angosta una (no existe product_id, pero "el schema tiene exactamente dos fronteras" es falso y ya lo era el día que se escribió) y refuta la quinta, que es nuestra: aisotropy no scopea por cuatro ejes, product_id no entra en ninguna clave de storage, así que en el eje de producto estamos en L0 igual que Dify.
tags: [architecture, arena, research, dify, langgenius, multi-tenancy, isolation, licensing, capability-absence, code-truth]
timestamp: 2026-08-04T00:00:00Z
---

# Barrido de ausencia — las 32 afirmaciones del arena de Dify

> **Por qué existe este documento.** `.claude/rules/capability-absence-claims.md` exige que toda
> afirmación de la forma "X no tiene Y" pase por un barrido de cinco puntos (core, extensiones
> oficiales, plataforma, integraciones oficiales de terceros, docs del vendor), con URL o
> `file:line` por hallazgo, etiqueta de madurez con fecha de release, ubicación en la escalera
> L0–L3 y rigor simétrico sobre aisotropy. `arenas/dify/` aporta **32 entradas** al ledger de
> `scripts/absence-claim-guard.py` y **nunca tuvo barrido**. No porque nadie lo escribiera: hasta
> el 2026-08-04 el nombre `dify` no estaba en la lista `COMPETITORS` del guard, así que sus claims
> de ausencia eran **invisibles** y el ledger lo mostraba con 1 entrada. Son afirmaciones sobre el
> producto de otra empresa, publicadas hace meses, sin que nadie verificara ninguna.

## 0. Método y fuentes

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md — este documento ES el barrido. -->

Barrido hecho el **2026-08-04**. Fuente primaria: clon real de `langgenius/dify` a `HEAD`
**`4213e6b150a17055f3bca6fe8e4a4f29670453fd`** (2026-08-04 14:11 UTC), release publicada más
reciente **`1.16.1`** (2026-07-28). Todos los `api/<path>:<line>` de abajo son de ese clon y
fueron leídos, no recordados. Comandos usados (reproducibles sobre el mismo clon):

```bash
git clone --depth 1 --filter=blob:none --sparse https://github.com/langgenius/dify.git
git -C dify sparse-checkout set api
grep -rn "product_id" api/                             # 7 hits, todos payment_product_id
grep -rniE "CREATE POLICY|ENABLE ROW" api/             # 2 hits, ambos comentarios Python
grep -rn "tenant_id ==" api/services/ | wc -l          # 480
grep -rn "current_tenant_id" api/services/ | wc -l     # 193
grep -rniE "with_loader_criteria|current_setting" api/models api/extensions api/libs   # 0
```

El arena original se escribió contra el commit `1aea3460af1d` (2026-07-16) y su propio README ya
advierte que los offsets `:NNN` no fueron re-chequeados. Este barrido re-verifica contra HEAD y,
donde importa la cronología, contra ese commit vía la API de contenidos de GitHub.

## 1. Las 32 líneas son cinco afirmaciones

| # | Afirmación | Líneas del ledger | Veredicto |
|---|---|---|---|
| **A** | Dify no tiene eje de producto: no existe `product_id` en ningún lado, el schema tiene **exactamente dos** fronteras (`tenant_id`, `app_id`), y el plano RAG no tiene columna de producto | 15 | **MÁS ANGOSTA** |
| **B** | Aislamiento shared-schema: `tenant_id` como columna filtrada 100% en código de aplicación, sin RLS ni schema-per-tenant; `Conversation`/`Message` no tienen `tenant_id`; la lectura sin gate está *disponible* | 12 | **SE SOSTIENE** |
| **C** | Dify no tiene librería in-process / embebible: la única forma de correr un turno es POST a un servidor vivo | 2 | **SE SOSTIENE** |
| **D** | La licencia propia de Dify (Apache-2.0 modificada §1a) prohíbe operar multi-tenant sin licencia comercial; es el único framework del arena set cuya licencia bloquea Orbit | 2 | **SE SOSTIENE** |
| **E** | *(nuestra)* aisotropy ata los **cuatro** ejes `(tenant, user, session, product)` desde `context.Context`, enforced por statement; la lectura cross-product es *irrepresentable* | 1 | **NO SE SOSTIENE** |

Distribución por archivo: `arenas/dify/FEATURES.md` (6), `README.md` (3), `DX-NOTES.md` (3),
`orbit/agent.py` (1), `orbit/models.py` (2), `orbit/store.py` (3), `scripts/smoke.py` (5),
`scripts/product_isolation_demo.py` (8), `tests/test_product_isolation.py` (1).

---

## 2. Afirmación A — "Dify no tiene concepto de producto"

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

La afirmación tal como está publicada tiene tres partes, y **no corren juntas**.

**A1 — "no existe `product_id` en ningún lado de `api/models`".** `grep -rn "product_id" api/`
devuelve 7 hits a HEAD: `ProviderOrder.payment_product_id`
(`api/models/provider.py:224`, un SKU de pago), su migración
(`api/migrations/versions/bf0aec5ba2cf_add_provider_order.py:35,55`) y cuatro usos en tests. No
hay ninguna columna `product_id` que sea un eje de scope. **Se sostiene.**

**A2 — "el schema tiene exactamente dos fronteras: workspace (`tenant_id`) y app (`app_id`)".**
**Falso a HEAD, y —esto es lo caro— ya era falso el día que se escribió.** A HEAD hay claves de
scope sub-tenant sobre planos de datos reales:

| Tabla | Clave de scope | `file:line` |
|---|---|---|
| `agent_workspaces` | único sobre `(tenant_id, owner_type, owner_id, owner_scope_key, active_guard)` | `api/models/agent.py:464-482` |
| `agent_workspace_bindings` | índices sobre `(tenant_id, workspace_id, status)` y `(tenant_id, agent_id, status)`; columnas `tenant_id`, `app_id`, `workspace_id`, `agent_id` | `api/models/agent.py:502-524` |
| `agent_drive_files` | `UniqueConstraint("tenant_id", "agent_id", "key")` — un plano KV por agente | `api/models/agent.py:568` |

La parte cronológica: `api/models/agent.py` **existía en el commit `1aea3460af1d` que el arena
leyó**, con 25.829 bytes (`GET /repos/langgenius/dify/contents/api/models/agent.py?ref=1aea3460af1d`
→ 200, sha `467d7a4b7532730f9a2265f821e31216ea221d79`). Las migraciones que crean esas tablas son
`2026_05_18_1330-c6a9f4b12d3e_add_agent_domain_models.py` y
`2026_06_08_1339-7bad07dc267d_add_agent_drive_files_agent_drive_kv.py`, ambas anteriores al
2026-07-16. O sea: no es decay, es que el conteo "exactamente dos" nunca se verificó.

Una trampa que hay que nombrar para no sobrecorregir: `AgentWorkspaceOwnerType`
(`api/models/agent.py:126-132`) tiene el docstring **"Product scope that owns a Workspace"** y
`AgentWorkspace` (`:464`) dice **"Mutable Workspace owned by one product scope"**. Suena a que
Dify sí modela "producto". No: sus valores son `WORKFLOW_RUN`, `CONVERSATION`, `BUILD_DRAFT` —
"product scope" ahí significa *qué superficie del producto Dify* creó el workspace, no un producto
del cliente. **No rescata el cuarto eje.** Es exactamente el tipo de coincidencia de nombre que
`gates-sin-trampa` clasifica como coincidencia, no como deuda.

**A3 — "el plano RAG no tiene columna de producto/app".** `Dataset`
(`api/models/dataset.py:180`) y `DocumentSegment` (`api/models/dataset.py:919`) llevan `tenant_id`
y ninguna columna de app/agente/producto — los mismos offsets que el arena citó, todavía exactos.
El backend Qdrant filtra in-scan por `group_id` = `dataset_id`
(`api/providers/vdb/vdb-qdrant/src/dify_vdb_qdrant/qdrant_vector.py:291-296`, `models.Filter(must=[FieldCondition(key="group_id", ...)])`),
no por tenant: el scope de tenant es transitivo vía `Dataset.tenant_id`. **Se sostiene.**

### Los cinco puntos

1. **Core** — A1 y A3 verificados arriba contra HEAD `4213e6b1`. A2 refutado.
2. **Extensiones oficiales** — `langgenius/dify-official-plugins` (Apache-2.0, push 2026-08-04),
   `dify-plugin-sdks`, `dify-plugin-daemon`. Los plugins se instalan **por workspace**
   (el daemon reporta "which plugin was installed to which workspace",
   https://github.com/langgenius/dify-plugin-daemon): la unidad sigue siendo el workspace, ninguna
   extensión introduce un eje sub-workspace de producto. Los 30 backends de vector store se
   movieron a paquetes `api/providers/vdb/vdb-*` con entry points `dify.vector_backends`
   (`api/core/rag/datasource/vdb/vector_backend_registry.py:1-14`); ninguno agrega columna de
   producto — reciben `group_id = dataset_id`.
3. **Plataforma** — Dify Enterprise contesta el eje con **un workspace por cliente**:
   "Only Enterprise Includes: Multiple Workspaces & Enterprise Management" y "One Dify cluster,
   many business units. Tenant-level quotas, billing, and access policies"
   (https://dify.ai/dify-enterprise). La Community Edition es **"Single Workspace"** según esa
   misma página. Esto es relevante para A: la respuesta del vendor al "muchos productos/clientes"
   no es un `product_id`, es *comprar la Enterprise y hacer un workspace de cada uno*.
4. **Integraciones oficiales de terceros** — deployments de marketplace (AWS, Azure) y la
   integración de Alibaba Cloud DMS exponen la consola de workspaces; ninguna agrega eje de scope.
   Los proveedores de vector store (Qdrant y compañía) sí tienen multitenancy propia por payload,
   pero Dify no la plumbea por producto: el único filtro que emite es `group_id`.
5. **Docs del vendor** — https://docs.dify.ai/en/use-dify/workspace/readme describe el workspace
   como la unidad que contiene "applications, knowledge bases, team members, model configurations,
   integrations, and billing" y afirma que "your resources are completely isolated from other
   organizations". **Patrón prescrito**, no primitivo: la frontera que el vendor documenta es el
   workspace, y por debajo del workspace no prescribe ninguna.

### Madurez

| Hallazgo | Etiqueta | Último release / push |
|---|---|---|
| `product_id` como eje de scope, en core | **NONE** | — (HEAD 2026-08-04) |
| Columna de producto en el plano RAG | **NONE** | — (HEAD 2026-08-04) |
| Ejes sub-tenant reales (`agent_workspaces`, `agent_drive_files`) | **NATIVE** | release 1.16.1, 2026-07-28 |
| Workspace-por-cliente como respuesta al eje | **PLATFORM** (Enterprise, de pago) | página vigente 2026-08-04 |

### Veredicto A — MÁS ANGOSTA

Lo que sobrevive: *no existe eje de producto* y *el plano RAG no tiene frontera de producto*. Lo
que hay que borrar: *"exactamente dos fronteras"*. Frase exacta de reemplazo, para los tres
lugares que la publican (`arenas/dify/DX-NOTES.md`, `FEATURES.md`,
`scripts/product_isolation_demo.py`):

> El schema no modela un eje de producto: no existe ninguna columna `product_id` de scope en
> `api/models` (el único `payment_product_id` es un SKU de pago en `ProviderOrder`). Las fronteras
> que sí modela son el workspace (`tenant_id`), la app (`app_id`) y —desde las migraciones de
> agentes de 2026-05/06— el agente y su workspace de trabajo (`agent_workspaces`,
> `agent_drive_files`, con clave `(tenant_id, agent_id, …)`); ninguna de ellas es un producto del
> cliente, y el plano RAG (`Dataset`/`DocumentSegment`) no lleva columna de app, agente ni
> producto.

---

## 3. Afirmación B — shared-schema, sin RLS, `Conversation` sin `tenant_id`

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

1. **Core** — todo verificado a HEAD `4213e6b1`:
   - `class Conversation(Base)` — `api/models/model.py:1162`; su única clave de pertenencia es
     `app_id` (`:1189`). No hay `tenant_id`.
   - `class Message(Base)` — `api/models/model.py:1535`; `app_id` y `conversation_id`, sin
     `tenant_id` en toda la clase.
   - `class App(Base)` — `api/models/model.py:406`, `tenant_id` en `:421`, índice
     `app_tenant_id_idx`.
   - **0 RLS**: `grep -rniE "CREATE POLICY|ENABLE ROW" api/` devuelve 2 hits y **ninguno es SQL** —
     son comentarios Python que dicen "Create policy based on billing configuration"
     (`api/schedule/clean_messages.py:30`, `api/commands/retention.py:1517`). Esto es un chequeo
     que podía refutar la afirmación y no la refutó.
   - **480 predicados `tenant_id ==` escritos a mano** en `api/services/` y 193 usos de
     `current_tenant_id`, contra **0** mecanismos de guardia global: no hay
     `with_loader_criteria`, ni session scopeada, ni `current_setting` de Postgres en
     `api/models`, `api/extensions` ni `api/libs`. Esa relación 480-a-0 es la afirmación B en
     forma de número.
   - El servicio propio de Dify **mantiene** el gate: `api/services/conversation_service.py:53`
     filtra por `Conversation.app_id == app_model.id` con `app_model` ya resuelto por tenant. La
     lectura sin gate es la del caller, no la de Dify — que es exactamente lo que el arena dice.
2. **Extensiones oficiales** — ninguna agrega RLS. El plugin daemon corre plugins como subproceso
   (STDIN/STDOUT), runtime de debug (TCP) o serverless (AWS Lambda)
   (https://github.com/langgenius/dify-plugin-daemon): el scope que transporta es el workspace,
   por parámetro.
3. **Plataforma** — Enterprise agrega RBAC, SSO (SAML/OIDC/OAuth2), MFA y multi-workspace
   (https://dify.ai/dify-enterprise). Eso es enforcement de **identidad**, no una garantía a nivel
   de base de datos. Si Enterprise agrega RLS no se puede saber desde el OSS — ver §8.
4. **Integraciones oficiales de terceros** — las guías de deployment (AWS/Azure marketplace,
   Alibaba DMS) despliegan el mismo Postgres compartido; ninguna prescribe RLS ni
   schema-per-tenant.
5. **Docs del vendor** — la doc de workspace afirma aislamiento completo entre organizaciones
   (https://docs.dify.ai/en/use-dify/workspace/readme). **Patrón prescrito** y, notablemente, es
   *cierto para el usuario de Dify*: el código propio de Dify escribe el gate. La afirmación del
   arena es sobre el **desarrollador que construye sobre el source**, y el arena ya lo dice con
   todas las letras ("not a Dify defect", "the naive caller's omission"). Esa distinción es lo que
   la salva.

### Madurez

| Hallazgo | Etiqueta | Fecha |
|---|---|---|
| RLS / schema-per-tenant en core | **NONE** | HEAD 2026-08-04 |
| Bind identidad→tenant con chequeo de membresía | **NATIVE** | `api/models/account.py:166`, release 1.16.1 (2026-07-28) |
| Guardia de query global (que impida olvidar el filtro) | **NONE** | HEAD 2026-08-04 |
| RBAC / multi-workspace enforcement | **PLATFORM** (Enterprise, de pago) | página vigente 2026-08-04 |

### Veredicto B — SE SOSTIENE

Las 12 líneas quedan como están y se anotan. El único matiz que vale agregar, y que el arena ya
tenía: `set_tenant_id_with_session` (`api/models/account.py:166-177`) **no lanza** cuando no
encuentra el `TenantAccountJoin`, hace `return` y deja `_current_tenant` sin setear — falla
cerrado río abajo, pero en silencio en el punto del bind.

---

## 4. Afirmación C — "Dify no tiene librería in-process / embebible"

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

1. **Core** — `sdks/` a HEAD contiene `nodejs-client` y `php-client`, y `sdks/README.md` apunta a
   `langgenius/java-client`, `langgenius/dify-sdk-go` y `langgenius/ruby-sdk`. **Todos son
   clientes REST del Service-API.** No hay modo embebido.
   **Novedad que hay que declarar:** a HEAD aparecen `dify-agent/` (paquete Python, `version =
   "1.16.1"`) y `dify-agent-runtime/` (módulo Go, servidor `shellctl`). Podría parecer la
   refutación; no lo es: `dify-agent` **no está publicado en PyPI**
   (https://pypi.org/pypi/dify-agent/json → 404) y su extra `server` depende de `fastapi`, `e2b`,
   `grpclib` y `redis` — es un servicio con su propio Dockerfile
   (`dify-agent-runtime/docker/Dockerfile`), no una librería que uno importe para correr Dify
   en proceso.
2. **Extensiones oficiales** — los plugins **no corren in-process**: el daemon los arranca como
   subproceso y habla por STDIN/STDOUT, o por TCP, o los invoca en Lambda por HTTP
   (https://github.com/langgenius/dify-plugin-daemon). `dify-sandbox` (Apache-2.0, push
   2026-07-31) es otro servicio aparte.
3. **Plataforma** — Dify Cloud expone el mismo `POST /v1/chat-messages`. Sigue siendo REST.
4. **Integraciones oficiales de terceros** — `dify-client` en PyPI (MIT) es "A package for
   interacting with the Dify Service-API", **último release 0.1.10 del 2023-11-15**
   (https://pypi.org/pypi/dify-client/json). `langgenius/dify-sdk-go` está **archivado**, último
   push 2024-10-31, sin licencia declarada. Los clientes de comunidad (`dify-api-python`,
   `haoyuhu/dify-client-python`) son igualmente HTTP.
5. **Docs del vendor** — la superficie documentada para desarrolladores es el Service-API por app
   con una key `app-…`.

### Madurez

| Hallazgo | Etiqueta | Último release |
|---|---|---|
| Runtime Dify embebible in-process | **NONE** | — |
| Clientes REST oficiales (node, php, java, ruby) | **OFFICIAL-EXTENSION** | repos activos 2026-08-04 |
| `dify-client` Python | **OFFICIAL-THIRD-PARTY-INTEGRATION** (MIT, self-host OK) | 0.1.10, **2023-11-15** (abandonado) |
| `dify-sdk-go` | **COMMUNITY-ONLY** (fork archivado) | push 2024-10-31 |
| `dify-agent` / `dify-agent-runtime` | **NATIVE** pero servicio, no librería; no publicado | in-repo, release 1.16.1 (2026-07-28) |

### Veredicto C — SE SOSTIENE

Se anotan las dos líneas. Vale agregar en el arena, sin cambiar la afirmación, que a HEAD el repo
trae un runtime de agente propio (`dify-agent`, `dify-agent-runtime`) que **sigue siendo un
servicio**, para que la próxima lectura no lo descubra como si refutara algo.

---

## 5. Afirmación D — la licencia

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

1. **Core** — `LICENSE` en `main` (© 2025 LangGenius, Inc.) es Apache-2.0 **modificada**, y §1a
   dice textualmente que, salvo autorización escrita de Dify, "you may not use the Dify source
   code to operate a multi-tenant environment", definiendo que "one tenant corresponds to one
   workspace" (https://github.com/langgenius/dify/blob/main/LICENSE). Verbatim idéntico al citado
   en `arenas/dify/README.md`. Trae además la cláusula de LOGO/copyright sobre `web/`.
2. **Extensiones oficiales** — `dify-official-plugins`, `dify-plugin-daemon`, `dify-plugin-sdks` y
   `dify-sandbox` son **Apache-2.0 limpio**; `dify-docs` es CC-BY-4.0. La restricción vive
   únicamente en el repo principal — que es el que hay que operar.
3. **Plataforma** — https://dify.ai/dify-enterprise: "Commercial License Authorization" figura como
   exclusiva de Enterprise, la Community "Complies with Dify Open Source License" y ofrece
   **"Single Workspace"**. Es la confirmación comercial de §1a: el camino legal a multi-tenant es
   pagar.
4. **Integraciones oficiales de terceros** — las ofertas de marketplace (Azure/AWS "Dify
   Enterprise") se venden explícitamente como licencia Enterprise con capacidad multi-tenant.
5. **Docs / hilos del vendor** — hay hilos abiertos de la comunidad preguntando exactamente el caso
   de Orbit (https://github.com/langgenius/dify/issues/4685,
   https://github.com/langgenius/dify/discussions/6476) y la respuesta es consistente: self-host
   para tu propia empresa sí, revenderlo compartido no.

**La mitad comparativa** ("el único framework de este arena set cuya licencia bloquea el caso de
uso"): los demás arenas declaran MIT o Apache-2.0 en su propio README — adk-go (Apache-2.0),
cloudflare-agents (MIT), eino (Apache-2.0), eve (Apache-2.0), flue (Apache-2.0), genkit-go
(Apache-2.0), langgraph (MIT), letta (Apache-2.0), llamaindex (MIT), mastra (Apache-2.0),
pydantic-ai (MIT), spring-ai (Apache-2.0). Ninguno tiene restricción de campo de uso. **Se
sostiene**, con el alcance explícito de que está verificado contra los arenas que existen hoy.

### Veredicto D — SE SOSTIENE

Se anotan las dos líneas.

---

## 6. Afirmación E — rigor simétrico: la nuestra no se sostiene

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

El arena de Dify cierra cada sección de producto con la comparación:

> "aisotropy binds all four scope axes `(tenant, user, session, product)` from `context.Context`
> at the request boundary, enforced per-statement — the unscoped read is *unrepresentable*"
> (`arenas/dify/README.md:273-275`; equivalentes en `FEATURES.md:124` y `:161-164`,
> `scripts/product_isolation_demo.py:5-6` y `:102-103`)

**Es falsa, y el chequeo que la refuta es un `grep`:**

| Superficie | Componentes reales | `file:line` |
|---|---|---|
| Working memory (BadgerDB) | `Key{Tenant, User, Session}` — **tres** | `internal/memory/key.go:87` |
| Tenancy (value object) | `Scope{tenant, user}` | `internal/tenancy/scope.go:72` |
| Semantic (Qdrant/in-process) | `Scope{tenant, user}` | `internal/semantic/semantic.go:58` |
| Facts (FTS5/rqlite) | `Scope{tenant, user}` | `internal/facts/facts.go:54` |

`ProductID` en aisotropy **no es un eje de scope**: es un selector de persona/prompt. Su propio
doc-string lo dice — *"Product or module ID (selects the persona base prompt)"*
(`internal/api/ask.go:49`), `internal/api/aisdk.go:165` lo llama "Optional persona/product
selector", y `internal/api/persona.go:164` lo usa como clave de `PersonaKey`. Los únicos lugares
donde llega son logs estructurados (`internal/api/core.go:110`,
`internal/api/stream_core.go:192`) y la hidratación del system prompt
(`internal/api/bitrate_hydrator.go:43`). **Ningún store recibe `productID` como predicado.**

Es decir: en el eje de producto **aisotropy está en L0, exactamente donde el arena ubica a Dify**.
El documento audita a Dify por no tener una frontera que nosotros tampoco enforceamos. Ese es
justo el modo de falla que la norma llama "una comparación que se audita con indulgencia a sí
misma es marketing, no ingeniería".

Los otros dos bordes de nuestro L3, ya registrados en `docs/architecture/arena-tenancy-levels.md`
y que corresponde repetir acá porque el arena de Dify no los menciona:

- **Modo NoAuth**: con `AISOTROPY_ALLOW_NOAUTH=1` el edge degrada a L1 — la identidad es lo que el
  caller ponga en la URL.
- **Fachada de librería**: `App.Turn` exige la tripleta y la valida, pero acepta **cualquier**
  string de tenant que el embebedor pase; la *procedencia* de la identidad es trabajo del
  embebedor.

Esto coincide, encontrado por separado, con `docs/architecture/arena-sweep-adk-go-2026-08-04.md`,
que llegó al mismo hallazgo desde el arena de ADK Go el mismo día.

### Veredicto E — NO SE SOSTIENE

Frase exacta de reemplazo para `arenas/dify/README.md:273-277` y sus equivalentes:

> **vs. aisotropy:** aisotropy ata `(tenant, user, session)` desde `context.Context` en el borde
> de request y lo enforcea por statement — la lectura sin scope es *irrepresentable*, no
> simplemente evitable. En el eje de **producto**, en cambio, aisotropy está al mismo nivel que
> Dify: `product_id` es un selector de persona (`internal/api/ask.go:49`), no entra en ninguna
> clave de storage, así que la lectura cross-product tampoco está enforceada acá. Lo que sí
> diferencia a aisotropy es la licencia permisiva sin límite de campo de uso.

---

## 7. Escalera L0–L3

Dify no es una librería embebible sino una **plataforma**, así que la escalera hay que aplicarla
por capa — es la diferencia que el encargo pedía no asumir resuelta.

| Capa | Nivel | Justificación |
|---|---|---|
| **Bind identidad → tenant** | **L2** | `Account.set_tenant_id_with_session` (`api/models/account.py:166-177`) verifica una fila de `TenantAccountJoin` antes de dejar que una cuenta actúe como workspace. No podés cambiarte a un workspace del que no sos miembro. Es enforcement en un borde, y es mejor que la mayoría del arena set. |
| **Plano de datos por query** | **L1** | 480 `.where(tenant_id == …)` a mano en `api/services/`, cero guardias globales. Omitir el filtro compila, corre y devuelve datos de otro tenant. |
| **Plano de chat (`Conversation`/`Message`)** | **L1 transitivo** | No hay ni columna que chequear: el scope es transitivo por un `app_model` ya resuelto. Un `session.get(Conversation, id)` no tiene nada contra qué validar. |
| **Eje de producto** | **L0** | No existe el concepto. |
| **Tier Enterprise** | **L2 declarado, no verificado** | Multi-workspace + RBAC + SSO son enforcement de identidad; no es código auditable desde el OSS. |

Y aisotropy, con el mismo rasero y en el mismo documento:

| Capa | Nivel | Justificación |
|---|---|---|
| Storage (working/facts/semantic) | **L3** | Clave compuesta `(tenant, user, session)` validada antes de tocar disco; filtro `Must:` in-scan en Qdrant con meta-test que rompe el build si se degrada a post-filter. |
| Edge HTTP con auth configurada | **L2** | Bind must-agree entre el JWT verificado y el path; default-deny sin authenticator. |
| Edge HTTP en NoAuth | **L1** | La identidad es el path. |
| Fachada de librería | **entre L1 y L3** | El scope no se puede omitir, pero su procedencia la garantiza el embebedor. |
| **Eje de producto** | **L0** | `product_id` no entra en ninguna clave de storage. Igual que Dify. |

---

## 8. Qué no se pudo verificar

- **Si Dify Enterprise agrega RLS, schema-per-tenant o cualquier garantía a nivel de base que el
  OSS no tiene.** El tier Enterprise es cerrado; lo único público es la página de producto. La
  ubicación "L2 declarado, no verificado" de §7 es honesta sobre eso.
- **La frase "Multiple workspaces are supported when you need complete isolation between different
  legal entities, regulatory environments, or client projects"** apareció en un resumen de
  búsqueda atribuido a `docs.dify.ai/en/use-dify/workspace/readme`, pero el fetch directo de esa
  página **no la contiene**. No se usa como evidencia. El punto que sostenía —que el vendor
  contesta el eje con workspace-por-cliente— queda sostenido por la página de Enterprise, que sí
  fue leída directamente.
- **El marketplace de plugins no fue enumerado exhaustivamente.** Se revisaron los repos oficiales
  de la organización `langgenius`, no los cientos de plugins de comunidad. Un plugin que
  introdujera un eje de producto sería, de todos modos, **COMMUNITY-ONLY**.
- **Los offsets `:NNN` del arena contra el commit `1aea3460af1d`** no se re-chequearon: este
  barrido re-verificó contra HEAD `4213e6b1`. Coincidencia notable: `dataset.py:180` y
  `dataset.py:919` siguen siendo exactos; `account.py:166` también; `model.py:1141` se movió a
  `:1162` y `model.py:417` a `:421`.
- **Los 30 backends de vector store** se auditó sólo Qdrant en cuanto a forma del filtro. Los otros
  29 no se leyeron uno por uno.

## 9. Qué se anotó y qué se corrigió en `arenas/dify/`

| Archivo | Anotadas (`SWEPT:`) | Corregidas |
|---|---|---|
| `README.md` | claim B | claim A2 ("exactly two boundaries" no aparece acá) y claim E (`vs. aisotropy`) |
| `FEATURES.md` | claims A1/A3, B | claim A2 y la premisa de cuatro ejes |
| `DX-NOTES.md` | claims A3, D | claim A2 |
| `orbit/agent.py` | claim C | — |
| `orbit/models.py` | claims B, C | — |
| `orbit/store.py` | claim B | — |
| `scripts/smoke.py` | claim B | — |
| `scripts/product_isolation_demo.py` | claims A1/A3 | claim A2 y la comparación con aisotropy |
| `tests/test_product_isolation.py` | claim A1 | — |

No hay claims de Dify en JSON, así que no quedó ninguna sin poder anotar por formato.
`arenas/live/dify/` no existe.

## 10. Cross-links

- `.claude/rules/capability-absence-claims.md` — la norma que exige este barrido.
- `docs/architecture/arena-tenancy-levels.md` — la escalera L0–L3 y la ubicación honesta de
  aisotropy que §6 y §7 reusan.
- `docs/architecture/arena-sweep-adk-go-2026-08-04.md` — llegó al mismo hallazgo sobre el cuarto
  eje de aisotropy, por otro camino, el mismo día.
- `docs/architecture/arena-sweep-letta-2026-08-04.md`,
  `docs/architecture/arena-sweep-langgraph-state-locality-2026-08-04.md` — barridos hermanos.
- `scripts/absence-claim-guard.py` — el ledger que hizo visible esta deuda al agregar `dify` a
  `COMPETITORS`.
- `.claude/rules/causal-claims-about-runs.md` — el chequeo desconfirmatorio de §3 (los 2 hits de
  `CREATE POLICY` que resultaron ser comentarios) es una aplicación directa.
