# kardinal-promoter Makefile

# Tools
CONTROLLER_GEN         ?= $(LOCALBIN)/controller-gen
CONTROLLER_GEN_VERSION ?= v0.17.3
GOLANGCI_LINT          ?= $(LOCALBIN)/golangci-lint
GOLANGCI_LINT_VERSION  ?= v2.11.4
GOVULNCHECK            ?= $(LOCALBIN)/govulncheck
LOCALBIN               ?= $(shell pwd)/bin

# Build
BINARY_CONTROLLER = bin/kardinal-controller
BINARY_CLI        = bin/kardinal
BINARY_AGENT      = bin/kardinal-agent
GO                = go
GOPROXY          ?= https://proxy.golang.org

# Docker image (the chart deploys $(IMG_REPO):$(IMG_TAG) when installed by `make install`)
IMG_REPO ?= ghcr.io/pnz1990/kardinal-promoter
IMG_TAG  ?= dev
IMG      ?= $(IMG_REPO):$(IMG_TAG)

# kind cluster used by the e2e targets
KIND_CLUSTER ?= kardinal-e2e

.PHONY: all build build-controller build-cli build-agent ui ui-test ui-test-e2e test test-integration test-cover \
        lint lint-local vet vuln generate manifests \
        install uninstall docker-build helm-lint validate-manifests \
        install-kro setup-e2e-env setup-e2e-env-fast setup-multi-cluster-env eks-up eks-down \
        e2e-setup e2e-teardown kind-up kind-down \
        test-e2e test-e2e-kind test-e2e-journey-1 test-e2e-journey-2 test-e2e-journey-3 \
        test-e2e-journey-4 test-e2e-journey-5 \
        tools help

all: generate build test lint

## Build
build: build-controller build-cli build-agent

build-controller:
	$(GO) build -o $(BINARY_CONTROLLER) ./cmd/kardinal-controller/

build-cli:
	$(GO) build -o $(BINARY_CLI) ./cmd/kardinal/

build-agent:
	$(GO) build -o $(BINARY_AGENT) ./cmd/kardinal-agent/

## UI
ui: ## Build the embedded React UI (requires Node.js and npm)
	cd web && npm ci && npm run build

ui-test: ## Run React component unit tests (vitest, requires Node.js and npm)
	cd web && npm ci && npm test

ui-test-e2e: ## Run Playwright E2E tests (requires Node.js, npm, and a built dist/)
	cd web && npm ci && npm run build && npx playwright install chromium --with-deps && npm run test:e2e

## Test
## Cluster e2e tests (test/e2e with build tag e2e) are excluded; see test-e2e-kind.
test:
	$(GO) test ./... -race -count=1 -timeout 120s

test-integration: ## Run integration tests (fake client, no cluster required)
	$(GO) test ./test/integration/... -tags integration -race -count=1 -timeout 120s

test-cover:
	$(GO) test ./... -race -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html

## Lint / Vet
vet:
	$(GO) vet ./...

## lint-local: run go vet + staticcheck locally (faster than golangci-lint, catches QF1008-class issues)
## Install staticcheck: go install honnef.co/go/tools/cmd/staticcheck@latest
lint-local:
	$(GO) vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not found — install with: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
		exit 1; \
	fi

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

vuln: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

