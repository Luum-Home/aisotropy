#!/usr/bin/env bash
# Read-only, bounded static evidence for the robustness audit.
# The default static mode never returns success: it cannot establish behavioral
# validation. Exit 0 is reserved for --targeted after its focused tests pass.

set -uo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/robustness-audit.sh [--targeted] [--fixture-truncate-source PATH] [--help]

Runs deterministic, read-only static checks against explicit source scopes.

  --targeted  Opt in to focused Go tests in disposable, offline scratch space.
  --fixture-truncate-source PATH
              Test-only: simulate an empty required static source. It must exit 2.
  --help      Print this help.

Exit status: 0 --targeted found no blockers or unknown source shape and its tests passed
             1 a recognized static blocker was found
             2 an input, source shape, dependency, resource guard, or behavioral
               validation requirement could not be evaluated
USAGE
}

mode=static
fixture_truncate_source=
semicolon=$(printf '\073')
while [ "$#" -gt 0 ]
do
  if [ "$1" = --targeted ]
  then
    mode=targeted
  elif [ "$1" = --fixture-truncate-source ]
  then
    if [ "$#" -lt 2 ] || [ -z "$2" ]
    then
      echo "robustness-audit: --fixture-truncate-source requires a path" >&2
      exit 2
    fi
    fixture_truncate_source=$2
    shift
  elif [ "$1" = --help ] || [ "$1" = -h ]
  then
    usage
    exit 0
  else
    echo "robustness-audit: unknown argument: $1" >&2
    usage >&2
    exit 2
  fi
  shift
done

for required in awk cat dirname find git grep sed sort tr wc
do
  if ! command -v "$required" >/dev/null 2>&1
  then
    echo "robustness-audit: required command not found: $required" >&2
    exit 2
  fi
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script_dir_status=$?
if [ "$script_dir_status" -ne 0 ]
then
  echo "robustness-audit: cannot resolve script directory" >&2
  exit 2
fi
repo_root=$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null)
repo_root_status=$?
if [ "$repo_root_status" -ne 0 ]
then
  echo "robustness-audit: cannot resolve a Git checkout from $script_dir" >&2
  exit 2
fi
cd "$repo_root" || {
  echo "robustness-audit: cannot enter repository root" >&2
  exit 2
}

if command -v sha256sum >/dev/null 2>&1
then
  hash_tool=sha256sum
elif command -v shasum >/dev/null 2>&1
then
  hash_tool=shasum
else
  echo "robustness-audit: neither sha256sum nor shasum is available" >&2
  exit 2
fi

hash_file() {
  if [ "$hash_tool" = sha256sum ]
  then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

hash_stream() {
  if [ "$hash_tool" = sha256sum ]
  then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

count_text_lines() {
  count=$(printf '%s\n' "$1" | wc -l | tr -d '[:space:]')
  count_status=$?
  if [ "$count_status" -ne 0 ]
  then
    echo "robustness-audit: line count failed" >&2
    exit 2
  fi
  printf '%s\n' "$count"
}

print_first_lines() {
  printf '%s\n' "$1" | sed -n "1,${2}p"
  print_status=$?
  if [ "$print_status" -ne 0 ]
  then
    echo "robustness-audit: evidence rendering failed" >&2
    exit 2
  fi
}

collect_runtime_paths() {
  for root in aisotropy internal cmd deploy
  do
    if [ ! -d "$root" ]
    then
      echo "robustness-audit: runtime source scope is missing: $root" >&2
      return 2
    fi
    # This exact file list is both the default content-scan input and the
    # STATIC_CONTENT_SCOPE fingerprint manifest. It deliberately includes
    # untracked source-like files inside the bounded runtime trees, while not
    # reading hidden credential files or Git metadata.
    find "$root" -type f \( -name '*.go' -o -name '*.md' -o -name '*.yaml' -o -name '*.yml' -o -name '*.json' -o -name '*.sh' -o -name '*.tpl' -o -name '*.txt' \) -print || return 2
  done | LC_ALL=C sort -u
}

collect_targeted_paths() {
  for root in internal/auth internal/memory internal/semantic internal/api
  do
    if [ ! -d "$root" ]
    then
      echo "robustness-audit: targeted source scope is missing: $root" >&2
      return 2
    fi
    find "$root" -type f -name '*.go' -print || return 2
  done | LC_ALL=C sort -u
}

collect_targeted_mutation_paths() {
  {
    printf '%s\n' go.mod go.sum scripts/resource-guard.sh scripts/robustness-audit.sh
    for root in aisotropy internal cmd deploy
    do
      if [ ! -d "$root" ]
      then
        echo "robustness-audit: mutation-audit source scope is missing: $root" >&2
        return 2
      fi
      # This wider scope catches changes made by focused tests without reading
      # Git metadata or copying the working tree (which could copy secrets).
      find "$root" -type f -print || return 2
    done
  } | LC_ALL=C sort -u
}

fingerprint_manifest() {
  while IFS= read -r path
  do
    if [ -z "$path" ]
    then
      continue
    fi
    if [ ! -r "$path" ]
    then
      echo "robustness-audit: fingerprint input is missing or unreadable: $path" >&2
      return 2
    fi
    digest=$(hash_file "$path")
    digest_status=$?
    if [ "$digest_status" -ne 0 ] || [ -z "$digest" ]
    then
      echo "robustness-audit: cannot hash fingerprint input: $path" >&2
      return 2
    fi
    printf 'PATH %s\nSHA256 %s\n' "$path" "$digest"
  done
}

if ! head=$(git rev-parse HEAD 2>/dev/null)
then
  echo "robustness-audit: cannot read HEAD" >&2
  exit 2
fi
runtime_paths=$(collect_runtime_paths)
runtime_paths_status=$?
if [ "$runtime_paths_status" -ne 0 ] || [ -z "$runtime_paths" ]
then
  echo "robustness-audit: cannot enumerate runtime source scope" >&2
  exit 2
fi
default_paths=$( {
  printf '%s\n' LICENSE
  printf '%s\n' docs/architecture/reality-ledger.md
  printf '%s\n' docs/decisions/0081-shared-working-memory-tier-and-local-quota.md
  printf '%s\n' scripts/robustness-audit.sh
  printf '%s\n' "$runtime_paths"
} | LC_ALL=C sort -u )
default_paths_status=$?
if [ "$default_paths_status" -ne 0 ] || [ -z "$default_paths" ]
then
  echo "robustness-audit: cannot assemble default content scope" >&2
  exit 2
fi
static_content_fingerprint=$(printf '%s\n' "$default_paths" | fingerprint_manifest | hash_stream)
static_content_status=$?
if [ "$static_content_status" -ne 0 ] || [ -z "$static_content_fingerprint" ]
then
  echo "robustness-audit: cannot fingerprint default content scope" >&2
  exit 2
fi
git_index_fingerprint=$(git ls-files -s | hash_stream)
git_index_status=$?
if [ "$git_index_status" -ne 0 ] || [ -z "$git_index_fingerprint" ]
then
  echo "robustness-audit: cannot fingerprint Git index input" >&2
  exit 2
fi
git_tag_fingerprint=$(git for-each-ref --format='%(refname) %(objectname) %(objecttype)' refs/tags | hash_stream)
git_tag_status=$?
if [ "$git_tag_status" -ne 0 ] || [ -z "$git_tag_fingerprint" ]
then
  echo "robustness-audit: cannot fingerprint Git tag input" >&2
  exit 2
fi
default_input_fingerprint=$(printf 'STATIC_CONTENT %s\nGIT_INDEX %s\nGIT_TAGS %s\n' "$static_content_fingerprint" "$git_index_fingerprint" "$git_tag_fingerprint" | hash_stream)
default_input_status=$?
if [ "$default_input_status" -ne 0 ] || [ -z "$default_input_fingerprint" ]
then
  echo "robustness-audit: cannot fingerprint default audit inputs" >&2
  exit 2
fi
license_hash=$(hash_file LICENSE)
license_hash_status=$?
if [ "$license_hash_status" -ne 0 ] || [ -z "$license_hash" ]
then
  echo "robustness-audit: cannot hash LICENSE" >&2
  exit 2
fi
go_files=$(git ls-files '*.go' | wc -l | tr -d '[:space:]')
go_files_status=$?
if [ "$go_files_status" -ne 0 ]
then
  echo "robustness-audit: cannot count tracked Go files" >&2
  exit 2
fi
test_files=$(git ls-files '*_test.go' | wc -l | tr -d '[:space:]')
test_files_status=$?
if [ "$test_files_status" -ne 0 ]
then
  echo "robustness-audit: cannot count tracked Go tests" >&2
  exit 2
fi
adr_files=$(git ls-files 'docs/decisions/[0-9][0-9][0-9][0-9]-*.md' | wc -l | tr -d '[:space:]')
adr_files_status=$?
if [ "$adr_files_status" -ne 0 ]
then
  echo "robustness-audit: cannot count tracked ADR files" >&2
  exit 2
fi
tags=$(git tag --list | wc -l | tr -d '[:space:]')
tags_status=$?
if [ "$tags_status" -ne 0 ]
then
  echo "robustness-audit: cannot count Git tags" >&2
  exit 2
fi
runtime_file_count=$(count_text_lines "$runtime_paths")
default_file_count=$(count_text_lines "$default_paths")

echo "robustness-audit: head=$head"
echo "FINGERPRINT STATIC_CONTENT_SCOPE=$static_content_fingerprint files=$default_file_count"
echo "FINGERPRINT GIT_INDEX=$git_index_fingerprint"
echo "FINGERPRINT GIT_TAGS=$git_tag_fingerprint"
echo "FINGERPRINT DEFAULT_INPUTS=$default_input_fingerprint"
echo "METRIC runtime_scope_files=$runtime_file_count command='find aisotropy internal cmd deploy selected source files | sort -u | wc -l'"
echo "METRIC default_content_scope_files=$default_file_count command='default input manifest | wc -l'"
echo "METRIC license_sha256=$license_hash"
echo "METRIC tracked_go_source_files=$go_files command='git ls-files *.go | wc -l'"
echo "METRIC tracked_go_test_files=$test_files command='git ls-files *_test.go | wc -l'"
echo "METRIC tracked_adr_files=$adr_files command='git ls-files docs/decisions/[0-9][0-9][0-9][0-9]-*.md | wc -l'"
echo "METRIC git_tags=$tags command='git tag --list | wc -l'"

blockers=0
blocker() {
  blockers=$((blockers + 1))
  echo "BLOCKER $1"
}
info() {
  echo "INFO $1"
}
review() {
  echo "REVIEW $1"
}
static_signal() {
  label=$1
  fixed=$2
  shift 2
  if [ "$#" -eq 0 ]
  then
    echo "robustness-audit: static signal has no declared source: $label" >&2
    exit 2
  fi
  for source in "$@"
  do
    if [ "$fixture_truncate_source" = "$source" ]
    then
      echo "robustness-audit: fixture simulates an empty required static source: $source" >&2
      exit 2
    fi
    if [ ! -f "$source" ] || [ ! -r "$source" ] || [ ! -s "$source" ]
    then
      echo "robustness-audit: required static source is missing, unreadable, or empty: $source" >&2
      exit 2
    fi
  done
  output=$(grep -nF -- "$fixed" "$@")
  signal_status=$?
  if [ "$signal_status" -eq 1 ]
  then
    echo "robustness-audit: required static source has an unknown shape for $label" >&2
    exit 2
  fi
  if [ "$signal_status" -ne 0 ]
  then
    echo "robustness-audit: static signal failed for $label" >&2
    exit 2
  fi
  signal_count=$(count_text_lines "$output")
  echo "STATIC_SIGNAL $label count=$signal_count"
  print_first_lines "$output" 4
  return 0
}

if [ "$license_hash" = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30" ]
then
  echo "PASS Apache-2.0 canonical LICENSE hash"
else
  blocker "LICENSE_HASH_MISMATCH expected canonical Apache-2.0 digest"
fi

# How long may a cached JWKS key set outlive a failing refresh?
#
# Both shapes recognised, for the reason spelled out at PERSIST_AND_CONTINUE
# below: a static_signal on the vulnerable line alone exits 2 the moment the
# defect is fixed, and "cannot evaluate" is not a verdict.
#
# unbounded: the refresh error is discarded whenever a cache exists, so a key the
# IdP rotated out keeps verifying for as long as the endpoint stays broken.
jwks_unbounded=$(grep -cF "if err := s.refreshLocked(ctx, now)${semicolon} err != nil && s.keys == nil {" internal/auth/jwt_rs256.go)
# bounded: past ttl+maxStale the cache is dropped and lookups reject.
jwks_bounded=$(grep -cF 'if s.keys != nil && now.Sub(s.fetchedAt) >= s.ttl+s.maxStale {' internal/auth/jwt_rs256.go)
jwks_drops=$(grep -cF 'JWKS cache is stale beyond the grace window' internal/auth/jwt_rs256.go)
if [ "$jwks_unbounded" -ge 1 ]
then
  blocker "JWKS_STALE_CACHE refresh failure with an existing key set is not rejected by this branch"
elif [ "$jwks_bounded" -ge 1 ] && [ "$jwks_drops" -ge 1 ]
then
  info "JWKS_STALE_BOUNDED a failing refresh serves the cache only inside ttl+maxStale, then drops it and rejects"
  info "JWKS_REVOCATION_WINDOW a rotated-out key still verifies for up to ttl+maxStale (5m+30m on the defaults) — bounded, not zero"
else
  echo "robustness-audit: cannot determine the JWKS stale-cache policy" >&2
  exit 2
fi

static_signal DESTRUCTIVE_REINGEST 'store.DeleteBySource(ctx, sc, KindDocument, doc.ID)' internal/semantic/ingest.go
static_signal REINGEST_EMBED 'vecs, err := EmbedPassage(ctx, emb, chunks)' internal/semantic/ingest.go
delete_line=$(grep -nF 'store.DeleteBySource(ctx, sc, KindDocument, doc.ID)' internal/semantic/ingest.go | sed -n '1s/:.*//p')
delete_line_status=$?
embed_line=$(grep -nF 'vecs, err := EmbedPassage(ctx, emb, chunks)' internal/semantic/ingest.go | sed -n '1s/:.*//p')
embed_line_status=$?
if [ "$delete_line_status" -ne 0 ] || [ "$embed_line_status" -ne 0 ] || [ -z "$delete_line" ] || [ -z "$embed_line" ]
then
  echo "robustness-audit: cannot derive semantic source order" >&2
  exit 2
fi
if [ "$delete_line" -lt "$embed_line" ]
then
  blocker "DESTRUCTIVE_REINGEST delete precedes embedding in the current source order"
else
  # embed precedes delete: the fallible work happens first and the store is only
  # mutated once a replacement exists. Added 2026-08-08, when the reorder this
  # check asked for actually landed and the script had no branch for it — it
  # could say "bad" or "I cannot tell", never "good", because the good shape did
  # not exist in the tree when it was written. A checker with no representation
  # for the outcome it demands reports exit 2 forever after the fix, which reads
  # as "still broken" to anyone who does not open it.
  #
  # This does NOT loosen the check: the blocker branch above is untouched, and
  # restoring the destructive order still trips it.
  info "REINGEST_ORDER embedding precedes deletion; a failed re-ingest leaves the prior version in place"
  info "REINGEST_ATOMICITY delete-then-upsert is still two calls with no Store transaction — a failure between them loses that document's chunks"
fi

static_signal CHAT_RETURNS_OUTPUT 'return chatBuildOutput(finalReply, in.TenantID, active, usage), nil' internal/api/core.go
# Does a failed user-turn write still let the turn reach the model?
#
# Both outcomes are recognised here, deliberately. static_signal fails closed on
# absence, which is right for evidence that MUST exist and wrong for a signal
# whose DISAPPEARANCE is the fix — used that way it exits 2 the moment the defect
# is repaired, and "cannot evaluate" reads as "still broken".
#
# swallowed: the persist helper logs and returns nothing, so both callers walk
# straight into the model call and the reply is later appended to a transcript
# whose prompt is missing.
persist_swallowed=$(grep -cF 'func chatPersistUserTurn(ctx context.Context, wm *memory.WorkingMemory, key memory.Key, prompt string) {' internal/api/chat.go)
# fail-closed: the helper returns the error and each caller aborts on it.
persist_returns=$(grep -cF 'func chatPersistUserTurn(ctx context.Context, wm *memory.WorkingMemory, key memory.Key, prompt string) error {' internal/api/chat.go)
persist_aborts=$(grep -cF 'if err := chatPersistUserTurn(ctx, d.WM, key, forwarded); err != nil {' internal/api/core.go)
stream_aborts=$(grep -cF 'return fmt.Errorf("could not record the user turn; the request was not sent to the model: %w", err)' internal/api/stream_core.go)
if [ "$persist_swallowed" -ge 1 ]
then
  blocker "PERSIST_AND_CONTINUE chat can return output after a logged user-turn persistence failure"
elif [ "$persist_returns" -ge 1 ] && [ "$persist_aborts" -ge 1 ] && [ "$stream_aborts" -ge 2 ]
then
  info "PERSIST_FAIL_CLOSED a failed user-turn write aborts both chat paths before the model call"
  info "PERSIST_RECORD_TURN the ASSISTANT turn (chatRecordTurn) is still log-and-continue — a reply can be answered and not remembered"
else
  echo "robustness-audit: cannot determine the user-turn persistence failure policy" >&2
  exit 2
fi

static_signal OUTBOX_RETRY_CONTINUE "if err := sink.Consolidate(ctx, p.event)${semicolon} err != nil {" internal/memory/outbox.go
outbox_fields=$(awk '
    /^type OutboxEvent struct \{/ { inside=1 }
    /^type OutboxEvent struct \{/ { next }
    inside && /^}/ { exit }
    inside { print }
  ' internal/memory/outbox.go)
  outbox_fields_status=$?
outbox_fields_status=$?
if [ "$outbox_fields_status" -ne 0 ] || [ -z "$outbox_fields" ]
then
  echo "robustness-audit: cannot inspect OutboxEvent fields" >&2
  exit 2
fi
outbox_attempt_field=$(printf '%s\n' "$outbox_fields" | grep -cE '^[[:space:]]*(Attempts?|Retries|Retry|MaxAttempts)[[:space:]]')
# A counter field was never the property that matters — an event can carry an
# attempt count and still be retried forever. What the blocker actually asks for
# is a TERMINAL policy, and "terminal" must not mean "deleted": dropping an
# exhausted event would trade an unbounded retry for silent data loss, which is
# the failure the outbox exists to prevent. So require the bound AND the park.
outbox_bound=$(grep -cF 'if e.Attempts < maxAttempts {' internal/memory/outbox.go)
outbox_parks=$(grep -cF 'txn.Set(deadOutboxKey(e), payload)' internal/memory/outbox.go)
if [ "$outbox_attempt_field" -ge 1 ] && [ "$outbox_bound" -ge 1 ] && [ "$outbox_parks" -ge 1 ]
then
  info "OUTBOX_RETRY_BOUNDED an event that keeps failing is parked in the dead-letter prefix after its attempt budget, not retried forever"
  info "OUTBOX_DEAD_LETTER_UNDRAINED parked events are preserved and countable (DeadOutboxCount) but nothing re-drains them — recovery is an operator action, not automatic"
elif [ "$outbox_attempt_field" -eq 0 ] && [ "$outbox_bound" -eq 0 ] && [ "$outbox_parks" -eq 0 ]
then
  blocker "OUTBOX_UNBOUNDED_RETRY retry continues and OutboxEvent has no attempt field in this bounded source review"
else
  echo "robustness-audit: OutboxEvent retry policy is half-present and cannot be classified" >&2
  exit 2
fi

static_signal WORKING_MEMORY_UNIMPLEMENTED '✅ **Decided, not implemented.** Zero lines of the port are written.' docs/decisions/0081-shared-working-memory-tier-and-local-quota.md
static_signal WORKING_MEMORY_PROCESS_LOCAL 'const EraseScopeProcessLocal = "process-local"' aisotropy/erase.go
blocker "SHARED_WORKING_MEMORY_UNIMPLEMENTED ADR-0081 and process-local working memory rule out a shared hosted topology"

static_signal AUTH_BOOT_FAILS_CLOSED 'case errors.Is(err, aisotropy.ErrNoAuthenticator):' cmd/aisotropyd/main.go
static_signal AUTH_NOAUTH_NONLOCAL_GUARD "if gErr := aisotropy.NoAuthNonLocalGuard(addr)${semicolon} gErr != nil {" cmd/aisotropyd/main.go
info "AUTH_BOOT fail-closed by default, with explicit NoAuth and non-loopback override guards"

runtime_file_array=()
while IFS= read -r path
do
  runtime_file_array+=("$path")
done <<EOF
$runtime_paths
EOF
control_plane_hits=$(grep -n -E 'billing|subscription|invoice|payment|control[ -]?plane' "${runtime_file_array[@]}")
control_plane_status=$?
if [ "$control_plane_status" -eq 0 ]
then
  control_plane_count=$(count_text_lines "$control_plane_hits")
  echo "STATIC_SIGNAL CONTROL_PLANE_CONTENT count=$control_plane_count"
  print_first_lines "$control_plane_hits" 6
  info "CONTROL_PLANE_CONTENT requires human classification and does not prove a service control plane"
elif [ "$control_plane_status" -eq 1 ]
then
  info "CONTROL_PLANE_ABSENCE bounded runtime content scan found no matching terms"
else
  echo "robustness-audit: control-plane content scan failed" >&2
  exit 2
fi

if [ "$mode" = targeted ]
then
  guard_output=$(scripts/resource-guard.sh --explain)
  guard_status=$?
  if [ "$guard_status" -ne 0 ]
  then
    printf '%s\n' "$guard_output" >&2
    echo "robustness-audit: targeted mode refused because resource guard did not pass" >&2
    exit 2
  fi
  if ! command -v go >/dev/null 2>&1
  then
    echo "robustness-audit: targeted mode requires go" >&2
    exit 2
  fi
  for required in mkdir mktemp rm
  do
    if ! command -v "$required" >/dev/null 2>&1
    then
      echo "robustness-audit: targeted mode requires command: $required" >&2
      exit 2
    fi
  done
  targeted_package_paths=$(collect_targeted_paths)
  targeted_package_paths_status=$?
  if [ "$targeted_package_paths_status" -ne 0 ] || [ -z "$targeted_package_paths" ]
  then
    echo "robustness-audit: cannot enumerate targeted package inputs" >&2
    exit 2
  fi
  targeted_paths=$( {
    printf '%s\n' go.mod
    printf '%s\n' go.sum
    printf '%s\n' scripts/resource-guard.sh
    printf '%s\n' scripts/robustness-audit.sh
    printf '%s\n' "$targeted_package_paths"
  } | LC_ALL=C sort -u )
  targeted_paths_status=$?
  if [ "$targeted_paths_status" -ne 0 ] || [ -z "$targeted_paths" ]
  then
    echo "robustness-audit: cannot enumerate targeted inputs" >&2
    exit 2
  fi
  targeted_content_fingerprint=$(printf '%s\n' "$targeted_paths" | fingerprint_manifest | hash_stream)
  targeted_content_status=$?
  if [ "$targeted_content_status" -ne 0 ] || [ -z "$targeted_content_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint targeted content inputs" >&2
    exit 2
  fi

  # The resource guard passed, so targeted mode may now create its only
  # writable state. It is deliberately isolated from the user's Go caches.
  targeted_scratch=$(mktemp -d "${TMPDIR:-/tmp}/robustness-audit.XXXXXX")
  targeted_scratch_status=$?
  if [ "$targeted_scratch_status" -ne 0 ] || [ -z "$targeted_scratch" ] || [ ! -d "$targeted_scratch" ]
  then
    echo "robustness-audit: cannot create targeted scratch directory" >&2
    exit 2
  fi
  cleanup_targeted_scratch() {
    if [ -n "${targeted_scratch:-}" ] && [ -d "$targeted_scratch" ]
    then
      rm -rf -- "$targeted_scratch" || echo "robustness-audit: cannot remove targeted scratch directory" >&2
    fi
  }
  trap cleanup_targeted_scratch EXIT HUP INT TERM
  if ! mkdir -p "$targeted_scratch/go-cache" "$targeted_scratch/go-mod-cache" "$targeted_scratch/go-tmp"
  then
    echo "robustness-audit: cannot initialize targeted scratch directories" >&2
    exit 2
  fi

  # Read the existing module cache only as a file:// module proxy. Go fills a
  # fresh mod cache from those verified artifacts; it cannot fall back to HTTP
  # or direct VCS access. A cache miss therefore exits 2 below, without I/O to
  # the network or a persistent Go cache.
  source_modcache=$(env GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOVCS=off GOWORK=off GOFLAGS=-mod=readonly CGO_ENABLED=0 GOENV=off go env GOMODCACHE)
  source_modcache_status=$?
  if [ "$source_modcache_status" -ne 0 ] || [ -z "$source_modcache" ]
  then
    echo "robustness-audit: cannot locate the local Go module cache for offline targeted mode" >&2
    exit 2
  fi
  local_module_proxy=$source_modcache/cache/download
  if [ ! -d "$local_module_proxy" ]
  then
    echo "robustness-audit: local Go module proxy is absent; populate the module cache before offline targeted mode" >&2
    exit 2
  fi
  local_module_proxy_manifest=$(find "$local_module_proxy" -type f -print | LC_ALL=C sort)
  local_module_proxy_manifest_status=$?
  if [ "$local_module_proxy_manifest_status" -ne 0 ] || [ -z "$local_module_proxy_manifest" ]
  then
    echo "robustness-audit: local Go module proxy has no readable cached artifacts" >&2
    exit 2
  fi
  local_module_proxy_fingerprint=$(printf '%s\n' "$local_module_proxy_manifest" | fingerprint_manifest | hash_stream)
  local_module_proxy_fingerprint_status=$?
  if [ "$local_module_proxy_fingerprint_status" -ne 0 ] || [ -z "$local_module_proxy_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint local Go module proxy artifacts" >&2
    exit 2
  fi
  local_module_proxy_location_fingerprint=$(printf '%s\n' "$local_module_proxy" | hash_stream)
  local_module_proxy_location_status=$?
  if [ "$local_module_proxy_location_status" -ne 0 ] || [ -z "$local_module_proxy_location_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint local Go module proxy location" >&2
    exit 2
  fi

  targeted_go_command=(env
    "GOCACHE=$targeted_scratch/go-cache"
    "GOMODCACHE=$targeted_scratch/go-mod-cache"
    "GOTMPDIR=$targeted_scratch/go-tmp"
    "GOPROXY=file://$local_module_proxy"
    GOSUMDB=off
    GONOPROXY=none
    GOPRIVATE=none
    GONOSUMDB='*'
    GOTOOLCHAIN=local
    GOVCS=off
    GOENV=off
    GOWORK=off
    GOFLAGS=-mod=readonly
    CGO_ENABLED=0
    go)
  go_version=$("${targeted_go_command[@]}" version)
  go_version_status=$?
  if [ "$go_version_status" -ne 0 ]
  then
    echo "robustness-audit: cannot read Go tool version" >&2
    exit 2
  fi
  collect_targeted_go_environment() {
    for environment_name in GOOS GOARCH GOMOD GOWORK GOFLAGS CGO_ENABLED GOVERSION GOROOT GOPATH GOPROXY GOSUMDB GONOPROXY GOPRIVATE GONOSUMDB GOTOOLCHAIN GOVCS GOENV
    do
      environment_value=$("${targeted_go_command[@]}" env "$environment_name") || return 2
      printf '%s=%s\n' "$environment_name" "$environment_value"
    done
    printf '%s\n' 'GOCACHE=temporary' 'GOMODCACHE=temporary' 'GOTMPDIR=temporary'
  }
  go_environment=$(collect_targeted_go_environment)
  go_environment_status=$?
  if [ "$go_environment_status" -ne 0 ]
  then
    echo "robustness-audit: cannot read targeted Go environment" >&2
    exit 2
  fi
  go_deps=$("${targeted_go_command[@]}" list -deps ./internal/auth ./internal/memory ./internal/semantic ./internal/api 2>"$targeted_scratch/go-list.stderr")
  go_deps_status=$?
  if [ "$go_deps_status" -ne 0 ]
  then
    echo "robustness-audit: local module proxy cannot satisfy targeted dependencies offline; no network fallback was attempted" >&2
    exit 2
  fi
  targeted_pre_test_manifest=$(collect_targeted_mutation_paths)
  targeted_pre_test_manifest_status=$?
  if [ "$targeted_pre_test_manifest_status" -ne 0 ] || [ -z "$targeted_pre_test_manifest" ]
  then
    echo "robustness-audit: cannot enumerate targeted mutation-audit paths" >&2
    exit 2
  fi
  targeted_pre_test_manifest_fingerprint=$(printf '%s\n' "$targeted_pre_test_manifest" | hash_stream)
  targeted_pre_test_manifest_status=$?
  if [ "$targeted_pre_test_manifest_status" -ne 0 ] || [ -z "$targeted_pre_test_manifest_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint targeted mutation-audit manifest before tests" >&2
    exit 2
  fi
  targeted_pre_test_fingerprint=$(printf '%s\n' "$targeted_pre_test_manifest" | fingerprint_manifest | hash_stream)
  targeted_pre_test_status=$?
  if [ "$targeted_pre_test_status" -ne 0 ] || [ -z "$targeted_pre_test_fingerprint" ]
  then
    echo "robustness-audit: cannot snapshot targeted mutation-audit paths before tests" >&2
    exit 2
  fi
  targeted_environment_fingerprint=$(printf 'GOCACHE=temporary\nGOMODCACHE=temporary\nGOTMPDIR=temporary\nGOPROXY=file://LOCAL_MODULE_PROXY\nGOSUMDB=off\nGONOPROXY=none\nGOPRIVATE=none\nGONOSUMDB=*\nGOTOOLCHAIN=local\nGOVCS=off\nGOENV=off\nGOWORK=off\nGOFLAGS=-mod=readonly\nCGO_ENABLED=0\nLOCAL_MODULE_PROXY_LOCATION=%s\nLOCAL_MODULE_PROXY_ARTIFACTS=%s\n%s\n' "$local_module_proxy_location_fingerprint" "$local_module_proxy_fingerprint" "$go_environment" | hash_stream)
  targeted_environment_status=$?
  if [ "$targeted_environment_status" -ne 0 ] || [ -z "$targeted_environment_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint the effective targeted Go environment" >&2
    exit 2
  fi
  targeted_tool_fingerprint=$(printf 'GO_EXECUTABLE %s\nGO_VERSION %s\nTARGETED_ENVIRONMENT %s\nGO_DEPENDENCY_GRAPH %s\nRESOURCE_GUARD_OUTPUT %s\n' "$(command -v go)" "$go_version" "$targeted_environment_fingerprint" "$go_deps" "$guard_output" | hash_stream)
  targeted_tool_status=$?
  if [ "$targeted_tool_status" -ne 0 ] || [ -z "$targeted_tool_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint targeted tool inputs" >&2
    exit 2
  fi
  targeted_input_fingerprint=$(printf 'DEFAULT_INPUTS %s\nTARGETED_CONTENT %s\nTARGETED_TOOLS %s\n' "$default_input_fingerprint" "$targeted_content_fingerprint" "$targeted_tool_fingerprint" | hash_stream)
  targeted_input_status=$?
  if [ "$targeted_input_status" -ne 0 ] || [ -z "$targeted_input_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint targeted inputs" >&2
    exit 2
  fi
  echo "FINGERPRINT TARGETED_CONTENT_SCOPE=$targeted_content_fingerprint"
  echo "FINGERPRINT TARGETED_ENVIRONMENT=$targeted_environment_fingerprint covers=GOCACHE,GOMODCACHE,GOTMPDIR,GOPROXY,GOSUMDB,GONOPROXY,GOPRIVATE,GONOSUMDB,GOTOOLCHAIN,GOVCS,GOENV,GOWORK,GOFLAGS,CGO_ENABLED,GOOS,GOARCH,GOMOD,GOVERSION,GOROOT,GOPATH,LOCAL_MODULE_PROXY_LOCATION,LOCAL_MODULE_PROXY_ARTIFACTS"
  echo "TARGETED_GO toolchain=local-toolchain cache=temporary module-proxy=local-file-proxy"
  echo "FINGERPRINT TARGETED_LOCAL_MODULE_PROXY=$local_module_proxy_fingerprint location_sha256=$local_module_proxy_location_fingerprint"
  echo "FINGERPRINT TARGETED_TOOLS=$targeted_tool_fingerprint covers=GO_EXECUTABLE,GO_VERSION,TARGETED_ENVIRONMENT,GO_DEPENDENCY_GRAPH,RESOURCE_GUARD_OUTPUT"
  echo "FINGERPRINT TARGETED_INPUTS=$targeted_input_fingerprint"
  echo "TARGETED go test ./internal/auth ./internal/memory ./internal/semantic ./internal/api offline=file-proxy scratch=temporary"
  if ! "${targeted_go_command[@]}" test ./internal/auth ./internal/memory ./internal/semantic ./internal/api
  then
    echo "robustness-audit: targeted test gate failed" >&2
    exit 2
  fi
  targeted_post_test_manifest=$(collect_targeted_mutation_paths)
  targeted_post_test_manifest_status=$?
  if [ "$targeted_post_test_manifest_status" -ne 0 ] || [ -z "$targeted_post_test_manifest" ]
  then
    echo "robustness-audit: cannot enumerate targeted mutation-audit paths after tests" >&2
    exit 2
  fi
  targeted_post_test_manifest_fingerprint=$(printf '%s\n' "$targeted_post_test_manifest" | hash_stream)
  targeted_post_test_manifest_status=$?
  if [ "$targeted_post_test_manifest_status" -ne 0 ] || [ -z "$targeted_post_test_manifest_fingerprint" ]
  then
    echo "robustness-audit: cannot fingerprint targeted mutation-audit manifest after tests" >&2
    exit 2
  fi
  if [ "$targeted_pre_test_manifest_fingerprint" != "$targeted_post_test_manifest_fingerprint" ]
  then
    echo "robustness-audit: targeted tests added or removed an audited repository path" >&2
    exit 2
  fi
  targeted_post_test_fingerprint=$(printf '%s\n' "$targeted_post_test_manifest" | fingerprint_manifest | hash_stream)
  targeted_post_test_status=$?
  if [ "$targeted_post_test_status" -ne 0 ] || [ -z "$targeted_post_test_fingerprint" ]
  then
    echo "robustness-audit: cannot snapshot targeted mutation-audit paths after tests" >&2
    exit 2
  fi
  if [ "$targeted_pre_test_fingerprint" != "$targeted_post_test_fingerprint" ]
  then
    echo "robustness-audit: targeted tests modified an audited repository path" >&2
    exit 2
  fi
fi

if [ "$blockers" -eq 0 ]
then
  if [ "$mode" = targeted ]
  then
    echo "RESULT targeted gates passed with no recognized static blockers"
    exit 0
  fi
  echo "RESULT static audit found no recognized blocker, but behavioral validation requires --targeted" >&2
  exit 2
fi

echo "RESULT blockers=$blockers"
exit 1
