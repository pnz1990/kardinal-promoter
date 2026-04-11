# Item 024: Rendered Manifests — Branch Layout + kustomize-build Step Routing (Stage 6 partial / J6)

> **Queue**: queue-011
> **Branch**: `024-rendered-manifests`
> **Depends on**: 012 (SCM + steps engine, merged), 013 (PromotionStep reconciler, merged)
> **Dependency mode**: merged
> **Contributes to**: J6 (Rendered manifests)
> **Priority**: HIGH — enables enterprise GitOps pattern (rendered manifests)

---

## Goal

Implement the `layout: branch` pipeline configuration and ensure the `kustomize-build` step
is correctly routed when this layout is configured. Enables the rendered-manifests pattern
where Kustomize is executed at promotion time and the rendered YAML is committed to
environment-specific branches.

---

## Deliverables

### 1. `layout` field on Pipeline EnvironmentSpec

In `api/v1alpha1/pipeline_types.go`, add to `EnvironmentSpec`:

```go
// Layout configures how the promotion interacts with the Git repo layout.
// "directory" (default): env manifests are in a subdirectory of the main branch.
// "branch": rendered manifests are committed to a separate env branch.
// +kubebuilder:validation:Enum=directory;branch
// +kubebuilder:default=directory
// +optional
Layout string `json:"layout,omitempty"`
```

Regenerate DeepCopy and CRD YAML.

### 2. `kustomize-build` step (already exists, ensure it is in registry)

The `kustomize-build` step is listed in the vision but may not be registered yet.
In `pkg/steps/steps/kustomize.go` (or a new file), add a `kustomizeBuildStep` that:
- Runs `kustomize build <envPath>` and captures the output
- Writes the rendered YAML to a temp file and stores the path in `Outputs["renderedManifestPath"]`
- Idempotent: re-running produces the same manifest (same git state)
- If kustomize binary is not in PATH, return a helpful error message

### 3. Default step sequence for `layout: branch`

In `pkg/steps/defaults.go`, extend `DefaultSequenceForBundle`:
- When `layout == "branch"`, use sequence:
  `[git-clone, kustomize-set-image, kustomize-build, git-commit, git-push, open-pr, wait-for-merge, health-check]`
  (for `pr-review`) or without open-pr/wait-for-merge for `auto`
- `kustomize-build` must come after `kustomize-set-image` (before git-commit)
- This is independent of bundle type — `layout: branch` applies to image and config bundles alike

### 4. `git-clone` step for branch layout

When `layout: branch`, the `git-clone` step must:
- Clone the source branch (from `pipeline.spec.git.branch`)
- Checkout the env branch (create if not exists: `env/<env-name>`)
- Store the env branch name in `Outputs["envBranch"]`

The `git-commit` step already uses the branch from Outputs; no changes needed there.

### 5. `examples/rendered-manifests/` example

- `pipeline.yaml` with `layout: branch` and two environments
- `README.md` explaining the branch structure

### 6. Update docs/rendered-manifests.md

If the file doesn't exist, create it with the workflow from J6 in definition-of-done.md.

---

## Acceptance Criteria

- [ ] `Pipeline.spec.environments[].layout` field exists with enum validation
- [ ] `layout: branch` routes to kustomize-build in the step sequence
- [ ] `kustomize-build` step is registered and returns rendered manifest path in outputs
- [ ] `examples/rendered-manifests/pipeline.yaml` applies without error
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames
