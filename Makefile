# kardinal-promoter Makefile

# Tools
CONTROLLER_GEN         ?= $(LOCALBIN)/controller-gen
CONTROLLER_GEN_VERSION ?= v0.17.3
GOLANGCI_LINT          ?= $(LOCALBIN)/golangci-lint
GOLANGCI_LINT_VERSION  ?= v2.11.4
GOVULNCHECK            ?= $(LOCALBIN)/govulncheck
GOVULNCHECK_VERSION    ?= v1.8.0
STATICCHECK            ?= $(LOCALBIN)/staticcheck
STATICCHECK_VERSION    ?= v0.8.1
DEADCODE               ?= $(LOCALBIN)/deadcode
DEADCODE_VERSION       ?= v0.52.0
LOCALBIN               ?= $(shell pwd)/bin

# Build
BINARY_CONTROLLER = bin/kardinal-controller
BINARY_CLI        = bin/kardinal
GO                = go
GOPROXY          ?= https://proxy.golang.org

# Docker image (the chart deploys $(IMG_REPO):$(IMG_TAG) when installed by `make install`)
IMG_REPO ?= ghcr.io/pnz1990/kardinal-promoter
IMG_TAG  ?= dev
IMG      ?= $(IMG_REPO):$(IMG_TAG)

.PHONY: all build build-controller build-cli ui ui-test ui-test-e2e test test-integration test-cover \
        lint lint-local vet vuln hygiene hygiene-go hygiene-web envtest generate manifests api-docs \
        install uninstall docker-build helm-lint validate-manifests \
        install-kro e2e-tools e2e-up e2e-down test-e2e-live e2e-all changelog-release release-gate \
        tools help

all: generate build test lint

## Build
build: build-controller build-cli

build-controller:
	$(GO) build -o $(BINARY_CONTROLLER) ./cmd/kardinal-controller/

build-cli:
	$(GO) build -o $(BINARY_CLI) ./cmd/kardinal/

## UI
tokens: ## Regenerate the design tokens (web/tokens/tokens.json -> web/src/styles/tokens.css, web/src/tokens.ts, docs/stylesheets/tokens.css)
	node scripts/gen-tokens.mjs

ui: ## Build the embedded React UI (requires Node.js and npm)
	cd web && npm ci && npm run build

ui-test: ## Run React component unit tests (vitest, requires Node.js and npm)
	cd web && npm ci && npm test

ui-test-e2e: ## Run Playwright E2E tests (requires Node.js, npm, and a built dist/)
	cd web && npm ci && npm run build && npx playwright install chromium --with-deps && npm run test:e2e

## Test
## The live e2e tests (test/e2e/live, build tag e2e) are excluded; see test-e2e-live.
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
## staticcheck is installed into bin/ at STATICCHECK_VERSION.
lint-local: $(STATICCHECK)
	$(GO) vet ./...
	$(STATICCHECK) ./...

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

vuln: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

hygiene: hygiene-go hygiene-web ## Fail on dead code and dead files (Go, docs, scripts, web); exceptions in hack/hygiene/allow.txt

hygiene-go: $(DEADCODE) ## Go functions no main or test reaches (deadcode), orphan docs pages and assets, scripts nothing calls, docs that name a missing command, flag or CRD field
	$(GO) run ./hack/hygiene -deadcode $(DEADCODE)
	$(GO) test ./test/examples -count=1 -run 'TestDocumented|TestSchemaHas'

hygiene-web: ## Unused files, exports and dependencies in web/ (knip; needs npm ci in web/)
	cd web && npm run knip

## envtest: install every CRD of config/crd/bases and the chart's admission policies on a real
## kube-apiserver, the oldest and the newest Kubernetes of hack/tool-versions.env (needs helm).
## ENVTEST_K8S=<version> runs one.
envtest: ## Install the CRDs and admission policies on the oldest and newest supported API servers
	@. hack/tool-versions.env; \
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$$SETUP_ENVTEST_VERSION && \
	for v in $${ENVTEST_K8S:-$$ENVTEST_K8S_MIN $$ENVTEST_K8S_MAX}; do \
	  assets=$$($(CURDIR)/bin/setup-envtest use $$v --bin-dir $(CURDIR)/bin/envtest -p path) || exit 1; \
	  echo "== Kubernetes $$v"; \
	  KUBECONFIG=/dev/null KUBEBUILDER_ASSETS=$$assets $(GO) test -tags envtest ./test/helm/ -run "TestInstallOnAPIServer|TestActorsOnAPIServer|TestActorIndexOverflowOnAPIServer|TestActorIndexAvailabilityOnAPIServer|TestHelmInstallsTheIndex|TestActorAttacksOnAPIServer|TestFreeTextLimitsRatchetOnAPIServer|TestTokenReviewAudienceOnAPIServer|TestReceiptsOnAPIServer|TestApprovalFieldsOnAPIServer|TestChangeRequestsOnAPIServer|TestBundleCIRunURLCredentialsOnAPIServer|TestRollbackMarkersOnAPIServer|TestPolicyNamespacesDocsOnAPIServer|TestAgentBudgetsOnAPIServer|TestAgentBudgetPoliciesOnAPIServer" -count=1 || exit 1; \
	done

