---
type: Report
title: Verificación de vulnerabilidades Go después de recuperar la rama
description: Alcance de dependencias y resultados reproducibles de la recuperación del 17 de octubre.
tags: [security, dependencies, go]
---

# Verificación Go, 2026-10-17

La referencia recuperada es `99e9ab15efa3b3d192312e65237b7a206a662efb`.
Los resultados de cada corrida quedan en
[evidence/go-vulns-2026-10-17](evidence/go-vulns-2026-10-17/).
Cada JSON conserva comando, revisión, entorno, exit y SHA256 de la configuración
Git común antes y después. Los logs grandes quedan en
`$GIT_COMMON_DIR/recovery/go-vulns-2026-10-17/` y se regeneran con el mismo comando.

## Alcance de las dependencias

El diagnóstico previo versionado está en
[govulncheck-before.log](../research/evidence/go-vulns-2026-10-16/govulncheck-before.log).
Sus campos `Fixed in` indican los mínimos aplicados: x/net v0.60.0,
gRPC v1.83.1 y Go 1.26.9. x/net v0.59.0 queda por debajo de la corrección
reportada. El detalle de los avisos sigue en
[el informe original](../research/go-vulns-2026-10-16.md).

La fase `graph` del [runner](../../scripts/verify-go-vulns-2026-10-17.py)
reproduce las restricciones transitivas: x/net v0.60.0 exige Go 1.26.0,
x/crypto v0.57.0, x/sys v0.48.0 y x/text v0.42.0. Este último exige
x/tools v0.49.0, x/mod v0.41.0 y x/sync v0.23.0. gRPC v1.83.1 exige
OpenTelemetry v1.44.0 y genproto/rpc `v0.0.0-20260526163538-3dc84a4a5aaa`.
Los módulos que ya exigían versiones superiores las conservan.

El comando `git diff --name-only 99e9ab1^ 99e9ab1 -- '**/go.mod' go.mod`
enumera los manifiestos tocados. `arenas/eino`, `arenas/genkit-go`,
`arenas/live/runner` y `bench/async` reciben solamente el pin del compilador.
Los demás cambios originales corresponden a las correcciones y sus mínimos
transitivos.

El escaneo independiente de los módulos anidados encontró avisos alcanzables
que el escaneo raíz no cubría. Los resultados rojos se conservan en
[before](evidence/go-vulns-2026-10-17/before/). Se aplicaron estos pins adicionales:

| Módulo | Avisos alcanzables | Pin mínimo aplicado |
| --- | --- | --- |
| `arenas/adk-go` | GO-2026-6505 | `go.opentelemetry.io/otel/sdk@v1.45.0` |
| `arenas/genkit-go` | GO-2026-6505, GO-2026-5506, GO-2026-4394 | `go.opentelemetry.io/otel/sdk@v1.45.0` |
| `arenas/live/eino` | GO-2026-6505 | `go.opentelemetry.io/otel/sdk@v1.45.0` |
| `arenas/live/genkit-go` | GO-2026-6505 | `go.opentelemetry.io/otel/sdk@v1.45.0` |
| `bench/streaming` | GO-2026-6443 | `google.golang.org/grpc@v1.83.2` |

Cada archivo `*-update.json` registra el `go get` de una versión fijada,
limitado a su módulo, con SHA256 de configuración e inputs. No se ejecutó
`go mod tidy`. SDK v1.45.0 exige la familia OTel v1.45.0 y go-logr v1.4.4.
En el Genkit antiguo también exige auto/sdk v1.2.1 y x/sys v0.47.0.
La API OTel v1.45.0 importa `attribute/internal/xxhash` y exige
`github.com/cespare/xxhash/v2 v2.3.0`. Se incorporó esa dependencia indirecta en
Genkit, con el [comando fijado](evidence/go-vulns-2026-10-17/arenas-genkit-go-resolve-xxhash.json)
sugerido por el compilador. Los errores de carga previos quedan en
[missing-sum-before](evidence/go-vulns-2026-10-17/missing-sum-before/).
El patch gRPC de streaming no arrastró otras versiones.

## Reproducir

En macOS/zsh, desde el worktree, con Go 1.26.9 ya instalado:

```sh
python3 scripts/verify-go-vulns-2026-10-17.py --config-sha256 aeaaeff73cb2d6720521e3c3a5743dceebafc4329ff6d09e6d51a6edcc48e11e --phase inspect
```

