# Item 012: SCM Provider, Steps Engine, and Git Built-ins (Stage 5)

> **Queue**: queue-006
> **Branch**: `012-scm-and-steps-engine`
> **Depends on**: 010 (merged — CEL/PolicyGate), 009 (merged — Graph Builder)
> **Dependency mode**: merged
> **Assignable**: immediately
> **Contributes to**: J1, J3, J4 (core promotion machinery)
> **Priority**: HIGH — blocks all end-to-end journeys

---

## Goal

Implement the SCM provider interface + GitHub implementation, the steps engine
(`pkg/steps/`), and all Git/manifest built-in steps needed for the full promotion
loop.

Design spec: `docs/design/08-promotion-steps-engine.md`, roadmap Stage 5.

---

## Deliverables

### 1. `pkg/scm` package

```
pkg/scm/
  provider.go        # SCMProvider interface
  github.go          # GitHubProvider implementation (go-github)
  git_client.go      # GitClient interface + GoGitClient (go-git)
  pr_template.go     # PR body template: provenance, gate compliance, upstream verification tables
```

**SCMProvider interface:**
- `OpenPR(ctx, repo, title, body, head, base string) (prURL string, prNumber int, error)`
- `ClosePR(ctx, repo string, prNumber int) error`
- `CommentOnPR(ctx, repo string, prNumber int, body string) error`
- `GetPRStatus(ctx, repo string, prNumber int) (merged bool, open bool, error)`
- `ParseWebhookEvent(payload []byte, signature string) (WebhookEvent, error)`

**GitClient interface:**
- `Clone(ctx, url, branch, dir string) error` — shallow `--depth=1`
- `Checkout(ctx, dir, branch string) error`
- `CommitAll(ctx, dir, message string) error`
- `Push(ctx, dir, remote, branch string, token string) error`

### 2. `pkg/steps` package

```
pkg/steps/
  step.go           # Step interface + StepState + StepResult
  engine.go         # Engine: executes step sequence, accumulates outputs
  registry.go       # Built-in step registry (maps name → Step)
  defaults.go       # Default step sequence from Pipeline env config
  steps/
    git_clone.go    # git-clone: clone repo to WorkDir
    kustomize.go    # kustomize-set-image: edit kustomization.yaml
    git_commit.go   # git-commit: stage+commit with structured message
    git_push.go     # git-push: push branch via GitClient
    open_pr.go      # open-pr: call SCMProvider.OpenPR; store prURL in outputs
    wait_for_merge.go # wait-for-merge: poll GetPRStatus; return Pending until merged
    health_check.go   # health-check: stub returning Success (real adapters in Stage 7)
  steps_test.go     # Unit tests for each step (table-driven, mock SCM + git)
```

### 3. Unit tests

- Table-driven tests for each step using mock `SCMProvider` and `GitClient`
- Engine tests: verify output accumulation and step sequencing
- PR template test: verify all three tables are present in output
- Default sequence test: `auto` approval → no open-pr; `pr-review` → includes open-pr

---

## Acceptance Criteria

- [ ] `pkg/scm.SCMProvider` interface defined with all 5 methods
- [ ] `pkg/scm.GitHubProvider` implements `SCMProvider` using `google/go-github`
- [ ] `pkg/scm.GitClient` interface defined; `GoGitClient` uses `go-git/go-git`
- [ ] PR body template includes provenance table, gate compliance table, upstream verification table
- [ ] `pkg/steps.Engine` executes a step sequence and accumulates outputs
- [ ] All 7 built-in steps implemented (git-clone, kustomize-set-image, git-commit, git-push, open-pr, wait-for-merge, health-check stub)
- [ ] Default step sequence: `auto` approval omits open-pr/wait-for-merge; `pr-review` includes them
- [ ] `go build ./...` passes
- [ ] `go test ./pkg/scm/... ./pkg/steps/...` passes with `-race`
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames
- [ ] All new packages have `_test.go` files

---

## Notes

- Use `go-github v67` and `go-git v5` (add to `go.mod` if not present — check first)
- Token sourced from `StepState.Git.Token` (resolved from Kubernetes Secret by caller)
- Shallow clone target: under 2 seconds for cached repos
- `health-check` step: stub returns `StepResult{Status: StepSuccess}` — real adapters in item 014
- Do NOT implement the webhook endpoint yet (that is in item 013)
