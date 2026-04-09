# 09: Config-Only Promotions

> Status: Outline
> Depends on: 08-promotion-steps-engine (config-merge is a step), 02-pipeline-to-graph-translator
> Blocks: nothing (additive)

Promoting configuration changes independently from image changes.

## Scope

- Bundle type field
  - `type: image` (default): existing behavior. References container images.
  - `type: config`: references a Git commit SHA from a configuration repository.
  - `type: mixed` (Phase 3): references both images and a Git commit.
  - The type determines which promotion steps are used by default:
    - image: git-clone, kustomize-set-image (or helm-set-image), git-commit, ...
    - config: git-clone, config-merge, git-commit, ...
    - mixed: git-clone, config-merge, kustomize-set-image, git-commit, ...

- Config Bundle artifact spec
  ```yaml
  spec:
    type: config
    artifacts:
      gitCommit:
        repository: https://github.com/myorg/app-config
        sha: "abc123def456"
        message: "Update resource limits for all environments"
  ```
  - `repository`: the source Git repo containing the config change
  - `sha`: the specific commit to promote
  - `message`: human-readable description (used in PR body and commit message)

- Config-merge step implementation
  - Two strategies:
    - `cherry-pick` (default): Apply the referenced commit as a cherry-pick onto the environment's current state. Works when the commit modifies files that exist in the environment directory.
    - `overlay`: Copy changed files from the source repo (at the referenced SHA) into the environment directory. Works for additive config changes (new ConfigMaps, updated resource limits).
  - The strategy is configurable per step:
    ```yaml
    steps:
      - uses: config-merge
        config:
          strategy: overlay    # or cherry-pick
          sourcePath: configs/my-app/
    ```
  - Default when strategy is omitted: `cherry-pick`

- How config Bundles interact with existing features
  - Pipeline: unchanged. Config Bundles go through the same Pipeline as image Bundles.
  - PolicyGates: unchanged. CEL context includes `bundle.type` so gates can differentiate:
    ```yaml
    expression: 'bundle.type == "config" || bundle.upstreamSoakMinutes >= 30'
    ```
  - PR evidence: shows the Git commit message, changed files, and source commit link instead of image digest/tag.
  - Health verification: unchanged. After the config change is applied via Git, health adapters verify the deployment.
  - Rollback: unchanged. Rollback creates a new config Bundle pointing to the previous commit.
  - Bundle superseding: config Bundles and image Bundles for the same Pipeline can coexist. A new config Bundle does NOT supersede a pending image Bundle (different types).

- Git Subscription for config
  - The Subscription CRD supports `type: gitCommit`:
    ```yaml
    spec:
      pipeline: my-app
      source:
        type: gitCommit
        gitCommit:
          repository: https://github.com/myorg/app-config
          branch: main
          path: configs/my-app/     # only watch changes in this path
          interval: 5m
    ```
  - When a new commit touching files under the specified path is detected, the controller creates a config Bundle with `sha` set to the new commit.
  - Path filtering prevents unrelated changes from triggering Bundles.

- CI integration for config Bundles
  - Webhook: `POST /api/v1/bundles` with `"type": "config"` and `artifacts.gitCommit` fields
  - CLI: `kardinal create bundle my-app --type config --git-commit abc123 --git-repo https://...`
  - kubectl: `kubectl apply -f config-bundle.yaml`

- Mixed Bundles (Phase 3)
  - A single Bundle with `type: mixed` carrying both `artifacts.images` and `artifacts.gitCommit`
  - The default step sequence includes both `config-merge` and `kustomize-set-image`
  - Use case: deploying a new image version that requires corresponding config changes (new env vars, updated resource limits)

- Edge cases
  - Config commit conflicts: what if the cherry-pick fails because the environment directory has diverged? Mark the step as Failed, include the conflict details in the PR comment.
  - Empty config merge: what if the source commit has no changes relevant to the environment directory? The step succeeds as a no-op, commit message notes "no changes for this environment."
  - Git repository auth: the config source repo may be different from the GitOps repo. The step needs credentials for both. Use a separate `secretRef` on the `gitCommit` artifact spec.
