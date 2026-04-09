# 08: Promotion Steps Engine

> Status: Outline
> Depends on: 01-graph-integration, 03-promotionstep-reconciler
> Blocks: 09-config-only-promotions (config-merge is a step)

The step execution engine inside the PromotionStep reconciler. Handles built-in steps, custom webhook steps, and default step inference.

## Scope

- Step interface: `Step.Execute(ctx, state) -> (StepResult, error)`
  - StepState carries: Pipeline spec, Environment spec, Bundle spec, local work dir path, outputs from previous steps
  - StepResult returns: success/fail, message, outputs map (passed to next step)
  - All steps must be idempotent (safe to re-run after crash)

- Built-in step implementations
  - `git-clone`: Clone the GitOps repo from the Git cache into a local work directory. Supports checking out specific branches.
  - `kustomize-set-image`: Run `kustomize edit set-image` for each image in the Bundle. Updates the image reference in the environment directory.
  - `helm-set-image`: Patch a configurable path in `values.yaml` (default: `image.tag`). Supports nested paths.
  - `kustomize-build`: Run `kustomize build` and write rendered output to the environment directory (Rendered Manifests pattern).
  - `config-merge`: Apply a config Bundle's Git commit changes to the environment directory (see 09-config-only-promotions).
  - `git-commit`: Commit changes with a structured message including Bundle version, environment name, Pipeline name.
  - `git-push`: Push to the target branch. Used for `approval: auto` environments.
  - `open-pr`: Open a PR with promotion evidence. Sets labels, writes the PR body with policy gates, provenance, upstream verification.
  - `wait-for-merge`: Wait for the PR to be merged via webhook. On controller restart, reconcile by listing open PRs.
  - `health-check`: Verify health via the configured adapter (Deployment, Argo CD, Flux, Argo Rollouts, Flagger). Waits until timeout.

- Default step inference
  - When `steps` field is omitted from the environment, the controller infers the sequence:
    - Base: `git-clone` + (update step based on strategy) + `git-commit`
    - Approval auto: + `git-push` + `health-check`
    - Approval pr-review: + `git-push` + `open-pr` + `wait-for-merge` + `health-check`
    - Config Bundle: replace update step with `config-merge`
  - The inference logic must be documented clearly so users know what they get by default

- Custom step webhook protocol
  - Any `uses` value not matching a built-in step dispatches as HTTP POST
  - Request: `StepRequest` JSON (pipeline, environment, bundle, context, step config)
  - Response: `StepResponse` JSON (success: bool, message: string, outputs: map)
  - Authentication: Bearer token from step config (per-step Secret reference)
  - Timeout: configurable per step via `config.timeout` (default: 60s)
  - Retry: no automatic retry. If the step fails, the PromotionStep is marked Failed.
  - Error handling: network errors treated as failures. Non-2xx status codes treated as failures.

- Step state passing
  - Each step's `outputs` map is merged into the shared StepState.Outputs
  - Downstream steps can reference upstream outputs (e.g., `git-commit` outputs `commitSHA`, `open-pr` outputs `prNumber`)
  - Built-in output keys: documented per step

- Step execution model
  - Steps execute sequentially (no parallel steps within one PromotionStep)
  - The reconciler tracks the current step index in PromotionStep status
  - On crash and restart, the reconciler resumes from the current step index
  - Each step checks if its work is already done (idempotent) before executing

- Error handling
  - Step returns error: PromotionStep marked Failed, Graph stops downstream
  - Step returns success: false: same as error
  - Step timeout: treated as failure
  - Partial progress: steps before the failed step have already committed. The Git state may be partially updated. Rollback logic (Section 8 of design-v2.1) handles reverting.

- Relationship to Kargo's PromotionTask
  - Kargo's PromotionTask is a reusable, named step sequence that can be referenced from multiple Stages
  - Our equivalent: when multiple environments share the same custom steps, they can reference the same `steps` list via a shared ConfigMap or (Phase 3) a PromotionTemplate CRD
  - Phase 1: steps are inline on the environment. Phase 3: PromotionTemplate CRD for reuse.
