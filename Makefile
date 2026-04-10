# kardinal-promoter Makefile

# Tools
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
GOLANGCI_LINT  ?= $(LOCALBIN)/golangci-lint
GOVULNCHECK    ?= $(LOCALBIN)/govulncheck
LOCALBIN       ?= $(shell pwd)/bin

# Build
BINARY_CONTROLLER = bin/kardinal-controller
BINARY_CLI        = bin/kardinal
GO                = go
GOPROXY          ?= https://proxy.golang.org

# Docker image
IMG ?= kardinal-promoter:dev

.PHONY: all build build-controller build-cli test lint vet generate manifests \
        install docker-build helm-lint \
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

## Test
test:
	$(GO) test ./... -race -count=1 -timeout 120s

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
generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) crd paths="./..." output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./..." output:rbac:artifacts:config=config/rbac

## Install CRDs into current cluster
install: manifests
	kubectl apply -f config/crd/bases/

## Docker
docker-build:
	docker build -t ${IMG} .

## Helm
helm-lint:
	helm lint chart/kardinal-promoter

## Kind cluster for E2E
kind-up:
	kind create cluster --name kardinal-e2e --config test/e2e/kind-config.yaml
	kubectl config use-context kind-kardinal-e2e

kind-down:
	kind delete cluster --name kardinal-e2e

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
	GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

$(GOLANGCI_LINT): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

$(GOVULNCHECK): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@latest

tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) $(GOVULNCHECK)

## Help
help:
	@echo "kardinal-promoter build targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-30s %s\n", $$1, $$2}'
