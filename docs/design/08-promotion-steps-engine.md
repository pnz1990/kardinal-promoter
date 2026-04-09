# 08: Promotion Steps Engine

> Status: Comprehensive
> Depends on: 01-graph-integration, 03-promotionstep-reconciler
> Blocks: 09-config-only-promotions (config-merge is a step)

## Purpose

The promotion steps engine executes a sequence of steps for each PromotionStep CR. It handles built-in steps (Git operations, manifest updates, PR lifecycle, health checks), custom webhook steps, default step inference, and step state passing.

This is the runtime inside the PromotionStep reconciler. The reconciler manages the state machine (see 03); the steps engine executes the actual work at each state.

## Go Package Structure

```
pkg/
  steps/
    engine.go           # Step sequence executor
    registry.go         # Built-in step registry
    step.go             # Step interface definition
    state.go            # StepState and StepResult types
    webhook.go          # Custom step webhook dispatcher
    defaults.go         # Default step sequence inference
    steps/
      git_clone.go      # git-clone step
      kustomize.go      # kustomize-set-image step
      kustomize_build.go # kustomize-build step (Phase 2)
      helm.go           # helm-set-image step (Phase 2)
      config_merge.go   # config-merge step (see 09)
      git_commit.go     # git-commit step
      git_push.go       # git-push step
      open_pr.go        # open-pr step
      wait_for_merge.go # wait-for-merge step
      health_check.go   # health-check step
    steps_test.go       # Unit tests for each step
```

## Core Types

```go
// Step is a single unit of work in the promotion sequence.
type Step interface {
    // Execute runs the step. Returns success/failure/pending.
    // Must be idempotent: safe to call again after a crash.
    Execute(ctx context.Context, state *StepState) (StepResult, error)
    // Name returns the step identifier (e.g., "git-clone", "open-pr").
    Name() string
}

// StepState carries all context needed by a step.
type StepState struct {
    Pipeline    PipelineSpec            // Pipeline CRD spec
    Environment EnvironmentSpec         // Current environment config
    Bundle      BundleSpec              // Bundle being promoted
    BundleType  string                  // "image" or "config"
    WorkDir     string                  // Local Git work tree path
    Outputs     map[string]interface{}  // Accumulated outputs from previous steps
    Git         GitConfig               // Git URL, provider, secretRef
    StepConfig  map[string]interface{}  // Per-step config from the Pipeline YAML
}

// StepResult is the outcome of a step execution.
type StepResult struct {
    Status  StepStatus              // Success, Failed, Pending
    Message string                  // Human-readable description
    Outputs map[string]interface{}  // Outputs to pass to subsequent steps
}

type StepStatus string
const (
    StepSuccess StepStatus = "Success"
    StepFailed  StepStatus = "Failed"
    StepPending StepStatus = "Pending"  // long-running, reconciler should requeue
)
```

## Step Execution Engine

```go
type Engine struct {
    registry  *Registry       // built-in step registry
    webhookFn WebhookFunc     // custom step dispatcher
}

// RunStep executes a single step by name, resolving built-in or custom.
func (e *Engine) RunStep(ctx context.Context, stepDef StepDefinition, state *StepState) (StepResult, error) {
    builtIn, ok := e.registry.Get(stepDef.Uses)
    if ok {
        state.StepConfig = stepDef.Config
        return builtIn.Execute(ctx, state)
    }
    // Not a built-in step: dispatch as custom webhook
    return e.webhookFn(ctx, stepDef, state)
}
```

The PromotionStep reconciler calls `engine.RunStep()` for each step in the sequence, advancing `status.currentStepIndex` on success and merging outputs into `status.stepOutputs`.

## Default Step Inference

When `spec.steps` is omitted from the environment, the engine infers the sequence:

