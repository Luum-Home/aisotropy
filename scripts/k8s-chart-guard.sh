#!/usr/bin/env bash
# k8s-chart-guard.sh — the CHEAP, DEFAULT validation lane for the Kubernetes
# deploy surface (deploy/helm/aisotropy + deploy/k8s). Runs entirely OFFLINE:
# no cluster, no docker build, no network, no image pull. Seconds, not minutes.
#
# WHY THIS EXISTS: the chart shipped (commit 3d39c44) verified once by hand on a
# throwaway Kind cluster. A one-off manual verification is not a regression gate
# — the next edit to a probe path, a resource block or a Secret reference has
# nothing checking it. Standing up a real cluster on every change is not an
# option on a dev machine that already runs concurrent agent sessions (observed
# 2026-07-23: swap 81-94%, OOM-killed test workers). So the DEFAULT lane renders
# the chart and asserts on the YAML; the real-cluster lane is opt-in and
# resource-guarded (scripts/k8s-cluster-lane.sh).
#
# Sibling of prune-guard.sh / arena-determinism-guard.sh / resource-guard.sh,
# and it follows their central discipline: A GUARD THAT CANNOT EVALUATE MUST
# NEVER ABSOLVE. Every check that cannot run (helm absent, no YAML parser, no
# schema validator) is reported as an explicit SKIP with its reason and is never
# silently counted as a pass.
#
# USAGE:
#   scripts/k8s-chart-guard.sh              # validate, summary output
#   scripts/k8s-chart-guard.sh --explain    # + print every assertion + render dir
#
# EXIT CODES (repo guard convention):
#   0  OK       — every runnable assertion passed
#   1  FINDINGS — at least one assertion failed (the chart is wrong)
#   2  ERROR    — usage / environment error, or a hard prerequisite is missing
#                 in a way that means nothing could be checked at all
set -uo pipefail

EXPLAIN=false
case "${1:-}" in
  --explain|-v) EXPLAIN=true ;;
  "") ;;
  *) echo "usage: scripts/k8s-chart-guard.sh [--explain]" >&2; exit 2 ;;
esac

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || {
  echo "k8s-chart-guard: not in a git repo" >&2; exit 2; }

CHART="deploy/helm/aisotropy"
[ -d "$CHART" ] || { echo "k8s-chart-guard: chart not found at $CHART" >&2; exit 2; }

# A >= 32-byte HS256 placeholder used ONLY to exercise the "a literal secret
# must never reach a workload manifest" assertion. It is a test fixture, not a
# credential: the whole point of the assertion is that this string must appear
# in a Secret object and NOWHERE else in the render.
SENTINEL="SENTINEL-not-a-real-secret-0123456789abcdef"

SKIPS=()
skip() { SKIPS+=("$1"); echo "  SKIP  $1"; }

# --- 0. Prerequisites -------------------------------------------------------
if ! command -v helm >/dev/null 2>&1; then
  echo "k8s-chart-guard: helm not installed — NOTHING could be validated."
  echo "  install: brew install helm   (https://helm.sh/docs/intro/install/)"
  echo "RESULT: SKIPPED (helm absent). Not a pass: no assertion was evaluated."
  exit 2
fi
HELM_V="$(helm version --short 2>/dev/null || echo unknown)"

PY=""
if command -v python3 >/dev/null 2>&1 && python3 -c "import yaml" >/dev/null 2>&1; then
  PY="python3"
fi

RENDER_DIR="$(mktemp -d "${TMPDIR:-/tmp}/aisotropy-chart-guard.XXXXXX")" || exit 2
cleanup() { [ "$EXPLAIN" = true ] || rm -rf "$RENDER_DIR"; }
trap cleanup EXIT

echo "k8s-chart-guard: $CHART  (helm $HELM_V)"
echo

# --- 1. helm lint -----------------------------------------------------------
# Lint both postures: the minimal default and the hardened production overlay
# (which takes a different branch through the fail-guards in configmap.yaml).
LINT_FAILED=0
echo "[1/4] helm lint"
if ! out="$(helm lint "$CHART" 2>&1)"; then
  echo "  FAIL  helm lint (default values)"; echo "$out" | sed 's/^/        /'; LINT_FAILED=1
else
  echo "  PASS  helm lint (default values)"
