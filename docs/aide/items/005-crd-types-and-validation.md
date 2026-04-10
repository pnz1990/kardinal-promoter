# Item 005: Complete CRD Types, Validation Markers, Generated YAML, and Roundtrip Tests

> **Queue**: queue-002 (Stage 1)
> **Branch**: `005-crd-types-and-validation`
> **Depends on**: 001, 002, 003, 004 (all Stage 0 items — foundation complete)
> **Dependency mode**: merged
> **Assignable**: immediately (all deps done)
> **Contributes to**: All journeys (type system foundation)

---

## Goal

The scaffold from Stage 0 created stub Go types with partial fields. This item
completes them with all roadmap-specified fields, OpenAPI validation markers, generated
CRD YAML, sample manifests, and roundtrip JSON marshal/unmarshal tests.

No controller logic. Schema artifacts only.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 1: CRD Types and Validation

---

## Deliverables

### 1. `Pipeline` CRD type — complete all fields in `api/v1alpha1/pipeline_types.go`

`spec.git` (top-level, shared by all environments):
- `url` (string, required — GitOps repo URL)
- `branch` (string — default branch)
- `layout` (enum: `directory|branch`, default: `directory`)
- `provider` (enum: `github|gitlab`, default: `github`)
- `secretRef` (object: `name`, `namespace` — reference to Secret with token)

`spec.environments[]`:
- `name` (required, MinLength=1)
- `path` (string — subdirectory in repo for `layout: directory`)
- `approvalMode` (enum: `auto|pr-review`, default: `auto`)
- `updateStrategy` (enum: `kustomize|helm`, default: `kustomize`)
- `healthAdapter` (string — `deployment|argocd|flux|argoRollouts`)
- `healthTimeout` (string — duration, default: `30m`)
- `deliveryDelegate` (string — `argoRollouts|flagger`, optional)
- `dependsOn` ([]string — names of other environments in this pipeline)
- `shard` (string — for distributed mode agent routing, optional)

`spec.policyGates[]`:
- `name` (string — reference to PolicyGate name)
- `namespace` (string — PolicyGate namespace)

`spec.paused` (bool, default: false)

`status.phase` (enum: `Ready|Degraded|Unknown`, default: `Unknown`)
`status.conditions[]` (metav1.Condition)

### 2. `Bundle` CRD type — complete all fields in `api/v1alpha1/bundle_types.go`

`spec.type` (enum: `image|config|mixed`, required)
`spec.pipeline` (string, required)
`spec.images[]`:
- `repository` (required)
- `tag` (string)
- `digest` (string — sha256:...)

`spec.configRef`:
- `gitRepo` (string)
- `commitSHA` (string)

`spec.provenance`:
- `commitSHA` (string)
- `ciRunURL` (string)
- `author` (string)
- `timestamp` (metav1.Time)
- `rollbackOf` (string — name of Bundle this rolls back)

`spec.intent`:
- `targetEnvironment` (string — promote only to this env, optional)
- `skipEnvironments` ([]string)

`status.phase` (enum: `Available|Promoting|Verified|Failed|Superseded`)
`status.conditions[]` (metav1.Condition)
`status.environments[]` — per-environment evidence:
- `name` (string)
- `phase` (string)
- `prURL` (string)
- `prMergedAt` (*metav1.Time)
- `mergedBy` (string)
- `healthCheckedAt` (*metav1.Time)
- `gateResults[]` (object: `gateName`, `result`, `reason`, `evaluatedAt`)

### 3. `PolicyGate` CRD type — complete all fields in `api/v1alpha1/policygate_types.go`

`spec.expression` (string, required, MinLength=1)
`spec.message` (string)
`spec.recheckInterval` (string, default: `5m`)
`spec.skipPermission` (bool, default: false)
`spec.selector` (metav1.LabelSelector — for org-level auto-injection)

`status.ready` (bool, default: false)
`status.reason` (string)
`status.lastEvaluatedAt` (*metav1.Time)
`status.conditions[]` (metav1.Condition)

### 4. `PromotionStep` CRD type — complete all fields in `api/v1alpha1/promotionstep_types.go`

**IMPORTANT**: This CRD uses `status.state` (not `status.phase`). The Graph controller
generates `readyWhen` expressions like `${dev.status.state == "Verified"}`. Using
`status.phase` instead would break all Graph DAG advancement. See `design-v2.1.md` §3.5
and every `${env.status.state}` reference in the design.

`spec.pipelineName` (string, required)
`spec.bundleName` (string, required)
`spec.environment` (string, required)
`spec.stepType` (string, required — e.g. `git-clone`, `open-pr`, etc.)
`spec.inputs` (map[string]string — step inputs from pipeline config)

