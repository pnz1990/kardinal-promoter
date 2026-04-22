# Spec: issue-978 — helm install to first Bundle in under 10 minutes

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 5 — Adoption`
- **Implements**: `helm install` to first Bundle in under 10 minutes (🔲 → ✅)

---

## Zone 1 — Obligations

### O1 — demo.enabled Helm value
`values.yaml` MUST have a `demo.enabled: false` value. When `demo.enabled: true`:
- A Helm template (`templates/demo.yaml`) creates a `Pipeline` CR named `demo` in the
  release namespace.
- The Pipeline targets `https://github.com/pnz1990/kardinal-demo` with branch `main`,
  directory-layout, environments `[test, uat, prod]`, kustomize update strategy,
  `approval: auto` for test/uat, `approval: pr-review` for prod.
- The Pipeline's `secretRef.name` defaults to `github-token` (configurable via
  `demo.secretRef.name`).
- The template is not rendered when `demo.enabled: false`.

### O2 — demo.image Helm value
When `demo.enabled: true`, `values.yaml` MUST have a `demo.image` value.
Default: `ghcr.io/pnz1990/kardinal-test-app:sha-DEMO` (not a real SHA — signals user
to `kardinal create bundle demo --image <real-sha>`).

### O3 — quickstart.md < 10-minute path
`docs/quickstart.md` MUST include a "Fast Start" section at the top (before prerequisites)
showing the 3-command path:
1. `helm install kardinal-promoter ... --set demo.enabled=true --set github.token=$GITHUB_PAT`
2. `kardinal get pipelines` (should show demo pipeline)  
3. `kardinal create bundle demo --image ghcr.io/pnz1990/kardinal-test-app:sha-<SHA>`

This section MUST note: "No GitOps repo setup required. Estimated time: under 10 minutes."

### O4 — tasks.md created
`tasks.md` created before code (eng.md §2d MANDATORY).

### O5 — `spec.md` present
This spec file is present at `.specify/specs/issue-978/spec.md`. ✅

### O6 — build, tests, lint pass
After implementation: `go build ./...`, `go vet ./...`, `go test ./... -race -count=1 -timeout 120s`
all pass with zero failures.

---

## Zone 2 — Implementer's judgment

- The demo Pipeline CR in the Helm template can use `helm template`'s full Go templating.
- The demo.yaml template may use `{{- if .Values.demo.enabled }}` guard.
- Exact YAML structure of the Pipeline matches the existing `examples/quickstart/pipeline.yaml`.
- The quickstart.md "Fast Start" section should be 10 lines or fewer.

---

## Zone 3 — Scoped out

- No changes to the controller binary or pkg/ (this is purely chart + docs + CLI docs).
- No actual deployment of `kardinal-test-app` workload — the demo Pipeline manages
  promotions but the user still needs the target app running. The quickstart.md explains this.
- No changes to `kardinal init` (already fully functional).
