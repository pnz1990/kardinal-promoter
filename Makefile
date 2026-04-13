# kardinal-promoter Makefile

# Tools
CONTROLLER_GEN         ?= $(LOCALBIN)/controller-gen
CONTROLLER_GEN_VERSION ?= v0.17.3
GOLANGCI_LINT          ?= $(LOCALBIN)/golangci-lint
GOVULNCHECK            ?= $(LOCALBIN)/govulncheck
LOCALBIN               ?= $(shell pwd)/bin

# Build
BINARY_CONTROLLER = bin/kardinal-controller
BINARY_CLI        = bin/kardinal
GO                = go
GOPROXY          ?= https://proxy.golang.org

# Docker image
IMG ?= kardinal-promoter:dev

.PHONY: all build build-controller build-cli ui ui-test test test-integration lint vet generate manifests \
        install uninstall docker-build helm-lint \
        install-krocodile \
        test-e2e test-e2e-journey-1 test-e2e-journey-2 test-e2e-journey-3 \
        test-e2e-journey-4 test-e2e-journey-5 \
        kind-up kind-down tools help

all: generate build test lint

## Build
build: build-controller build-cli

build-controller:
	$(GO) build -o $(BINARY_CONTROLLER) ./cmd/kardinal-controller/

build-cli:
	$(GO) build -o $(BINARY_CLI) ./cmd/kardinal/

## UI
ui: ## Build the embedded React UI (requires Node.js and npm)
	cd web && npm ci && npm run build

ui-test: ## Run React component unit tests (vitest, requires Node.js and npm)
	cd web && npm ci && npm test

## Test
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
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./api/..." output:rbac:artifacts:config=config/rbac

## Install CRDs and chart into current cluster
install: manifests ## Install CRDs and chart into current cluster
	kubectl apply -f config/crd/bases/
	helm upgrade --install kardinal chart/kardinal-promoter \
		--namespace kardinal-system --create-namespace

## Remove chart and CRDs from cluster
uninstall: ## Remove chart from cluster
	helm uninstall kardinal -n kardinal-system || true
	kubectl delete -f config/crd/bases/ || true

## Docker
docker-build:
	docker build -t ${IMG} .

## Helm
helm-lint:
	helm lint chart/kardinal-promoter

## Kind cluster for E2E
kind-up: ## Create local kind cluster and install krocodile + kardinal-promoter
	kind create cluster --name kardinal-e2e --config test/e2e/kind-config.yaml
	kubectl config use-context kind-kardinal-e2e
	$(MAKE) install-krocodile
	$(MAKE) install

install-krocodile: ## Build and install the krocodile Graph controller (pinned commit — see hack/install-krocodile.sh)
	bash hack/install-krocodile.sh

setup-e2e-env: ## Full single-cluster E2E: kind + krocodile + ArgoCD + test app in test/uat/prod
	bash hack/setup-e2e-env.sh

setup-e2e-env-fast: ## Single-cluster E2E without ArgoCD (faster, for integration testing)
	SKIP_ARGOCD=1 bash hack/setup-e2e-env.sh

setup-multi-cluster-env: ## Multi-cluster E2E: kind (test+uat) + EKS prod cluster. Requires AWS creds + EKS cluster (see terraform/krombat).
	bash hack/setup-multi-cluster-env.sh

eks-up: ## Create EKS prod cluster for multi-cluster E2E via Terraform (requires AWS creds)
	cd terraform/krombat && terraform init && terraform apply -auto-approve
	@echo ""
	@echo "Cluster ready. Update kubeconfig with:"
	@cd terraform/krombat && terraform output -raw kubeconfig_update_command

eks-down: ## Destroy EKS prod cluster (saves cost when not running E2E)
	cd terraform/krombat && terraform destroy -auto-approve

kind-down:
	kind delete cluster --name kardinal-e2e

e2e-setup: ## Convenience: create kind cluster + install krocodile + kardinal (same as kind-up but more verbose output)
	bash hack/e2e-setup.sh

e2e-teardown: ## Convenience: tear down the e2e kind cluster
	bash hack/e2e-teardown.sh

## E2E Tests — each journey maps to docs/aide/definition-of-done.md
# These are the acceptance tests. The project is complete when all pass.

test-e2e: kind-up test-e2e-journey-1 test-e2e-journey-2 test-e2e-journey-3 test-e2e-journey-4 test-e2e-journey-5

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
	GOBIN=$(LOCALBIN) $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

$(GOVULNCHECK): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@latest

tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) $(GOVULNCHECK)

## Help
help:
	@echo "kardinal-promoter build targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-30s %s\n", $$1, $$2}'
