# Spec: kardinal init — GitOps repo scaffolding

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future` — Lens 5: Adoption
- **Implements**: `` `kardinal init` generates Pipeline YAML but does not scaffold the GitOps repo `` (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

### O1 — `--scaffold-gitops` flag
`kardinal init --scaffold-gitops` MUST:
- Accept a `--gitops-dir DIR` flag (default: `.gitops`). If the directory does not exist, create it.
- For each environment in `cfg.Environments` (default: test, uat, prod), create:
  - `<gitops-dir>/environments/<env>/kustomization.yaml` with a minimal Kustomize base pointing to an `image` with the placeholder value `REPLACE_ME:latest`.
  - Content must include `apiVersion: kustomize.config.k8s.io/v1beta1`, `kind: Kustomization`, and an `images:` block.
- Print a summary to stdout listing each created file.
- Be idempotent: if a file already exists, do NOT overwrite it (print a "skipped (already exists)" line instead).
- Violation: any of the above files absent after a successful run, or existing files silently overwritten.

### O2 — `--scaffold-gitops` flag must work without a git repo
The scaffold operation uses only the local filesystem. It does NOT attempt `git init`, `git add`, or any git operation. The output is a set of files the user can add to any existing repo.
- Violation: `kardinal init --scaffold-gitops` calling any `exec.Command("git", ...)`.

### O3 — Pipeline YAML still generated
`--scaffold-gitops` does NOT skip Pipeline YAML generation. The `pipeline.yaml` (or `--output` file) must still be written alongside the scaffold.
- Violation: `pipeline.yaml` absent when `--scaffold-gitops` is passed.

### O4 — `--demo` flag scaffolds a demo kustomization
`kardinal init --demo` MUST scaffold the gitops directory (as per O1) AND set the image in the kustomization to the latest `kardinal-test-app` SHA image reference (`ghcr.io/pnz1990/kardinal-test-app:sha-DEMO`). It does NOT require internet access to do this — the `sha-DEMO` placeholder is a compile-time constant sufficient for documentation purposes.
- Violation: `--demo` producing identical output to non-demo mode.

### O5 — `--gitops-dir` respects absolute and relative paths
Both absolute paths (e.g. `/tmp/my-gitops`) and relative paths (e.g. `./my-gitops`) must work.
- Violation: `--gitops-dir /tmp/testdir` failing with a path error when `/tmp/testdir` does not exist.

### O6 — Help text updated
`kardinal init --help` must mention `--scaffold-gitops` and `--demo` in the command description.
- Violation: `--help` output not containing "scaffold" or "demo".

### O7 — Tests
`TestInitScaffold_CreatesKustomizationFiles` MUST pass: creates a temp dir, calls `scaffoldGitOps`, verifies all env kustomization files exist with correct content.
`TestInitScaffold_IdempotentOnSecondRun` MUST pass: runs twice, verifies existing files are NOT overwritten.
`TestInitScaffold_DemoMode` MUST pass: verifies demo placeholder image is present in kustomization output.
- Violation: any of these tests absent or failing.

---

## Zone 2 — Implementer's judgment

- Whether to use `os.MkdirAll` vs a helper — MkdirAll is idiomatic.
- The exact wording of the "skipped" message.
- Whether `kustomization.yaml` includes a `resources:` block — may be empty for a minimal scaffold.
- The exact placeholder image name in non-demo mode: `REPLACE_ME:latest` is the spec; implementer may choose a more descriptive placeholder.

---

## Zone 3 — Scoped out

- Git operations (git init, git push, git branch creation) — the scaffold is filesystem only.
- Creating ArgoCD Application manifests — out of scope for this PR.
- Helm chart overlays — Kustomize only for the MVP scaffold.
- Helm `--demo` chart deployment — the `--demo` flag here only affects the kustomization placeholder image. A full `helm install --demo` mode is a separate feature.
- Multi-cluster gitops directory layouts — single `environments/<env>` layout only.
