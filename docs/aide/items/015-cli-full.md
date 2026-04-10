# Item 015: Full CLI — create bundle, policy, rollback, pause/resume (Stage 8 part 2)

> **Queue**: queue-007
> **Branch**: `015-cli-full`
> **Depends on**: 013 (merged — PromotionStep reconciler)
> **Dependency mode**: merged
> **Assignable**: immediately (parallel with 014)
> **Contributes to**: J3, J4, J5 (CLI workflow journeys)
> **Priority**: HIGH — J5 journey requires all CLI commands

---

## Goal

Implement the remaining CLI commands from Stage 8: `create bundle`, `promote`,
`rollback`, `pause`, `resume`, `policy list`, `policy test`, `policy simulate`,
`history`, `diff`, `version` (already done). All commands create or read CRDs.

Design spec: `docs/design/03-promotionstep-reconciler.md` (kardinal explain done),
roadmap Stage 8.

---

## Deliverables

### 1. `kardinal create bundle` command

In `cmd/kardinal/cmd/create_bundle.go`:
```bash
kardinal create bundle <pipeline> --image <repo:tag> [--type image|config] [--namespace ns]
```
- Creates a `Bundle` CRD with spec.type=image, spec.images from --image, spec.pipeline from arg
- Prints: `Bundle <name> created. Tracking at: kardinal get bundles <pipeline>`
- Accepts multiple --image flags

### 2. `kardinal promote` command

In `cmd/kardinal/cmd/promote.go`:
```bash
kardinal promote <pipeline> --env <environment>
```
- Creates a `PromotionStep` CRD with spec.pipelineName, spec.environment, spec.stepType="pr-review"
- Prints PR URL when available (poll for 10s, then print "Promoting: track with kardinal explain")

### 3. `kardinal rollback` command

In `cmd/kardinal/cmd/rollback.go`:
```bash
kardinal rollback <pipeline> --env <environment> [--to <bundle-name>] [--emergency]
```
- Lists verified Bundles for the pipeline, finds the most recent one before current
- Creates a new Bundle with `spec.provenance.rollbackOf` = previous Bundle name
- If --to is specified, uses that Bundle name
- Prints: `Rolling back <pipeline> in <env>: PR opened at <url>`

### 4. `kardinal pause` and `kardinal resume`

In `cmd/kardinal/cmd/pause.go`:
```bash
kardinal pause <pipeline>
```
- Patches `Pipeline.spec.paused = true`
- Prints: `Pipeline <name> paused. No new promotions will start.`

```bash
kardinal resume <pipeline>
```
- Patches `Pipeline.spec.paused = false`
- Prints: `Pipeline <name> resumed.`

### 5. `kardinal policy list`

In `cmd/kardinal/cmd/policy.go`:
```bash
kardinal policy list [--pipeline <name>]
```
- Lists PolicyGates in namespace
- Table: NAME | SCOPE | APPLIES-TO | RECHECK | READY | LAST-EVALUATED

### 6. `kardinal policy simulate`

```bash
kardinal policy simulate --pipeline <name> --env <environment> \
  [--time "Saturday 3pm"] [--soak-minutes N]
```
- Builds a mock CEL context from flags
- Evaluates each PolicyGate against the mock context using the CEL evaluator
- Prints:
  ```
  RESULT: BLOCKED
  Blocked by: no-weekend-deploys
  Message: "Production deployments are blocked on weekends"
  ```
  or:
  ```
  RESULT: PASS
  no-weekend-deploys: PASS (Tuesday 10:00 UTC, isWeekend=false)
  ```

### 7. `kardinal history`

```bash
kardinal history <pipeline>
```
- Lists Bundles for pipeline sorted by creation time, newest first
- Table: BUNDLE | TYPE | PHASE | CREATED | AGE

### 8. Unit tests

Table-driven tests for all new commands using fake client:
- `TestCreateBundle_CreatesBundle`
- `TestRollback_CreatesBundleWithRollbackOf`
- `TestPause_PatchesPipelinePaused`
- `TestResume_UnpausesPipeline`
- `TestPolicyList_ShowsGates`
- `TestPolicySimulate_BlockedOnWeekend`
- `TestPolicySimulate_PassOnWeekday`
- `TestHistory_ListsBundles`

---

## Acceptance Criteria

- [ ] `kardinal create bundle <pipeline> --image <repo:tag>` creates a Bundle CRD
- [ ] `kardinal rollback <pipeline> --env <env>` creates a rollback Bundle with `rollbackOf`
- [ ] `kardinal pause <pipeline>` patches Pipeline.spec.paused=true
- [ ] `kardinal resume <pipeline>` patches Pipeline.spec.paused=false
- [ ] `kardinal policy list` shows PolicyGates with scope, applies-to, recheckInterval
- [ ] `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED with reason
- [ ] `kardinal policy simulate` with all gates passing returns PASS with table
- [ ] `kardinal history <pipeline>` lists Bundles sorted by age
- [ ] All commands use `sigs.k8s.io/controller-runtime/pkg/client` to talk to Kubernetes
- [ ] `go build ./...` passes
- [ ] `go test ./cmd/kardinal/... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames

---

## Notes

- `kardinal policy simulate` uses `pkg/cel.Evaluator` directly — no controller required
- Time flag "Saturday 3pm" should be parsed as UTC; use `time.Parse` with flexible format
- For --soak-minutes: set `bundle.upstreamSoakMinutes` in CEL context
- `rollbackOf` is already in `BundleProvenance.RollbackOf` (item 005)
- All commands must match the output format in `docs/cli-reference.md`
