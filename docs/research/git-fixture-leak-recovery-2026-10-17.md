# Recuperación de Git tras la fuga de fixtures

El 2026-10-17 se restauró la rama `chore/go-vulns-x-net-grpc-go1269` a
`99e9ab15efa3b3d192312e65237b7a206a662efb`, sin modificar archivos del árbol.
El tip anterior quedó preservado en `refs/archive/fixture-leak-2026-10-16`:
`0a9d95f9eced3d41bad95c3ee9d3f4054153d22f`.

La revisión actual encontró `core.bare=false`, por lo que no se volvió a
escribir ese valor. Sí permanecían las entradas locales falsas `user.name=t` y
`user.email=t@t`; se eliminó exclusivamente esa sección. La comparación de
lectura con el clon hermano `luum-marketplace` confirmó `core.bare=false` y
ausencia de identidad local. Se conservaron remotos, ramas y opciones de core.

## Evidencia y respaldos

Los respaldos privados están en el directorio común de Git, dentro de
`recovery/fixture-leak-2026-10-16/`. No se versiona la configuración completa:
puede contener información de acceso. Quedaron estos artefactos:

- `config.before`, `index.before`, `HEAD.before`, reflogs y ref de la rama.
- `branch-before.bundle`: historia completa del tip anterior; verificado con
  `git bundle verify` y preservado antes de modificar configuración o refs.
- `status.before.txt`, `index.before.txt`, `diff.before.patch` y
  `diff-cached.before.patch`: estado anterior del índice y árbol.
- `worktree-vs-99e9ab1.json`: comparación de los blobs del target con archivos
  presentes en disco, con resultado vacío. Por eso `reset --mixed` recuperó el
  índice y movió la rama sin tocar archivos; el estado posterior quedó limpio.
- `recovery-transitions.json`: punteros antes/después y alcance de la reparación.
- `config.sha256`: baseline de la configuración reparada.

El respaldo `config.before` tiene SHA256
`a65db3204ce150c409653f2ea9dbe5069cfa63173835f1b1359e6523a2ce7902`.
La configuración reparada tiene SHA256
`aeaaeff73cb2d6720521e3c3a5743dceebafc4329ff6d09e6d51a6edcc48e11e`.
El verificador siguiente recalcula el hash y vigila su estabilidad antes y
después de cada subproceso Git. Cambiar la configuración exige revisar la causa;
no se actualiza el baseline para ocultar una variación.

## Reproducir la comprobación

En la máquina de recuperación, macOS con zsh, desde este worktree:

```sh
python3 scripts/verify-git-fixture-recovery.py --expect-config-sha256 aeaaeff73cb2d6720521e3c3a5743dceebafc4329ff6d09e6d51a6edcc48e11e
```

Resultado esperado: `status=pass`, exit `0`. El script descarta todas las
variables `GIT_*` heredadas y usa `GIT_OPTIONAL_LOCKS=0`. Un hallazgo devuelve
`1`; un error de ejecución devuelve `2`. Es un verificador local de los
respaldos de este incidente: un clon nuevo no contiene los archivos privados
de recuperación y debe fallar por su ausencia.

El target debe ser ancestro de `HEAD`, por lo que se aceptan commits posteriores
del arreglo. Ninguno de los commits presentes entre el target y la ref de
archivo puede ser ancestro de `HEAD`: también se rechaza una contaminación
parcial que no incluya el último fixture.

Para restaurar el bundle en un directorio nuevo de prueba, desde este worktree
en macOS con zsh:

```sh
git clone --no-checkout --branch chore/go-vulns-x-net-grpc-go1269 ../aisotropy/.git/recovery/fixture-leak-2026-10-16/branch-before.bundle /ruta/nueva/fixture-restaurado
```

Reemplazar el destino por una ruta inexistente. El bundle guarda la ref de la
rama, pero no un `HEAD` simbólico; por eso hay que indicar `--branch`. La
restauración independiente en un clon temporal terminó con exit `0` y recuperó
el tip de fixtures. En ese clon, el verificador aceptó un commit hijo del target
con exit `0` y rechazó el tip de fixtures con exit `1`. Los comandos y salidas
de esa revisión están en el respaldo privado `independent-review-light.json`.

Control negativo de la vigilancia, sin escribir configuración:

