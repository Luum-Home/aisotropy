# Local Kubernetes bring-up

One-command aisotropy on a lightweight, local cluster. The default path is the
**truly minimal** one: a single Distroless Go binary that boots a complete
runtime with in-process memory tiers (BadgerDB working memory + in-process
SQLite/FTS5 facts + in-process cosine semantic). **No rqlite, no Qdrant, no
Docker-in-the-cluster.**

```bash
deploy/local/up.sh        # auto-detects Kind or k3d, builds + loads the image, installs
# ... work ...
deploy/local/down.sh      # delete the cluster
```

`up.sh` will:

1. detect **Kind** or **k3d** (whichever is installed; `ENGINE=kind|k3d` to force),
2. create a throwaway cluster named `aisotropy` (idempotent — reused if present),
3. `docker build -t aisotropy:local .` from the repo `Dockerfile`,
4. side-load that tag into the cluster (`kind load` / `k3d image import`),
5. install with **Helm** if present, else **Kustomize** (`INSTALLER=helm|kustomize` to force),
6. print the `port-forward` + `curl /healthz` commands.

Add the networked memory tiers (rqlite + Qdrant) with `TIERS=networked`.

> The image tag `aisotropy:local` exists only on your machine, so the manifests
> use `imagePullPolicy: IfNotPresent` — the cluster never tries to pull it from a
> registry. This is why the image must be *loaded* into the node, not just built.

---

## Per-tool copy-paste

Everything below assumes you are at the repo root and the image is built:

```bash
docker build -t aisotropy:local .
```

### Kind (Kubernetes-in-Docker)

```bash
kind create cluster --name aisotropy
kind load docker-image aisotropy:local --name aisotropy

helm upgrade --install aisotropy deploy/helm/aisotropy \
  --namespace aisotropy --create-namespace --wait
# or, without Helm:
kubectl apply -k deploy/k8s/base

kubectl -n aisotropy port-forward svc/aisotropy 8080:8080   # svc/aisotropyd if you used kubectl apply -k
curl -s localhost:8080/healthz     # -> {"status":"ok"}
```

Tear down: `kind delete cluster --name aisotropy`.

### k3d (k3s-in-Docker)

```bash
k3d cluster create aisotropy
k3d image import aisotropy:local -c aisotropy

helm upgrade --install aisotropy deploy/helm/aisotropy \
  --namespace aisotropy --create-namespace --wait
kubectl -n aisotropy port-forward svc/aisotropy 8080:8080
curl -s localhost:8080/healthz
```

Tear down: `k3d cluster delete aisotropy`.

### k3s (bare host, no Docker wrapper)

k3s bundles containerd, not Docker, so import the image straight into containerd:

```bash
docker save aisotropy:local -o /tmp/aisotropy.tar
sudo k3s ctr images import /tmp/aisotropy.tar

sudo k3s kubectl apply -k deploy/k8s/base
# Helm against the k3s kubeconfig:
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
helm upgrade --install aisotropy deploy/helm/aisotropy -n aisotropy --create-namespace --wait
```

### OrbStack (built-in Kubernetes)

OrbStack ships a one-click Kubernetes that shares OrbStack's Docker image store,
so **no image load step is needed** — a locally built tag is already visible:

```bash
# Enable Kubernetes in OrbStack (Settings -> Kubernetes), then:
docker build -t aisotropy:local .          # OrbStack's builder = the cluster's image store
kubectl config use-context orbstack
helm upgrade --install aisotropy deploy/helm/aisotropy -n aisotropy --create-namespace --wait
kubectl -n aisotropy port-forward svc/aisotropy 8080:8080
curl -s localhost:8080/healthz
```

### Talos Linux

Talos is an API-managed, immutable cluster. Build once, push to a registry Talos
can reach (Talos nodes have no Docker to `load` into), then apply:

```bash
docker build -t <your-registry>/aisotropy:local .
docker push  <your-registry>/aisotropy:local

helm upgrade --install aisotropy deploy/helm/aisotropy \
  -n aisotropy --create-namespace \
  --set image.repository=<your-registry>/aisotropy \
  --set image.tag=local \
  --set image.pullPolicy=Always --wait
```

For an all-local Talos (e.g. `talosctl cluster create`), run a local registry and
point the nodes' registry mirror at it, then use `image.pullPolicy=Always`.

### Podman (as the Kind/container engine)

Podman can back Kind in place of Docker:

```bash
export KIND_EXPERIMENTAL_PROVIDER=podman
kind create cluster --name aisotropy

podman build -t aisotropy:local .
# stream the image into kind's node via stdin:
podman save aisotropy:local | kind load image-archive /dev/stdin --name aisotropy

kubectl apply -k deploy/k8s/base
```

### DevPod

DevPod gives you a dev container/workspace; run any of the above **inside** it.
The simplest self-contained combo is DevPod + k3d (Docker available in the
workspace):

```bash
# inside the DevPod workspace:
k3d cluster create aisotropy
docker build -t aisotropy:local .
k3d image import aisotropy:local -c aisotropy
kubectl apply -k deploy/k8s/base
kubectl -n aisotropy port-forward --address 0.0.0.0 svc/aisotropyd 8080:8080
```

---

## Makefile shortcuts

```bash
make -C deploy/local up                       # bring up (auto-detect)
make -C deploy/local up TIERS=networked       # + rqlite + Qdrant
make -C deploy/local up ENGINE=k3d            # force k3d
make -C deploy/local lint                     # helm lint (both toggles)
make -C deploy/local template                 # render both toggles
make -C deploy/local port-forward             # forward svc to :8080
make -C deploy/local health                   # curl /healthz
make -C deploy/local down                      # delete cluster
```

## Enabling auth / persistence / networked tiers

| Want | Helm | Kustomize |
|---|---|---|
| JWT auth (HS256, >= 32 bytes) | `--set auth.jwtSecret="$(openssl rand -base64 48)"` | populate `AISOTROPY_JWT_SECRET` in the `aisotropy-secrets` Secret |
| Anthropic key | `--set anthropicApiKey=sk-ant-...` | add `ANTHROPIC_API_KEY` to the Secret |
| On-disk persistence | `--set persistence.enabled=true` | uncomment the `persistence` component in `base/kustomization.yaml` |
| Networked tiers | `--set tiers.networked.enabled=true` | `kubectl apply -k deploy/k8s/overlays/networked-tiers` |

See `docs/deployment/kubernetes.md` for the full reference.