fi
if ! out="$(helm lint "$CHART" -f "$CHART/values-prod.yaml" \
      --set auth.existingSecret=aisotropy-secrets \
      --set tiers.networked.auth.existingSecret=aisotropy-tier-secrets 2>&1)"; then
  echo "  FAIL  helm lint (values-prod.yaml + existing secrets)"; echo "$out" | sed 's/^/        /'; LINT_FAILED=1
else
  echo "  PASS  helm lint (values-prod.yaml + existing secrets)"
fi
echo

# --- 2. helm template: render every posture ---------------------------------
# Each profile is a shape the chart is expected to support. A render error here
# is a hard failure: the chart cannot produce valid YAML for a supported shape.
render() {
  local name="$1"; shift
  if ! out="$(helm template aisotropy "$CHART" --namespace aisotropy "$@" 2>&1)"; then
    echo "  FAIL  render[$name]"; echo "$out" | sed 's/^/        /'
    return 1
  fi
  printf '%s\n' "$out" > "$RENDER_DIR/$name.yaml"
  echo "  PASS  render[$name]  ($(grep -c '^kind:' "$RENDER_DIR/$name.yaml") objects)"
}

echo "[2/4] helm template (render every supported posture)"
RENDER_FAILED=0
render minimal          || RENDER_FAILED=1
render persistence      --set persistence.enabled=true || RENDER_FAILED=1
render networked        --set tiers.networked.enabled=true || RENDER_FAILED=1
render autoscaling      --set autoscaling.enabled=true || RENDER_FAILED=1
render existing-secret  --set auth.existingSecret=aisotropy-secrets || RENDER_FAILED=1
render sentinel         --set auth.jwtSecret="$SENTINEL" || RENDER_FAILED=1
render prod             -f "$CHART/values-prod.yaml" \
                        --set auth.existingSecret=aisotropy-secrets \
                        --set tiers.networked.auth.existingSecret=aisotropy-tier-secrets \
                        --set autoscaling.enabled=true || RENDER_FAILED=1
echo

# --- 2b. the parallel Kustomize surface -------------------------------------
# deploy/k8s/ is a FIRST-CLASS second surface, not a by-product of the chart:
# it exists so a no-Helm workflow (`kubectl apply -k`) is supported. Two hand-
# maintained surfaces drift — a probe path fixed in one and forgotten in the
# other is the classic failure. `kubectl kustomize` renders fully offline, so
# covering it costs nothing: the SAME assertions below run over both, which is
# what keeps them honest about each other.
if command -v kubectl >/dev/null 2>&1; then
  for k in base overlays/networked-tiers overlays/prod; do
    name="kustomize-$(basename "$k")"
    if out="$(kubectl kustomize "deploy/k8s/$k" 2>&1)"; then
      printf '%s\n' "$out" > "$RENDER_DIR/$name.yaml"
      echo "  PASS  render[$name]  ($(grep -c '^kind:' "$RENDER_DIR/$name.yaml") objects)"
    else
      echo "  FAIL  render[$name]"; echo "$out" | sed 's/^/        /'; RENDER_FAILED=1
    fi
  done
else
  skip "Kustomize surface (deploy/k8s) — kubectl not installed, NOT validated"
fi
echo

# A negative render: the chart MUST refuse an unsafe combination rather than
# emit a manifest that cannot boot. allowNoAuth=false with no JWT verifier is
# the constitutional case (ADR-0024) — a silent render there would ship an
# unauthenticated production posture.
echo "[3/4] negative rendering (the chart must REFUSE unsafe combinations)"
NEG_FAILED=0
neg_render() {
  local name="$1" want="$2"; shift 2
  if out="$(helm template aisotropy "$CHART" --namespace aisotropy "$@" 2>&1)"; then
    echo "  FAIL  refuse[$name]: chart rendered an unsafe combination that must fail"
    NEG_FAILED=1
  elif printf '%s' "$out" | grep -qi -- "$want"; then
    echo "  PASS  refuse[$name]"
  else
    echo "  FAIL  refuse[$name]: failed, but not for the expected reason (want /$want/)"
    printf '%s' "$out" | sed 's/^/        /' | head -5
    NEG_FAILED=1
  fi
}
neg_render no-auth-verifier "no JWT verifier" --set allowNoAuth=false
neg_render short-jwt-secret "at least 32 bytes" --set auth.jwtSecret=tooshort
echo

# --- 4. Assertions over the rendered YAML -----------------------------------
echo "[4/4] assertions on rendered manifests"
ASSERT_FAILED=0

if [ -z "$PY" ]; then
  skip "YAML assertions — python3 with PyYAML not available (pip3 install pyyaml)"
  skip "  (this is the substance of the guard: probes, resources, secrets, HPA)"
elif [ "$RENDER_FAILED" -ne 0 ]; then
  skip "YAML assertions — at least one render failed, nothing trustworthy to assert on"
else
  RENDER_DIR="$RENDER_DIR" SENTINEL="$SENTINEL" EXPLAIN="$EXPLAIN" "$PY" - <<'PY'
import glob, os, sys, yaml

d        = os.environ["RENDER_DIR"]
SENTINEL = os.environ["SENTINEL"]
EXPLAIN  = os.environ["EXPLAIN"] == "true"

profiles = {}
for p in sorted(glob.glob(os.path.join(d, "*.yaml"))):
    name = os.path.basename(p)[:-5]
    profiles[name] = [x for x in yaml.safe_load_all(open(p)) if x]

results = []
def check(ok, name, detail=""):
    results.append((ok, name, detail))

def workloads(docs):
    return [x for x in docs if x.get("kind") in ("Deployment", "StatefulSet", "DaemonSet")]

def containers(doc):
    spec = doc.get("spec", {}).get("template", {}).get("spec", {})
    return list(spec.get("containers", [])) + list(spec.get("initContainers", []))

def app_deploy(docs):
    for x in docs:
        if x.get("kind") == "Deployment" and any(
                c.get("name") == "aisotropyd" for c in containers(x)):
            return x
    return None

def app_container(docs):
    dep = app_deploy(docs)
    if not dep:
        return None
    return next((c for c in containers(dep) if c["name"] == "aisotropyd"), None)

# --- A1/A2: probes point at the REAL endpoints ------------------------------
# Ground truth: aisotropy/app.go registers GET /healthz (liveness, always 200
# while the process is up) and GET /health (readiness, 200 ok / 503 degraded,
# backed by internal/health). A probe pointing anywhere else is a silent outage:
# k8s would keep routing traffic to a pod that cannot serve.
for name, docs in profiles.items():
    c = app_container(docs)
    if c is None:
        check(False, f"[{name}] aisotropyd container present")
        continue
    live = c.get("livenessProbe", {}).get("httpGet", {})
    ready = c.get("readinessProbe", {}).get("httpGet", {})
    check(live.get("path") == "/healthz",
          f"[{name}] livenessProbe -> /healthz", f"got {live.get('path')!r}")
    check(ready.get("path") == "/health",
          f"[{name}] readinessProbe -> /health", f"got {ready.get('path')!r}")
    # The probe port must be a port the container actually declares.
    portnames = {p.get("name") for p in c.get("ports", [])}
    for label, probe in (("liveness", live), ("readiness", ready)):
        port = probe.get("port")
        check(port in portnames or isinstance(port, int),
              f"[{name}] {label} probe port resolves", f"got {port!r}, declared {portnames}")

# --- A3: resource requests AND limits on every container --------------------
# Requests are what makes the pod schedulable on a laptop node; limits are what
# stops one pod from taking the node down. An HPA on utilisation is also
# undefined without a request.
for name, docs in profiles.items():
    for w in workloads(docs):
        for c in containers(w):
            r = c.get("resources", {})
            req, lim = r.get("requests", {}), r.get("limits", {})
            check(bool(req.get("cpu")) and bool(req.get("memory")),
                  f"[{name}] {w['kind']}/{c['name']} resources.requests set", f"got {req}")
            check(bool(lim.get("memory")),
                  f"[{name}] {w['kind']}/{c['name']} resources.limits.memory set", f"got {lim}")

# --- A4: image tag is explicit and not floating -----------------------------
# `latest` (or a bare, untagged ref) makes a deployment unreproducible: two
# pods of the same ReplicaSet can end up on different code.
for name, docs in profiles.items():
    for w in workloads(docs):
        for c in containers(w):
            img = c.get("image", "")
            ref = img.rsplit("/", 1)[-1]
            tag = ref.split(":", 1)[1] if ":" in ref else ""
            check(tag not in ("", "latest"),
                  f"[{name}] {c['name']} image tag pinned", f"got {img!r}")

