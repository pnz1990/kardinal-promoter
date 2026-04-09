# 09: Config-Only Promotions

> Status: Comprehensive
> Depends on: 08-promotion-steps-engine (config-merge is a step), 02-pipeline-to-graph-translator
> Blocks: nothing (additive)

## Purpose

Config-only promotions allow teams to promote configuration changes (resource limits, environment variables, feature flags, ConfigMap contents) through the same Pipeline and PolicyGate flow as image promotions, without changing any container images.

This spec covers the Bundle `type: config` artifact format, the `config-merge` promotion step, and the Git Subscription for auto-detecting config changes.

## Bundle Type Field

Bundles have a `spec.type` field:

| Type | Artifacts | Default step | Use case |
|---|---|---|---|
| `image` (default) | `spec.artifacts.images[]` | `kustomize-set-image` or `helm-set-image` | New container image version |
| `config` | `spec.artifacts.gitCommit` | `config-merge` | Configuration change without image change |
| `mixed` (Phase 3) | Both `images[]` and `gitCommit` | `config-merge` then `kustomize-set-image` | Image + config change together |

## Config Bundle CRD

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: my-app-config-update-1712567890
  labels:
    kardinal.io/pipeline: my-app
spec:
  type: config
  artifacts:
    gitCommit:
      repository: https://github.com/myorg/app-config   # source repo with the config change
      sha: "abc123def456"                                 # specific commit to promote
      message: "Update resource limits for all environments"
      path: "configs/my-app/"                             # optional: path within the source repo
      secretRef: { name: config-repo-token }              # optional: if different from Pipeline git.secretRef
  provenance:
    commitSHA: "abc123def456"
    ciRunURL: "https://github.com/myorg/app-config/actions/runs/67890"
    author: "platform-team"
    buildTimestamp: "2026-04-09T14:00:00Z"
  intent:
    target: prod
```

### Artifact Fields

| Field | Required | Description |
|---|---|---|
| `gitCommit.repository` | Yes | HTTPS URL of the repository containing the config change |
| `gitCommit.sha` | Yes | The specific Git commit SHA to promote |
| `gitCommit.message` | No | Human-readable description (used in PR body and commit message) |
| `gitCommit.path` | No | Subdirectory within the source repo. Only files under this path are merged. Default: repo root. |
| `gitCommit.secretRef` | No | Kubernetes Secret with a `token` field for the source repo. Default: uses the Pipeline's `git.secretRef`. |

## Config-Merge Step

The `config-merge` step applies the config commit's changes to the target environment directory in the GitOps repo.

### Strategies

**cherry-pick** (default): Applies the referenced commit as a Git cherry-pick onto the environment's current state.

```go
func (s *ConfigMergeStep) cherryPick(ctx context.Context, state *StepState) (StepResult, error) {
    configDir := state.StepConfig["sourceDir"].(string) // from git-clone of the config repo
    envDir := filepath.Join(state.WorkDir, state.Environment.Path)

    // Get the diff from the referenced commit
    diff, err := gitDiffCommit(configDir, state.Bundle.Artifacts.GitCommit.SHA)
    if err != nil {
        return StepResult{Status: StepFailed, Message: "Failed to read commit diff: " + err.Error()}, nil
    }

    // Apply the diff to the environment directory
    if err := applyDiff(envDir, diff); err != nil {
        return StepResult{Status: StepFailed, Message: "Cherry-pick conflict: " + err.Error()}, nil
    }

    return StepResult{Status: StepSuccess, Message: "Config changes applied via cherry-pick"}, nil
}
```

**overlay**: Copies changed files from the source repo (at the referenced SHA) into the environment directory, overwriting existing files.

```go
func (s *ConfigMergeStep) overlay(ctx context.Context, state *StepState) (StepResult, error) {
    configDir := state.StepConfig["sourceDir"].(string)
    envDir := filepath.Join(state.WorkDir, state.Environment.Path)
    sourcePath := state.Bundle.Artifacts.GitCommit.Path

    // List files changed in the referenced commit
    changedFiles, err := gitChangedFiles(configDir, state.Bundle.Artifacts.GitCommit.SHA)
    if err != nil {
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }

    // Copy each changed file from the source to the environment directory
    for _, file := range changedFiles {
        if sourcePath != "" && !strings.HasPrefix(file, sourcePath) {
            continue // skip files outside the configured path
        }
        relativePath := strings.TrimPrefix(file, sourcePath)
        src := filepath.Join(configDir, file)
        dst := filepath.Join(envDir, relativePath)
        if err := copyFile(src, dst); err != nil {
            return StepResult{Status: StepFailed, Message: "Failed to copy " + file + ": " + err.Error()}, nil
        }
    }

    if len(changedFiles) == 0 {
        return StepResult{Status: StepSuccess, Message: "No config changes for this environment (no-op)"}, nil
    }

    return StepResult{Status: StepSuccess, Message: fmt.Sprintf("Config overlay: %d files updated", len(changedFiles))}, nil
}
```

### Strategy Selection

The strategy is configurable via step config:

```yaml
steps:
  - uses: config-merge
    config:
      strategy: overlay       # "cherry-pick" (default) or "overlay"
      sourcePath: configs/my-app/