```sh
python3 scripts/verify-git-fixture-recovery.py --expect-config-sha256 0000000000000000000000000000000000000000000000000000000000000000
```

Resultado esperado: `status=finding`, exit `1`, antes de lanzar Git.

## Operaciones de recuperación realizadas

Tras respaldar configuración, índice y bundle, se comprobaron nuevamente el tip
y las entradas de identidad. Se ejecutaron estas operaciones sobre el worktree
y su directorio común de Git, con exit `0`:

```sh
git config --file ../aisotropy/.git/config --remove-section user
git update-ref refs/archive/fixture-leak-2026-10-16 0a9d95f9eced3d41bad95c3ee9d3f4054153d22f 0000000000000000000000000000000000000000
git reset --mixed 99e9ab1
```

Estos comandos documentan una reparación ya realizada; no son un script para
repetirla. La creación de la ref utilizó el valor anterior nulo para impedir
sobrescribir un archivo preexistente. Antes de crearla, `rev-parse --verify
--quiet` devolvió `1` porque todavía no existía. Una consulta previa con
`show-ref --verify --hash` devolvió `128` por esa ausencia y detuvo el primer
intento antes de escribir refs; se reemplazó por la consulta con exit esperado.

Las únicas variables Git heredadas al comenzar eran `GIT_EDITOR` y
`GIT_PAGER`; no se reproducía una fuga activa mediante `GIT_DIR` en esta
sesión. El reflog anterior preservado registra los commits de fixture después
del commit de dependencias. La atribución del origen y la verificación de
vulnerabilidades se documentan aparte: esta recuperación no ejecutó builds,
tests, hooks ni govulncheck.

## Control independiente del índice de pre-commit

Este control posterior ejecuta el pre-commit sobre un fixture temporal. El
manifiesto está staged sólo en un índice alternativo: el hook debe disparar la
auditoría y conservar `GIT_INDEX_FILE`. El control con el índice normal no debe
dispararla. En macOS/zsh, desde este worktree:

```sh
python3 - <<'PY'
from pathlib import Path
import hashlib, os, subprocess, tempfile
root = Path.cwd()
gd = (root / (root / '.git').read_text().split(':', 1)[1].strip()).resolve()
common = (gd / (gd / 'commondir').read_text().strip()).resolve()
config = common / 'config'
baseline = 'aeaaeff73cb2d6720521e3c3a5743dceebafc4329ff6d09e6d51a6edcc48e11e'
env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
env['GIT_OPTIONAL_LOCKS'] = '0'
def run(argv, cwd, extra=None):
    assert hashlib.sha256(config.read_bytes()).hexdigest() == baseline
    result = subprocess.run(argv, cwd=cwd, env=dict(env, **(extra or {})),
                            capture_output=True, text=True, timeout=30)
    assert hashlib.sha256(config.read_bytes()).hexdigest() == baseline
    assert result.returncode == 0, (argv, result.stderr)
    return result.stdout
with tempfile.TemporaryDirectory(prefix='aisotropy-precommit-index-') as temp:
    repo = Path(temp)
    run(['git', 'init', '-q'], repo)
    (repo / 'sentinel').write_text('fixture\n')
    run(['git', 'add', 'sentinel'], repo)
    run(['git', '-c', 'user.name=fixture', '-c',
         'user.email=fixture@example.invalid', 'commit', '-qm', 'initial'], repo)
    (repo / 'scripts').mkdir()
    (repo / 'scripts/license-audit.sh').write_text(
        '#!/bin/sh\nprintf "audit-index:%s\\n" "$GIT_INDEX_FILE"\n')
    (repo / 'go.mod').write_text('module fixture\ngo 1.26.9\n')
    alternate = {'GIT_INDEX_FILE': str(repo / 'alternate-index')}
    run(['git', 'read-tree', 'HEAD'], repo, alternate)
    run(['git', 'add', 'go.mod'], repo, alternate)
    hook = ['bash', str(root / 'scripts/license-gate-precommit.sh')]
    assert 'audit-index:' not in run(hook, repo)
    assert 'audit-index:' + alternate['GIT_INDEX_FILE'] in run(hook, repo, alternate)
print('PASS: pre-commit preserves its alternate staged index')
PY
```

Resultado de la revisión: exit `0`. No corre Go ni modifica el repo real.