El hash corresponde a la configuración recuperada y revisada en esta sesión.
En otro clon hay que pasar el SHA256 de su configuración revisada. El runner
rechaza un hash diferente antes de lanzar procesos. También vigila el hash
durante cada corrida y detiene el grupo de procesos ante un cambio.

Se reemplaza `inspect` por `graph`, `download`, `build`, `vet`, `test`, `vuln`,
`modules`, `supply` o `suite` según el chequeo. Las fases pesadas usan
`scripts/with-heavy-lane.sh`, en serie, con `GOMAXPROCS=2` y `-p=2`.
El entorno elimina `GIT_*` heredadas y rechaza variables `SKIP_*=1`.
`-mod=readonly` y el control de hashes de manifiestos detectan cambios de alcance.
Las corridas finales también registran el fingerprint del código Go antes y
después, y fallan si las fuentes cambian durante la ejecución.

`modules` deriva la lista de módulos anidados del diff de manifiestos de
`99e9ab1` y ejecuta build, test y govulncheck en cada uno. Los ejemplos usan
`-tags keybroker`, como el control de supply-chain. Cada resultado conserva
su directorio, argumentos y exit en un JSON separado. `modules.json` agrupa
los resultados. El modo se detiene ante el primer fallo.
`--resume` reutiliza resultados del mismo código/configuración y completa el
inventario aun cuando un módulo tenga un fallo ordinario. El resultado global
sigue siendo rojo mientras exista ese fallo. Un cambio de configuración,
fuentes o HEAD siempre detiene la corrida.
Para reutilizar un resultado comprueba el hash del código Go, los manifiestos
del módulo y los de sus reemplazos locales. Un cambio de SDK en un módulo
independiente no invalida los resultados de otro. Los módulos modificados
repiten build, test y escaneo con el nuevo hash de inputs.

`suite` permite ejecutar además `scripts/test-all.sh --quick`.
Los tests se ejecutan sin `-trimpath` por la búsqueda de fixtures mediante
`runtime.Caller` documentada en el informe original. Build, vet y escaneo
conservan `-trimpath`.

## Resultados

| Fase | Resultado | Evidencia |
| --- | --- | --- |
| Compilador y recursos | exit 0 | [inspect.json](evidence/go-vulns-2026-10-17/inspect.json) |
| Restricciones transitivas | exit 0 | [graph.json](evidence/go-vulns-2026-10-17/graph.json) |
| Build raíz sobre fuente final | exit 0 | [build-final.json](evidence/go-vulns-2026-10-17/build-final.json) |
| Vet raíz | exit 0 | [vet.json](evidence/go-vulns-2026-10-17/vet.json) |
| Tests raíz, `-count=1` | exit 0 | [test.json](evidence/go-vulns-2026-10-17/test.json) |
| Escaneo raíz ampliado | exit 0, sin vulnerabilidades alcanzables | [vuln-verbose.json](evidence/go-vulns-2026-10-17/vuln-verbose.json) |
| Build, tests y escaneo de los 22 módulos anidados tocados | 66 resultados exit 0, inputs vigentes | [results-summary.log](evidence/go-vulns-2026-10-17/results-summary.log), [comandos por módulo](evidence/go-vulns-2026-10-17/modules.json) |
| Supply-chain completo | exit 0 | [supply.json](evidence/go-vulns-2026-10-17/supply.json), [log](evidence/go-vulns-2026-10-17/supply.log) |

El [escaneo ampliado](evidence/go-vulns-2026-10-17/vuln-verbose.log) distingue
el aviso importado [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443): un
panic de servidor gRPC ante headers de autoridad/Host ausentes. Está presente
en gRPC v1.83.1 y se corrige en v1.83.2. El código raíz no alcanza sus símbolos
afectados, así que el scanner termina en exit 0. La corrección de alcance
mínimo conserva v1.83.1 en raíz. El arreglo adicional sube gRPC a v1.83.2
en `bench/streaming`, donde el aviso era alcanzable.
Los avisos de módulo sobre `golang.org/x/crypto/openpgp` (GO-2026-5932, sin
versión corregida) quedan registrados como dependencia requerida sin paquete
afectado importado ni símbolos llamados. No se los cuenta como vulnerabilidades
alcanzables corregidas.

Los registros iniciales de vet y tests raíz preceden al agregado del
fingerprint al runner. Sus comandos y exits quedan conservados. Build final,
escaneo ampliado, supply-chain y los módulos tienen hashes de inputs.
La publicación usa el hook pre-push completo y conserva el monitor de
configuración y fuentes, sin `--no-verify`, variables de salto ni `-u`.
