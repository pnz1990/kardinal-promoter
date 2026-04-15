# Spec 510: krocodile Bundling — Build, Version, and Vendor in Helm Chart

> Created: 2026-04-15
> Status: Implemented (v0.7.0)
> GitHub: #568
> Authors: Architecture session

---

## Summary

Build and publish the krocodile Graph controller OCI image as part of the
kardinal-promoter release pipeline. Bundle it in the Helm chart so a single
`helm install` installs both controllers with no manual steps.

---

## Problem

kardinal-promoter previously required users to run `hack/install-krocodile.sh`
as a separate step before installing the controller. This script clones the
krocodile repo, builds a Go binary, builds a Docker image, and applies Kubernetes
manifests — requiring git, go, and docker on the operator's machine.

This violates the enterprise expectation that a `helm install` is self-contained
and produces a fully functional system.

---

## Solution

### 1. krocodile OCI image (`ghcr.io/pnz1990/kardinal-promoter/krocodile:COMMIT`)

- Built in `release.yml` from the pinned commit in `hack/install-krocodile.sh`
- Published to `ghcr.io/pnz1990/kardinal-promoter/krocodile:<commit>` and `:vX.Y.Z`
- Multi-arch: `linux/amd64` and `linux/arm64`
- Uses `gcr.io/distroless/static:nonroot` base (minimal attack surface)
- Built with `Dockerfile.krocodile` in `hack/`

### 2. Helm chart krocodile template (`chart/kardinal-promoter/templates/krocodile.yaml`)

Rendered when `krocodile.enabled: true` (default). Installs:
- `kro-system` Namespace
- `graphs.experimental.kro.run` CRD
- `graphrevisions.experimental.kro.run` CRD
- `kardinal-graph-controller` ClusterRole
- `kardinal-graph-controller` ClusterRoleBinding
- `graph-controller` ServiceAccount in `kro-system`
- `graph-controller` Deployment using `krocodile.image.repository:krocodile.image.tag`

When `krocodile.enabled: false`: nothing is rendered. Users who already run
krocodile independently can opt out.

### 3. values.yaml section

```yaml
krocodile:
  enabled: true
  replicaCount: 1
  pinnedCommit: "948ad6c"
  apiGroup: "experimental.kro.run"
  namespace: "kro-system"
  image:
    repository: ghcr.io/pnz1990/kardinal-promoter/krocodile
    tag: ""          # defaults to pinnedCommit
    pullPolicy: IfNotPresent
  resources:
    requests: { cpu: 100m, memory: 128Mi }
    limits: { memory: 512Mi }
  nodeSelector: {}
  tolerations: []
```

### 4. Upgrade flow

When upgrading krocodile:
1. Update `KROCODILE_COMMIT` in `hack/install-krocodile.sh`
2. Update `krocodile.pinnedCommit` in `values.yaml`
3. Update `krocodile.commit` annotation in `Chart.yaml`
4. Run compat checks per upgrade protocol in `AGENTS.md`
5. PR and release — CI builds the new image automatically

---

## Files Changed

| File | Change |
|---|---|
| `chart/kardinal-promoter/templates/krocodile.yaml` | New: Namespace, CRDs, RBAC, Deployment |
| `chart/kardinal-promoter/values.yaml` | Expanded krocodile section |
| `chart/kardinal-promoter/templates/_helpers.tpl` | Added `krocodile.image` helper |
| `chart/kardinal-promoter/Chart.yaml` | Version 0.6.0, krocodile.commit annotation |
| `.github/workflows/release.yml` | krocodile build + push; Docker login; Helm OCI push; multi-arch |
| `hack/Dockerfile.krocodile` | New: minimal image for pre-built binaries |
| `hack/setup-e2e-env.sh` | Use Helm chart instead of separate install script |
| `Makefile` | Updated `install-krocodile` target description |
| `docs/installation.md` | Rewritten: single helm install, no manual krocodile step |
| `docs/quickstart.md` | Updated install section |
| `test/helm/chart_test.go` | Added krocodile template tests |
| `AGENTS.md` | Updated krocodile upgrade protocol |

---

## Acceptance Criteria

- [ ] `helm template kardinal-promoter chart/kardinal-promoter` renders Graph CRDs and `graph-controller` Deployment
- [ ] `helm template ... --set krocodile.enabled=false` renders nothing for `graph-controller`
- [ ] `helm lint chart/kardinal-promoter` passes with 0 errors
- [ ] `ghcr.io/pnz1990/kardinal-promoter/krocodile:948ad6c` is built and pushed in release CI
- [ ] Release notes reference the bundled krocodile commit
- [ ] `docs/installation.md` no longer references `hack/install-krocodile.sh` as a required step
- [ ] `TestHelmTemplateKrocodileDisabled` passes
