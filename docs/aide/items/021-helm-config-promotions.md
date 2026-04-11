# Item 021: Helm Strategy + Config-Only Bundle Promotions (Stage 12)

> **Queue**: queue-010
> **Branch**: `021-helm-config-promotions`
> **Depends on**: 013 (PromotionStep reconciler, merged)
> **Dependency mode**: merged
> **Contributes to**: J1 (config promotion path), expanded use cases
> **Priority**: HIGH — enables Helm users and config-only promotions

---

## Goal

Add Helm as an update strategy and implement config-only Bundle promotion. Together, these
complete the Phase 2 update strategy matrix and enable promotion of configuration changes
independent of image updates.

---

## Deliverables

### 1. `helm-set-image` step (`pkg/steps/steps/helm_set_image.go`)

- Reads `values.yaml` (or `values-<env>.yaml`) from the cloned repo directory
- Updates the image tag at the path specified in `spec.environments[].helm.imagePathTemplate`
  (e.g., `.image.tag` → updates `image.tag:` in the YAML)
- Uses `sigs.k8s.io/yaml` or `gopkg.in/yaml.v3` (already in go.mod) for YAML parsing
- Step outputs: `helmValuesPath` (path to modified file)
- Registered via `steps.Register("helm-set-image", ...)`

### 2. `config-merge` step (`pkg/steps/steps/config_merge.go`)

- Applies changes from `Bundle.spec.configRef.commitSHA` to the working directory
- Strategy: `overlay` only in Phase 1 (cherry-pick is complex and risky; defer to later)
  - `overlay`: reads the commit's diff from the Bundle's configRef gitRepo, applies changed
    files on top of the env branch
- Step inputs from accumulator: `configRepo`, `configCommitSHA`
- Step outputs: `mergedFiles` (count of files changed)
- Registered via `steps.Register("config-merge", ...)`

### 3. Default step sequence for config Bundles

In `pkg/reconciler/promotionstep/reconciler.go` (or the step engine dispatcher):
- `Bundle.spec.type == "config"` → use sequence:
  `[git-clone, config-merge, git-commit, git-push, open-pr, wait-for-merge, health-check]`
- `Bundle.spec.type == "image"` + `approvalMode: pr-review` + `updateStrategy: helm` →
  `[git-clone, helm-set-image, git-commit, git-push, open-pr, wait-for-merge, health-check]`

### 4. Config Bundle supersession (separate tracking per type)

In `pkg/reconciler/bundle/reconciler.go`:
- `supersedeSiblings`: only supersede bundles of the same type
  (image bundles don't supersede config bundles and vice versa)

### 5. `examples/config-promotion/` example

- `pipeline.yaml` with a config-promotion Pipeline
- `bundle.yaml` showing a config Bundle referencing a commit SHA

### 6. Unit tests

- `TestHelmSetImage_UpdatesTagInValues`: mock git dir with values.yaml, verify tag updated
- `TestConfigMerge_AppliesOverlay`: mock config commit, verify files merged
- `TestBundleReconciler_ConfigBundleDoesNotSupersededImageBundle`: image and config bundles coexist
- `TestBundleReconciler_ConfigBundleSupersededsByNewConfigBundle`: new config supersedes old config

---

## Acceptance Criteria

- [ ] Pipeline with `updateStrategy: helm` updates values.yaml tag correctly
- [ ] Config Bundle with `configRef.commitSHA` triggers config-merge step
- [ ] Image and config Bundles for the same Pipeline coexist independently
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames
