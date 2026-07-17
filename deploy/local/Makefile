# Convenience targets for the local Kubernetes story.
# Run from anywhere: `make -C deploy/local up`.
#
# Vars (override on the command line, e.g. `make up ENGINE=k3d TIERS=networked`):
ENGINE    ?= auto        # auto | kind | k3d
INSTALLER ?= auto        # auto | helm | kustomize
TIERS     ?= minimal     # minimal | networked
CLUSTER   ?= aisotropy
IMAGE     ?= aisotropy:local
NAMESPACE ?= aisotropy

HERE := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
ROOT := $(abspath $(HERE)/../..)

export ENGINE INSTALLER TIERS CLUSTER IMAGE NAMESPACE

.PHONY: up down image lint template dry-run port-forward health logs

## up: create the cluster, build+load the image, install aisotropy
up:
	@bash $(HERE)/up.sh

## down: delete the local cluster
down:
	@bash $(HERE)/down.sh

## image: build the Distroless image from the repo Dockerfile
image:
	docker build -t $(IMAGE) $(ROOT)

## lint: helm lint (minimal + networked)
lint:
	helm lint $(ROOT)/deploy/helm/aisotropy
	helm lint $(ROOT)/deploy/helm/aisotropy --set tiers.networked.enabled=true --set auth.jwtSecret="this-is-a-32-byte-minimum-secret!"

## template: render both toggles to stdout
template:
	@echo "### minimal ###"
	helm template aisotropy $(ROOT)/deploy/helm/aisotropy
	@echo "### networked ###"
	helm template aisotropy $(ROOT)/deploy/helm/aisotropy --set tiers.networked.enabled=true

## dry-run: kubectl server dry-run of the raw manifests (needs a reachable cluster)
dry-run:
	kubectl apply --dry-run=client -k $(ROOT)/deploy/k8s/base
	kubectl apply --dry-run=client -k $(ROOT)/deploy/k8s/overlays/networked-tiers

## port-forward: forward the aisotropyd Service to localhost:8080
port-forward:
	kubectl -n $(NAMESPACE) port-forward svc/aisotropyd 8080:8080

## health: curl the liveness endpoint (requires an active port-forward)
health:
	curl -s localhost:8080/healthz && echo

## logs: tail the aisotropyd pod logs
logs:
	kubectl -n $(NAMESPACE) logs -l app.kubernetes.io/name=aisotropy -f
