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
BINARY_AGENT      = bin/kardinal-agent
GO                = go
GOPROXY          ?= https://proxy.golang.org

# Docker image
IMG ?= kardinal-promoter:dev

.PHONY: all build build-controller build-cli build-agent ui ui-test ui-test-e2e test test-integration lint vet generate manifests \
        install uninstall docker-build helm-lint validate-manifests \
        install-krocodile \
        test-e2e test-e2e-journey-1 test-e2e-journey-2 test-e2e-journey-3 \
        test-e2e-journey-4 test-e2e-journey-5 \
        kind-up kind-down tools help lint-local

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

## Validate Pipeline manifests in demo/ and examples/ against the CRD schema
## Mirrors the 'Validate demo and example manifests against CRD schema' CI step in ci.yml.
## Use this locally to catch schema drift before pushing.
## Requires: config/crd/bases/ to be generated (run 'make manifests' first if not present).
validate-manifests: ## Validate all Pipeline manifests in demo/ and examples/ against CRD schema (no cluster needed)
	@echo "Validating Pipeline manifests against CRD schema..."
	@if command -v kubeconform >/dev/null 2>&1; then \
	  echo "Using kubeconform for full JSON Schema validation..."; \
	  SCHEMA_DIR=$$(mktemp -d); \
	  python3 -c "import json,yaml,os; crd=yaml.safe_load(open('config/crd/bases/kardinal.io_pipelines.yaml')); schema=crd['spec']['versions'][0]['schema']['openAPIV3Schema']; os.makedirs('$$SCHEMA_DIR',exist_ok=True); json.dump(schema,open('$$SCHEMA_DIR/kardinal.io_pipelines.json','w')); print('Schema extracted to $$SCHEMA_DIR')" 2>/dev/null || (echo "Schema extraction failed — run 'make manifests' first"; exit 1); \
	  FAILURES=0; \
	  for manifest in $$(find demo/ examples/ -name "*.yaml" -o -name "*.yml" | xargs grep -l "kind: Pipeline" 2>/dev/null); do \
	    echo "  Validating $$manifest..."; \
	    kubeconform -schema-location "$$SCHEMA_DIR/{{.ResourceKind}}.json" -strict "$$manifest" 2>/dev/null || FAILURES=$$((FAILURES+1)); \
	  done; \
	  rm -rf "$$SCHEMA_DIR"; \
	  if [ $$FAILURES -gt 0 ]; then \
	    echo ""; \
	    echo "❌ $$FAILURES manifest(s) failed kubeconform validation."; \
	    echo "Run 'make manifests generate' and update manifests to match current CRD schema."; \
	    exit 1; \
	  fi; \
	else \
	  echo "kubeconform not found — using Python field-name check (install kubeconform for full JSON Schema validation)"; \
	  FAILURES=0; \
	  for manifest in $$(find demo/ examples/ -name "*.yaml" -o -name "*.yml" | xargs grep -l "kind: Pipeline" 2>/dev/null); do \
	    echo "  Validating $$manifest..."; \
	    python3 -c "import yaml,sys,re; m=sys.argv[1]; raw=open(m).read(); [sys.exit(0) for _ in [1] if re.search(r'{{.*}}',raw)]; docs=list(yaml.safe_load_all(raw)); [sys.exit('FAIL: '+m+': unknown health field: '+k) for d in docs if d and d.get('kind')=='Pipeline' for e in d.get('spec',{}).get('environments',[]) for k in e.get('health',{}).keys() if k not in ('type','timeout','cluster','labelSelector','resource')]; print('  OK: '+m)" "$$manifest" || FAILURES=$$((FAILURES+1)); \
	  done; \
	  if [ $$FAILURES -gt 0 ]; then \
	    echo ""; \
	    echo "❌ $$FAILURES manifest(s) failed validation."; \
	    echo "Run 'make manifests generate' and update manifests to match current CRD schema."; \
	    exit 1; \
	  fi; \
	fi
	@echo "✅ All Pipeline manifests valid against current CRD schema."

## Kind cluster for E2E
kind-up: ## Create local kind cluster and install kardinal-promoter (with bundled krocodile)
	kind create cluster --name kardinal-e2e --config test/e2e/kind-config.yaml
	kubectl config use-context kind-kardinal-e2e
	$(MAKE) install-krocodile
	$(MAKE) install

install-krocodile: ## Build and load krocodile image into kind (local dev only — not needed for Helm chart install)
	@echo "Note: In production, krocodile is bundled in the Helm chart (krocodile.enabled=true)."
	@echo "This target is for local development when using 'make install' with a local chart."
	bash hack/install-krocodile.sh

setup-e2e-env: ## Full single-cluster E2E: kind + krocodile + ArgoCD + test app in test/uat/prod
	bash hack/setup-e2e-env.sh

setup-e2e-env-fast: ## Single-cluster E2E without ArgoCD (faster, for integration testing)
	SKIP_ARGOCD=1 bash hack/setup-e2e-env.sh

setup-multi-cluster-env: ## Multi-cluster E2E: kind (test+uat) + EKS prod cluster. Requires AWS creds + EKS cluster (see terraform/eks-e2e).
	bash hack/setup-multi-cluster-env.sh

eks-up: ## Create EKS prod cluster for multi-cluster E2E via Terraform (requires AWS creds)
	cd terraform/eks-e2e && terraform init && terraform apply -auto-approve
	@echo ""
	@echo "Cluster ready. Update kubeconfig with:"
	@cd terraform/eks-e2e && terraform output -raw kubeconfig_update_command

eks-down: ## Destroy EKS prod cluster (saves cost when not running E2E)
	cd terraform/eks-e2e && terraform destroy -auto-approve

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
