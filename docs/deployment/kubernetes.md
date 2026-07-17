# Deploying aisotropy on Kubernetes

aisotropy ships as a **single Distroless Go binary** (`gcr.io/distroless/static-debian12:nonroot`,
built from the repo `Dockerfile`). It boots a complete runtime from **in-process
defaults** — BadgerDB working memory + in-process SQLite/FTS5 facts + in-process
cosine semantic — so the minimal Kubernetes deployment is **one Deployment + one
Service and nothing else**. No rqlite, no Qdrant, no Docker-in-the-cluster.

Two shapes, one image:

| Shape | What runs | When |
|---|---|---|
| **Minimal** (default) | aisotropyd only, in-process tiers | local clusters, demos, single-tenant, "just run it" |
| **Networked tiers** (opt-in) | aisotropyd + rqlite (facts) + Qdrant (semantic) | the tiered-memory moat, mirrors `docker-compose.yml` |

Both are provided as a **Helm chart** (`deploy/helm/aisotropy`) and as **raw
Kustomize manifests** (`deploy/k8s/`). Pick whichever your workflow prefers.

Grounding (all values come from the real repo):

- Image: `Dockerfile` → Distroless static nonroot, `EXPOSE 8080`, entrypoint `/aisotropyd`, runs as UID/GID `65532`.
- Listen port: `AISOTROPY_ADDR` (default `:8080`, from `cmd/aisotropyd` / `aisotropy/app.go`).
- Liveness: `GET /healthz` → `{"status":"ok"}` (outside the OpenAPI contract).
- Readiness: `GET /health` → `{"status":"ok"|"degraded",...}`, HTTP 200 or 503 (`internal/health`).
- Auth: `AISOTROPY_JWT_SECRET` (HS256, **≥ 32 bytes** or the binary refuses to boot; unset → NoAuth, ADR-0024).
- On-disk mode: `AISOTROPY_DATA_DIR` persists BadgerDB working memory + embedded SQLite facts.
- Networked tiers: `RQLITE_URL`, `QDRANT_HOST` / `QDRANT_PORT` / `QDRANT_COLLECTION`.

---

## 1. The minimal path (start here)

### Prerequisites

- A cluster (see [local clusters](#4-local-cluster-notes): Kind, k3d, k3s, OrbStack, Talos, DevPod).
- `kubectl`. Helm optional (raw Kustomize works without it).
- The image loaded into the cluster (local clusters don't share your Docker image store):

```bash
docker build -t aisotropy:local .
# then, for your engine — see section 4. e.g. Kind:
kind load docker-image aisotropy:local --name aisotropy
```

The manifests use `imagePullPolicy: IfNotPresent`, so a locally loaded
`aisotropy:local` tag is used as-is and never pulled from a registry.

### Install with Helm

```bash
helm upgrade --install aisotropy deploy/helm/aisotropy \
  --namespace aisotropy --create-namespace --wait
```

That is the whole minimal deployment: one Deployment (in-process tiers), one
Service, a ConfigMap. No Secret, no PVC, no StatefulSets are rendered.

### Install with Kustomize (no Helm)

```bash
kubectl apply -k deploy/k8s/base
kubectl -n aisotropy rollout status deploy/aisotropyd
```

### Verify

```bash
# Helm release Service is "aisotropy"; Kustomize Service is "aisotropyd".
kubectl -n aisotropy port-forward svc/aisotropy 8080:8080     # or svc/aisotropyd
curl -s localhost:8080/healthz     # -> {"status":"ok"}
curl -s localhost:8080/health      # -> {"status":"ok",...} once tiers respond
```

### One command

```bash
deploy/local/up.sh        # auto-detects Kind/k3d, builds+loads the image, installs
deploy/local/down.sh      # tear down
```

> **Security note.** With `AISOTROPY_JWT_SECRET` unset the binary boots in
> **NoAuth** mode: the path `tenant_id`/`user_id` are trusted with no credential
> check. That is fine for a local throwaway cluster and keeps the minimal path
> zero-config, but set a real secret (below) before anything is reachable.

---

## 2. Turning on the knobs

### Authentication (JWT, HS256)

The secret must be **≥ 32 bytes** — the binary refuses to boot on a shorter key,
and the Helm chart fails `template`/`install` early with the same rule.

```bash
helm upgrade --install aisotropy deploy/helm/aisotropy -n aisotropy --create-namespace \
  --set auth.jwtSecret="$(openssl rand -base64 48)"
```

Or reference a Secret you manage yourself:

```bash
helm ... --set auth.existingSecret=my-aisotropy-secret \
         --set auth.jwtSecretKey=AISOTROPY_JWT_SECRET
```

Kustomize: put the value into the `aisotropy-secrets` Secret (the Deployment
already wires it with `optional: true`, so it stays NoAuth until you do):

```bash
kubectl -n aisotropy create secret generic aisotropy-secrets \
  --from-literal=AISOTROPY_JWT_SECRET="$(openssl rand -base64 48)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

### LLM provider key (optional)

Unset → the keyless **stub** LLM (canned responses); the runtime still boots.

```bash
helm ... --set anthropicApiKey=sk-ant-...
# Kustomize: add ANTHROPIC_API_KEY to the aisotropy-secrets Secret.
```

### On-disk persistence (`AISOTROPY_DATA_DIR`)

Persists BadgerDB working memory + the embedded SQLite facts store across pod
restarts. Uses a PVC on the cluster's default StorageClass (kind: `standard`,
k3s/k3d: `local-path`).

```bash
helm ... --set persistence.enabled=true --set persistence.size=1Gi
```

Kustomize: uncomment the `persistence` component in `deploy/k8s/base/kustomization.yaml`.

---

## 3. The networked-tiers overlay (the moat)

This layers the same binary onto **real** networked memory tiers — rqlite (facts,
SQLite + FTS5, Raft) and Qdrant (semantic, HNSW with per-tenant payload
filtering) — deployed as StatefulSets with their own PVCs. It is the Kubernetes
equivalent of the repo's `docker-compose.yml`, and flips aisotropyd off its
in-process fallbacks via `RQLITE_URL` and `QDRANT_HOST`.

### Helm

```bash
helm upgrade --install aisotropy deploy/helm/aisotropy -n aisotropy --create-namespace \
  --set tiers.networked.enabled=true --wait
```

Renders, additionally: `rqlite` + `qdrant` StatefulSets and Services, plus the
`RQLITE_URL` / `QDRANT_HOST` / `QDRANT_PORT` / `QDRANT_COLLECTION` env wiring.

### Kustomize

```bash
kubectl apply -k deploy/k8s/overlays/networked-tiers
```

The overlay = `base` + `rqlite.yaml` + `qdrant.yaml` + an env patch on the
`aisotropyd` Deployment.

### Verify the moat is live

```bash
kubectl -n aisotropy port-forward svc/aisotropy 8080:8080
curl -s localhost:8080/health | jq     # facts + semantic probes now hit the
                                        # REAL rqlite/Qdrant -> {"status":"ok"}
```

`/health` returns `503 degraded` until both tiers report ready — the readiness
probe on aisotropyd will hold traffic until then.

Ports (mirrors `docker-compose.yml`): rqlite HTTP `4001` / Raft `4002`; Qdrant
REST `6333` / gRPC `6334` (the Go client uses gRPC).

### Tier hardening (on by default in this overlay)

The networked tiers do **not** run open. A shared-cluster pod cannot reach them
and bypass the app-layer tenant-isolation gate, because:

- **rqlite auth** — `rqlited` runs with `-auth` (an authz users file). The
  `aisotropy` app user has full perms; the anonymous `*` user has only
  `ready`/`status` so the unauthenticated `/readyz` probe still works while
  `/db/query` and `/db/execute` require credentials. aisotropyd presents them via
  `RQLITE_URL` (basic-auth embedded in the URL).
- **Qdrant API key** — the Qdrant service requires `QDRANT__SERVICE__API_KEY`;
  aisotropyd presents the same value via `QDRANT_API_KEY`.
- **Secrets, not ConfigMaps** — both credentials live in the `aisotropy-tier-secrets`
  Secret. The values shipped in the overlay are **LOCAL-ONLY placeholders**
  (`*-CHANGE-ME`); override them out-of-band for anything reachable.
- **NetworkPolicy** — `networkpolicy.yaml` default-denies ingress to the rqlite
  and Qdrant pods and allows **only** the `aisotropyd` pod (pod-selector based).
  This is enforced only by a policy-capable CNI (Calico/Cilium/Antrea); kind's
  kindnet and k3s's flannel render it but do not enforce — install a capable CNI
  for real isolation.
- **Non-root tier pods** — rqlite and Qdrant run `runAsNonRoot` (UID 1000,
  `fsGroup` for the PVC), `allowPrivilegeEscalation: false`, all capabilities
  dropped, `seccompProfile: RuntimeDefault` — mirroring the aisotropyd pod.

### Helm equivalent

```bash
helm upgrade --install aisotropy deploy/helm/aisotropy -n aisotropy --create-namespace \
  --set tiers.networked.enabled=true \
  --set tiers.networked.auth.qdrantApiKey="$(openssl rand -base64 32)" \
  --set tiers.networked.auth.rqlitePassword="$(openssl rand -base64 24)"
```

The chart renders the tier Secret, the NetworkPolicy (`networkPolicy.enabled`,
default true), and the hardened tier pods. Point at your own Secret instead with
`--set tiers.networked.auth.existingSecret=<name>` (keys: `QDRANT_API_KEY`,
`RQLITE_URL`, `rqlite-users.json`).

---

## 3a. The production overlay (reachable deployments)

`overlays/networked-tiers` is still a **local/dev** posture: aisotropyd runs
NoAuth and does not require TLS. For anything reachable, use `overlays/prod`,
which layers two more gates on top of everything above:

```bash
# 1. Provide real secrets FIRST (the overlay ships none):
kubectl -n aisotropy create secret generic aisotropy-secrets \
  --from-literal=AISOTROPY_JWT_SECRET="$(openssl rand -base64 48)" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n aisotropy create secret generic aisotropy-tier-secrets \
  --from-literal=QDRANT_API_KEY="$(openssl rand -base64 32)" \
  --from-literal=RQLITE_URL="http://aisotropy:$(openssl rand -base64 24 | tr -d /=+)@rqlite:4001" \
  --from-file=rqlite-users.json=/path/to/your/rqlite-users.json \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. Apply the hardened overlay:
kubectl apply -k deploy/k8s/overlays/prod
```

What `overlays/prod` changes vs. `networked-tiers`:

- **`AISOTROPY_REQUIRE_TLS=1`** — aisotropyd refuses plaintext. Front it with a
  TLS-terminating ingress/proxy (see [reverse-proxy.md](reverse-proxy.md) and
  `deploy/proxy/nginx.conf`'s `listen 443 ssl` block).
- **`AISOTROPY_ALLOW_NOAUTH` is removed** — the NoAuth opt-out is gone, so a
  `>= 32`-byte `AISOTROPY_JWT_SECRET` is **required** (the binary trusts no
  unauthenticated identity).

Helm equivalent — `values-prod.yaml` (`requireTLS: true`, `allowNoAuth: false`):

```bash
helm upgrade --install aisotropy deploy/helm/aisotropy -n aisotropy --create-namespace \
  -f deploy/helm/aisotropy/values-prod.yaml \
  --set auth.jwtSecret="$(openssl rand -base64 48)" \
  --set tiers.networked.auth.qdrantApiKey="$(openssl rand -base64 32)" \
  --set tiers.networked.auth.rqlitePassword="$(openssl rand -base64 24)"
```

The chart **fails to render** if `allowNoAuth: false` and no JWT verifier is set —
a fail-closed guard against exposing a NoAuth deployment.

---

## 4. Local cluster notes

Full copy-paste for each is in [`deploy/local/README.md`](../../deploy/local/README.md).
Summary of the one difference that matters — **how the image reaches the node**:

| Tool | Image into cluster | Notes |
|---|---|---|
| **Kind** | `kind load docker-image aisotropy:local --name <c>` | Kubernetes-in-Docker; `up.sh` default |
| **k3d** | `k3d image import aisotropy:local -c <c>` | k3s-in-Docker; lightest full-featured option |
| **k3s** | `k3s ctr images import <tar>` | bare host, containerd (not Docker); `docker save` first |
| **OrbStack** | *none* | built-in k8s shares OrbStack's image store — build and go |
| **Talos** | push to a registry | immutable nodes, no `load`; use `image.pullPolicy=Always` |
| **Podman** | `podman save ... | kind load image-archive /dev/stdin` | Podman as Kind's engine (`KIND_EXPERIMENTAL_PROVIDER=podman`) |
| **DevPod** | run k3d/Kind inside the workspace | pair with k3d for a self-contained combo |

All of them then use the **same** `helm install` / `kubectl apply -k` from
sections 1–3 — only the build+load step differs.

---

## 5. What's in the box

```
deploy/
  helm/aisotropy/            Helm chart (minimal-by-default)
    Chart.yaml
    values.yaml              image, replicas, resources, auth.jwtSecret,
                             tiers.networked.enabled, persistence, ingress
    values-prod.yaml         hardened prod overlay (requireTLS, allowNoAuth:false)
    templates/               deployment, service, configmap, secret, pvc,
                             ingress, serviceaccount, rqlite, qdrant,
                             tier-secret, networkpolicy, NOTES
  k8s/
    base/                    minimal Kustomize base (in-process tiers)
    components/persistence/  opt-in PVC + AISOTROPY_DATA_DIR
    overlays/networked-tiers/  base + rqlite + Qdrant + env patch + tier auth
                             (Secret) + NetworkPolicy + non-root tier pods
    overlays/prod/           networked-tiers + AISOTROPY_REQUIRE_TLS, NoAuth off
  rqlite/                    users.json — LOCAL-ONLY rqlite -auth file (compose)
  local/
    up.sh / down.sh          one-command Kind/k3d bring-up + teardown
    Makefile                 make up / down / lint / template / health
    README.md                per-tool copy-paste (Kind/k3d/k3s/OrbStack/Talos/Podman/DevPod)
```

### Pod hardening (both shapes)

The aisotropyd pod runs `runAsNonRoot` (UID 65532, matching the Distroless
image), `readOnlyRootFilesystem: true` (with an `emptyDir` at `/tmp`), all
capabilities dropped, `allowPrivilegeEscalation: false`, and `seccompProfile:
RuntimeDefault`. `fsGroup: 65532` lets the optional data PVC be group-writable.

The **rqlite and Qdrant** tier pods (networked overlay) mirror this: `runAsNonRoot`
(UID 1000 with `fsGroup: 1000` for the data PVC), `allowPrivilegeEscalation: false`,
all capabilities dropped, `seccompProfile: RuntimeDefault`. They keep a writable
root filesystem (unlike aisotropyd) because both write working files outside the
mounted data volume. Combined with tier auth and the NetworkPolicy, the memory
tiers are reachable only by aisotropyd and only with credentials.

### Related docs

- `docs/deployment/docker-compose.md` — the Docker Compose equivalent.
- `docs/deployment/distroless.md` — why the image is Distroless + the size/RAM numbers.
- `.claude/rules/services-config.md` — the full `AISOTROPY_*` env-var contract.