## Generate (CRD manifests + DeepCopy)
## IMPORTANT: Run 'make manifests generate' after any change to api/v1alpha1/ types,
##            then commit the updated config/crd/bases/, docs/reference/api.md and zz_generated.deepcopy.go.
##            CI enforces this via the 'Check CRD and deepcopy are up to date' step.
generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="./api/..." output:crd:artifacts:config=config/crd/bases
	rm -f chart/kardinal-promoter/crds/*.yaml
	mkdir -p chart/kardinal-promoter/crds
	cp config/crd/bases/*.yaml chart/kardinal-promoter/crds/
	$(MAKE) api-docs

## docs/reference/api.md is generated from config/crd/bases.
## TestAPIReferenceIsUpToDate (hack/gen-api-docs, run by "go test ./...") fails when it is stale.
api-docs: ## Regenerate docs/reference/api.md from the CRDs in config/crd/bases
	$(GO) run ./hack/gen-api-docs --crds config/crd/bases --output docs/reference/api.md

## docs/reference/helm-values.md and controller-flags.md come from values.yaml and the
## controller's flags. TestHelmValuesPageIsUpToDate (hack/gen-reference-docs/refdocs) and
## TestControllerFlagsPageIsUpToDate (cmd/kardinal-controller) fail when stale.
reference-docs: ## Regenerate docs/reference/helm-values.md and controller-flags.md
	$(GO) run ./hack/gen-reference-docs

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

install-kro: ## Install upstream kro with the Graph controller (GraphKind feature gate) into the current (or KUBE_CONTEXT) context
	bash hack/install-kro.sh

## Live e2e suites (test/e2e/live, test/e2e/README.md): a kind cluster per
## suite with real git servers, GitOps engines and the controller built from
## this checkout.
## KIND_K8S picks the Kubernetes minor (e.g. 1.37; default kind-config.yaml's),
## COUNT the go test -count and RUN the go test -run pattern (default the suite's).
## JOBS is how many clusters e2e-all runs at once.
SUITE ?= core
KIND_K8S ?=
COUNT ?= 1
RUN ?=
SHARD ?=
JOBS ?= 4

e2e-tools: ## Install the pinned kind, kubectl and helm (hack/tool-versions.env) into bin/e2e
	bash hack/e2e/tools.sh

e2e-up: ## Create or update the kind cluster for live suite SUITE (default core)
	KIND_CLUSTER=$${KIND_CLUSTER:-kardinal-e2e-$(SUITE)} KIND_K8S=$(KIND_K8S) bash hack/e2e/up.sh $(SUITE)

test-e2e-live: ## Run live suite SUITE against the cluster from make e2e-up; fails on any skip
	KIND_CLUSTER=$${KIND_CLUSTER:-kardinal-e2e-$(SUITE)} COUNT=$(COUNT) RUN='$(RUN)' SHARD=$(SHARD) bash hack/e2e/run.sh $(SUITE)

e2e-down: ## Delete the kind cluster of live suite SUITE (and the multi-cluster suite's spoke); KIND_CLUSTER must start with kardinal-e2e- or kp-
	c=$${KIND_CLUSTER:-kardinal-e2e-$(SUITE)}; \
	case "$$c" in kardinal-e2e-*|kp-*) ;; *) echo "e2e-down: refusing to delete kind cluster $$c: KIND_CLUSTER must start with kardinal-e2e- or kp-" >&2; exit 1 ;; esac; \
	kc=$${KUBECONFIG:-test/e2e/results/$$c/kubeconfig}; kc=$${kc%%:*}; \
	kind delete cluster --name "$$c" --kubeconfig "$$kc" && \
	if kind get clusters 2>/dev/null | grep -qx "$$c-spoke"; then kind delete cluster --name "$$c-spoke" --kubeconfig "$$kc"; fi

changelog-release: ## Before tagging VERSION (vX.Y.Z): write docs/changelog.d into docs/changelog.md as VERSION and delete the fragments
	@test -n "$(VERSION)" || { echo "usage: make changelog-release VERSION=vX.Y.Z"; exit 1; }
	$(GO) run ./hack/changelog release $(VERSION)

e2e-all: ## Run every live suite on this host, JOBS clusters at a time, and prove the coverage (hack/e2e/all.sh)
	JOBS=$(JOBS) COUNT=$(COUNT) bash hack/e2e/all.sh

release-gate: ## A release's whole evidence on this host, one report (hack/release-gate); re-runs only what failed. RELEASE_GATE_ARGS, e.g. -clusters 3 -lock DIR
	$(GO) run ./hack/release-gate $(RELEASE_GATE_ARGS)

## Tools
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(GOLANGCI_LINT): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(STATICCHECK): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

$(DEADCODE): $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION)

tools: $(CONTROLLER_GEN) $(GOLANGCI_LINT) $(GOVULNCHECK) $(STATICCHECK) $(DEADCODE)

## Help
help:
	@echo "kardinal-promoter build targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-30s %s\n", $$1, $$2}'
