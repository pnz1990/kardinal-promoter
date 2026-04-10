# Item 003: Dockerfile (Multi-Stage) + Helm Chart Skeleton

> **Queue**: queue-001 (Stage 0)
> **Branch**: `003-dockerfile-and-helm`
> **Depends on**: 001-go-module-scaffold (must be `done` in state.json)
> **Assignable**: after item 001 merged to main
> **Contributes to**: Journey 1 (install via Helm), Journey 7 (multi-tenant install)

---

## Goal

Deliver a multi-stage Dockerfile that produces a minimal distroless image and a
Helm chart skeleton that deploys the controller. The chart installs cleanly to a
kind cluster even before the controller has real logic.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 0 (Dockerfile + Helm chart deliverables)

---

## Deliverables

1. `Dockerfile` (multi-stage):
   - Stage 1: `golang:1.23-alpine` builder, `go build -o /bin/kardinal-controller ./cmd/kardinal-controller`
   - Stage 2: `gcr.io/distroless/static:nonroot` final image
   - `ENTRYPOINT ["/bin/kardinal-controller"]`
   - No shell in final image; no root user
2. `Makefile` `docker-build` target updated:
   `docker build -t ${IMG} .` where `IMG ?= kardinal-promoter:dev`
3. `chart/kardinal-promoter/` Helm chart:
   - `Chart.yaml`: `name: kardinal-promoter`, `version: 0.1.0`, `appVersion: 0.1.0`
   - `values.yaml`:
     - `image.repository: ghcr.io/pnz1990/kardinal-promoter/controller`
     - `image.tag: latest`
     - `replicaCount: 1`
     - `logLevel: info`
   - `templates/deployment.yaml`: Deployment with liveness/readiness probes on `:8081`
     and metrics on `:8080` (ports declared; handler not yet implemented)
   - `templates/serviceaccount.yaml`: ServiceAccount
   - `templates/clusterrole.yaml`: ClusterRole (stub — get/list/watch on `*`)
   - `templates/clusterrolebinding.yaml`: ClusterRoleBinding
   - `templates/service.yaml`: Service exposing 8080 and 8081
   - `templates/_helpers.tpl`: standard chart helpers (name, labels)
4. `Makefile` `helm-lint` target: `helm lint chart/kardinal-promoter`
5. `.dockerignore` covering: `bin/`, `*.test`, `docs/`, `examples/`, `chart/`
6. Apache 2.0 header in all template files where applicable

---

## Acceptance Criteria

- [ ] `make docker-build` runs without error (Docker must be installed in CI)
- [ ] `helm lint chart/kardinal-promoter` passes with zero warnings
- [ ] `helm template kardinal-promoter chart/kardinal-promoter` produces valid YAML (no errors)
- [ ] `helm install kardinal chart/kardinal-promoter --dry-run --generate-name` succeeds
- [ ] Dockerfile final image is non-root (distroless/nonroot)
- [ ] `go build ./...` still passes (Dockerfile doesn't break Go build)

---

## Self-Validation Commands

```bash
make docker-build
helm lint chart/kardinal-promoter
helm template kardinal-promoter chart/kardinal-promoter | kubectl apply --dry-run=client -f -
```

---

## Journey Contribution

Journey 1 Step 1:
```bash
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace
```
This item establishes the Helm chart structure used in that command.