```

When `config-merge` is inferred as the default step (config Bundle without explicit steps), the strategy is `cherry-pick` and `sourcePath` is taken from `Bundle.spec.artifacts.gitCommit.path`.

### Git Clone for Config Repo

The `config-merge` step requires the config source repo to be cloned alongside the GitOps repo. The `git-clone` step is modified for config Bundles:

```go
func (s *GitCloneStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    // Clone the GitOps repo (same as for image Bundles)
    gitOpsRepo, err := s.cloneGitOps(ctx, state)
    if err != nil {
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }
    state.WorkDir = gitOpsRepo.WorkDir
    outputs := map[string]interface{}{"repoDir": gitOpsRepo.WorkDir}

    // For config Bundles: also clone the config source repo
    if state.BundleType == "config" || state.BundleType == "mixed" {
        configRepoURL := state.Bundle.Artifacts.GitCommit.Repository
        configSecretRef := state.Bundle.Artifacts.GitCommit.SecretRef
        if configSecretRef == "" {
            configSecretRef = state.Git.SecretRef // fallback to Pipeline's git secret
        }
        configRepo, err := s.cloneRepo(ctx, configRepoURL, configSecretRef, state.Bundle.Artifacts.GitCommit.SHA)
        if err != nil {
            return StepResult{Status: StepFailed, Message: "Failed to clone config repo: " + err.Error()}, nil
        }
        outputs["configRepoDir"] = configRepo.WorkDir
    }

    return StepResult{Status: StepSuccess, Outputs: outputs}, nil
}
```

The `config-merge` step reads `state.Outputs["configRepoDir"]` to find the cloned config repo.

## Default Step Sequence for Config Bundles

When a config Bundle is processed and `steps` is omitted:

```
git-clone (GitOps repo + config source repo)
config-merge (cherry-pick or overlay)
git-commit
git-push (auto) or git-push + open-pr + wait-for-merge (pr-review)
health-check
```

This is inferred by `defaults.go` (see 08-promotion-steps-engine, Step Inference section).

## How Config Bundles Interact With Other Features

### PolicyGates

PolicyGates work identically for config Bundles. The CEL context includes `bundle.type`, so gates can differentiate:

```yaml
# Allow faster promotion for config-only changes
expression: 'bundle.type == "config" || bundle.upstreamSoakMinutes >= 30'
```

### PR Evidence

For config Bundles, the PR body shows:
- Config commit SHA (linked to the source repo)
- Config commit message
- Changed files list
- Source repo URL

Instead of image digest and tag, which are not relevant for config Bundles.

```go
func buildPRBody(state *StepState) string {
    if state.BundleType == "config" {
        return buildConfigPRBody(state) // shows commit, changed files, no image info
    }
    return buildImagePRBody(state)  // shows image, digest, tag
}
```

### Health Verification

Unchanged. After the config change is applied via Git, the health adapter verifies the Deployment, Argo CD Application, or Flux Kustomization. Config changes that affect resource limits, env vars, or ConfigMaps will trigger a rolling update, which the health adapter detects.

### Rollback

Rollback creates a new config Bundle pointing to the previous config commit SHA. The rollback follows the same Pipeline, PolicyGates, and PR flow.

### Bundle Superseding

Config Bundles and image Bundles for the same Pipeline coexist independently. A new config Bundle does NOT supersede a pending image Bundle, and vice versa. Superseding only occurs within the same Bundle type.

This is checked during superseding logic:

```go
func shouldSupersede(existing, incoming *Bundle) bool {
    if existing.Spec.Type != incoming.Spec.Type {
        return false // different types don't supersede each other
    }
    if existing.Annotations["kardinal.io/pin"] == "true" {
        return false
    }
    return true
}
```

## Subscription CRD for Git (Phase 3)

The Subscription CRD supports `type: gitCommit` to watch a Git repository for new commits:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-config-watch
spec:
  pipeline: my-app
  source:
    type: gitCommit
    gitCommit:
      repository: https://github.com/myorg/app-config
      branch: main
      path: configs/my-app/          # only watch changes in this path
      interval: 5m
      secretRef: { name: config-repo-token }
```

