# Spec: Fix Demo E2E — health.flux field not in CRD schema (Issue #925)

## Design reference
- **Design doc**: N/A — infrastructure change with no user-visible behavior change
  (fixes invalid example YAML to match existing schema)

---

## Zone 1 — Obligations (falsifiable)

**O1** — `demo/manifests/flux/pipeline.yaml` MUST NOT contain any field that is
not present in the `HealthConfig` Go struct (`api/v1alpha1/pipeline_types.go`).
Specifically: `spec.environments[*].health.flux` (sub-object) must be removed.

**O2** — `examples/flux-demo/pipeline.yaml` MUST NOT contain invalid health
sub-fields (`flux.name`, `flux.namespace`).

**O3** — `examples/argo-rollouts-demo/pipeline.yaml` MUST NOT contain invalid
health sub-fields (`argocd.name`, `argocd.namespace`, `argoRollouts.name`,
`argoRollouts.namespace`, `resource.name`, `resource.namespace`).

**O4** — After the fix, `kubectl apply -f demo/manifests/flux/pipeline.yaml` to
a cluster with the current CRD schema installed MUST NOT fail with
`strict decoding error: unknown field`.

---

## Zone 2 — Implementer's judgment

- Remove the invalid sub-fields from the YAML files. Add comments explaining
  that the resource name is derived by convention.
- Do NOT change the `HealthConfig` Go struct (that is a separate enhancement
  tracked in issue opened alongside this fix).

---

## Zone 3 — Scoped out

- Adding explicit override fields to `HealthConfig` (filed as follow-up issue).
- Validating all other YAML files in the repo against the CRD schema.