```go
func InferDefaultSteps(env EnvironmentSpec, bundleType string) []StepDefinition {
    steps := []StepDefinition{
        {Uses: "git-clone"},
    }

    // Update step depends on bundle type
    switch bundleType {
    case "config":
        steps = append(steps, StepDefinition{Uses: "config-merge"})
    default: // "image"
        switch env.Update.Strategy {
        case "helm":
            steps = append(steps, StepDefinition{Uses: "helm-set-image"})
        default: // "kustomize"
            steps = append(steps, StepDefinition{Uses: "kustomize-set-image"})
        }
    }

    steps = append(steps, StepDefinition{Uses: "git-commit"})

    switch env.Approval {
    case "auto":
        steps = append(steps, StepDefinition{Uses: "git-push"})
    case "pr-review":
        steps = append(steps,
            StepDefinition{Uses: "git-push"},
            StepDefinition{Uses: "open-pr"},
            StepDefinition{Uses: "wait-for-merge"},
        )
    }

    steps = append(steps, StepDefinition{Uses: "health-check"})
    return steps
}
```

## Built-in Steps

### git-clone

Clones the GitOps repo from the Git cache into the work directory.

```go
func (s *GitCloneStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    // Check if work dir already has the repo at the right commit
    if isUpToDate(state.WorkDir, state.Git.Branch) {
        return StepResult{Status: StepSuccess, Message: "Already cloned"}, nil
    }
    repo, err := s.gitClient.Clone(ctx, state.Git.URL, CloneOptions{
        Branch:    state.Git.Branch,
        CacheDir:  s.cacheDir,
        SecretRef: state.Git.SecretRef,
    })
    if err != nil {
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }
    state.WorkDir = repo.WorkDir
    return StepResult{
        Status:  StepSuccess,
        Message: "Cloned",
        Outputs: map[string]interface{}{"repoDir": repo.WorkDir, "headCommit": repo.HeadCommit},
    }, nil
}
```

**Outputs:** `repoDir` (work dir path), `headCommit` (current HEAD SHA).
**Idempotent:** checks if work dir is up to date before cloning.

### kustomize-set-image

Runs `kustomize edit set-image` for each image in the Bundle.

```go
func (s *KustomizeSetImageStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    envDir := filepath.Join(state.WorkDir, state.Environment.Path)
    for _, img := range state.Bundle.Artifacts.Images {
        current := readCurrentImage(envDir, img.Name)
        if current == img.Reference {
            continue // already set, idempotent
        }
        cmd := exec.CommandContext(ctx, "kustomize", "edit", "set-image",
            fmt.Sprintf("%s=%s", img.Name, img.Reference))
        cmd.Dir = envDir
        if out, err := cmd.CombinedOutput(); err != nil {
            return StepResult{Status: StepFailed, Message: string(out)}, nil
        }
    }
    return StepResult{Status: StepSuccess, Message: "Image(s) updated"}, nil
}
```

**Outputs:** none (modifies files in work dir).
**Idempotent:** checks current image before running kustomize.

### helm-set-image (Phase 2)

Patches the image tag at a configurable path in `values.yaml`.

```go
func (s *HelmSetImageStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    valuesPath := filepath.Join(state.WorkDir, state.Environment.Path, "values.yaml")
    tagPath := state.StepConfig["tagPath"].(string) // default: "image.tag"
    for _, img := range state.Bundle.Artifacts.Images {
        if err := patchYAMLValue(valuesPath, tagPath, img.Tag()); err != nil {
            return StepResult{Status: StepFailed, Message: err.Error()}, nil
        }
    }
    return StepResult{Status: StepSuccess, Message: "Helm values updated"}, nil
}
```

### kustomize-build (Phase 2)

Runs `kustomize build` and writes the rendered output. This supports the Rendered Manifests pattern.

```go
func (s *KustomizeBuildStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    envDir := filepath.Join(state.WorkDir, state.Environment.Path)
    outputDir := state.StepConfig["outputDir"].(string) // where to write rendered output
    cmd := exec.CommandContext(ctx, "kustomize", "build", envDir)
    output, err := cmd.CombinedOutput()
    if err != nil {
        return StepResult{Status: StepFailed, Message: string(output)}, nil
    }
    if err := os.WriteFile(filepath.Join(state.WorkDir, outputDir, "manifests.yaml"), output, 0644); err != nil {
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }
    return StepResult{Status: StepSuccess, Message: "Rendered manifests written"}, nil
}
```

### config-merge

See 09-config-only-promotions for the full spec.

### git-commit

Commits changes with a structured message.