### Detection Logic

```go
func (w *GitCommitWatcher) Discover(ctx context.Context, sub *Subscription) ([]ArtifactVersion, error) {
    // Clone or fetch the repo
    repo, err := w.gitClient.CloneOrFetch(ctx, sub.Spec.Source.GitCommit.Repository, sub.Spec.Source.GitCommit.Branch)
    if err != nil {
        return nil, err
    }

    // Get commits since last known commit
    lastKnownSHA := sub.Status.LastDiscoveredSHA
    commits, err := gitLogSince(repo, lastKnownSHA, sub.Spec.Source.GitCommit.Path)
    if err != nil {
        return nil, err
    }

    var versions []ArtifactVersion
    for _, commit := range commits {
        versions = append(versions, ArtifactVersion{
            Type:       "config",
            SHA:        commit.SHA,
            Message:    commit.Message,
            Repository: sub.Spec.Source.GitCommit.Repository,
            Path:       sub.Spec.Source.GitCommit.Path,
        })
    }
    return versions, nil
}
```

**Path filtering:** Only commits that touch files under the configured `path` trigger Bundle creation. Changes to other paths are ignored.

**Last known SHA tracking:** `sub.Status.LastDiscoveredSHA` tracks the most recent commit that was converted to a Bundle. On each poll, only commits after this SHA are processed.

## Mixed Bundles (Phase 3)

A single Bundle with `type: mixed` carries both images and a Git commit:

```yaml
spec:
  type: mixed
  artifacts:
    images:
      - name: my-app
        reference: ghcr.io/myorg/my-app:1.29.0
        digest: sha256:a1b2c3d4...
    gitCommit:
      repository: https://github.com/myorg/app-config
      sha: "def456"
      message: "Add new env var for v1.29.0"
```

Default step sequence for mixed Bundles:
```
git-clone (both repos)
config-merge
kustomize-set-image (or helm-set-image)
git-commit
git-push / open-pr / wait-for-merge
health-check
```

The config change is applied first, then the image update. This ensures the new config (e.g., new environment variable) is in place before the new image (which may require that variable) is deployed.

## CI Integration

### Webhook

```bash
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "pipeline": "my-app",
    "type": "config",
    "artifacts": {
      "gitCommit": {
        "repository": "https://github.com/myorg/app-config",
        "sha": "'$COMMIT_SHA'",
        "message": "Update resource limits",
        "path": "configs/my-app/"
      }
    },
    "provenance": {
      "commitSHA": "'$COMMIT_SHA'",
      "ciRunURL": "'$CI_URL'",
      "author": "'$AUTHOR'"
    }
  }'
```

### CLI

```bash
kardinal create bundle my-app \
  --type config \
  --git-commit abc123def456 \
  --git-repo https://github.com/myorg/app-config \
  --git-path configs/my-app/ \
  --commit abc123def456 \
  --ci-run https://github.com/myorg/app-config/actions/runs/67890
```

## Edge Cases

| Case | Behavior |
|---|---|
| Cherry-pick conflict | `config-merge` step fails with conflict details. PromotionStep set to Failed. Message includes which files conflicted. |
| Empty diff (no changes for this environment) | `config-merge` step succeeds as no-op. `git-commit` step detects no changes and succeeds as no-op. The promotion completes but with an empty PR (if pr-review) or no Git push (if auto). |
| Config repo unreachable | `git-clone` step fails. PromotionStep set to Failed. |
| Config repo has different auth than GitOps repo | `gitCommit.secretRef` specifies a separate Secret for the config repo. |
| Config and image Bundles in flight simultaneously | They coexist. Each gets its own Graph. They do not supersede each other. |

## Unit Tests

1. Config Bundle default steps: verify config-merge is inferred instead of kustomize-set-image.
2. Cherry-pick strategy: apply a commit with one changed file, verify file is updated in env directory.
3. Overlay strategy: copy changed files from source to env directory.
4. Empty diff: no files changed for the environment path, step succeeds as no-op.
5. Conflict: cherry-pick on a diverged directory, step fails with conflict message.
6. Git clone for config: verify both GitOps repo and config repo are cloned.
7. Separate auth: config repo uses its own secretRef.
8. Superseding: config Bundle does not supersede image Bundle.
9. Superseding: config Bundle supersedes older config Bundle.
10. PR body: verify config PR shows commit message and changed files, not image info.
11. PolicyGate: `bundle.type == "config"` evaluates to true for config Bundles.
12. Mixed Bundle (Phase 3): default steps include both config-merge and kustomize-set-image, in that order.