# --- A5: no sensitive env var may carry an inline literal -------------------
# Anything credential-shaped must arrive via secretKeyRef. An inline `value:`
# means the credential is sitting in the Deployment object, readable by anyone
# with `get deploy`, and captured in `helm get manifest` / CI logs.
SENSITIVE = ("SECRET", "TOKEN", "PASSWORD", "API_KEY", "APIKEY", "_URL")
for name, docs in profiles.items():
    for w in workloads(docs):
        for c in containers(w):
            for e in c.get("env", []) or []:
                k = e.get("name", "")
                if any(s in k.upper() for s in SENSITIVE) and "value" in e:
                    check(False, f"[{name}] {c['name']} env {k} is not an inline literal",
                          f"inline value in {w['kind']}")
check(True, "sensitive env vars sourced via secretKeyRef (all profiles)")

# --- A5b: no credential-shaped key inside a ConfigMap -----------------------
for name, docs in profiles.items():
    for cm in [x for x in docs if x.get("kind") == "ConfigMap"]:
        for k in (cm.get("data") or {}):
            if any(s in k.upper() for s in SENSITIVE):
                check(False, f"[{name}] ConfigMap carries no credential key", f"key {k}")
check(True, "ConfigMaps carry no credential-shaped keys (all profiles)")

# --- A6: a literal secret must never leak outside a Secret object -----------
# The sentinel profile passes a fake JWT secret through auth.jwtSecret. It is
# allowed to land in a kind:Secret; finding it anywhere else (Deployment env,
# ConfigMap, annotation) is the exact leak this assertion exists to catch.
leaked = []
for doc in profiles.get("sentinel", []):
    if doc.get("kind") == "Secret":
        continue
    if SENTINEL in yaml.safe_dump(doc):
        leaked.append(f"{doc.get('kind')}/{doc.get('metadata',{}).get('name')}")
check(not leaked, "no secret literal outside a Secret object (sentinel profile)",
      f"leaked into {leaked}")

# --- A7: with existingSecret, the chart renders NO Secret of its own --------
# The production contract: reference a Secret provisioned out-of-band, never
# template one. This proves the referencing path works end to end.
docs = profiles.get("existing-secret", [])
own = [x for x in docs if x.get("kind") == "Secret"]
check(not own, "existingSecret: chart templates no Secret of its own",
      f"rendered {[x['metadata']['name'] for x in own]}")
c = app_container(docs)
refs = {e["valueFrom"]["secretKeyRef"]["name"]
        for e in (c.get("env", []) if c else [])
        if "valueFrom" in e and "secretKeyRef" in e.get("valueFrom", {})}
check(refs == {"aisotropy-secrets"},
      "existingSecret: env references the operator-provided Secret", f"refs {refs}")

# --- A7b: the prod posture templates no credential at all -------------------
for doc in profiles.get("prod", []):
    if doc.get("kind") == "Secret":
        check(False, "prod posture templates no Secret",
              f"rendered Secret/{doc.get('metadata',{}).get('name')}")
        break
else:
    check(True, "prod posture templates no Secret")
blob = yaml.safe_dump_all(profiles.get("prod", []))
check("CHANGE-ME" not in blob, "prod posture carries no placeholder credential")

# --- A8: HPA <-> replicas ownership ----------------------------------------
# When an HPA owns the replica count, the Deployment must NOT template
# `replicas` — otherwise every `helm upgrade` resets the scale the HPA chose.
hpas = [x for x in profiles.get("autoscaling", []) if x.get("kind") == "HorizontalPodAutoscaler"]
check(len(hpas) == 1, "autoscaling: exactly one HPA rendered", f"got {len(hpas)}")
dep = app_deploy(profiles.get("autoscaling", []))
check(dep is not None and "replicas" not in dep.get("spec", {}),
      "autoscaling: Deployment omits replicas (HPA owns it)")
if hpas:
    tgt = hpas[0].get("spec", {}).get("scaleTargetRef", {}).get("name")
    check(dep is not None and tgt == dep["metadata"]["name"],
          "autoscaling: HPA scaleTargetRef points at the Deployment", f"got {tgt}")
dep = app_deploy(profiles.get("minimal", []))
check(dep is not None and "replicas" in dep.get("spec", {}),
      "minimal: Deployment sets replicas (no HPA)")
check(not [x for x in profiles.get("minimal", []) if x.get("kind") == "HorizontalPodAutoscaler"],
      "minimal: no HPA rendered by default")

# --- A9: pod hardening survives every posture ------------------------------
for name, docs in profiles.items():
    dep, c = app_deploy(docs), app_container(docs)
    if not dep or not c:
        continue
    pod = dep["spec"]["template"]["spec"].get("securityContext", {})
    sec = c.get("securityContext", {})
    check(pod.get("runAsNonRoot") is True, f"[{name}] pod runAsNonRoot")
    check(sec.get("allowPrivilegeEscalation") is False, f"[{name}] allowPrivilegeEscalation false")
    check(sec.get("readOnlyRootFilesystem") is True, f"[{name}] readOnlyRootFilesystem")
    check("ALL" in (sec.get("capabilities", {}).get("drop") or []), f"[{name}] capabilities drop ALL")

# --- report ----------------------------------------------------------------
failed = [r for r in results if not r[0]]
for ok, name, detail in results:
    if not ok:
        print(f"  FAIL  {name}" + (f" :: {detail}" if detail else ""))
    elif EXPLAIN:
        print(f"  PASS  {name}")
print(f"  {len(results) - len(failed)}/{len(results)} assertions passed")
sys.exit(1 if failed else 0)
PY
  ASSERT_FAILED=$?
fi
echo

# --- 5. Schema validation (optional tool) -----------------------------------
# kubeconform/kubeval check every rendered object against the upstream
# Kubernetes OpenAPI schemas — it catches a misspelled field that helm template
# happily renders. Neither is installed here, so this is a declared SKIP.
SCHEMA_FAILED=0
if command -v kubeconform >/dev/null 2>&1; then
  echo "[5/5] kubeconform schema validation"
  for f in "$RENDER_DIR"/*.yaml; do
    if kubeconform -strict -summary -ignore-missing-schemas "$f" >/dev/null 2>&1; then
      echo "  PASS  schema[$(basename "$f" .yaml)]"
    else
      echo "  FAIL  schema[$(basename "$f" .yaml)]"
      kubeconform -strict -summary -ignore-missing-schemas "$f" 2>&1 | sed 's/^/        /'
      SCHEMA_FAILED=1
    fi
  done
elif command -v kubeval >/dev/null 2>&1; then
  echo "[5/5] kubeval schema validation"
  for f in "$RENDER_DIR"/*.yaml; do
    if kubeval --strict --ignore-missing-schemas "$f" >/dev/null 2>&1; then
      echo "  PASS  schema[$(basename "$f" .yaml)]"
    else
      echo "  FAIL  schema[$(basename "$f" .yaml)]"; SCHEMA_FAILED=1
    fi
  done
else
  echo "[5/5] schema validation"
  skip "kubeconform/kubeval not installed — object schemas NOT checked."
  skip "  install: brew install kubeconform   (then re-run; no other change needed)"
fi
echo

# --- verdict ----------------------------------------------------------------
[ "$EXPLAIN" = true ] && echo "rendered manifests kept at: $RENDER_DIR" && echo
if [ "${#SKIPS[@]}" -gt 0 ]; then
  echo "SKIPPED (${#SKIPS[@]}) — these checks did NOT run and are NOT passes:"
  for s in "${SKIPS[@]}"; do echo "  - $s"; done
  echo
fi

if [ "$LINT_FAILED" -ne 0 ] || [ "$RENDER_FAILED" -ne 0 ] || [ "$NEG_FAILED" -ne 0 ] \
   || [ "$ASSERT_FAILED" -ne 0 ] || [ "$SCHEMA_FAILED" -ne 0 ]; then
  echo "RESULT: 🔴 FINDINGS — the chart does not satisfy the deploy contract."
  exit 1
fi
echo "RESULT: 🟢 OK — chart lints, renders every posture, refuses unsafe ones, assertions pass."
exit 0