```go
func (s *GitCommitStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    if !hasUncommittedChanges(state.WorkDir) {
        return StepResult{Status: StepSuccess, Message: "No changes to commit (idempotent)"}, nil
    }
    message := fmt.Sprintf("[kardinal] Promote %s to %s: %s\n\nBundle: %s\nPipeline: %s\nEnvironment: %s",
        state.Pipeline.Name, state.Environment.Name, state.Bundle.Version(),
        state.Bundle.Name, state.Pipeline.Name, state.Environment.Name)
    commitSHA, err := gitCommit(state.WorkDir, message)
    if err != nil {
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }
    return StepResult{
        Status:  StepSuccess,
        Message: "Committed",
        Outputs: map[string]interface{}{"commitSHA": commitSHA},
    }, nil
}
```

**Outputs:** `commitSHA`.
**Idempotent:** if no uncommitted changes, succeeds as no-op.

### git-push

Pushes to the target branch.

```go
func (s *GitPushStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    branch := resolveBranch(state) // environment branch or kardinal/<pipeline>/<env>/<version>
    if err := s.gitClient.Push(ctx, state.WorkDir, branch); err != nil {
        if isConflict(err) {
            // Re-fetch and retry (up to 3 times, handled by reconciler)
            return StepResult{Status: StepFailed, Message: "Push conflict: " + err.Error()}, nil
        }
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }
    return StepResult{
        Status:  StepSuccess,
        Message: "Pushed to " + branch,
        Outputs: map[string]interface{}{"branch": branch},
    }, nil
}
```

**Outputs:** `branch`.

### open-pr

Opens a PR with promotion evidence.

```go
func (s *OpenPRStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    branch := state.Outputs["branch"].(string)

    // Check if PR already exists (idempotent)
    existing, err := s.scm.FindPR(ctx, state.Git.URL, branch)
    if err == nil && existing != nil {
        return StepResult{
            Status: StepSuccess, Message: "PR already exists",
            Outputs: map[string]interface{}{"prURL": existing.URL, "prNumber": existing.Number},
        }, nil
    }

    // Build PR body with evidence
    body := buildPRBody(state)
    pr, err := s.scm.CreatePR(ctx, PROptions{
        Repo:   state.Git.URL,
        Head:   branch,
        Base:   state.Git.Branch,
        Title:  buildPRTitle(state),
        Body:   body,
        Labels: buildPRLabels(state),
    })
    if err != nil {
        return StepResult{Status: StepFailed, Message: err.Error()}, nil
    }
    return StepResult{
        Status: StepSuccess, Message: "PR opened: " + pr.URL,
        Outputs: map[string]interface{}{"prURL": pr.URL, "prNumber": pr.Number},
    }, nil
}
```

**Outputs:** `prURL`, `prNumber`.
**Idempotent:** checks for existing PR before creating.

### wait-for-merge

Returns Pending until the PR is merged. The PromotionStep reconciler detects the merge via webhook and sets `status.prMerged = true`.

```go
func (s *WaitForMergeStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    // The reconciler sets these flags based on webhook events
    if state.PRMerged {
        return StepResult{Status: StepSuccess, Message: "PR merged"}, nil
    }
    if state.PRClosed {
        return StepResult{Status: StepFailed, Message: "PR closed without merge"}, nil
    }
    return StepResult{Status: StepPending, Message: "Waiting for PR merge"}, nil
}
```

**Returns Pending:** the reconciler requeues and checks again later.

### health-check

Delegates to the health adapter (see 05-health-adapters).

```go
func (s *HealthCheckStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    adapter := s.registry.Resolve(state.Environment.Health.Type)
    status, err := adapter.Check(ctx, buildCheckOptions(state))
    if err != nil {
        return StepResult{}, err
    }
    if status.Healthy {
        return StepResult{
            Status: StepSuccess, Message: status.Reason,
            Outputs: map[string]interface{}{"healthDetails": status.Details},
        }, nil
    }
    // Not healthy yet: pending (reconciler will requeue after 10s)
    return StepResult{Status: StepPending, Message: status.Reason}, nil
}
```

## Custom Step Webhook

Any step name not matching a built-in step is dispatched as an HTTP POST:

```go
func dispatchWebhook(ctx context.Context, stepDef StepDefinition, state *StepState) (StepResult, error) {
    url := stepDef.Config["url"].(string)
    timeout := parseDurationOrDefault(stepDef.Config["timeout"], 60*time.Second)

    request := StepRequest{
        Pipeline:    state.Pipeline.Name,
        Environment: state.Environment.Name,
        Bundle:      state.Bundle,
        Context:     state.Outputs,
        Config:      stepDef.Config,
    }
    body, _ := json.Marshal(request)

    client := &http.Client{Timeout: timeout}
    resp, err := client.Post(url, "application/json", bytes.NewReader(body))
    if err != nil {
        return StepResult{Status: StepFailed, Message: "Webhook call failed: " + err.Error()}, nil
    }
    defer resp.Body.Close()

    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return StepResult{Status: StepFailed, Message: fmt.Sprintf("Webhook returned %d", resp.StatusCode)}, nil
    }

    var response StepResponse
    if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
        return StepResult{Status: StepFailed, Message: "Invalid webhook response: " + err.Error()}, nil
    }

    if response.Success {
        return StepResult{Status: StepSuccess, Message: response.Message, Outputs: response.Outputs}, nil
    }
    return StepResult{Status: StepFailed, Message: response.Message}, nil
}
```

**Authentication:** If `stepDef.Config["secretRef"]` is set, the webhook request includes a `Bearer` token from the referenced Secret.

**Retry:** No automatic retry. If the webhook fails, the PromotionStep is marked Failed. Users should implement retry in their webhook endpoint if needed.

## Step Output Passing

Each step's `Outputs` map is merged into the shared `StepState.Outputs` accumulator. Downstream steps can read upstream outputs:

```
git-clone   outputs: {repoDir: "/tmp/repo", headCommit: "abc123"}
kustomize   outputs: {} (modifies files, no explicit outputs)
git-commit  outputs: {commitSHA: "def456"}
git-push    outputs: {branch: "kardinal/my-app/prod/v1.29.0"}
open-pr     outputs: {prURL: "https://...", prNumber: 144}
```

The `open-pr` step accesses `state.Outputs["branch"]` to know which branch to create the PR from. The `wait-for-merge` step accesses `state.Outputs["prNumber"]` to know which PR to watch.

## PromotionTemplate (Phase 3)

When multiple environments share the same custom step sequence, a PromotionTemplate CRD allows defining the sequence once and referencing it:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PromotionTemplate
metadata:
  name: prod-steps
spec:
  steps:
    - uses: git-clone
    - uses: kustomize-set-image
    - uses: run-integration-tests
      config:
        url: https://test-runner.internal/validate
        timeout: 10m
    - uses: git-commit
    - uses: git-push
    - uses: open-pr
    - uses: wait-for-merge
    - uses: health-check
```

Referenced from a Pipeline:
```yaml
environments:
  - name: prod
    stepsRef: { name: prod-steps }
```

This is a Phase 3 feature. Phase 1-2 use inline `steps` on each environment.

## Unit Tests

1. Default inference: image Bundle + kustomize + auto -> correct sequence.
2. Default inference: image Bundle + kustomize + pr-review -> correct sequence with open-pr and wait-for-merge.
3. Default inference: config Bundle + auto -> correct sequence with config-merge instead of kustomize-set-image.
4. Default inference: image Bundle + helm + pr-review -> correct sequence with helm-set-image.
5. git-clone: idempotent (already cloned, returns success).
6. kustomize-set-image: idempotent (image already set, returns success).
7. git-commit: idempotent (no changes, returns success).
8. open-pr: idempotent (PR exists, returns existing PR).
9. wait-for-merge: returns Pending when prMerged is false.
10. wait-for-merge: returns Success when prMerged is true.
11. wait-for-merge: returns Failed when prClosed is true.
12. health-check: returns Success when adapter reports Healthy.
13. health-check: returns Pending when adapter reports unhealthy.
14. Custom webhook: successful response parsed correctly.
15. Custom webhook: non-2xx status treated as failure.
16. Custom webhook: timeout treated as failure.
17. Output passing: open-pr reads branch from git-push output.
