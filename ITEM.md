# Item 016: PR Evidence, Labels, and Webhook Reliability (Stage 10)

> **Queue**: queue-007
> **Branch**: `016-pr-evidence`
> **Depends on**: 013 (merged — PromotionStep reconciler + open-pr step)
> **Dependency mode**: merged
> **Assignable**: immediately (parallel with 014 and 015)
> **Contributes to**: J1, J4 (PR quality + rollback PR structure)
> **Priority**: MEDIUM — improves J1 PR quality, not on critical path

---

## Goal

Complete the PR evidence feature (F6) with the full structured body, GitHub labels,
and a robust webhook + startup reconciliation path. The `open-pr` step currently
opens a PR with a basic body. This item adds the full evidence tables.

Design spec: `docs/design/08-promotion-steps-engine.md` (PR template section),
roadmap Stage 10.

---

## Deliverables

### 1. Full PR body template in `pkg/scm/pr_template.go`

Extend `RenderPRBody` to include all three required tables:

**Policy gate compliance table:**
```
| Gate | Namespace | Result | Reason | Last Evaluated |
|------|-----------|--------|--------|----------------|
| no-weekend-deploys | platform-policies | PASS | schedule.isWeekend=false | 2026-04-10T14:00Z |
```

**Artifact provenance table:**
```
| Image | Tag | Digest | CI Run | Commit SHA | Author |
|-------|-----|--------|--------|------------|--------|
| nginx | 1.25 | sha256:... | https://... | abc1234 | alice |
```

**Upstream verification table:**
```
| Environment | Health Checked At | Elapsed |
|-------------|------------------|---------|
| test | 2026-04-10T14:05Z | 8m |
| uat | 2026-04-10T14:20Z | 23m |
```

The `StepState` already carries `GateResults` and `UpstreamEnvironments` —
populate these from PromotionStep status before the open-pr step runs.

### 2. GitHub label management

In `pkg/scm/github.go`, add:
```go
func (g *GitHubProvider) EnsureLabels(ctx context.Context, repo string, labels []Label) error
```
- Creates labels if they don't exist: `kardinal`, `kardinal/promotion`, `kardinal/rollback`, `kardinal/emergency`
- Called once on controller startup

### 3. Label application in `open-pr` step

In `pkg/steps/steps/open_pr.go`:
- After OpenPR succeeds, call `SCMProvider.AddLabelsToPR(ctx, repo, prNumber, labels)` 
- Add to `SCMProvider` interface: `AddLabelsToPR(ctx context.Context, repo string, prNumber int, labels []string) error`
- Apply `kardinal` and `kardinal/promotion` to every promotion PR

### 4. Webhook improvements

In `cmd/kardinal-controller/webhook.go`:
- Add retry with exponential backoff (3 retries, 30s max) on transient errors
- Add `GET /webhook/scm/health` endpoint (already done in item 013 — verify it works)
- Add structured logging: `kardinal_webhook_events_total` via a counter variable

### 5. Unit tests

- `TestPRTemplate_ContainsAllTables`: verify all 3 tables in rendered body
- `TestPRTemplate_GateCompliance`: correct pass/fail values per gate
- `TestPRTemplate_Provenance`: image tag, digest, CI run present
- `TestEnsureLabels_CreatesIfMissing`: mock GitHub API, verify label creation
- `TestOpenPR_AppliesLabels`: after PR creation, labels are applied

---

## Acceptance Criteria

- [ ] PR body contains policy gate compliance table (gate | result | reason | last-evaluated)
- [ ] PR body contains artifact provenance table (image | tag | digest | CI run | commit SHA | author)
- [ ] PR body contains upstream verification table (env | health-checked-at | elapsed)
- [ ] Labels `kardinal` and `kardinal/promotion` applied to every promotion PR
- [ ] `EnsureLabels` creates labels on controller startup if missing
- [ ] `AddLabelsToPR` method added to SCMProvider interface and GitHubProvider
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames

---

## Notes

- `pkg/scm/pr_template.go` already has the skeleton — extend the existing RenderPRBody
- StepState.GateResults and UpstreamEnvironments are already fields in step.go
- The PromotionStep reconciler should populate these before calling the step engine
- PR body update on gate re-evaluation (CommentOnPR edit) is deferred to Stage 10 full
- `AddLabelsToPR` adds: `PATCH /repos/{owner}/{repo}/issues/{issue_number}/labels`
