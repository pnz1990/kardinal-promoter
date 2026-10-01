# 09: Config-Only Promotions

> Status: Partially implemented. This page was rewritten on 2026-09-29 to match the code.
> The original design (cherry-pick and overlay strategies, `spec.artifacts.gitCommit`,
> a `gitCommit` Subscription) was never built; what is left of it is listed
> under [Not implemented](#not-implemented). Mixed Bundles were added on 2026-10-01.
> Depends on: 08-promotion-steps-engine (config-merge is a step), 02-pipeline-to-graph-translator
> Blocks: nothing (additive)
> User docs: [concepts](../concepts.md), [CI integration](../ci-integration.md), `examples/config-promotion/`

## Purpose

Config-only promotions allow teams to promote configuration changes (resource limits, environment variables, feature flags, ConfigMap contents) through the same Pipeline and PolicyGate flow as image promotions, without changing any container images.

This spec covers the Bundle `type: config` format, the `config-merge` promotion step, and the Git Subscription that creates config Bundles.

## Bundle Type Field

Bundles have a `spec.type` field:

| Type | Artifacts | Update step | Use case |
|---|---|---|---|
| `image` (default) | `spec.images[]` | `kustomize-set-image` or `helm-set-image` | New container image version |
| `config` | `spec.configRef` | `config-merge` | Configuration change without image change |
| `mixed` | `spec.images[]` and `spec.configRef` | `config-merge`, then the `image` update step | An image and the config change it needs, in one commit per environment |

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
  pipeline: my-app
  configRef:
    gitRepo: https://github.com/myorg/app-config   # optional: defaults to the Pipeline's git.url
    commitSHA: "abc123def456"                        # the commit to promote
  provenance:
    commitSHA: "abc123def456"
    ciRunURL: "https://github.com/myorg/app-config/actions/runs/67890"
    author: "platform-team"
  intent:
    targetEnvironment: prod
```

### ConfigRef Fields

| Field | Required | Description |
|---|---|---|
| `configRef.gitRepo` | No | HTTPS URL of the repository holding the config commit. Default: the Pipeline's `git.url`. |
| `configRef.commitSHA` | Yes, for a config promotion | The commit to promote. Without it, `config-merge` is a no-op. |

There is no per-Bundle Secret. The Pipeline's Git token is sent to the config repo only when it has the same origin (scheme, host and port) as the Pipeline's `git.url`; otherwise the config repo is cloned without credentials.

## Config-Merge Step

For a `config` or `mixed` Bundle with a `configRef.commitSHA`, `git-clone` checks out `configRef.gitRepo` at that commit into a directory next to the GitOps checkout (`pkg/steps/steps/git_clone.go`). `config-merge` (`pkg/steps/steps/config_merge.go`) then copies one directory from it into the GitOps checkout:

- It copies the environment's directory: `environments[].path`, or `environments/<name>` when the path is empty. The same relative path is read from the config commit and written in the GitOps repo.
- It overwrites files with the same name and creates missing ones. It never deletes files, and it skips `.git`, symlinks and special files.
- The step fails, and does not retry, when the config commit has no directory for the environment, the config source was not checked out, or the environment path leaves the checkout.
- Without `configRef.commitSHA` it succeeds as a no-op.

There is one strategy and no step config. The source path is always the environment's path.

## Default Step Sequence for Config Bundles

`DefaultSequenceForBundle` (`pkg/steps/defaults.go`) replaces the image update step with `config-merge` when the Bundle type is `config`:

```
git-clone (GitOps repo + config source at configRef.commitSHA)
config-merge
git-commit
git-push
open-pr + wait-for-merge (pr-review only)
health-check
```

A `mixed` Bundle runs `config-merge` and then the image update step (`kustomize-set-image`,
or `helm-set-image` with `update.strategy: helm`) before `git-commit`, so one commit carries
both changes. The images are written last, so they win over any image pin in the config
commit's copy of the environment directory. `update.strategy: argocd` refuses config and
mixed Bundles.

## How Config Bundles Interact With Other Features

### PolicyGates

PolicyGates work identically for config Bundles. The CEL context includes `bundle.type`, so gates can differentiate:

```yaml
# Allow faster promotion for config-only changes
expression: 'bundle.type == "config" || upstream.uat.soakMinutes >= 30'
```

### PR Evidence

The PR body (`pkg/scm/pr_template.go`) is the same for every Bundle type. For a config Bundle its image table is empty, and the provenance section shows `provenance.commitSHA`.

### Health Verification

Unchanged. After the config change is applied via Git, the health adapter verifies the Deployment, Argo CD Application, or Flux Kustomization. Config changes that affect resource limits, env vars, or ConfigMaps will trigger a rolling update, which the health adapter detects.

### Rollback

Rollback creates a new Bundle with `provenance.rollbackOf` set to the target, and it follows the same Pipeline, PolicyGates, and PR flow. The new Bundle has the target's type, with two exceptions. First, without `--to`, a deployed image or config Bundle goes back to the newest earlier images or config commit, and a mixed Bundle may have deployed them: an image rollback to a mixed target restores only the target's images, and a config rollback to a mixed target restores only its config commit. The rollback Bundle then has the deployed Bundle's type, and the rest stays as deployed. Second, when a mixed Bundle is deployed and the target's type cannot deploy everything that Bundle changed, the rollback Bundle is mixed. A config or mixed rollback merges the target's config commit; when the deployed Bundle is a config or mixed Bundle and the target has no commit, it carries the newest earlier Verified one. See [Rollback](../rollback.md#images-the-target-does-not-name).

### Bundle Superseding

Config Bundles and image Bundles for the same Pipeline coexist independently. A new config Bundle does NOT supersede a pending image Bundle, and vice versa. Superseding only occurs within the same Bundle type (the `sibling.Spec.Type != b.Spec.Type` check in `pkg/reconciler/bundle/reconciler.go`).

## Subscription CRD for Git

A Subscription with `type: git` watches a branch and creates a `config` Bundle for each new head commit:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-config-watch
spec:
  type: git
  pipeline: my-app
  git:
    repoURL: https://github.com/myorg/app-config
    branch: main          # default main
    pathGlob: configs/**  # recorded but not applied yet (see below)
    interval: 5m          # default 5m
```

### Detection Logic

`GitWatcher` (`pkg/source/git.go`) reads the branch head over the Git Smart HTTP protocol, without cloning. When the head differs from `status.lastSeenDigest`, the Subscription reconciler creates a Bundle with `type: config`, `configRef.gitRepo` set to `git.repoURL`, and `configRef.commitSHA` and `provenance.commitSHA` set to the new head. It then records the head in `status.lastSeenDigest` and the Bundle name in `status.lastBundleCreated`.

Only the latest head is promoted: commits between two polls do not get their own Bundle. `pathGlob` is not applied, so any new commit on the branch creates a Bundle (#495).

## CI Integration

### Webhook

The Bundle API (`POST /api/v1/bundles` on the webhook port, `:8083`) takes the
Bundle's `spec.configRef` (`gitRepo`, `commitSHA`). It rejects unknown fields, so the
original design's `artifacts.gitCommit` shape (with `message`, `path` and `secretRef`) is refused.

```bash
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "pipeline": "my-app",
    "type": "config",
    "configRef": {
      "gitRepo": "https://github.com/myorg/app-config",
      "commitSHA": "'$COMMIT_SHA'"
    },
    "provenance": {
      "commitSHA": "'$COMMIT_SHA'",
      "ciRunURL": "'$CI_URL'",
      "author": "'$AUTHOR'"
    }
  }'
```

### CLI

`kardinal create bundle --type config --config-commit <sha>` sets `configRef.commitSHA`; `--config-repo` sets `configRef.gitRepo` (default: the Pipeline repo). The CLI applies the Bundle API's checks, so a config Bundle without `--config-commit` is refused (#1285). The Graph builder also fails a config Bundle without `configRef.commitSHA` with `InvalidSpec`, whichever way it was created.

```bash
kardinal create bundle my-app --type config \
  --config-repo https://github.com/myorg/app-config --config-commit "${COMMIT_SHA}" \
  --commit "${COMMIT_SHA}" --ci-run-url "${CI_URL}"
```

A config Bundle can also come from the Bundle API above, a `type: git` Subscription, or `kubectl apply` of a Bundle with `spec.configRef`:

```bash
kubectl create -f - <<EOF
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  generateName: my-app-config-
  labels:
    kardinal.io/pipeline: my-app
spec:
  type: config
  pipeline: my-app
  configRef:
    gitRepo: https://github.com/myorg/app-config
    commitSHA: "${COMMIT_SHA}"
  provenance:
    commitSHA: "${COMMIT_SHA}"
    ciRunURL: "${CI_URL}"
EOF
```

## Edge Cases

| Case | Behavior |
|---|---|
| Config commit has no directory for the environment | `config-merge` fails permanently. PromotionStep set to Failed. |
| Nothing changed for this environment | `config-merge` copies the same content. `git-commit` finds no changes and succeeds as a no-op. |
| Config repo unreachable | `git-clone` fails. PromotionStep set to Failed. |
| Config repo on another origin than the GitOps repo | Cloned without credentials, so a private repo there fails in `git-clone`. |
| Config and image Bundles in flight simultaneously | They coexist. Each gets its own Graph. They do not supersede each other. |

## Not implemented

These parts of the original design were never built. Open an issue before relying on any of them.

- A **cherry-pick** strategy that applies the commit's diff, and an **overlay** strategy that copies only the files the commit changed. `config-merge` copies the whole environment directory instead.
- `strategy` and `sourcePath` step config, and a source path that differs from the environment path.
- A per-Bundle `secretRef` for the config repo.
- `message` and `path` fields on the config reference.
- A config-specific PR body (commit message, changed files).
- Subscription path filtering (`pathGlob`) and one Bundle per commit.
- CLI flags for a config reference.
