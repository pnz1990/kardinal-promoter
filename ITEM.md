# Item 017: `kardinal init` + Quickstart Working End-to-End (Stage 11 part 1)

> **Queue**: queue-008
> **Branch**: `017-kardinal-init`
> **Depends on**: 015 (merged — full CLI), 016 (merged — PR evidence)
> **Dependency mode**: merged
> **Assignable**: immediately
> **Contributes to**: J1 (Quickstart), J5 (CLI workflow)
> **Priority**: HIGH — closes v0.2.0 Epic #42, enables J1 to pass

---

## Goal

Implement `kardinal init` interactive wizard that generates a Pipeline YAML from
minimal input and validates it. Update quickstart example and docs to reflect the
current implementation state so J1 can be demonstrated end-to-end.

Design spec: roadmap Stage 11 (`kardinal init` part), Stage 8 (`kardinal init` deliverable).

---

## Deliverables

### 1. `kardinal init` command

In `cmd/kardinal/cmd/init.go`:

```bash
kardinal init
```

Interactive prompts:
1. Application name (e.g., `nginx-demo`)
2. Namespace (default: `default`)
3. Environments (comma-separated: `test,uat,prod`)
4. Git repository URL (e.g., `https://github.com/myorg/gitops`)
5. Base branch (default: `main`)
6. Update strategy: `kustomize` or `helm` (default: `kustomize`)
7. Approval mode for each environment: `auto` or `pr-review` (default: prod=pr-review, others=auto)

Output: writes `pipeline.yaml` to current directory (or stdout with `--stdout`).

The generated Pipeline YAML must:
- Pass `kubectl apply --dry-run=client` without errors (validated before printing)
- Include correct spec.environments[] with gitRepo, branch, path, approvalMode fields
- Include spec.git.credentialsSecretRef pointing to "github-token" Secret

### 2. Update quickstart example

In `examples/quickstart/pipeline.yaml`:
- Ensure all fields match current CRD spec (v1alpha1 fields from items 005+006)
- Add comments explaining each field
- Include `spec.paused: false` explicitly

In `examples/quickstart/bundle.yaml`:
- Update to use `kardinal create bundle` syntax as example comment
- Ensure `spec.pipeline` field matches the pipeline name

### 3. Update quickstart docs

In `docs/quickstart.md`:
- Replace TBD sections with working commands
- Update step 6 ("Create a Bundle") to use `kardinal create bundle nginx-demo --image ghcr.io/nginx/nginx:1.29.0`
- Update step 8 to use `kardinal explain nginx-demo --env prod`
- Add `kardinal init` to the Install section as an alternative to applying YAML directly
- Ensure all steps 1-10 from definition-of-done.md J1 journey are documented

### 4. Unit tests

In `cmd/kardinal/cmd/init_test.go`:
- `TestInit_GeneratesValidPipelineYAML`: verify generated YAML has required fields
- `TestInit_AllEnvironments`: verify all listed environments are in spec.environments
- `TestInit_DefaultApprovalModes`: last env gets pr-review, others get auto

---

## Acceptance Criteria

- [ ] `kardinal init` prompts the user and generates a Pipeline YAML
- [ ] `kardinal init --stdout` prints to stdout instead of file
- [ ] Generated YAML has valid apiVersion/kind, spec.environments[], spec.git
- [ ] `examples/quickstart/pipeline.yaml` uses current CRD field names
- [ ] `docs/quickstart.md` steps 1-10 match definition-of-done.md J1 exactly
- [ ] `go build ./...` passes
- [ ] `go test ./cmd/kardinal/... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames
