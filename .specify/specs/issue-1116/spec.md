# Spec: issue-1116 — Add ResourceRef to HealthConfig

## Zone 1 — Obligations (falsifiable)

1. `HealthConfig` must have a `Resource *ResourceRef` field with JSON tag `resource,omitempty`
2. `ResourceRef` must have `Kind`, `Name`, `Namespace` fields (all optional, all strings)
3. A Pipeline with `health.resource.name` and `health.resource.namespace` set must be accepted
   by the CRD API server without "unknown field" error
4. When `health.resource.name` is set, the health Watch node must use that name instead of
   the pipeline name
5. When `health.resource.namespace` is set, the health Watch node must use that namespace
   instead of the environment name
6. When `health.resource.name` is empty and `health.resource` is set, the Watch node must
   fall back to the pipeline name
7. Build (`go build ./...`) and all tests (`go test ./... -race`) must pass

## Zone 2 — Implementer's judgment

- `Kind` field in ResourceRef is accepted but not used by the Watch node template (which
  always watches a Deployment); it is present for future extensibility and for the PDCA YAML
  to apply without error
- DeepCopy is manual (no code generator available in CI without network access to install
  controller-gen at build time); correctness verified by zero-allocation value copy semantics

## Zone 3 — Scoped out

- Using `ResourceRef.Kind` to watch non-Deployment resources (future enhancement)
- Changing the ArgoCD/Flux/ArgoRollouts/Flagger health adapters to support ResourceRef

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: health check capabilities
- **Implements**: PDCA S9 "Health check failure blocks promotion" scenario unblocked
