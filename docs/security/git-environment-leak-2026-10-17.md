# Fuga del entorno Git durante pre-push

Revisión del 2026-10-17, agente Codex. La fuga está en aisotropy: los helpers de
fixtures y los ejecutores host-side usaban `git -C <directorio>` conservando el
entorno del hook. Una variable como `GIT_DIR` puede seleccionar otro repositorio
aunque cambie el directorio de trabajo. El control negativo de
[`scripts/evidence-git-environment-leak-2026-10-17.py`](../../scripts/evidence-git-environment-leak-2026-10-17.py)
reproduce una escritura en el repositorio señuelo con esa misma operación.

La entrada fue `scripts/pre-push-gate.sh`, que ejecuta
`scripts/test-all.sh --quick`, incluida la suite raíz. En el incidente, el hook
compartido era un symlink absoluto al script del clon principal. Ese enlace
también impedía que un arreglo del script en el worktree se ejecutara al pushear.

Los helpers identificados son `initRepo` y `commitInWorktree` en
`internal/sandbox/worktree_test.go`, y `gitInit`, `gitRun` y `gitOut` en
`internal/taskagent/executor_test.go`. Escriben la identidad de fixture
`t <t@t>` y los commits `seed` o `initial`. `sandboxExecutor.gitCommit` en
`internal/taskagent/executor.go` agrega la identidad `aisotropy <aisotropy@local>`
y los mensajes que le pasan los tests.

El archivo original `pre-push-root-tests.log`, conservado en la evidencia del
encargo 13, relaciona directamente `TestGitCommitAddFailure` con
`ffd2635b58f9b901ca09be341a580afdff6af41d`, mensaje `msg`.
`TestSandboxExecutor_GitCommitHostSide` muestra `2696683 add note` seguido de los
commits de fixture y de `99e9ab1`. Por eso la atribución al ejecutor está probada
por el SHA del resultado, además de la coincidencia de mensajes y autores.
La configuración `user.name=t` y `user.email=t@t` coincide con las escrituras de
ambos helpers. El log no permite distinguir cuál fue su última escritura.

El reflog y los objetos preservados en
`refs/archive/fixture-leak-2026-10-16` contienen esta secuencia. La opción
`--incident-evidence` del script vuelve a leer los objetos y comprueba las
correlaciones contra el log original, sin depender de este texto.

| Commit | Fecha local del autor | Mensaje | Autor |
| --- | --- | --- | --- |
| `1c9d4db` | 2026-10-16 19:39:48 -0300 | seed | t |
| `709f846` | 2026-10-16 19:39:56 -0300 | initial | t |
| `ffd2635` | 2026-10-16 19:39:58 -0300 | msg | aisotropy |
| `2c0267a` | 2026-10-16 19:39:58 -0300 | initial | t |
| `2696683` | 2026-10-16 19:39:58 -0300 | add note | aisotropy |
| `0a9d95f` | 2026-10-16 19:39:58 -0300 | initial | t |

Se reprodujo un mecanismo que escribe `core.bare=true` con la misma operación de
los fixtures: `git -C <temporal> init -q`, cuando `GIT_DIR` apunta al directorio
administrativo de un worktree enlazado y falta `GIT_WORK_TREE`. Git escribe ese
valor en la config compartida del repositorio señuelo. El mismo experimento con
un directorio `.git` ordinario conserva `core.bare=false`: esa primera prueba
no cubría la estructura del worktree afectado.

El runner prueba ambos casos y ejecuta después el mismo `init` con
`scripts/clear-git-env.sh`. El destino se inicializa y la config común, HEAD,
índice, árbol y archivos del worktree señuelo quedan iguales. Esto prueba un
mecanismo compatible con el incidente. No se capturó el entorno del hook
histórico ni se identificó cuál de los helpers hizo su última escritura.
La búsqueda de escritores con `--bare` o `core.bare` explícitos no alcanza a
encontrar este cambio implícito de `git init`.

El arreglo agrega `internal/gitenv.Command`: elimina `GIT_*` sólo del entorno
del subprocess cuando el código selecciona explícitamente un repositorio.
Lo usan el ciclo de worktrees, el commit host-side, el abort de merge y los
fixtures de ambas áreas, incluidos los de la fachada con tag `keybroker`.
Los callers pueden agregar después una identidad propia del fixture. El resto
del entorno se conserva, y el proceso padre no se modifica.

`scripts/clear-git-env.sh` limpia también los subprocesses lanzados por pre-push y
por test-all, una vez determinada la raíz. El pre-commit mantiene su entorno,
incluido `GIT_INDEX_FILE`, que necesita para los commits parciales.
`scripts/pre-push-hook.sh` selecciona el script del worktree activo.
`scripts/install-hooks.sh` instala una copia de ese dispatcher y respalda el hook
anterior. En la recuperación local, el symlink original quedó en
`.git/hooks/pre-push.before-fixture-leak-fix-2026-10-17`.

Para reproducir en esta Mac con macOS y zsh, desde la raíz del worktree, con
Python 3, Git y la versión de Go fijada por el repo:

```sh
python3 scripts/evidence-git-environment-leak-2026-10-17.py
python3 scripts/evidence-git-environment-leak-2026-10-17.py --go-tests
python3 scripts/evidence-git-environment-leak-2026-10-17.py --negative-control
```

Las corridas completadas dieron exit 0. La primera prueba la limpieza shell y
el dispatcher realmente instalado usando un repositorio temporal con un script
instrumentado, además de los controles positivo y negativo con worktree
enlazado. La segunda corrió las suites completas de `internal/gitenv`,
`internal/sandbox` e `internal/taskagent`, y el subconjunto sin Docker
`TestApp_RunTask_(CustomProviderIsUsed|MaxDurationTearsDownRunaway)` con tag
`keybroker`. El proceso de pruebas heredó `GIT_DIR`, `GIT_WORK_TREE`,
`GIT_COMMON_DIR`, `GIT_INDEX_FILE` y una inyección `GIT_CONFIG_*` apuntando al
señuelo. Su config, HEAD, índice, árbol y archivos quedaron iguales.

El control mutante copia sólo el helper y su archivo de tests a un módulo
temporal, cambia `clean(os.Environ())` por `os.Environ()` y corre el mismo
`TestCommandIgnoresInheritedGitEnvironment`. Debe fallar con exit 1 por cambios
en el señuelo. El script exige ese fallo específico y devuelve 0 cuando lo
observa. Ninguna corrida del control apunta al repo real.

Para contrastar los commits con la evidencia original, pasale la carpeta
conservada del encargo 13 como argumento:

```sh
python3 scripts/evidence-git-environment-leak-2026-10-17.py --incident-evidence "$INCIDENT_EVIDENCE"
```

La búsqueda de escritores explícitos, también desde macOS/zsh y la raíz:

```sh
rg -n --hidden --glob '*.go' --glob '*.py' --glob '*.sh' --glob '!graft/**' -- '--bare|core\.bare|PlainInit|repository\.Init' arenas examples bench scripts internal aisotropy cmd test tools
```

Antes de agregar la reproducción, el único resultado fue la comprobación de
recuperación en `scripts/verify-git-fixture-recovery.py`, que exige
`core.bare=false`. El runner de este informe también aparece ahora porque
comprueba expresamente el cambio de ese valor.

El script toma el SHA-256 de la config compartida al entrar y lo compara antes y
después de cada subprocess. Corta con `STOP` si cambia. En todas las corridas
anteriores se conservó
`aeaaeff73cb2d6720521e3c3a5743dceebafc4329ff6d09e6d51a6edcc48e11e`.
