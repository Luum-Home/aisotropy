# ADR-0072: The runtime config FILE refuses secrets — fail-closed at load, env-only, in a public repo

**Status:** Accepted (2026-07-27 — implementation landed with the ADR: `aisotropy/runtimeconfig.go`
refusal + drift guard, `aisotropy/runtimeconfig_test.go`, `aisotropy.runtime.example.yaml`,
`.gitignore`). Every number and command quoted below is real output from the command named
alongside it.
**Supersedes in part:** [ADR-0040 §5](0040-runtime-config-file-env-precedence.md) ("Secrets:
loadable, but not preferred"), and closes the **operator decision left open** in that ADR's
2026-07-27 addendum. Everything else in ADR-0040 — the mirrored schema, YAML+TOML, the
`env > file > default` precedence, lenient unknown keys, the inert reference file — stands unchanged.
**Related:** ADR-0016 (the separate wiring `Config`), ADR-0024 / ADR-0025 (the JWT + ops-token auth
surface whose secrets this governs), `.claude/rules/constitutional-gates.md` ("Secrets … only via
environment variables — never committed, never logged"), `docs/vision/thesis.md`.

## Context

[ADR-0040](0040-runtime-config-file-env-precedence.md) added a runtime config **file** mirroring the
`AISOTROPY_*` / service env surface. Its §5 ruled that the five secret-bearing knobs
(`anthropic_api_key`, `jwt_secret`, `ops_token`, `qdrant_api_key`, `embed_api_key`) **are loadable
from the file** as a local/dev convenience, with a doc comment stating that env or a secret manager
"remains preferred" and that values are never logged. The shipped reference file
`aisotropy.runtime.example.yaml` accordingly documented all five as commented-out knobs, complete
with `anthropic_api_key: "sk-ant-..."` and `jwt_secret: "change-me"` placeholders, plus prose
advising the operator to keep any filled-in copy out of git.

That is **security by convention**, and it has one decisive problem this project cannot absorb:

**This repository is PUBLIC.** The failure mode is not hypothetical or gradual. An operator copies
the example to `aisotropy.runtime.yaml` (the file the loader auto-discovers — the example tells them
to), pastes a real provider key into the line the example already wrote for them, and later runs
`git add .`. The result is a real credential in a public commit. Git history is append-only: the fix
is not `git rm`, it is **key rotation**, plus the assumption that the key was harvested between push
and rotation. There is no undo.

Three properties make the convention-based posture inadequate here rather than merely imperfect:

1. **The blast radius is asymmetric.** Being wrong in the strict direction costs an operator one
   confusing boot error and one `export`. Being wrong in the lenient direction costs a rotated
   production credential and an unbounded exposure window.
2. **A `.gitignore` is advisory.** `git add -f` walks straight past it, as does any tool that stages
   by explicit path, as does a fresh clone whose `.gitignore` predates the entry. It reduces
   accident frequency; it cannot make the accident impossible.
3. **The project's own thesis is "by construction, not by convention."** Tenant isolation is
   enforced by a scope the caller cannot bypass ([ADR-0011](0011-tenant-scoped-statement-builder.md));
   quota is checked *before* the spend, not after. A secrets posture resting on a doc comment saying
   "preferred" is the one place the codebase argued the opposite way.

ADR-0040's addendum recorded this exact question as **open for an operator ruling**, precisely
because reversing it is a behavior change to an accepted decision and a public API — not an agent's
unilateral call. The operator has now ruled.

## Decision

**The runtime config file may not supply secret material at all. A file that populates any key in
`secretEnvKeys` is REFUSED at load, fail-closed: the runtime does not start.**

### 1. Fail-at-load, not warn-and-ignore

`LoadRuntimeConfigFile` parses the file, then — before returning — checks whether the parsed config
populates any secret key. If it does, it returns the **zero `RuntimeConfig`** and an error wrapping
the new exported sentinel `ErrSecretInConfigFile`. The zero return is deliberate: no secret the file
carried survives the call, so a caller who ignores the error still cannot obtain the value.

Because materialization runs at the top of `startRuntime` under `applyConfigFile`, this propagates to
`Run` and `Turn` as a boot error — the server refuses to start.

**Why refuse rather than ignore-with-a-warning** (the alternative the operator explicitly asked to be
weighed):

- A warning is a *log line*, and log lines are the thing nobody reads during a working deploy. The
  operator whose key is in the file would see the runtime boot, work fine (because the same key is
  usually also in the env), and never learn that the file on disk is a loaded gun awaiting the next
  `git add`. Silent success is exactly the state the public-repo risk needs to make impossible.
- Ignoring is *ambiguous at the point of failure*: a file that specifies `anthropic_api_key` and an
  env that does not would boot on the keyless stub client and answer with canned text. The operator
  debugs a "why are my answers wrong" problem instead of reading a one-line error.
- Refusing is **loud, immediate, and self-correcting**: the operator sees the offending key named,
  the remedy spelled out, and fixes it in the ten seconds before it can ever be committed.
- The cost is bounded and one-time. The five keys are a closed set; the migration is `export`.

**Honest scope of the breakage:** this is a **breaking change** for anyone whose local dev setup put
one of the five keys in a runtime config file, which ADR-0040 §5 explicitly permitted. Their next boot
fails with a named error and an `export` instruction. That is the intended cost, taken deliberately,
and it is the reason this is its own ADR rather than an edit to ADR-0040. Non-secret knobs, both file
formats, and the whole precedence matrix are untouched — a file without secrets behaves exactly as
before.

### 2. The error is actionable and never echoes the value

```
aisotropy: runtime config "…/aisotropy.runtime.yaml" sets secret-bearing key(s)
ANTHROPIC_API_KEY: secrets must not be supplied by a runtime config file; set them
as environment variables instead (remove them from the file and export each one,
e.g. `export ANTHROPIC_API_KEY=...`; the value was not applied and has not been logged)
```

It names **which** key, says **what to do instead**, and states plainly that nothing was applied.
It contains the file **path** and the env-var **name** — both non-secret — and by construction never
the value: `refuseSecrets` builds its message from `secretKeysSet()`, which returns key *names* only
and cannot access values. `TestLoadRuntimeConfigFile_RefusesSecrets` asserts, per key and per format,
that the canary value appears nowhere in the error text.

### 3. The leak the refusal could not reach: parser error snippets (found while building this)

Writing the "never echoes the value" test surfaced a **pre-existing leak the refusal cannot close by
construction**: a MALFORMED file never gets far enough to be refused — it dies in the parser — and
**`github.com/goccy/go-yaml` annotates its errors with a source snippet of the offending file.** A
broken YAML config that also carried a real `anthropic_api_key:` line echoed that line **verbatim**
into the boot error, and from there into whatever logs the boot failure. Measured, before the fix:

| case | leaked the key? |
|---|---|
| YAML syntax error elsewhere in the file | **yes** |
| YAML type error elsewhere in the file | **yes** |
| YAML type mismatch on the secret key itself | **yes** |
| the three TOML equivalents (BurntSushi) | no |

`sanitizeParseError` now redacts the value half of any `secret_key: …` / `secret_key = …` line out of
a parser's error text before it is wrapped, for **both** formats — "the parser we use today does not
do it" is not a property worth depending on. The set of file keys it redacts is derived from the
struct **by reflection** (`secretFileKeys`), keyed on `secretEnvKeys`, so it cannot drift; a clean
error is returned **unchanged**, preserving `errors.Is`/`As` for the common case. Pinned by
`TestLoadRuntimeConfigFile_ParseErrorNeverEchoesSecrets` (7 cases, both formats) and
`TestSecretFileKeys_MatchSecretEnvKeys`.

This is worth recording beyond the fix: **"we never log the value" was already the claim in ADR-0040
§5, and it was not fully true** — not because materialize was wrong, but because a dependency's error
formatting was never audited against it. A secrets guarantee has to cover the error paths of the
libraries it delegates to, not only its own log statements.

### 4. Two barriers, not one

- `LoadRuntimeConfigFile` refuses — this is the file path, the actual threat model.
- `materialize` refuses **again**, so a `RuntimeConfig` constructed directly in Go (or reaching
  materialization through some future loader added above it) still cannot write secret material into
  the process environment. The refusal is all-or-nothing: a poisoned config applies **none** of its
  keys, not "the safe ones".
- `redactSecretKeys` is kept as a residual logging backstop even though a secret can no longer reach
  the applied-keys list. It costs one map lookup and is what keeps the logging path safe if the
  refusal is ever narrowed.

### 5. The drift guard — the sixth key, not the five

The five known keys are safe today because someone thought about them. The real risk is the **sixth**:
a knob added next year (`webhook_signing_key`, `admin_password`, `smtp_credential`) that is secret in
substance, is never registered in `secretEnvKeys`, and therefore silently becomes file-loadable and
log-printable again — undoing this ADR without anyone editing it.

`TestSecretishEnvKeys_AreClassified` closes that. It enumerates **every** environment variable
`RuntimeConfig` can materialize — by **reflection over the struct**, so the list is derived from the
code and nothing can hide from it — and requires every name matching
`(?i)(KEY|SECRET|TOKEN|PASSWORD|CREDENTIAL)` to be **explicitly classified**: either registered in
`secretEnvKeys` (hence refused), or listed in `nonSecretSecretishEnvKeys` **with a written reason**.
Silence fails the test. Two keys are allowlisted today, both genuinely public:
`AISOTROPY_JWT_PUBLIC_KEY` (a PEM public key — published by design) and
`AISOTROPY_JWT_PUBLIC_KEY_FILE` (a path to it). The guard also fails on **stale** entries in either
map, so it cannot rot in the other direction.

The guard was **mutation-tested**: adding an unclassified `webhook_signing_key` field to
`RuntimeConfig` turns it red with the exact remediation message, and it returns green when removed.

### 6. The example file documents the refusal instead of the knobs

`aisotropy.runtime.example.yaml` no longer contains a settable line for any of the five keys —
neither uncommented nor commented. A commented `# anthropic_api_key: "sk-ant-..."` in a file
operators are told to copy is an *invitation* to paste a real key into a committable artifact, and it
would not even work now. In its place is a SECRETS section stating that the loader refuses them, the
`export` form for each of the five, and the public-repo reasoning. `TestExampleRuntimeConfig_IsRealAndInert`
now enforces both directions: every non-secret field must be documented, and every secret field must
be **absent** as a settable key.

### 7. `.gitignore` as the second barrier (not the defense)

`aisotropy.runtime*.{yaml,yml,toml}` are ignored, with `!aisotropy.runtime.example.yaml` negated back
in — the reference is committed on purpose and now provably carries no secret key. The wildcard also
covers the `aisotropy.runtime.local.yaml` convention the old example suggested. This is belt to the
loader's braces: it lowers accident frequency, while the loader is what makes the accident
impossible.

## Consequences

**Positive**
- A real credential cannot enter this public repository through the config-file surface, regardless
  of operator discipline, `.gitignore` state, or `git add -f`.
- The secrets posture now matches the constitutional gate ("secrets only via environment variables")
  and the project's by-construction thesis, instead of contradicting both with a "preferred".
- The failure is loud, named, and self-correcting, at the earliest possible moment (boot), rather
  than silent-and-wrong-later.
- The drift guard makes the property survive schema growth — the failure mode that would otherwise
  quietly repeal this ADR.

**Negative / honest scope**
- **Breaking change** for local/dev setups that used ADR-0040 §5 as permitted. Migration is one
  `export` per key; the error names the key and the command.
- Secret-bearing **fields remain on the `RuntimeConfig` struct**. They must: they are how the loader
  *detects* the offense. A Go caller can still assign them, and nothing stops them being read back
  from the struct — but no code path in this package will materialize them, and no file can populate
  them. Removing the fields entirely would break the public API and, worse, make a file naming those
  keys silently ignored (lenient unknown-key handling) — the exact warn-and-ignore behavior this ADR
  rejects. Keeping the fields is what makes the refusal *detectable*.
- The refusal is **name-based**, keyed on `secretEnvKeys`. It cannot know that a knob named
  `embed_url` happens to have a token in its query string. The drift guard raises the floor
  (anything secret-*looking* must be classified) but a deliberately misnamed secret is out of scope.
- One more thing to remember when adding a knob: a new secret-ish field needs a line in
  `secretEnvKeys` or the allowlist. The guard turns that from "remember" into "the tests fail".

## Alternatives considered

- **Warn and ignore** (the other option the operator asked to be weighed). Rejected on the grounds in
  §1: silence during a working boot is exactly the state the public-repo risk cannot tolerate, and
  the resulting behavior (stub client, canned answers) is a worse debugging experience than a named
  error.
- **Keep §5, rely on `.gitignore` alone.** Rejected: advisory, bypassable, and absent from any clone
  predating the entry. It is kept as the second barrier, not as the defense.
- **Refuse only when the file is inside a git work tree.** Rejected: magic, environment-dependent
  behavior (ADR-0002's anti-magic gate), and it fails the case that matters most — a file created
  outside a repo and later copied into one.
- **Delete the secret fields from `RuntimeConfig`.** Rejected: a public-API break that would make
  offending files *silently ignored* rather than refused, because unknown keys are lenient by
  decision (ADR-0040 addendum §1). Detection requires the fields.
- **Encrypt secrets in the file (SOPS/age-style).** Rejected for now as a much larger surface (key
  management, a new dependency, a new failure mode) solving a problem the environment already solves
  for free. A consumer wanting sealed-secret workflows supplies the decrypted value through the env,
  which this ADR mandates anyway.
