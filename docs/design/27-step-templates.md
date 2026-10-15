# Design: StepTemplate custom steps

> Status: Proposed (spec for AGENTIC-PLAN §5, v0.14.0). Implementation waits until v0.13.0 is
> tagged, and follows in separate PRs.
> Created: 2026-10-15
> Depends on: [19-verdicts.md](19-verdicts.md) (the v0.12 `StepTemplate` registration and
> `AgentRun`; implementation in review in #1870, not on main), [17-actors.md](17-actors.md)
> (AgentIdentity, the actor stamp).
> Verified against: kro v0.10.0-rc.0 (`54a203b`), Kubernetes `k8s.io/api`, `k8s.io/apiserver`
> and `k8s.io/apiextensions-apiserver` v0.37.1, and this repository at `8f5d86e`.
> Source references are `path:line` in those trees, or in this repository.

---

## 1. Problem

Teams want to run their own logic inside a promotion: scan the rendered manifests for policy
violations before a PR is opened, write a generated file next to the image update, run a load
test after deploy and hand its p99 to the prod gate, ask a change-management system for a
ticket number and put it in the PR. Today they have two options, and neither fits:

- **Hooks** (`environments[].hooks`, `docs/hooks.md`) run any Job the team writes, at `pre` and
  `post` only. A hook has no inputs, no outputs and no access to the change being made. Its
  Job is a team-written `JobSpec`: any image, any volume, any Secret, any ServiceAccount on the
  controller's `--hook-service-accounts` list.
- **Nothing** at the points in between: after the image update and before the commit, or after
  the commit and before the change leaves kardinal (the push and the PR).

kardinal once had a generic step list (`environments[].steps`, a webhook `custom` step,
`verify-image`, `integration-test`). It was rejected and removed (#1244, #1282, closed "not
planned" on 2026-09-30) because the webhook step was an SSRF path from the controller,
`verify-image` ran cosign as a subprocess, and `integration-test` created Jobs from the
reconciler. This design does none of those: no custom code runs in the controller, the
controller makes no call on a step's behalf, and every Job is created by a reconciler that owns
it and writes only its own status. The owner put custom steps back in scope for v0.14
(AGENTIC-PLAN §5) on those terms.

This spec defines:

- `StepTemplate` for steps (section 3): the v0.12 registration gains `output: Outputs`, input
  and output schemas, hook points, image trust and tenancy;
- how a Pipeline uses a template per environment (section 4);
- the four hook points and how they map onto the step engine and the PromotionStep state machine
  (section 5);
- `StepRun`, the run object of a `pre` or `post` step (section 6), and the workspace steps that
  run inside the environment's promotion Job (section 7);
- outputs: schema validation, Graph references and the `steps.*` CEL variable (section 8);
- the sandbox (section 9), versioning (section 10), tenancy (section 11), AgentRun (section 12).

### Scope

- Admin-registered, digest-pinned images that compute something about a promotion and report
  structured outputs, at four hook points.
- Workspace steps that may read, and at `after-update` edit, the environment's files before
  kardinal commits them.
- Outputs readable by later Graph nodes, by gates (`steps.<name>.outputs.*`), in the PR
  evidence and in the audit trail.

### Non-goals

- **Writing to the cluster.** A step does not deploy, migrate or mutate anything through the
  Kubernetes API. That is what hooks are for; hooks stay as they are.
- **Running anything in the controller.** No exec, no subprocess, no plugin loading, no webhook
  from the controller.
- **Replacing the built-in sequence.** git-clone, the update strategies, commit, push, open-pr,
  wait-for-merge and health-check stay kardinal's. A step cannot remove or reorder them.
- **Git credentials for custom code.** No hook point gives a step the Pipeline's git Secret.
- **Large artifacts.** Outputs are small structured values (section 8.1). A step that produces a
  report stores it elsewhere and outputs its URL.
- **Fleet sources for gates** (section 8.4) and **cluster-scoped `use` grants per namespace**
  (section 11): follow-ups.

### Graph-first

The AGENTS.md questions, in order, for each piece:

| Piece | Watch (`ref`) node? | Owned node with a status-writing reconciler? | CEL? |
|-------|---------------------|----------------------------------------------|------|
| Registration | Not read through a `ref`: a namespaced Graph reading a cluster-scoped object needs a cluster-wide grant per namespace (ledger G19). The translator snapshots it into the Graph instead (section 10). | The StepTemplate reconciler validates it and writes `status.ready` and `status.revisions`. | No |
| `pre` / `post` run | | **Yes**: `StepRun`, a collection item the Graph owns; its reconciler runs the Job and writes only `StepRun.status` (section 6). | Admission and ordering are `def` CEL over read-back data |
| `after-update` / `before-pr` run | | **Yes**: the existing owned `RenderRun`, whose reconciler writes `RenderRun.status.steps` (section 7). | |
| Outputs to later nodes | **Yes**: a selector `ref` reads the runs back; templates copy `status.outputs` (6.2). | | Yes, in templates |
| Outputs to gates | Same `ref` | The PolicyGate reconciler, from its own spec (8.4) | **Yes**: one new variable, `steps` |
| The step waits | | The PromotionStep reads only its Graph-mirrored `spec.live.steps` (5.2) | |

Anti-patterns checked: no exec or subprocess in a reconciler (custom code runs only in Pods);
no HTTP in a promotion's hot path (the one registry call, the signature, is in the StepTemplate
reconciler, once per revision); no cross-CRD status write (each reconciler writes its own
status; the PromotionStep's record of a run is in its own `status.stepRecords`, and the Graph
carries it to a re-created StepRun's spec); `time.Now()` only when writing `startedAt`,
`deadline` and `finishedAt` to status. One read is a cross-object decision input: the StepRun
and RenderRun reconcilers read the StepTemplate at start to verify the snapshot (10.2). That is a
fail-closed check against a registration only admins write, the same read #1870's AgentRun
reconciler makes (`TemplateProblem`); the Graph alternative is a `ref`, which G19 makes
expensive in every tenant namespace.

## 2. Terms

| Term | Meaning |
|------|---------|
| **StepTemplate** | Cluster-scoped registration of a step image, written by cluster admins (section 3). |
| **Custom step** | One use of a StepTemplate in one Pipeline environment, at one hook point, with a name (section 4). |
| **Hook point** | Where a custom step runs: `pre`, `after-update`, `before-pr`, `post` (section 5). |
| **Standalone step** | A custom step at `pre` or `post`. It runs in its own Pod, as a `StepRun` (section 6). |
| **Workspace step** | A custom step at `after-update` or `before-pr`. It runs as a container in the environment's promotion Job, next to the environment's files (section 7). |
| **Promotion Job** | The Job that does an environment's git work outside the controller: the RenderRun Job. Today only `layout: branch` has one; with this spec, so does a `layout: directory` environment that has a workspace step (section 7.1). |
| **Revision** | A hash of everything about a template that changes what runs (section 10). |
| **Outputs** | The JSON object a step reports, validated against the template's output schema. |

## 3. StepTemplate

### 3.1 What v0.12 already defines

19-verdicts §8.2 defines `StepTemplate` with `output: Verdict` as its only value, run by
`AgentRun`. The implementation in review (#1870, `api/v1alpha1/steptemplate_types.go` on that
branch, not on main) has `description`, `output`, `image`, `command`, `args`, `env`, `secretEnv`,
`agent` (an AgentIdentity, required), `timeout`, `resources`, `egress` and `runtimeClassName`, and
gives each run a ServiceAccount of its own (`kr-<run>`) bound only to chart roles. This spec keeps
every one of those fields and their meaning, so v0.12 registrations do not migrate. The one
change: `agent` becomes optional, with a CRD rule that keeps it required for `output: Verdict`.

### 3.2 Shape

```yaml
apiVersion: kardinal.io/v1alpha1
kind: StepTemplate
metadata:
  name: manifest-scan
spec:
  description: Scans the environment's manifests and reports policy findings.
  output: Outputs                      # Verdict (v0.12, AgentRun) | Outputs (v0.14, custom steps)
  hookPoints: [before-pr, post]        # where Pipelines may use it; Outputs only
  image: ghcr.io/acme/manifest-scan@sha256:9b2e...   # a digest is required for Outputs (3.4)
  imageVerification:                   # optional; required by --step-template-require-signature
    authorities:                       # the ImageVerification SignatureAuthority type
      - name: acme-release
        keyless:
          issuer: https://token.actions.githubusercontent.com
          subject: https://github.com/acme/manifest-scan/.github/workflows/release.yml@refs/heads/main
  command: ["/scan"]
  args: ["--format", "json"]
  env:                                 # literal only; KARDINAL_* is reserved
    - {name: SEVERITY_FLOOR, value: medium}
  secretEnv: []                        # standalone steps only (9.2)
  agent: ""                            # optional for Outputs: the AgentIdentity the step acts for (12)
  kubernetesAccess: none               # none | read-target; standalone steps only (9.2)
  timeout: 5m                          # default 10m, at most 2h for Outputs
  resources:
    requests: {cpu: 100m, memory: 128Mi}
    limits: {cpu: "1", memory: 512Mi}  # limits required for Outputs (9.4)
  egress: []                           # standalone steps only (9.3)
  runtimeClassName: ""                 # optional: gVisor or Kata
  workspace:                           # workspace steps only (7)
    access: read                       # read | write; write only at after-update
    paths: ["**/*.yaml"]               # optional narrowing, relative to the environment path
  inputs:                              # OpenAPI v3 structural schema of the inputs object
    type: object
    properties:
      policySet: {type: string, enum: [baseline, strict], default: baseline}
    additionalProperties: false
  outputs:                             # OpenAPI v3 structural schema of the outputs object
    type: object
    required: [findings, highest]
    properties:
      findings: {type: integer, minimum: 0}
      highest: {type: string, enum: [none, low, medium, high, critical]}
      reportURL: {type: string, maxLength: 2048, pattern: '^https://'}
    additionalProperties: false
  scope:                               # who may use it (11)
    namespaces: ["payments-*", checkout]
    pipelines: []                      # empty: any
    environments: []                   # empty: any
status:
  ready: true
  reason: ""
  observedGeneration: 4
  revision: 7f3c2a91                   # of the current spec (10)
  revisions:                           # the last 10, newest first (10)
    - {revision: 7f3c2a91, image: ghcr.io/acme/manifest-scan@sha256:9b2e..., since: "2026-10-14T09:00:00Z",
       signature: {verified: true, authority: acme-release}}
```

Field rules, checked by CRD CEL rules where they need no lookup and by the StepTemplate
reconciler otherwise (`status.ready=false` with the reason):

- `output: Outputs` requires `hookPoints`, `outputs` and `resources.limits`; `output: Verdict`
  refuses `hookPoints`, `inputs`, `outputs` and `workspace` (a verifier is placed by a verify
  gate, 19-verdicts §8.3, never by `customSteps`).
- A template with a workspace hook point (`after-update`, `before-pr`) refuses `secretEnv`,
  `egress` and `kubernetesAccess: read-target`: the container runs in the promotion
  Job's Pod, which holds git credentials in other containers and shares one network (9.3).
  A template may list both a workspace and a standalone hook point; then those fields must be
  empty everywhere. Admins register two templates when they need both.
- `agent`, for `output: Outputs`, is optional. When set, the AgentIdentity must be ready, not
  suspended, and in scope for each run (#1870's `agentProblem` check), and the step's
  AuditEvents carry its actor stamp (17-actors §7). It gives the Pod no identity: a step's only
  Kubernetes credential is the one `kubernetesAccess` grants (9.2).
- `workspace.access: write` requires `hookPoints` to include `after-update` and only workspace
  points; `before-pr` always mounts the workspace read-only (5.3).
- `inputs` and `outputs` must be structural schemas (the apiextensions rules: every node typed,
  no `x-kubernetes-preserve-unknown-fields`, no `x-kubernetes-validations`, no `$ref`), at most
  32 KiB each. The reconciler checks this with the same validation the API server applies to a
  CRD's schema. Why OpenAPI and not JSON Schema: decision S6.

### 3.3 The StepTemplate reconciler

`pkg/reconciler/steptemplate` writes only the StepTemplate's status. #1870 has the v0.12 checks
in `TemplateProblem` (`pkg/reconciler/agentrun/reconciler.go` on that branch); this adds:

1. the field rules of 3.2 and schema structurality;
2. the image digest (3.4) and, when `imageVerification` is set, the signature (3.4);
3. the revision (section 10): `status.revision` is a hash of the runnable spec; when it changes,
   the new revision is prepended to `status.revisions` (at most 10 entries) with its image and
   signature result.

It calls the registry only for the signature, once per (revision, digest): a digest is
immutable, so a verified signature stays verified until the template changes. It does not
re-verify on a timer. Key revocation is handled by changing the template (section 10.3).

### 3.4 Image trust

- **Digest.** `output: Outputs` requires `image` to be `<repository>@sha256:<64 hex>` (CRD
  rule). #1870's `--step-template-require-digest` keeps governing `output: Verdict`.
- **Signature.** `imageVerification.authorities` uses the `SignatureAuthority` type of
  `api/v1alpha1/imageverification_types.go` (a PEM key in a Secret, or a keyless Fulcio
  identity), verified by the same code the ImageVerification reconciler uses
  (`pkg/reconciler/imageverification/verify.go:94` `verifyImage`, `registry.go:101`
  `Signatures`). Key Secrets of a cluster-scoped template live in the controller's namespace.
  With the controller flag `--step-template-require-signature`, a template without
  `imageVerification` is not ready.
- **Pull.** The kubelet pulls by digest, so what was verified is what runs.

What this does not do: verify attestations of the image (v0.13's attestation work can be applied
to templates later), or stop an admin from registering a bad image. The template is the trust
anchor; the controller proves only that the digest is the one an authority signed.

## 4. Using a template in a Pipeline

```yaml
spec:
  environments:
    - name: uat
      customSteps:
        - name: scan                       # DNS label, unique in the environment
          template: manifest-scan
          at: before-pr                    # one of the template's hookPoints
          inputs: {policySet: strict}      # literal, validated against the inputs schema
          onFailure: fail                  # fail (default) | continue
          timeout: 3m                      # optional; may only lower the template's
        - name: load
          template: k6-load
          at: post
          inputsFrom:                      # an input taken from an earlier step's output (8.3)
            - {input: baseURL, step: smoke, output: url}
    - name: prod
      customSteps:
        - name: scan
          template: manifest-scan
          at: before-pr
```

- **Field name.** `customSteps`, not `steps`: decision S16.
- **At most 10** custom steps per environment (`MaxItems`), and at most 4 workspace steps.
- **`inputs`** are literal JSON values. The translator validates them against the template's
  `inputs` schema when it builds the Graph, with defaults applied; a mismatch fails the Bundle
  with the schema error, and `kardinal validate` reports the same.
- **`inputsFrom`** maps an input to an output of an earlier custom step: in the same
  environment (an earlier hook point, or the same standalone point), or in an upstream
  environment (`environment: <name>`). The translator checks that the output exists in the
  source template's `outputs` schema and that its schema is assignable to the input's (the same
  type, and at least as narrow). There is no expression language in the Pipeline: decision S10.
- **`onFailure: continue`** records the failure, gives the step no outputs, and lets the
  promotion go on. A later step or a gate that reads its outputs then sees the step as `Failed`
  (8.4). Default `fail`.
- **Not allowed** at workspace points with `update.strategy: argocd`: that strategy does no git
  work, so there is no workspace (`pkg/steps/defaults.go:68-75`).

`kardinal validate` and the Pipeline's `Ready=False/ValidationFailed` report a template that does
not exist, is not ready, is out of scope for the Pipeline (section 11), does not list the hook
point, or whose schemas reject the inputs.

## 5. Hook points, the step engine and the state machine

### 5.1 Where each point is

The PromotionStep state machine (`api/v1alpha1/promotionstep_types.go:305-311`, the `State` enum) and
the step sequence (`pkg/steps/defaults.go:68` `DefaultSequenceForBundle`) are unchanged in shape.
Each hook point sits on a boundary that already exists:

| Point | When | Runs as | Waits on it | Sees |
|-------|------|---------|-------------|------|
| `pre` | The environment could start: upstreams Verified, gates ready, the Bundle not Superseded or Rejected. The same moment a pre hook runs (`pkg/graph/hooks.go:31-34`). | StepRun (own Pod) | The step stays `Pending` until each pre StepRun is terminal, like `spec.preHooks` | Context and inputs |
| `after-update` | In the promotion Job, after the image or config update, before the commit | Container in the promotion Job | The promotion Job, so the `render` step (`Promoting`) | The environment's files, writable (5.3) |
| `before-pr` | In the promotion Job, after the commit, before the push (and so before the PR) | Container in the promotion Job | The same | The committed files, read-only |
| `post` | The health check passed: the step entered `Verifying` (`status.verificationStartedAt`) | StepRun (own Pod) | The step is `Verified` only when each post StepRun succeeded, like `spec.postHooks` | Context, inputs, and with `kubernetesAccess: read-target` the target namespace |

`before-pr` runs before the push in both approval modes. In an `auto` environment there is no PR,
but the point is the same: the last moment the change can be stopped before it leaves kardinal.
A step that exits non-zero there fails the promotion Job, and nothing is pushed.

### 5.2 pre and post: the existing hook waits

The PromotionStep reconciler already waits for pre and post HookRuns through Graph-written fields
it reads, never the HookRuns themselves: `spec.preHooks`/`spec.postHooks` and their results in
`spec.live.hooks`, which a mirror `patch` node writes (`pkg/graph/hooks.go:20-47`; ledger G14).
Standalone steps use the same mechanism with new fields:

- `spec.preSteps`, `spec.postSteps`: the StepRun names of the environment, set by the Graph;
- `spec.live.steps []LiveStepRun{name, step, phase, message, outputs}`: mirrored from the StepRuns
  by the same mirror node that writes `spec.live.hooks`.

The state machine gains no state. `Pending` waits on pre hooks and pre StepRuns; `Verifying` waits
on post hooks, analyses and post StepRuns. A pre StepRun that fails (with `onFailure: fail`) fails
the step, as a failed pre hook does; a post StepRun that fails applies `onHealthFailure`, as a
failed post hook does.

The reconciler records each terminal StepRun it read in its own status,
`status.stepRecords []StepRecord{name, revision, phase, message, outputs}`, the HookRun pattern
(`status.hookRecords`, `promotionstep_types.go:482-485`), so a re-created StepRun can take its
recorded result (6.4).

### 5.3 after-update and before-pr: inside the promotion Job

A workspace step needs the environment's files. A git working tree cannot cross a reconcile
boundary (ledger G8, the git and SCM row): it lives in one process's filesystem. For `layout:
branch` that process is already the RenderRun Job: `pkg/steps/defaults.go:77-86` gives such an
environment the sequence `render, [open-pr, wait-for-merge,] health-check`, and the render Job
runs `git-clone, <update>, render-manifests, git-commit, git-push`
(`pkg/steps/defaults.go:34-48` `RenderJobSequence`). For `layout: directory` the working tree is
in the controller (`status.workDir`, `promotionstep_types.go:358-363`), and custom code may not run there.

So an environment with a workspace step gets a promotion Job in both layouts (section 7): for
`layout: directory`, a RenderRun in `mode: edit`. Its PromotionStep runs the same sequence as a
`layout: branch` environment: the `render` step asks for the RenderRun, waits for it, and checks
its report against the remote (`pkg/steps/steps/render_run.go:52-63` and `:104`). The step engine
gets no new step type, and the controller does no git work for that environment.

The workspace points have exact places in the Job's sequence:

```
layout: directory (mode: edit)                layout: branch (mode: render)
  git-clone                                     git-clone (DRY source and rendered branch)
  <update> (kustomize/helm/yaml/config-merge)   <update> in the DRY checkout (never committed)
  after-update steps   (files of env path)      after-update steps   (DRY files of env path)
  git-commit                                    render-manifests, git-commit
  before-pr steps      (committed files)        before-pr steps      (rendered files)
  git-push                                      git-push
```

In `layout: branch`, an `after-update` edit lands in the DRY checkout before rendering, which is
never committed (`defaults.go:30-33`), so it reaches the rendered branch only through the render:
the render marker (`RenderRunResult.MarkerDigest`) covers it, and drift detection keeps working.

### 5.4 Ordering

- **Workspace steps** of one point run one after another, in list order (they are sequential
  init containers, 7.2). A later workspace step sees the earlier ones' edits and, in
  `/kardinal/steps.json`, their outputs.
- **Standalone steps** of one point run concurrently. An `inputsFrom` edge orders two of them:
  the Graph admits a StepRun only when every StepRun it takes inputs from is Succeeded (6.2).
- **Hooks and steps.** Pre hooks and pre steps are independent of each other: both start at the
  same moment, and the step waits for both. Post likewise.
- **Across points**: `pre` < `after-update` < `before-pr` < `post`, by construction.

Decision S15 records why workspace steps are sequential and standalone steps are not.

## 6. StepRun: pre and post steps

### 6.1 The object

Namespaced (`stepruns.kardinal.io`, short name `sr`, categories `kardinal-all`), in the Bundle's
namespace, created only by the Bundle's Graph (the `graph-objects` admission policy,
`chart/kardinal-promoter/templates/identity-admission.yaml:370`, gains `stepruns`; ledger G17).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: StepRun
metadata:
  name: checkout-1-5-0-uat-post-load-3f9a1c     # boundedName, at most 52 characters (6.3)
  namespace: payments
  labels: {kardinal.io/pipeline: checkout, kardinal.io/bundle: checkout-1-5-0,
           kardinal.io/bundle-uid: 1b2c..., kardinal.io/environment: uat,
           kardinal.io/step: load, kardinal.io/step-at: post}
spec:
  pipelineName: checkout
  bundleName: checkout-1-5-0
  bundleUID: 1b2c...
  version: 1.5.0
  environment: uat
  step: load                         # the custom step's name in the Pipeline
  at: post
  template:                          # the snapshot of section 10, verified against the template
    name: k6-load
    revision: 7f3c2a91
    image: ghcr.io/acme/k6-load@sha256:...
    command: [...]
    args: [...]
    env: [...]
    secretEnv: [...]
    kubernetesAccess: read-target
    timeout: 20m                     # the lower of the template's and the Pipeline's
    resources: {...}
    egress: [...]
    runtimeClassName: ""
    inputsSchemaHash: 41c0...
    outputsSchemaHash: 9e7d...
  inputs: {vus: 50}                  # literal inputs, defaults applied
  inputsFrom:                        # literal mapping (4)
    - {input: baseURL, run: checkout-1-5-0-uat-pre-smoke-8c21d0, output: url}
  upstreamOutputs:                   # Graph-rendered: outputs of the runs named in inputsFrom (6.2)
    - {run: checkout-1-5-0-uat-pre-smoke-8c21d0, outputs: {url: "https://checkout.uat.example.com"}}
  targetNamespace: checkout-uat      # with read-target: the environment's workload namespace
  onFailure: fail
  stepAdvanced: false                # Graph-rendered, as HookRun's (6.4)
  recorded: {}                       # Graph-rendered from the step's status.stepRecords (6.4)
status:
  phase: Succeeded                   # Pending | Running | Succeeded | Failed | Skipped; last three terminal
  message: "outputs: p99Ms=212, errorRate=0.001"
  jobName: checkout-1-5-0-uat-post-load-3f9a1c
  jobUID: ...
  specHash: ...
  startedAt, deadline, finishedAt: ...
  inputs: {vus: 50, baseURL: "https://checkout.uat.example.com"}   # what the Pod got, validated
  outputs: {p99Ms: 212, errorRate: 0.001}                          # validated (8.1); {} when none
  outputsDigest: sha256:...          # of the canonical JSON of outputs, for receipts
  serviceAccount: ksr-<run>          # with read-target only
  conditions: [...]                  # Bounded, OutputsValid, SpecChangedAfterStart
```

`status.phase` has the HookRun and RenderRun terminal rule
(`renderrun_types.go:149`: a finished phase cannot change). `status.outputs` and `status.inputs`
are `x-kubernetes-preserve-unknown-fields` objects, so kro types them as `dyn`
(`pkg/cel/types.go:228-238` in kro): a reference like `outputs.p99Ms` compiles whatever the
template's schema is. That has a cost, recorded as ledger G26 (8.3).

### 6.2 The Graph: data, admission, collection

The same shape as the compact Graph's RenderRuns (`pkg/graph/compact_render.go:13-30`) and
HookRuns (`pkg/graph/compact_extras.go`), and as 19-verdicts §8.3's AgentRuns:

- `StepRunData` (a `def`): every StepRun of the Bundle as an item: `{name, environment, step,
  at, stepName, from: [run names of inputsFrom], spec}`, where `stepName` is the environment's
  PromotionStep name. Template snapshots are not copied into each item: a `StepTemplates` `def`
  holds each snapshot once, keyed by `<template>/<revision>`, and items carry the key (6.5).
- `refStepRuns` (a selector `ref` on `kardinal.io/bundle-uid`) reads the StepRuns back.
  `NodeStepsObserved` (compact) or `refSteps` (node shape) reads the steps back, as today.
- `PromotionStepRuns` (a `def`) keeps an item when its StepRun exists, or when all of:
  - the Bundle is not Superseded or Rejected;
  - for `pre`: the environment's step could be created (the admission the pre hooks use);
    for `post`: the step has `status.verificationStartedAt`;
  - every run in `from` is `Succeeded` in `refStepRuns`.
- `StepRuns` (collections of at most `MaxCollectionItems`, `pkg/graph/gates.go:209`) create one
  StepRun per admitted item. The template renders:

```cel
spec.template:        ${StepTemplates[Item.templateKey]}
spec.upstreamOutputs: ${refStepRuns.filter(r,
                          r.metadata.?labels[?"kardinal.io/bundle-uid"].orValue("") == "1b2c..." &&
                          r.metadata.name in Item.from &&
                          r.?status.?phase.orValue("") == "Succeeded")
                        .map(r, {"run": r.metadata.name, "outputs": r.status.outputs})}
spec.stepAdvanced:    ${<the step's state is past the point>}
spec.recorded:        ${<the step's status.stepRecords entry for Item.name, or {}>}
```

`readyWhen` on the collection is the terminal phase, for the UI; nothing waits on kro readiness
(ledger G1): the step waits on `spec.live.steps`, gates on their copy (8.4).

This is `${stepRun.status.outputs.x}` made concrete: a later node never names a StepRun node by
ID (they are collection items); it filters `refStepRuns` by name, which the translator knows,
and reads `status.outputs`. The StepRun reconciler builds the Pod's inputs from `spec.inputs`
plus each `inputsFrom` entry looked up in `spec.upstreamOutputs`: its own spec, which the Graph
wrote. It reads no other StepRun. The admission rule guarantees `from` runs are `Succeeded`,
and a Succeeded StepRun always has `status.outputs` (at least `{}`), so the `map` never reads
a missing field (G14 does not freeze the node).

Checked against kro v0.10.0-rc.0: a selector ref with no match is an empty, ready list, and a new
or changed StepRun re-renders its readers (`executor/simple.go:1235-1301`, `applyRefCollection`,
which registers the watch before the list); `def` over selector-ref data and a collection over
the `def` is what the compact shape's HookRuns and RenderRuns do on main. An item stays admitted
once its object exists, so a later change never prunes a run in flight (G11).

The node shape gives each StepRun its own node, gated by `resolvableWhen` like a HookRun node
(`pkg/graph/hooks.go:30-35`), with the same rendered fields.

### 6.3 The StepRun reconciler

`pkg/reconciler/steprun` writes only the StepRun's status, and creates children it owns with a
controller ownerReference: a NetworkPolicy, a Job and, with `kubernetesAccess: read-target`, a
ServiceAccount and one RoleBinding. It shares the Job lifecycle with HookRun and AgentRun
(create with an owner, adopt our own Job, observe, latch, deadline, delete on timeout with
background propagation; 19-verdicts §8.1 moves it into one package, `pkg/jobrun`).

```
no status.phase                → Pending; check bounds (9); verify the snapshot (10.2)
Pending, Skipped by the Graph   → stepAdvanced or Bundle halted before start → Skipped
Pending, recorded set           → take the recorded result (6.4)
Pending                         → compose and validate inputs (8.2); NetworkPolicy; [SA, RoleBinding]; Job
Running                         → observe the Job; on deadline delete it (Background) → Failed
Job Complete                    → read the step container's termination message; validate (8.1) → Succeeded or Failed
Job Failed                      → Failed with the reason (exit code, OOMKilled, DeadlineExceeded, image pull)
terminal                        → delete the SA and RoleBinding (if any); keep only SpecChangedAfterStart current
```

- **Names.** StepRun and Job names are `boundedName` (`pkg/graph/names.go:41`) of
  `<pipeline>-<bundle>-<env>-<at>-<step>`, at most 52 characters, so the Pod name keeps the full
  Job name (the Job controller cuts `generateName` to 58; 19-verdicts §4.2).
- **The Pod is built by the controller**, never from user YAML: section 9.
- **Idempotent.** Every write is a status patch with an optimistic lock on the base; a second
  reconcile of a terminal StepRun writes nothing; a Job of the run's name with another UID, or a
  missing Job while Running, fails the run (it is not run again), as HookRun and RenderRun do
  (`pkg/reconciler/renderrun/reconciler.go:342`, `job`).

### 6.4 Run once, and kro G12

Ledger G12: kro deletes and prunes without a propagation policy, so a raw Job node orphans its
Pods; it re-creates a deleted Job, which runs it again; and an edited Job template is an
immutable-field error that stops prune for the whole Graph
(`executor/simple.go:530-555`, `Delete` sends a UID precondition and no `propagationPolicy`).
StepRun avoids each:

| G12 effect | StepRun |
|------------|---------|
| Pods orphaned on delete or prune | kro deletes the StepRun (a custom resource); the Job, NetworkPolicy, ServiceAccount and RoleBinding have it as controller owner, so the garbage collector deletes them and the Job's Pods. Verified for HookRun on kind (ledger G12, `TestStep_Hook*`); the StepRun live test repeats it (13.3). The reconciler's own deadline deletion uses `PropagationPolicy: Background` (`renderrun/reconciler.go:612-613`). |
| A deleted Job runs again | The StepRun, not kro, creates the Job, and a missing Job while Running fails the run. |
| A deleted StepRun is re-created by kro and would run again | The PromotionStep recorded the run's result in `status.stepRecords` (5.2); the Graph renders it into the re-created StepRun's `spec.recorded`; a StepRun that starts with a record of its own revision and inputs takes it (phase and outputs) without a Job. The HookRun `spec.recorded` pattern (`pkg/reconciler/hookrun/reconciler.go:365`). A run deleted while Running and not yet recorded is recorded as `Failed` ("deleted while running; result unknown"). |
| An edited Job template stops the Graph | kro never applies a Job. A StepRun spec change after the Job started is not applied (condition `SpecChangedAfterStart`, as HookRun). |

Workspace steps run in the RenderRun Job, which already has all of these properties
(`renderrun/reconciler.go:510` controller owner; the phase rule; `status.jobUID`).

### 6.5 Size

A Graph is one etcd object, and kardinal refuses one estimated over 1.2 MB (ledger G10,
`pkg/graph/size.go`). A template snapshot is 0.5 to 2 KB. Copied into every item, 3 steps on a
200-target fleet would add up to 1.2 MB. So snapshots live once per `<template>/<revision>` in the
`StepTemplates` def, and an item is about 300 bytes: 600 items add about 180 KB. `CheckSize`
counts it as it counts everything else.

## 7. Workspace steps: the promotion Job

### 7.1 Which Job

| Environment | Promotion Job | Change |
|-------------|---------------|--------|
| `layout: branch` | The RenderRun Job, as today | `spec.steps` lists the workspace steps |
| `layout: directory`, no workspace step | None: the controller does the git work, as today | None |
| `layout: directory`, a workspace step | A RenderRun with `spec.mode: edit` | New: the environment's git work moves to the Job |

`mode: edit` runs `git-clone, <update>, [after-update], git-commit, [before-pr], git-push` in the
Job, with the push to the base branch (`auto`) or the promotion branch (`pr-review`), and the
same rebase-and-retry onto a moving base branch the controller uses (the git-push step code in
`pkg/steps/steps`, which the render Job already imports). Why extend RenderRun rather than add a
kind: decision S4.

Only environments that use a workspace step change. Every other environment keeps the in-process
sequence, so the cost (a Pod per promotion of that environment) is paid only where it buys
something.

### 7.2 The Pod

The RenderRun reconciler builds it (`renderrun/reconciler.go:383-466`, `JobSpec`). With workspace
steps it becomes a sequence of init containers and one main container. Init containers run one at
a time, each only after the previous one exited 0, so the phases cannot overlap:

| # | Container | Image | Mounts | Does |
|---|-----------|-------|--------|------|
| 1 | `prepare` | kardinal-render | `git` Secret (ro), `repo` (rw), `workspace` (rw) | Clone, update; copy the environment path's files, without `.git`, into `workspace` |
| 2..k | `step-<name>` (after-update) | template image | `workspace` (rw, or ro for `access: read`), `context` (ro), `tmp` | The custom step |
| k+1 | `commit` | kardinal-render | `repo` (rw), `workspace` (ro) | Diff `workspace` against `repo`, apply the allowed changes (7.3), render (branch), commit; copy the committed files into `committed` |
| k+2.. | `step-<name>` (before-pr) | template image | `committed` (ro), `context` (ro), `tmp` | The custom step |
| main | `render` | kardinal-render | `git` Secret (ro), `repo` (rw) | Push; report the result in its termination message |

- The `git` Secret volume is mounted only in `prepare` and `render`. A Secret volume is per
  container mount, so a step container cannot read it. The private clone (`repo`, with `.git`)
  is never mounted in a step container: a step cannot change the remote URL, the refs, git
  config or hooks that the trusted push uses. (The render code uses go-git, which runs no git
  hooks.)
- **Context.** `/kardinal/context.json` (a downward-API file of a Pod annotation, as 19-verdicts
  §8.5) and environment variables: `KARDINAL_PIPELINE`, `_BUNDLE`, `_BUNDLE_UID`, `_VERSION`,
  `_ENVIRONMENT`, `_STEP`, `_AT`, `_IMAGES`, `_INPUTS` (the validated inputs as JSON),
  `_WORKSPACE` (`/workspace`), `_DEADLINE`; and `/kardinal/steps.json`, the outputs of the earlier
  workspace steps in this Pod (written by `commit` for `before-pr` steps; by `prepare`, empty,
  for the first). Inputs of a workspace step that come from a `pre` StepRun are rendered by the
  Graph into the RenderRun's `spec.steps[].upstreamOutputs`, as for StepRuns (6.2).
- **Outputs.** Each step container writes its outputs JSON to its termination message
  (`terminationMessagePath: /dev/termination-log`, `TerminationMessagePolicy: File`). The
  reconciler reads them from the Pod's container statuses, as it reads the render container's
  today (`renderrun/reconciler.go:651-688`, `podResult`), from Pods the Job controls only
  (`jobPods`).
- **Failure.** `backoffLimit` is 0 when the RenderRun has steps: a custom step is not assumed
  idempotent, and a retried Pod would run it again. A step that exits non-zero, a step's
  OOMKilled, and an outputs document that fails validation (checked by `commit` for the next
  step, and by the reconciler for the record) fail the RenderRun with the step named:
  `step scan (manifest-scan@7f3c2a91) exited 3: ...`. A Pod does not go on past an init
  container that failed, so **`onFailure: continue` is refused for workspace steps** (a build
  error); a step that wants to be advisory exits 0 and reports its finding in its outputs.
- **Deadline.** `activeDeadlineSeconds` is the render timeout plus the sum of the workspace
  steps' timeouts. Kubernetes has no per-init-container deadline, so a step's timeout is a share
  of the Job's, not a separate kill: a hung step uses the whole remaining budget and the run
  fails as `step scan did not finish within the Job's deadline` (the reconciler names the
  container that was running).
- **Resources.** Each step container gets its template's resources (requests and limits
  required). The Pod's effective request is the larger of the largest init container and the
  main container, so sequential steps do not add up. The `workspace`, `committed` and `tmp`
  emptyDirs have size limits (256 MiB, 256 MiB, 64 MiB; the existing `work` is 1 GiB).

### 7.3 What a write step may change

`commit` treats `workspace` as untrusted data. It applies a change to `repo` only when:

- the path is under the environment's `path` (directory) or its DRY `path` (branch), and matches
  `workspace.paths` when the template sets them;
- it is a regular file or a directory: a symlink, device, FIFO or socket fails the run;
- it is not under `.kardinal/` (the render marker and kardinal's own files) and not `.git*`;
- the total size of changed files is at most 10 MiB, and at most 500 files change.

Anything else fails the RenderRun: `step gen (codegen@...) changed ../../.github/workflows/ci.yml,
outside environments/uat`. The commit message gains a trailer per write step,
`Kardinal-Step: gen codegen@sha256:9b2e...`, and the RenderRun result lists the changed paths per
step (at most 50 names, then a count), so the PR evidence can show them (section 8.5).

### 7.4 RenderRun API additions

```go
// RenderRunSpec gains:
// Mode is render (layout: branch, the default) or edit (layout: directory
// with workspace steps: no render-manifests, push to spec.git.renderedBranch,
// which is then the base branch).
// +kubebuilder:validation:Enum=render;edit
Mode string `json:"mode,omitempty"`
// Steps are the environment's workspace steps, in run order.
// +kubebuilder:validation:MaxItems=4
Steps []RenderRunStep `json:"steps,omitempty"`

type RenderRunStep struct {
    Name            string                 `json:"name"`
    At              string                 `json:"at"`        // after-update | before-pr
    Template        StepTemplateSnapshot   `json:"template"`  // section 10
    Inputs          runtime.RawExtension   `json:"inputs,omitempty"`
    InputsFrom      []StepInputFrom        `json:"inputsFrom,omitempty"`
    UpstreamOutputs []StepUpstreamOutputs  `json:"upstreamOutputs,omitempty"` // Graph-rendered
}

// RenderRunStatus gains:
Steps []RenderRunStepResult `json:"steps,omitempty"`

type RenderRunStepResult struct {
    Name          string               `json:"name"`
    Phase         string               `json:"phase"`             // Succeeded | Failed | NotRun
    Message       string               `json:"message,omitempty"`
    Outputs       runtime.RawExtension `json:"outputs,omitempty"` // validated; preserve-unknown
    OutputsDigest string               `json:"outputsDigest,omitempty"`
    ChangedPaths  []string             `json:"changedPaths,omitempty"` // write steps
    ChangedCount  int                  `json:"changedCount,omitempty"`
}
```

`LiveRenderRun` on the PromotionStep (`promotionstep_types.go:153-170`) gains `steps`, mirrored
by the existing `spec.live.renders` template. The `render` step copies them into its outputs for
open-pr and the evidence; nothing in the controller decides on them except open-pr's PR body.

Why workspace step results are in the RenderRun's status and not in StepRun objects: decision S3.
The CEL variable and Graph references of section 8 read both sources the same way.

### 7.5 Rebuilding a PR on a moved base

The controller rebuilds a `pr-review` environment's PR branch when its base branch moves
(`pkg/reconciler/promotionstep/pr_rebase.go`, #1504, #1640), by running the update again. For an
environment whose git work is in a promotion Job, a rebuild is a new RenderRun attempt (the name
gains `-a<N>` for N > 1), which runs the workspace steps again. A custom step need not be
deterministic, and a reviewer approved what the PR showed. So the rebuild compares the new tree
of the environment path with the PR's: when a workspace step made it differ (beyond the update
kardinal itself re-applies), the rebuild is refused and the step says so: `rebuilding PR #42 on
the moved base: step gen produced different files than the reviewed PR; ship the version again
to review the new change`. Decision S17.

## 8. Outputs

### 8.1 Validation

The step's outputs document must be a JSON object, at most 4 KiB of termination message (the
kubelet truncates a message over 4096 bytes, and all containers of a Pod together over 12 KiB:
`k8s.io/api@v0.37.1/core/v1/types.go:3279-3280`). A workspace Pod therefore has at most 4 steps
(sections 4 and 7.4). Their messages and the render result share the Pod's 12 KiB. The render
result needs about 1 KiB, so four steps that each use the full 4 KiB do not fit: the kubelet
truncates, the JSON does not parse, and the run fails with `step scan: outputs truncated`. The
user page says to keep workspace step outputs under 2.5 KiB.

The reconciler (StepRun or RenderRun) validates it with the apiextensions schema validator
(`k8s.io/apiextensions-apiserver@v0.37.1/pkg/apiserver/validation/validation.go:96`,
`NewSchemaValidator`, and `ValidateCustomResource` at `:152`), the same code the API server uses
for a CRD, in process, against the snapshot's `outputs` schema. Defaults are applied first. The
package is already a direct dependency (`go.mod`), so no new module.

- Valid: `status.outputs` holds the defaulted document, `outputsDigest` its canonical sha256,
  condition `OutputsValid=True`, phase `Succeeded`.
- Invalid, truncated, not JSON, or missing: `status.outputs: {}`, `OutputsValid=False` with the
  first five schema errors, phase `Failed` (or `Succeeded` with no outputs under `onFailure:
  continue`, which the outputs consumers see as `phase == "Failed"` on the step entry, 8.4).
  A run whose outputs are not valid never hands them to anyone.

Outputs are the image's claims. Validation proves their shape, not their truth.

### 8.2 Inputs

Inputs reach the step as `KARDINAL_INPUTS` and in the context file. The reconciler composes
`spec.inputs` and the `inputsFrom` values from `spec.upstreamOutputs`, applies the `inputs`
schema's defaults, validates, and writes the result to `status.inputs` before it creates the Job.
A composed document that fails validation fails the run (`Bounded=False`, the schema errors). The
translator already checked the literal part and the schema compatibility of each `inputsFrom`, so
this catches only an upstream output that its own schema allowed but the input schema does not
(for example a narrower `maxLength`). At most 16 KiB.

Inputs are not secret: they are in CR specs and status. A step that needs a credential gets it
through `secretEnv` (standalone steps only).

### 8.3 Graph references, and ledger G26

Later nodes read outputs from `refStepRuns` (6.2) or from the RenderRun read-back
(`refRenderRuns`, which the compact shape already has) by name, as above. kro types
`status.outputs` as `dyn` (6.1), so `r.status.outputs.p99Ms` compiles even when the template has
no `p99Ms`. At run time kro treats a missing map key as data not yet written: "no such key" is a
data-pending pattern (`pkg/graphengine/runtime/errors.go:37-49` in kro), and a node whose
expression is data pending waits, Unresolved, without an error
(`pkg/graphengine/runtime/node.go:360-368`). A typo in a reference, or an optional output a step
did not report, would leave a node waiting forever with no message.

kardinal therefore never relies on kro to find a bad output reference:

1. The translator resolves every `inputsFrom` and gate reference against the source template's
   `outputs` schema before it builds the Graph, and refuses an unknown output (`kardinal
   validate` reports it).
2. Expressions it generates read outputs only through the whole `outputs` object (copied as a
   value into the reader's spec), never a field path, so no generated expression can be data
   pending on an output key.
3. An optional output that is absent stays absent in the copy; the consumer (the StepRun
   reconciler composing inputs, the PolicyGate reconciler) reports "output reportURL was not
   reported by step scan" with a reason, instead of a silent wait.

This is a kro gap with a workaround, so it has a ledger entry:
[G26](16-graph-capability-ledger.md#g26-a-missing-key-in-an-untyped-field-reads-as-data-pending).

### 8.4 Gates: `steps.*`

**Mechanism.** The Approvals and Verdicts pattern (19-verdicts §5.1, decision V2): the Graph
copies the source environment's step results into each gate instance's spec, and the PolicyGate
reconciler turns that copy into a CEL variable. It reads no StepRun or RenderRun.

A gate that references `steps` (detected like `changewindow`:
`pkg/reconciler/policygate/reconciler.go:602`) reads one environment's steps:

```yaml
spec:
  expression: 'steps["load"].phase == "Succeeded" && steps["load"].outputs.p99Ms < 300'
  stepsFrom: uat        # default: the gated environment's only direct upstream;
                        # a build error when it has none or several
```

Each gate instance's `spec.steps` is rendered by the Graph from both sources:

```cel
refStepRuns.filter(r, <bundle-uid> && r.spec.environment == "uat" &&
                      r.?status.?phase.orValue("") in ["Succeeded", "Failed", "Skipped"])
  .map(r, {"name": r.spec.step, "at": r.spec.at, "phase": r.status.phase,
           "outputs": r.?status.?outputs.orValue({}),
           "template": r.spec.template.name, "revision": r.spec.template.revision})
+
refRenderRuns.filter(r, <bundle-uid> && r.spec.environment == "uat")
  .map(r, r.?status.?steps.orValue([]).map(s, {"name": s.name, "phase": s.phase,
           "outputs": s.?outputs.orValue({}), ...}))
  .flatten()
```

The PolicyGate CEL environment (`pkg/reconciler/policygate/cel_evaluator.go:106-115`) gains one
variable, `steps` (`DynType`), a map from step name to:

| Field | Type | Meaning |
|-------|------|---------|
| `steps.<name>.phase` | string | `Succeeded`, `Failed`, `Skipped`, `NotRun` |
| `steps.<name>.outputs.<key>` | per the template's schema | the validated outputs; `{}` unless Succeeded |
| `steps.<name>.at` | string | the hook point |
| `steps.<name>.template` | string | `<template>@<revision>` |

No new CEL function. A step that has not finished is absent, so `steps["load"]` is a CEL
"no such key" error and the gate is blocked, fails closed, with reason
`steps["load"]: step load of uat has not finished`. A gate on the source environment itself
cannot read its own `pre` steps (they start after its gates are ready): `stepsFrom` equal to the
gated environment is refused.

`stepsFrom` naming a fleet environment is refused in v0.14: a fleet has one StepRun per target,
and a single `steps.<name>` would have to pick one. Per-target step results are in the PR
evidence; a fleet-aware variable is a follow-up (section 15).

A gate on step outputs is not a verify gate: it has no quorum, no one-party rule and no voter
identity. When the decision needs those, use a Verdict (section 12).

### 8.5 Evidence

- **PR body.** A `### Custom steps` section after the gate table: per step, the name, the hook
  point, `template@revision`, the image digest, the phase, each output (values cut to 256
  characters) and, for write steps, the changed paths. Built by open-pr from `spec.live.steps`
  and the `render` step's outputs, which it already reads. The custom PR template gets
  `.CustomSteps`.
- **UI.** The stage drawer lists the environment's custom steps with phase, outputs and
  messages; the UI API returns them from the PromotionStep's live fields.
- **CLI.** `kardinal explain` prints them; `kardinal get stepruns` works through the printer
  columns (Pipeline, Env, Step, At, Phase, Age).
- **Audit.** `StepRunFinished` AuditEvents (template, revision, digest, phase, outputs digest), written by
  the PromotionStep reconciler's audit outbox when it records the step (5.2). The v0.13
  receipt includes each step's template revision, image digest and outputs digest.

## 9. The sandbox

### 9.1 The Pod, for every custom step

Built by the controller from the snapshot, never from user Pod YAML:

- Pod security `restricted`: non-root, `allowPrivilegeEscalation: false`, all capabilities
  dropped, `RuntimeDefault` seccomp, read-only root filesystem with an emptyDir `/tmp`; checked
  with the upstream Pod Security evaluator the HookRun reconciler uses
  (`pkg/reconciler/hookrun/podsecurity.go`) before the Job is created.
- `enableServiceLinks: false`, no host namespaces, no hostPath, no volumes other than those listed
  here, `restartPolicy: Never`, `backoffLimit: 0`.
- `runtimeClassName` from the template when set (gVisor or Kata).
- `automountServiceAccountToken: false`. A standalone step with `kubernetesAccess: read-target`
  gets a projected token of its own ServiceAccount at the standard path, with `expirationSeconds`
  covering the timeout. Every other step has no Kubernetes credential at all.

### 9.2 Credentials

| Credential | pre / post | after-update / before-pr |
|------------|------------|--------------------------|
| Pipeline git Secret | Never | Never (only `prepare` and `render` mount it) |
| SCM provider token, controller token | Never | Never |
| Kubernetes token | Only with `kubernetesAccess: read-target`: a ServiceAccount `ksr-<run>` that the reconciler creates, bound by one RoleBinding to the chart's step reader ClusterRole in `targetNamespace` (the AgentRun reader role of #1870: get, list, watch pods, logs, services, endpoints, events, workloads, endpointslices, ingresses; no Secrets, no ConfigMaps). Deleted when the run ends. | Never |
| `secretEnv` | Secrets in the Pipeline namespace labelled `kardinal.io/step-template: <template>` (#1870's rule). The reconciler also refuses a Secret that is the Pipeline's `spec.git.secretRef`, an SCM provider's, or the controller's own (`ControllerSecret.RefusesSecret`, `renderrun/reconciler.go:479`), even if labelled. | Refused (3.2) |

The controller binds only that one ClusterRole (the `_rbac.tpl` restriction #1870 adds for the
AgentRun roles), so it needs no `escalate` or `bind` beyond it.

### 9.3 Network

- **Standalone steps.** The reconciler creates a NetworkPolicy owned by the StepRun that selects
  the run's Pod: no ingress; egress to DNS, the template's `egress` CIDRs and ports, and, with
  `read-target`, the API server (the `default/kubernetes` EndpointSlice) and the Pods of
  `targetNamespace`. Nothing else: no cloud metadata address, no other namespace, no git host
  unless an admin lists it in `egress`. (19-verdicts §8.4 and #1870's AgentRun.)
- **Workspace steps** share the promotion Pod's network with `prepare` and `render`, which must
  reach the git host. A NetworkPolicy applies to a Pod, not a container, so the step container can
  reach the git host too. It holds no git credential, but an image that brings its own could push
  the environment's files to another repository on that host. The render code's own refusal of
  non-git traffic (`pkg/renderjob/network.go:26`, `ErrNetworkRefused`) is in-process and does
  not bind a foreign container. So:
  - a RenderRun with steps gets a NetworkPolicy of its own, owned by the RenderRun: DNS and the
    git host addresses the admin configured (`--workspace-step-git-egress`, chart
    `steps.workspaceGitEgress`), nothing else;
  - when that is not configured, workspace steps do not run: the RenderRun fails with
    `workspace steps need steps.workspaceGitEgress (the git host's addresses) so the step
    container cannot reach anything else`, unless the admin sets it to `unrestricted`, which
    the RenderRun status and the PR evidence then state;
  - the chart's render NetworkPolicy (`templates/render-networkpolicy.yaml`, off by default,
    `values.yaml:539-545`) is unchanged for runs without steps.
- A NetworkPolicy does nothing without a CNI that enforces it. kardinal cannot tell, so runs
  still start; the user page says so (19-verdicts §11).

### 9.4 Resources and time

- Requests and limits come from the template (limits required for `Outputs`); the controller
  caps them (`--step-max-cpu`, default 2; `--step-max-memory`, default 2Gi;
  `--step-max-ephemeral-storage`, default 1Gi). A template over a cap is not ready.
- Timeout: template default 10m, at most 2h; a Pipeline may lower it, not raise it. Standalone:
  `activeDeadlineSeconds` and the reconciler's deadline; workspace: a share of the Job deadline
  (7.2).
- Concurrency is bounded by Kubernetes, not by kardinal counting objects: admins set a
  ResourceQuota on `count/jobs.batch` or Pods in tenant namespaces. A Job create refused for quota
  leaves the run `Pending` with `waiting: namespace quota exceeded (count/jobs.batch)` and a
  backoff; it never fails it.

### 9.5 What a step may not touch

- The cluster, except reads of the target namespace with `read-target`. No writes anywhere.
- Git: no credentials; workspace writes only through `commit`'s filter (7.3); never `.git`,
  refs, the remote or kardinal's marker.
- Its own record: a step cannot write its StepRun, the RenderRun or any status (no token, or a
  token without those verbs). Its only output channel is its termination message.
- Other steps: only the inputs it was given and, for workspace steps, the outputs of earlier
  workspace steps in the same Pod.
- The controller: nothing a step does runs in, or is called by, the controller.

## 10. Versioning and upgrades of templates

### 10.1 Options

What runs when an admin changes a template while Bundles are in flight?

**(a) Live.** Each run reads the template when it starts (#1870's AgentRun does this).
- Pro: simplest; a fixed image applies at once.
- Con: the same Bundle's uat and prod steps can run different code; a run is not reproducible
  from its records; an admin edit changes in-flight promotions without anyone seeing it.

**(b) Snapshot at Graph build, verified at start.** The translator copies the template's runnable
spec into the Graph (a `StepTemplateSnapshot` with the revision); the run uses the snapshot; the
run reconciler starts it only when the snapshot's revision is one the template has in
`status.revisions` and is not revoked.
- Pro: a Bundle runs the revision it was built with, everywhere; records name the revision; an
  admin can still stop a bad revision (revoke); a forged snapshot does not run (10.2).
- Con: more fields in the Graph (6.5); a template change reaches a Bundle only when its Graph is
  rebuilt or a new Bundle is created.

**(c) Immutable templates, versions by name** (`manifest-scan-v2`).
- Pro: no ambiguity; plain names in records.
- Con: every upgrade edits every Pipeline; no way to stop an old version short of deleting it,
  which breaks in-flight Bundles with an error instead of a clear refusal.

**Decision: (b)** (S8).

### 10.2 The snapshot and its check

`StepTemplateSnapshot` holds `name`, `revision` and every field that changes what runs: image,
command, args, env, secretEnv, kubernetesAccess, timeout (after the Pipeline's lowering),
resources, egress, runtimeClassName, workspace, and the sha256 of the inputs and outputs
schemas (the schemas themselves are in the `StepTemplates` def, once).

`revision` is the first 8 bytes, hex, of the sha256 of the snapshot's canonical JSON without the
timeout lowering. At start the reconciler recomputes it from the snapshot it was given, and runs
only when:

1. it equals `spec.template.revision`;
2. the template exists, is ready, and has that revision in `status.revisions`;
3. the revision is not in the template's `spec.revokedRevisions`, and its image digest is not in
   `spec.revokedDigests`;
4. with `imageVerification`, that revision's signature is verified.

Otherwise the run is `Failed` with `Bounded=False`: `template manifest-scan revision 7f3c2a91
was revoked`. So a StepRun crafted by someone who can impersonate the Graph's identity (ledger
G16) cannot run an image the admin never registered: its snapshot would not match any revision.

### 10.3 What happens to in-flight Bundles

| Admin action | Not yet started | Running | Finished |
|--------------|-----------------|---------|----------|
| Edits the template (new revision) | Runs the revision of its Graph. A Graph rebuild (a Pipeline spec change, `ensurePipelineSpecCurrent`) re-snapshots runs that have not started. | Unaffected | Unaffected |
| Revokes a revision or digest | Fails, with the reason; the environment fails (`onFailure: fail`) | Unaffected (to stop it, delete the StepRun or reject the Bundle) | Unaffected; the evidence still names the revision |
| Keeps more than 10 revisions in use | The oldest drops out of `status.revisions` and its runs fail as "no longer registered" | | |
| Deletes the template | Fails: "template does not exist" | Unaffected | Unaffected |
| Changes `scope` to exclude a namespace | Fails, scope is checked at start too (11) | Unaffected | Unaffected |

`kardinal get steptemplates -o wide` shows each revision's use (the number of StepRuns and
RenderRun steps not yet terminal with it, counted by the CLI from a list), so an admin can see
what a revocation will stop.

To push a fix to in-flight Bundles at once, the admin revokes the old revision and the team
ships again (or `kardinal rollback`/re-ship). There is no "upgrade in flight" mode: it would be
option (a) under another name.

## 11. Tenancy: who may use which template

### 11.1 Options

**(a) Scope on the template.** `spec.scope.{namespaces,pipelines,environments}` (globs, the
AgentIdentity scope shape of 17-actors §3 and `actor.Matches`), written by the cluster admin who
owns the template.
- Pro: the boundary is on a cluster-scoped object only admins write; it is enforced where the
  run starts, whoever wrote the Pipeline.
- Con: coarse; it cannot say "alice may, bob may not" within a namespace.

**(b) An RBAC verb.** A `use` verb on `steptemplates`, checked by a ValidatingAdmissionPolicy on
Pipeline writes with the CEL authorizer:
`authorizer.group('kardinal.io').resource('steptemplates').name(t).check('use').allowed()`
(`k8s.io/apiserver@v0.37.1/pkg/cel/library/authz.go:145-170`).
- Pro: per user and group, with Kubernetes RBAC; checked at write time with a clear denial.
- Con: a Pipeline applied by a GitOps controller is checked against that controller's
  ServiceAccount, which usually holds wide rights, so it says little about the human; and it
  is only checked at write, not when a run starts.

**(c) Both.** (a) always; (b) as an opt-in admission check (chart
`steps.requireUseVerb: false` by default).

**Decision: (c)** (S9). (a) is the guarantee; (b) adds per-person control for clusters where
people write Pipelines directly.

### 11.2 Where scope is enforced

- **Translator** (Graph build): a Pipeline that names a template out of scope fails to build,
  with the reason (`kardinal validate` too).
- **Run start** (StepRun and RenderRun reconcilers): checked again, so a scope narrowed after
  the Graph was built still stops new runs (10.3).
- **Admission** (opt-in, (b)): the `<release>-step-templates` policy on Pipeline CREATE and
  UPDATE, for each template newly referenced in `customSteps`. A template already referenced
  before the update is not re-checked, so unrelated edits by a GitOps controller do not start
  failing when a person's grant is removed.

### 11.3 Namespaces and Secrets

A template is cluster-scoped; what it reads is per namespace. Its `secretEnv` Secrets are the
tenant's, in the Pipeline namespace, labelled for the template (9.2), so tenant A cannot hand the
template tenant B's Secret, and a template cannot read a Secret the tenant did not label for it.
StepRuns, their Jobs, Pods and NetworkPolicies are in the Bundle's namespace. A step with
`read-target` reads the environment's workload namespace, which the AgentIdentity-style scope does
not cover: the RoleBinding is created only when `targetNamespace` matches the template's
`scope.namespaces` too.

### 11.4 RBAC

- `steptemplates`: cluster admins write; the chart's viewer roles read (as #1870).
- `stepruns`: chart viewer roles `get, list, watch`; no user role creates, updates or deletes
  them (admission `graph-objects`, 6.1); the Graph ServiceAccount gains them in
  `templates/graph-rbac.yaml` (line 23 and 70 lists).
- The controller: `stepruns` and `stepruns/status`; `jobs`, `networkpolicies`,
  `serviceaccounts`, `rolebindings` in managed namespaces (it has them for AgentRun and render);
  `bind` on the step reader ClusterRole only.

## 12. AgentRun: one registration, two run kinds

AGENTIC-PLAN §5 says "AgentRun (3.3) is a StepTemplate whose output is a Verdict". With this spec
that holds for the registration: one `StepTemplate` kind, `output: Verdict` or `Outputs`. The
question is whether the run objects should merge.

### 12.1 Options

**(a) Keep AgentRun and StepRun separate; share the registration and the Job code.**
- Pro: each keeps semantics the other does not have. An AgentRun writes a Verdict with its own
  token, and admission binds that Verdict to the run through the Pod's name
  (19-verdicts §4.2); it has runs and attempts, is placed by a verify gate on a *downstream*
  environment, and is retried from an inconclusive Verdict by the Graph (19-verdicts §8.3, 9.1).
  A StepRun reports through its termination message with no token, is placed by a hook point in
  its own environment, and is never retried. Neither model leaks into the other. #1870 ships
  unchanged.
- Con: two kinds, two reconcilers over one `pkg/jobrun`; the bounds code (Pod builder,
  NetworkPolicy, per-run ServiceAccount) must stay shared deliberately.

**(b) Merge: AgentRun becomes a StepRun with `output: Verdict`.**
- Pro: one kind to learn and to grant.
- Con: every AgentRun rule becomes a branch inside StepRun (token or not, write a Verdict or a
  status, runs and attempts or not); the verdict admission's run binding would have to name
  StepRuns; and it changes #1870's API before it ships, or migrates it after.

**(c) StepRun produces Verdicts.** A `post` step's outputs are turned into a Verdict by the
controller.
- Con: the controller would write a Verdict on the image's behalf, so `spec.by` would be the
  controller, not the verifier: the identity admission of 19-verdicts §4 exists to prevent that.

**Decision: (a)** (S13). And in the other direction, a gate can already use any step's outputs
as a check through `steps.*` (8.4); that is a plain gate, without quorum or the one-party rule.
When a result needs those, the template is a verifier (`output: Verdict`).

### 12.2 What AgentRun gains

- The snapshot and revision check of section 10 (a work item: AgentRun reads the live template
  in #1870).
- `imageVerification` (3.4) applies to Verdict templates too.
- The shared `pkg/jobrun` (Job lifecycle) and the Pod builder of 9.1.

## 13. Test plan

### 13.1 Unit (table-driven, testify, `-race`)

- StepTemplate reconciler: every field rule of 3.2 (each refusal), schema structurality, digest,
  signature with key and keyless (the `signtest` fixtures of `pkg/reconciler/imageverification`),
  revision hash stability and history (11th revision drops the oldest), idempotency (second
  reconcile writes nothing).
- Snapshot check (10.2): matching, forged image, revoked revision, revoked digest, dropped
  revision, unverified signature, deleted template.
- Output and input validation (8.1, 8.2): valid, defaulted, wrong type, extra field, over 4 KiB,
  truncated JSON, empty message, `onFailure: continue`.
- StepRun reconciler: each state transition of 6.3, the recorded path (6.4), deleted Job while
  Running, foreign Job of the run's name, deadline delete with Background propagation, quota
  `Forbidden` → Pending, Pod security check, NetworkPolicy shape (standalone and read-target),
  ServiceAccount and RoleBinding created and deleted, Secret refusals (unlabelled, git Secret,
  SCM Secret), scope narrowed after build, idempotency.
- RenderRun with steps: Pod shape (mount matrix of 7.2: no step container mounts `git` or
  `repo`), `backoffLimit: 0`, deadline sum, per-step result from termination messages, the
  missing egress configuration refusal.
- `commit` filter (7.3): path outside, `..`, symlink, FIFO, `.kardinal/`, `.git`, size and count
  limits, `workspace.paths` narrowing; the trailer.
- Graph builder: StepRun data, admission (pre, post, `from` edges, superseded), collection and
  rendered fields in both shapes; the `StepTemplates` def dedup; size estimate for 200 targets ×
  3 steps; the gate `spec.steps` expression; validation errors (unknown output reference, schema
  incompatibility, `stepsFrom` self or fleet, `onFailure: continue` on a workspace step, argocd
  strategy).
- PolicyGate: the `steps` variable, a missing step fails closed with the reason.
- Admission (chart tests against a real API server): `graph-objects` refuses user-created
  StepRuns; the opt-in `use` policy allows and denies per user, and does not re-check a template
  already referenced.

Safety assertions must be able to fail: for the mount matrix, the `commit` filter, the snapshot
check, the Secret refusals and the scope check, each test is shown failing with the property
broken on purpose, then passing (the QA rule of 2026-10-14).

### 13.2 Fake-client journeys (`test/e2e/journeys_test.go` style), test → uat → prod

| # | Case | Expect |
|---|------|--------|
| SJ1 | `pre` step with outputs, a `post` step taking one as input | post StepRun admitted only after pre Succeeded; its `status.inputs` has the value |
| SJ2 | uat `post` step outputs `p99Ms: 450`; prod gate `steps["load"].outputs.p99Ms < 300` | prod blocked with the reason; with 212, prod promotes |
| SJ3 | `before-pr` scan in uat (`pr-review`) | RenderRun `mode: edit`; PR body has the Custom steps section |
| SJ4 | `after-update` write step in a `layout: branch` env | the render includes the edit; the marker digest matches; the next Bundle renders without drift |
| SJ5 | write step changes a file outside the path | RenderRun Failed naming the path; nothing pushed |
| SJ6 | template revised while the Bundle waits in uat | prod runs the old revision; a new Bundle runs the new one |
| SJ7 | revision revoked while the Bundle waits | prod step Failed with the revocation reason |
| SJ8 | StepRun deleted after Succeeded | re-created with `recorded`; no second Job; outputs kept |
| SJ9 | template out of scope for the namespace | Bundle fails to build with the reason |
| SJ10 | moved base under an open PR, a non-deterministic write step | rebuild refused with the reason (7.5) |
| SJ11 | `post` step fails, `onHealthFailure: rollback` | rollback Bundle created, as for a failed post hook |
| SJ12 | Bundle superseded before the pre StepRun started | StepRun Skipped |

### 13.3 Live e2e (kind, COUNT=2; rows for `test/e2e/coverage.tsv`, status planned)

Prefix `TestStep_Custom`, core suite, with a reference step image built in the repository
(`test/steps/echo`: a Go program that echoes its inputs as outputs, optionally edits a file or
probes an address) loaded into kind and registered by digest:

- `TestStep_CustomPrePostOutputs`: pre and post steps, outputs in the PR body and the gate.
- `TestStep_CustomWorkspaceDirectory`: `after-update` write and `before-pr` read in a
  `layout: directory` env; the commit trailer; the step container has no `/var/run/kardinal/git`.
- `TestStep_CustomWorkspaceBranch`: the same in `layout: branch`.
- `TestStep_CustomDeleteGraphDeletesPods`: delete the Bundle's Graph while a StepRun runs; no Pod
  of its Job remains (G12).
- `TestStep_CustomNetworkPolicy`: a standalone step's probe of an address outside `egress` fails
  (the step reports it in an output).
- `TestStep_CustomRevoked`: revoke the revision; the next run fails with the reason.
- `TestStep_CustomSignature`: a template with a keyless authority that does not match is not
  ready.

## 14. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| S1 | Where custom code runs | (a) the controller (plugin or exec); (b) a webhook the controller calls; (c) a Pod the controller builds | **(c)**: (a) is exec in a reconciler and (b) controller SSRF, the two reasons #1282 removed custom steps. |
| S2 | Run object for `pre`/`post` | (a) extend HookRun; (b) new StepRun; (c) reuse AgentRun | **(b)**: HookRun is a team `JobSpec` with no inputs, outputs or bounds (19-verdicts §8.1 (a)); AgentRun writes Verdicts with a token (12). A new kind keeps one mode per kind and shares `pkg/jobrun`. |
| S3 | Where workspace steps run | (a) each in its own Pod, handing the tree over through a git ref and SHA; (b) as containers in the environment's promotion Job, results in RenderRun status; (c) as in (b), plus a StepRun record per step mirrored by the Graph | **(b)**: (a) cannot run `after-update` in `layout: branch`, where the update is never committed (`defaults.go:30-33`), pushes intermediate states, and re-clones per step; (c) adds an object and a Graph walk per step for naming uniformity only, which the builder already gives (8.3, 8.4). |
| S4 | Promotion Job for `layout: directory` | (a) RenderRun `mode: edit`; (b) a new WorkspaceRun kind; (c) rename RenderRun to PromotionRun | **(a)**: the same reconciler, image, sandbox, admission, RBAC and Graph nodes; only the name reads oddly, and a `Mode` printer column says which. (c) is an API break for no behaviour. |
| S5 | Output channel | (a) termination message; (b) the Pod writes its own CR through the API; (c) an output file a trusted container collects | **(a)**: no credential in the step, the kubelet writes it, the reconciler already reads it for renders. (b) needs a token that can write status; (c) adds a hop and still ends in a termination message (one 4 KiB budget for all steps instead of one each). Cost: 4 KiB per step, 12 KiB per Pod. |
| S6 | Schema language | (a) OpenAPI v3 structural, validated with the apiextensions validator; (b) JSON Schema 2020-12 with a new library; (c) a flat list of typed keys | **(a)**: the validator Kubernetes uses for CRDs, already a dependency, no new module; admins know it from CRDs. (b) adds a dependency and a second dialect; (c) cannot express enums, bounds or nesting. |
| S7 | Image trust | (a) digest only; (b) digest plus optional cosign verification in the StepTemplate reconciler; (c) leave it to cluster admission (Kyverno, policy-controller) | **(b)**, with `--step-template-require-signature` to make it mandatory: reuses the ImageVerification code, and the result is in the template's status where runs check it. (c) remains possible on top. |
| S8 | Versioning | (a) live; (b) snapshot + revision check + revoke; (c) immutable versioned names | **(b)** (10.1). |
| S9 | Tenancy | (a) template scope; (b) RBAC `use` verb; (c) both, `use` opt-in | **(c)** (11.1). |
| S10 | How an input names an output | (a) CEL in the Pipeline; (b) structured `inputsFrom`; (c) Go templates | **(b)**: checkable against schemas at build, no expression evaluation of user text in the controller, no new CEL context in the Pipeline. |
| S11 | How gates read outputs | (a) the Graph copies into the gate spec; (b) the PolicyGate reconciler lists StepRuns; (c) a `def` that evaluates the condition | **(a)**: as for Approvals and Verdicts (19-verdicts V2); kro watches the sources; the reconciler explains with one function. |
| S12 | Which environment a gate reads | (a) `stepsFrom`, default the only direct upstream; (b) all ancestors merged by name; (c) `steps.<env>.<name>` | **(a)**: one name means one step, without collisions; (b) is ambiguous when two ancestors use a name; (c) makes every expression name environments the translator already knows. |
| S13 | AgentRun and StepRun | (a) separate, shared registration and Job code; (b) merge; (c) StepRun writes Verdicts | **(a)** (12.1). |
| S14 | Kubernetes access for a step | (a) none ever; (b) none by default, opt-in read-only reader role on the target; (c) a tenant ServiceAccount | **(b)**: post steps that test the deployed version need to find it; writes stay with hooks. (c) is #1870's rejected "borrow someone's RBAC". |
| S15 | Order within a point | (a) list order everywhere; (b) concurrent, ordered by `inputsFrom`; (c) workspace in list order, standalone concurrent by `inputsFrom` | **(c)**: workspace steps share one tree and are init containers, which are sequential anyway; standalone steps are independent Pods, and the data edges are already Graph admission. |
| S16 | Pipeline field | (a) reuse `steps`; (b) `customSteps`; (c) `extensions` | **(b)**: old YAML with the removed `steps` field (pruned since #1244) may still sit in users' repositories; giving the name a new meaning would turn it on silently or fail with confusing errors. |
| S17 | Rebuild on a moved base | (a) not rebuilt; (b) new attempt, re-run steps; (c) new attempt, refused when the steps' result differs from the reviewed PR | **(c)**: keeps rebuilds for deterministic steps and never changes reviewed content behind a reviewer. |
| S18 | Failure policy | (a) always fail; (b) `onFailure: fail \| continue`; (c) per-output defaults | **(b)** for standalone steps; workspace steps cannot continue past a failed init container, so they fail, and an advisory step exits 0 with its finding in its outputs. |

## 15. Upgrade path

From v0.13.x:

- New CRD `stepruns`; `steptemplates` gains fields (`hookPoints`, `inputs`, `outputs`,
  `workspace`, `kubernetesAccess`, `imageVerification`, `scope`, `revokedRevisions`,
  `revokedDigests`, status `revision` and `revisions`) and the `output` enum gains `Outputs`.
  Existing `output: Verdict` templates are unchanged and stay ready. Their `status.revisions`
  is filled on the first reconcile after the upgrade.
- RenderRun gains `spec.mode` (default `render`, the current behaviour), `spec.steps` and
  `status.steps`. Existing RenderRuns read as `mode: render` with no steps.
- Pipeline gains `environments[].customSteps`; PolicyGate gains `spec.stepsFrom` and
  `spec.steps` (Graph-written); PromotionStep gains `spec.preSteps`, `spec.postSteps`,
  `spec.live.steps`, `status.stepRecords`. All optional.
- Graphs built before the upgrade have no StepRun nodes; Bundles in flight finish as they were
  built. A Pipeline edit that adds `customSteps` rebuilds in-flight Graphs
  (`ensurePipelineSpecCurrent`): a step whose point an in-flight step already passed is
  `Skipped` (`stepAdvanced`), as a late hook is.
- Moving an environment from in-process git to `mode: edit` (adding its first workspace step)
  changes nothing for Bundles already past `Promoting`.
- Chart: new values `steps.workspaceGitEgress`, `steps.requireUseVerb`, controller flags of 9.4
  and `--step-template-require-signature`; the step reader ClusterRole (shared with AgentRun's).
- Downgrade to v0.13: remove `customSteps` first; StepRuns left behind are inert (no
  reconciler) and are deleted with their Bundles.

## 16. Scale

| Dimension | Cost | Bound |
|-----------|------|-------|
| StepRuns per Bundle | steps × environments (× targets for fleets) | 10 per environment; fleet pacing admits only targets being promoted (`compact_render.go:26-30` for RenderRuns, the same admission) |
| Graph size | ~300 B per StepRun item, snapshots once per revision | `CheckSize` (G10); 200 targets × 3 steps ≈ 180 KB |
| API objects per standalone run | StepRun, Job, Pod, NetworkPolicy; + ServiceAccount, RoleBinding with read-target | garbage-collected with the StepRun; access deleted at the end |
| Graph walks | each StepRun status write re-walks the Bundle's Graph (selector ref) | the reconciler writes status only on a phase change or a new message, as RenderRun (`renderrun/reconciler.go:619-624`) |
| Concurrent Pods | one per running step | namespace ResourceQuota (9.4); fleet pacing |
| Gate copies | outputs ≤ 4 KiB per step into each gate instance reading them | one source environment per gate (8.4) |
| Registry calls | one signature check per (template revision, digest) | none per run |
| Workspace Pods | one per promotion of an environment with workspace steps | only those environments (7.1) |

Many tenants share templates; each run's Secrets, ServiceAccount and NetworkPolicy are in its own
namespace, so one tenant's load or misconfiguration does not affect another's runs beyond the
shared controller workers (`--namespace-shard` applies to the StepRun reconciler as to the
others).

## 17. What this does not protect against

- **A malicious registered image.** The admin's registration, the digest and the signature are
  the trust. Inside the sandbox an image can still lie in its outputs, or, as a write step,
  change the environment's files within the allowed paths. The PR shows the changed paths, and
  `pr-review` puts a person in front of them.
- **Workspace steps reaching the git host** (9.3), without credentials of ours.
- **Prompt injection or poisoned input** to a step that reads the deployed application (`post`,
  `read-target`): its outputs are as good as its reasoning. Use a verify gate with several
  verifiers for decisions that matter (19-verdicts §11).
- **Non-enforcing CNIs** (9.3) and **runtime escapes** without a sandboxed runtime class.
- **Outputs as truth.** Schema validation proves shape only (8.1).

## 18. Work breakdown (issues, milestone v0.14.0)

1. **`pkg/jobrun`**: the Job lifecycle and restricted Pod builder shared by HookRun, AgentRun and
   StepRun (if #1870 has not already moved it). Size m.
2. **StepTemplate v0.14 fields and reconciler**: field rules, schemas, revisions, revocation,
   digest, signature (3, 10). CRD change. Size l.
3. **StepRun CRD and reconciler**: 6, 8.1, 8.2, 9 (standalone), with idempotency and the
   fail-on-purpose safety tests. CRD change. Size l.
4. **Graph: standalone steps** in both shapes: data, `StepTemplates` def, admission, collection,
   `spec.preSteps`/`postSteps`, the `spec.live.steps` mirror, `stepRecords`/`recorded`; the
   PromotionStep waits (5.2). Size l.
5. **Pipeline `customSteps`** and translator validation: scope, schemas, `inputsFrom`, the
   build errors of 4 and 8.3; `kardinal validate`. Size m.
6. **RenderRun `mode: edit` and workspace steps**: the Pod of 7.2, the `commit` filter (7.3),
   `status.steps`, the egress NetworkPolicy (9.3), the render Job phases in `cmd/kardinal-render`.
   CRD change. Size xl (split: edit mode first, then steps).
7. **PR rebuild for promotion-Job environments** (7.5). Size m.
8. **Gates: `stepsFrom`, `spec.steps`, the `steps` CEL variable**, docs in
   `docs/reference/cel-context.md`. Size m.
9. **Evidence**: PR section, UI drawer, `kardinal explain`, AuditEvents, receipt fields (8.5).
   Size m.
10. **Tenancy admission**: the opt-in `use` policy (11.1). Size s.
11. **AgentRun adopts snapshots and image verification** (12.2). Size m.
12. **Live e2e** of 13.3 with the reference step image, and `coverage.tsv` rows. Size l.
13. **User docs**: `docs/custom-steps.md` (with the sandbox limits of 9.3 and 17),
    `docs/pipeline-reference.md`, `docs/hooks.md` ("hooks or custom steps?"), changelog. Size m.

Implementation PRs that change CRD schemas are "risky" under the program rules: no auto-merge,
reported to the coordinator.
