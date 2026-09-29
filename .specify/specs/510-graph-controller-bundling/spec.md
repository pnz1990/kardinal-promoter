# Spec 510: Graph Controller Bundling — Build, Version, and Vendor in Helm Chart

> Created: 2026-04-15
> Status: Implemented (v0.7.0). Superseded 2026-09: kardinal now targets upstream
> [kro](https://github.com/kubernetes-sigs/kro) v0.10.0-rc.0, installed separately with
> `hack/install-kro.sh`. The bundled template, image build and values block described below were removed.
> GitHub: #568
> Authors: Architecture session

---

## Summary

Build and publish the pre-upstream Graph controller fork's OCI image as part of the
kardinal-promoter release pipeline. Bundle it in the Helm chart so a single
`helm install` installs both controllers with no manual steps.

---

## Problem

kardinal-promoter previously required users to run a separate install script
before installing the controller. The script cloned the Graph controller fork,
built a Go binary, built a Docker image, and applied Kubernetes
manifests — requiring git, go, and docker on the operator's machine.

This violates the enterprise expectation that a `helm install` is self-contained
and produces a fully functional system.

---

## Solution

### 1. Graph controller OCI image (`ghcr.io/pnz1990/kardinal-promoter/<graph-controller>:COMMIT`)

- Built in `release.yml` from the pinned commit in the install script
- Published under `ghcr.io/pnz1990/kardinal-promoter/` as `:<commit>` and `:vX.Y.Z`
- Multi-arch: `linux/amd64` and `linux/arm64`
- Uses `gcr.io/distroless/static:nonroot` base (minimal attack surface)
- Built with a dedicated Dockerfile in `hack/`

### 2. Helm chart Graph controller template

Rendered when the Graph controller values block was enabled (default). Installed:
- `kro-system` Namespace
- The fork's `graphs` CRD
- The fork's `graphrevisions` CRD
- `kardinal-graph-controller` ClusterRole
- `kardinal-graph-controller` ClusterRoleBinding
- `graph-controller` ServiceAccount in `kro-system`
- `graph-controller` Deployment using the bundled image repository and tag

When disabled: nothing was rendered. Users who already ran the
Graph controller independently could opt out.

### 3. values.yaml section

```yaml
<graph-controller>:
  enabled: true
  replicaCount: 1
  pinnedCommit: "948ad6c"
  apiGroup: "<fork API group>"
  namespace: "kro-system"
  image:
    repository: ghcr.io/pnz1990/kardinal-promoter/<graph-controller>
    tag: ""          # defaults to pinnedCommit
    pullPolicy: IfNotPresent
  resources:
    requests: { cpu: 100m, memory: 128Mi }
    limits: { memory: 512Mi }
  nodeSelector: {}
  tolerations: []
```

### 4. Upgrade flow

When upgrading the Graph controller:
1. Update the pinned commit in the install script
2. Update `pinnedCommit` in `values.yaml`
3. Update the commit annotation in `Chart.yaml`
4. Run compat checks per upgrade protocol in `AGENTS.md`
5. PR and release — CI builds the new image automatically

---

## Files Changed

| File | Change |
|---|---|
| `chart/kardinal-promoter/templates/` (Graph controller template) | New: Namespace, CRDs, RBAC, Deployment |
| `chart/kardinal-promoter/values.yaml` | Expanded Graph controller section |
| `chart/kardinal-promoter/templates/_helpers.tpl` | Added Graph controller image helper |
| `chart/kardinal-promoter/Chart.yaml` | Version 0.6.0, Graph controller commit annotation |
| `.github/workflows/release.yml` | Graph controller build + push; Docker login; Helm OCI push; multi-arch |
| `hack/` (Graph controller Dockerfile) | New: minimal image for pre-built binaries |
| `hack/setup-e2e-env.sh` | Use Helm chart instead of separate install script |
| `Makefile` | Updated Graph controller install target description |
| `docs/installation.md` | Rewritten: single helm install, no manual Graph controller step |
| `docs/quickstart.md` | Updated install section |
| `test/helm/chart_test.go` | Added Graph controller template tests |
| `AGENTS.md` | Updated Graph controller upgrade protocol |

---

## Acceptance Criteria

- [ ] `helm template kardinal-promoter chart/kardinal-promoter` renders Graph CRDs and `graph-controller` Deployment
- [ ] `helm template ...` with the Graph controller disabled renders nothing for `graph-controller`
- [ ] `helm lint chart/kardinal-promoter` passes with 0 errors
- [ ] The Graph controller image at commit `948ad6c` is built and pushed in release CI
- [ ] Release notes reference the bundled Graph controller commit
- [ ] `docs/installation.md` no longer references the separate install script as a required step
- [ ] The chart test for the disabled Graph controller passes