## Generate (CRD manifests + DeepCopy)
## IMPORTANT: Run 'make manifests generate' after any change to api/v1alpha1/ types,
##            then commit the updated config/crd/bases/ and zz_generated.deepcopy.go.
##            CI enforces this via the 'Check CRD and deepcopy are up to date' step.
generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="./api/..." output:crd:artifacts:config=config/crd/bases
	rm -f chart/kardinal-promoter/crds/*.yaml
	mkdir -p chart/kardinal-promoter/crds
	cp config/crd/bases/*.yaml chart/kardinal-promoter/crds/

## Install CRDs and chart into the CURRENT kube context, running $(IMG) (build and load it first)
install: manifests ## Install CRDs and chart into the current kube context (image $(IMG_REPO):$(IMG_TAG))
	@echo "Installing into kube context: $$(kubectl config current-context)"
	kubectl apply -f config/crd/bases/
	helm upgrade --install kardinal-promoter chart/kardinal-promoter \
		--namespace kardinal-system --create-namespace \
		--set image.repository=$(IMG_REPO) --set image.tag=$(IMG_TAG)

## Remove chart and CRDs from cluster
uninstall: ## Remove chart and CRDs from the current kube context
	@echo "Uninstalling from kube context: $$(kubectl config current-context)"
	helm uninstall kardinal-promoter -n kardinal-system || true
	kubectl delete -f config/crd/bases/ || true

## Docker
docker-build:
	docker build -t ${IMG} .

## Helm
helm-lint:
	helm lint chart/kardinal-promoter

## Validate every kardinal manifest in demo/ and examples/ against the generated CRD
## schemas (structural schema, pruning, CEL-free validation, label values, strict decode).
## Mirrors the 'Validate demo and example manifests' CI step. No cluster needed.
validate-manifests: ## Validate demo/ and examples/ manifests against the CRD schemas (no cluster needed)
	$(GO) test ./test/examples/... -count=1 -v -run 'TestExampleManifests|TestExamplePaths'

## Kind cluster for E2E
kind-up: e2e-setup ## Create local kind cluster and install kro + kardinal-promoter (alias of e2e-setup)

install-kro: ## Install upstream kro with the Graph controller (GraphKind feature gate) into the current (or KUBE_CONTEXT) context
	bash hack/install-kro.sh

setup-e2e-env: ## Full single-cluster E2E: kind + kro + ArgoCD + test app in test/uat/prod (only touches kind-$(KIND_CLUSTER))
	KIND_CLUSTER=$(KIND_CLUSTER) bash hack/setup-e2e-env.sh

setup-e2e-env-fast: ## Single-cluster E2E without ArgoCD (faster, for integration testing)
	KIND_CLUSTER=$(KIND_CLUSTER) SKIP_ARGOCD=1 bash hack/setup-e2e-env.sh

setup-multi-cluster-env: ## Multi-cluster E2E: kind (test+uat) + EKS prod cluster. Requires AWS creds + EKS cluster (see terraform/eks-e2e).
	bash hack/setup-multi-cluster-env.sh

eks-up: ## Create EKS prod cluster for multi-cluster E2E via Terraform (requires AWS creds; asks for confirmation)
	cd terraform/eks-e2e && terraform init && terraform apply
	@echo ""
	@echo "Cluster ready. Update kubeconfig with:"
	@cd terraform/eks-e2e && terraform output -raw kubeconfig_update_command

eks-down: ## Destroy EKS prod cluster (saves cost when not running E2E; asks for confirmation)
	cd terraform/eks-e2e && terraform destroy

kind-down: ## Delete the e2e kind cluster
	kind delete cluster --name $(KIND_CLUSTER)

e2e-setup: ## Create kind cluster + install kro + kardinal built from this checkout + quickstart fixtures
	KIND_CLUSTER=$(KIND_CLUSTER) KARDINAL_IMAGE_REPO=$(IMG_REPO) KARDINAL_IMAGE_TAG=$(IMG_TAG) bash hack/e2e-setup.sh

e2e-teardown: ## Convenience: tear down the e2e kind cluster
	KIND_CLUSTER=$(KIND_CLUSTER) bash hack/e2e-teardown.sh

## Journey tests — each journey maps to docs/aide/definition-of-done.md.
# test-e2e-journey-N run the fake-client journey tests (no cluster).
# test-e2e-kind runs the tagged cluster tests against kind-$(KIND_CLUSTER) only.

test-e2e: test-e2e-journey-1 test-e2e-journey-2 test-e2e-journey-3 test-e2e-journey-4 test-e2e-journey-5

test-e2e-kind: ## Cluster e2e tests (build tag e2e) against kind-$(KIND_CLUSTER); run make e2e-setup first
	KARDINAL_E2E_CONTEXT=kind-$(KIND_CLUSTER) $(GO) test -tags e2e ./test/e2e/... -run 'TestInfrastructure|TestKind' -count=1 -v -timeout 10m

test-e2e-journey-1: ## Quickstart: 3-env pipeline, PolicyGates, PR for prod
	@echo "=== Journey 1: Quickstart ==="
	$(GO) test ./test/e2e/... -run TestJourney1Quickstart -v -timeout 10m

test-e2e-journey-2: ## Multi-cluster fleet: parallel prod fan-out, Argo Rollouts
	@echo "=== Journey 2: Multi-cluster fleet ==="
	$(GO) test ./test/e2e/... -run TestJourney2MultiClusterFleet -v -timeout 15m

test-e2e-journey-3: ## Policy governance: gate simulation, CEL, weekend block
	@echo "=== Journey 3: Policy governance ==="
	$(GO) test ./test/e2e/... -run TestJourney3PolicyGovernance -v -timeout 5m

test-e2e-journey-4: ## Rollback: one command, rollback PR, same gates
	@echo "=== Journey 4: Rollback ==="
	$(GO) test ./test/e2e/... -run TestJourney4Rollback -v -timeout 10m

test-e2e-journey-5: ## CLI: every command in docs/cli-reference.md
	@echo "=== Journey 5: CLI workflow ==="
	$(GO) test ./test/e2e/... -run TestJourney5CLI -v -timeout 5m

## Tools
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(GOLANGCI_LINT): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@latest

tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) $(GOVULNCHECK)

## Help
help:
	@echo "kardinal-promoter build targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-30s %s\n", $$1, $$2}'
