# Item 007: Helm Chart Controller Deployment + RBAC + Integration Test

> **Queue**: queue-003 (Stage 2)
> **Branch**: `007-helm-chart-controller-deploy`
> **Depends on**: 006 (branch must exist on remote)
> **Dependency mode**: branch
> **Assignable**: after 006 branch exists on remote
> **Contributes to**: J1 (Quickstart — install step)

---

## Goal

Update the Helm chart skeleton from Stage 0 to add the controller Deployment,
ServiceAccount, ClusterRole, ClusterRoleBinding, and Service. Add a `make install`
target that applies the chart to the current cluster. Add an integration test that
applies a Bundle and Pipeline to a kind cluster and verifies status fields.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 2: Bundle and Pipeline Reconcilers (No-Op Baseline)
`docs/design/design-v2.1.md` — Deployment architecture section

---

## Deliverables

### 1. `chart/kardinal-promoter/templates/` — controller Deployment and RBAC

Add to the chart:
- `deployment.yaml` — controller Deployment:
  - `image: {{ .Values.image.repository }}:{{ .Values.image.tag }}`
  - `args: ["--leader-elect=true", "--zap-log-level={{ .Values.logLevel }}"]`
  - `livenessProbe.httpGet.path: /healthz`, port: 8081
  - `readinessProbe.httpGet.path: /readyz`, port: 8081
  - `resources.requests`: cpu=100m, memory=64Mi; `limits`: cpu=500m, memory=128Mi
  - `serviceAccountName: kardinal-controller`
  - `securityContext.runAsNonRoot: true`
- `serviceaccount.yaml` — ServiceAccount `kardinal-controller`
- `clusterrole.yaml` — ClusterRole with rules for:
  - `pipelines, bundles, policygates, promotionsteps` — verbs: get, list, watch, create, update, patch, delete
  - `pipelines/status, bundles/status, policygates/status, promotionsteps/status` — verbs: get, update, patch
  - `leases` (coordination.k8s.io) — verbs: get, list, watch, create, update, patch, delete
  - `events` — verbs: create, patch
- `clusterrolebinding.yaml` — binds ClusterRole to ServiceAccount
- `service.yaml` — Service exposing metrics port 8080

### 2. `chart/kardinal-promoter/values.yaml` — update values

Add/update:
```yaml
image:
  repository: ghcr.io/pnz1990/kardinal-promoter/controller
  tag: latest
  pullPolicy: IfNotPresent

replicaCount: 1
logLevel: info

leaderElect: true
metricsBindAddress: ":8080"
healthProbeBindAddress: ":8081"
```

### 3. `Makefile` — add `install` and `uninstall` targets

```makefile
install: manifests ## Install CRDs and chart into current cluster
    kubectl apply -f config/crd/bases/
    helm upgrade --install kardinal chart/kardinal-promoter \
        --namespace kardinal-system --create-namespace

uninstall: ## Remove chart from cluster
    helm uninstall kardinal -n kardinal-system || true
    kubectl delete -f config/crd/bases/ || true
```

### 4. Integration test: `test/integration/controller_test.go`

Write an integration test using `go test -tags integration` that:
1. Creates a fake client or uses the envtest framework
2. Applies a Pipeline CRD object
3. Applies a Bundle CRD object
4. Runs the reconcilers in-process (not via a real cluster)
5. Verifies within 5 seconds:
   - `Bundle.status.phase == "Available"`
   - `Pipeline.status.conditions[0].type == "Ready"`
   - `Pipeline.status.conditions[0].status == "False"`

Use `sigs.k8s.io/controller-runtime/pkg/envtest` OR the fake client approach.
Either is acceptable. If using envtest, add a Makefile target `test-integration` that
sets `KUBEBUILDER_ASSETS` correctly.

**Build tag**: `//go:build integration` on the test file.
**Test command**: `go test ./test/integration/... -tags integration -race`

This must pass in the PR CI (add `go test ./test/integration/... -tags integration` to CI).

---

## Acceptance Criteria (from roadmap Stage 2)

- [ ] `helm lint chart/kardinal-promoter` passes
- [ ] Chart templates render without errors: `helm template kardinal chart/kardinal-promoter`
- [ ] Controller Deployment, ServiceAccount, ClusterRole, ClusterRoleBinding, Service are all present in chart
- [ ] `make install` runs without error on a cluster with CRDs installed
- [ ] Integration test: apply Bundle and Pipeline, verify status within 5 seconds
- [ ] `go test ./test/integration/... -tags integration -race` passes
- [ ] `go vet ./...` clean
- [ ] Copyright header on all new files
- [ ] No banned filenames

---

## Journey Contribution

This item enables J1 step 1 (Quickstart): `helm install kardinal oci://...`
After this item, the chart can be installed and the controller will start watching
Pipeline and Bundle objects.

Journey validation step for PR body:
```bash
helm template kardinal chart/kardinal-promoter | grep -E "kind: (Deployment|ClusterRole|ServiceAccount)"
go test ./test/integration/... -tags integration -race -v 2>&1 | tail -30
```

---

## Anti-patterns to Avoid

- Do NOT add `util.go`, `helpers.go`, or `common.go`
- Do NOT mutate Deployments or Services in controller code
- Use `fmt.Errorf("context: %w", err)` for error wrapping
- Helm values must all be templated — no hardcoded image names in templates

---

## Notes for Engineer

Item 007 depends on item 006's branch existing on remote (not necessarily merged).
You can start work on the Helm chart templates in parallel with 006's implementation.
The integration test should import the reconcilers from `pkg/reconciler/bundle/` and
`pkg/reconciler/pipeline/` which come from item 006's branch.

If 006 is not yet merged when you open this PR, set `dependency_mode: branch` in
state.json. The coordinator will handle ordering.

The chart skeleton from item 003 already has `chart/kardinal-promoter/`. Do NOT create
a new chart — update the existing one.