`status.state` (enum: `Pending|Promoting|WaitingForMerge|HealthChecking|Verified|Failed`)

  State meanings (from `docs/design/03-promotionstep-reconciler.md`):
  - `Pending`: created by Graph, not yet picked up by reconciler
  - `Promoting`: step sequence executing (git-clone through git-push)
  - `WaitingForMerge`: `open-pr` complete, waiting for PR merge webhook
  - `HealthChecking`: merged, polling health adapter
  - `Verified`: health check passed, evidence copied to Bundle
  - `Failed`: any step or health check failed

`status.currentStepIndex` (int — index into the step sequence; survives reconciler restart for idempotent crash recovery, required by spec 003 FR-002)
`status.message` (string — human-readable detail about current state)
`status.prURL` (string — GitHub PR URL, set during WaitingForMerge)
`status.outputs` (map[string]string — step outputs accumulated across steps)
`status.conditions[]` (metav1.Condition)

The `+kubebuilder:printcolumn` should reference `.status.state`, not `.status.phase`.

### 5. Run `make manifests` — regenerate CRD YAML in `config/crd/bases/`

The generated YAML must reflect all new fields. Run `make manifests` and commit
the updated CRD YAML alongside the type changes.

### 6. Update `config/samples/` — add complete sample manifests for each CRD

Each sample must use all required fields and demonstrate a realistic minimal config.

- `config/samples/kardinal_v1alpha1_pipeline.yaml` — 3-environment pipeline (test/uat/prod)
- `config/samples/kardinal_v1alpha1_bundle.yaml` — image bundle targeting the sample pipeline
- `config/samples/kardinal_v1alpha1_policygate.yaml` — no-weekend-deploys gate
- `config/samples/kardinal_v1alpha1_promotionstep.yaml` — sample step (internal, not user-created)

### 7. Add roundtrip JSON marshal/unmarshal tests in `api/v1alpha1/types_test.go`

For each CRD type: marshal to JSON, unmarshal back, assert deep equality.
Tests must use `testify/assert` and `testify/require`.

---

## Acceptance Criteria (from roadmap Stage 1)

- [ ] All new fields present in Go types with correct json tags and kubebuilder markers
- [ ] `make manifests` regenerates CRD YAML without diff (idempotent)
- [ ] `make build` still passes (no compile errors)
- [ ] `go test ./api/... -race` passes (all roundtrip tests green)
- [ ] `go vet ./...` passes with no new warnings
- [ ] Generated CRD YAML would pass `kubectl apply --dry-run=server` (document in PR body)
- [ ] Sample manifests in `config/samples/` use all required fields
- [ ] Copyright header `// Copyright 2026 The kardinal-promoter Authors.` on all modified files
- [ ] No banned filenames (`util.go`, `helpers.go`, `common.go`)

---

## Journey Contribution

This item advances all journeys — it provides the type system every other item depends on.
Without complete types, no subsequent stage can implement correct field access.

Journey validation step from definition-of-done.md that this unlocks (partial):
```bash
kubectl apply -f config/crd/bases/  # installs CRDs without error
kubectl apply -f config/samples/    # creates valid objects
```

Include the output of these two commands in the PR body as journey validation evidence.

---

## Anti-patterns to Avoid

- Do NOT add `util.go`, `helpers.go`, or `common.go`
- Do NOT mutate Deployments/Services directly
- Do NOT add `kro` to go.mod
- Use `fmt.Errorf("context: %w", err)` — no bare errors
- Use zerolog via `zerolog.Ctx(ctx)` — no fmt.Println

---

## Notes for Engineer

Read `docs/aide/roadmap.md` Stage 1 deliverables carefully — the field list above
mirrors it exactly. Cross-reference `docs/design/design-v2.1.md` sections 2.1-2.4
for field semantics. The scaffold type files have `// Full field definitions are
added in Stage 1.` comments — replace them with the complete types.

**Three corrections vs the scaffold stubs (do not follow the stubs for these)**:

1. **PromotionStep `status.state` not `status.phase`** — The existing stub has `status.phase`
   but every design doc, every Graph `readyWhen` CEL expression (`${dev.status.state}`),
   and spec 003 uses `status.state`. Implement `status.state` and update the
   `+kubebuilder:printcolumn` annotation accordingly.

2. **PromotionStep enum values** — Correct values are
   `Pending|Promoting|WaitingForMerge|HealthChecking|Verified|Failed`
   (not `Pending|Running|Succeeded|Failed|Blocked`).

3. **Pipeline `spec.git` is top-level** — Git configuration is shared across all
   environments via `spec.git` (url, branch, layout, provider, secretRef).
   Do NOT put `gitRepo` or `branch` inside `spec.environments[]`.
   See `docs/design/design-v2.1.md` line 710 and `examples/quickstart/pipeline.yaml`.

Run `make manifests` after each type change to keep the generated CRD YAML in sync.
The `controller-gen` binary is already installed via the scaffold Makefile.
