# Design: Change risk, earned autonomy and adaptive pacing

> Status: Proposed (spec for v0.14.0, AGENTIC-PLAN §5 "Change risk score", "Earned autonomy",
> "Adaptive pacing in CEL"). Implementation waits until v0.13.0 is tagged and follows in separate PRs.
> Created: 2026-10-15
> Depends on: [17-actors.md](17-actors.md) (the actor stamp, `AgentIdentity`),
> [18-actor-policy.md](18-actor-policy.md) (`agent.*`, `approval.requiredWhen`, the one-party
> rule), [19-verdicts.md](19-verdicts.md) (AgentRun, the Job-per-run pattern), and
> [20-attestations.md](20-attestations.md) (the verified SLSA source commit), and
> [28-actor-analytics.md](28-actor-analytics.md) (the `DeploymentLog` earned autonomy reads, §7.2;
> it lands before §7). Specs 17 to 21 are on main (#1857, #1869, #1870, #1963, #1961); spec 28 is
> not.
> Audited: 2026-10-21 against `600a75656` (main, the v0.13 work), drift fixed in place; first
> verified against `8f5d86e`.
> Verified against: this repository at `600a75656` (main), kro v0.10.0-rc.0 (`54a203b`,
> `/local/home/rrroizma/kro-upstream`), cel-go v0.31.0, `k8s.io/apiserver` v0.37.1 and
> `github.com/bmatcuk/doublestar/v4` v4.10.2 from the module cache. Source references are
> `path:line` in those trees. External sources are in §2.

---


> **Merge order.** Implementation starts after v0.13.0 is tagged. The actors spec (#1857),
> actor-aware policy (#1869) and verdicts (#1870), whose CRDs and CEL variables this spec builds on,
> are on main.
>
> **Off by default.** Nothing in this spec changes a Pipeline that does not opt in: no ChangeRisk
> node, Job, `risk.*` value or wave is created until a Pipeline sets `spec.risk` (§4.3) or a fleet
> sets `waves`/`wavePreset` (§9). The `agent.*` track-record values (§7) are computed for every
> agent, but no policy reads them unless an admin writes one. §15 lists each default.

## 1. Problem and scope

Every version is treated the same today. A one-line README change and a 3,000-line database
migration written by an agent with two rollbacks this week get the same soak, the same approvals
and the same fleet pace. The controller already knows most of what separates them: who made the
version (the actor, spec 17), what failed before (`Pipeline.status.deploymentMetrics`), and the
commits the version carries. It does not combine them, and policy cannot read the result.

| # | Feature | Section |
|---|---------|---------|
| R1 | `ChangeRisk`: an owned Graph node per Bundle whose reconciler writes `status.{score,tier,factors}` | 4 |
| R2 | `path.glob()`: a CEL function, in the PolicyGate environment and proposed to kro | 5 |
| R3 | `risk.*` CEL variables | 6 |
| R4 | Earned autonomy: per-agent change failure rate, rework rate and rollbacks in `AgentIdentity.status` | 7 |
| R5 | Risk-adaptive soak and approvals, in CEL | 8 |
| R6 | Fleet wave presets (AWS: 1, 1, 3, 12, rest, with bake times) | 9 |

Non-goals:

- A trained or learned model. §2 says why.
- Reading commit messages, PR text or code content to score. Only paths and line counts are read.
- An LLM reviewer inside the score. That stage of RADAR maps to a Verdict from an AgentRun
  (spec 19), which a gate can already require (§2.2).
- Per-agent budgets (versions per day, concurrent prod promotions). They are a separate v0.14
  item; this spec only shares the deployment records with it (§7.2).
- Error-budget gates (Sloth, Pyrra). A separate v0.14 item.

Every open question below compares at least three options and records the choice. §16 is the
decision table.

## 2. Sources: what we take, and what does not transfer

### 2.1 Diff Risk Score (Meta, arXiv 2410.06351)

Abreu et al., "Moving Faster and Reducing Risk: Using LLMs in Release Deployment"
(<https://arxiv.org/abs/2410.06351>). Diff Risk Score (DRS) predicts the probability that a diff
causes a severe incident (SEV). The baseline is a logistic regression on about 12 features chosen
from about 100 (§III-A, Table I): added and deleted lines relative to file size (log scale), new
files, files modified, the log of the number of files in the diff, how many authors changed those
files, a previous SEV in the file and in its folder, whether a high-criticality service is touched,
file complexity, language, and the author's history with the file. The paper's gating (§II-C)
blocks the riskiest 5% of diffs on weekends, 10% in "yellow" periods and 50% in "red" periods
(Black Friday), and lets engineers land a blocked diff by escalating with a standard reason. The
baseline caught 18.7%, 27.9% and 84.6% of SEVs at those three levels; risk-aligned LLMs did
1.40x, 1.52x and 1.05x as well (Table IV). The test set had 181,052 diffs and 305 SEVs.

What we take:

| DRS idea | Here |
|----------|------|
| Size on a log scale (lines, files) | The `size` and `files` factors use log-spaced bands (4.6) |
| Previous SEV in the file and in its folder | The `history` factor: failed deployments that touched the same path class or directory (4.7) |
| High-criticality components | Path classes with points, matched with `path.glob` (4.3) |
| Gating levels tied to periods (weekend, yellow, red) | Tier and percentile combined with `schedule.*` and ChangeWindows in plain CEL (8.3) |
| Blocked changes land through an escalation with a reason | The existing gate override (break-glass, with a reason and an AuditEvent) |
| Scores and explanations shown to the developer | `status.factors[]` with the points of each factor, in the PR evidence, `kardinal why` and the UI |

What does not transfer:

- **The model.** DRS is trained on Meta's history of diffs and SEVs (305 SEVs in the test set
  alone). A kardinal installation has tens to thousands of deployments, and its failure labels
  are DORA signals (a failed health check, a rollback, a rejection), not attributed incidents. A
  per-installation regression would be fitted on a handful of failures, and Meta's coefficients
  describe Meta's code. kardinal therefore ships an additive points model with documented
  defaults (4.6). It is a policy input that an admin can read and tune, not a prediction.
- **LLM scoring.** Non-deterministic, needs model I/O, and would read diff content, which an agent
  can write to steer the result. Excluded from the score.
- **The unit of change.** DRS scores one diff at review time in a monorepo. kardinal promotes a
  version, which can carry many commits, whose source repository the controller may not be able
  to read, and whose GitOps change is often one image tag. The score is computed over the
  commit range between what the final environment runs and what the version brings (4.2).
- **Author expertise from git history.** The git author is asserted by whoever made the commit.
  kardinal uses the verified actor kind (spec 17) instead.
- **Percentile thresholds as the primary cut.** At Meta's volume a percentile is stable. A
  Pipeline with 30 deployments a month has a noisy one. Tiers use absolute scores; the percentile
  is exposed as a variable (6) for teams with volume.

### 2.2 RADAR (Meta, arXiv 2605.30208)

Adams et al., "Automating Low-Risk Code Review at Meta: RADAR, Risk Calibration, and Review
Efficiency" (<https://arxiv.org/abs/2605.30208>). RADAR approves low-risk diffs automatically through
a funnel: classify the diff by authorship and source (human, deterministic codemod, AI codemod,
runbook), eligibility gates, static heuristics, DRS, an LLM review, and deterministic validation.
Over 535,000 reviewed diffs its revert rate was one third, and its production incident rate one
fiftieth, of non-RADAR diffs. The DRS percentile threshold depends on the source: the safest 5% for
humans, 20% for bot sources that are not allow-listed, 50% for allow-listed runbooks. A runbook's
eligibility is earned from a 60-day lookback (no production incidents, low revert and rejection
rates, a minimum number of landed diffs). Sources have daily landing caps, can be paused when they
cause an incident, and have a landing delay that lets a person stop a change. Scope exclusions
(prefix and suffix block lists, sensitive configuration) apply before any score.

What we take:

| RADAR idea | Here |
|------------|------|
| Classify by source before scoring | The `actor` factor from the verified actor kind (4.6) |
| Thresholds per source | Policies over `actor.kind` and `risk.tier` (8) |
| Eligibility earned from a lookback with a minimum count | Earned autonomy: windowed track record with `agent.trackRecordSufficient` (7) |
| Pause a source that caused an incident | `AgentIdentity.spec.suspended` (spec 17) and freezes (spec 18); policies on `agent.rollbacks` (7.5) |
| Daily caps per source | The v0.14 budgets item (out of scope here) |
| Path block lists before any score | Path classes, including one that forces red (4.3) |
| LLM review stage | A verify gate fed by an AgentRun Verdict (spec 19), required by tier (8.2) |
| Deterministic validation and a landing delay | Health checks, bake, MetricChecks; soak scaled by tier (8.1) |

What does not transfer: RADAR approves *code review*, before merge. kardinal acts after the
artifact is built, on its way through environments, and cannot see test results or review
comments, only promotion outcomes. RADAR's outcome evidence is observational (the paper's
threats-to-validity section), so its numbers justify the shape, not any default here.

### 2.3 AWS waves (Builders' Library)

Clare Liguori, "Automating safe, hands-off deployments", Amazon Builders' Library (2020), now at
<https://builder.aws.com/content/3ErTKQOTKc5NIw031UePBPxTQ6I/automating-safe-hands-off-deployments>
(the former `aws.amazon.com/builders-library/automating-safe-hands-off-deployments/` redirects
there). Production is deployed in waves of increasing size: one Region with low traffic, then one
Region with high traffic, then three Regions, then up to 12, then the rest; "we can deploy to more
and more Regions in parallel in the same wave" once the early waves give confidence. Each wave
starts with a one-box stage. Bake times are "at least one hour after each one-box stage, at least
12 hours after the first regional wave, and at least two to four hours after each of the rest of
the regional waves", and the bake "includes requirements to wait for a specific number of data
points in the team's metrics". During the bake "the deployment is automatically rolled back if the
team's high-severity aggregate alarm goes into the alarm state". Deployments avoid nights,
weekends and holidays. Used in §9.

### 2.4 DORA definitions

From <https://dora.dev/guides/dora-metrics/>: change fail rate is "the ratio of deployments that
require immediate intervention following a deployment"; deployment rework rate is "the ratio of
deployments that are unplanned but happen as a result of an incident in production". The
controller already computes the first, with an exact definition of a deployment and a failure
(`pkg/reconciler/pipeline/metrics.go:316-498`, `api/v1alpha1/pipeline_types.go:1485-1508`). §7
reuses those definitions for agents.

## 3. Graph-first

| Concern | Mechanism | Kind (design rule 1) |
|---------|-----------|----------------------|
| One risk record per Bundle | `ChangeRisk`, an owned (template) node in each Bundle's Graph, both shapes | kro node |
| Reading the commits | A Job owned by the ChangeRisk, created by its reconciler (the RenderRun pattern) | reconciler, own status |
| Score and tier | ChangeRisk reconciler writes `ChangeRisk.status` once the Job finished | reconciler, own status |
| Waiting on the score | Gates whose expression reads `risk` hold until it is scored | reconciler (PolicyGate), own status |
| Graph readiness | `readyWhen` on the ChangeRisk node: scored or failed | kro node |
| Path matching | `path.glob()` | CEL function |
| Durable risk for history | Bundle reconciler copies a snapshot into `Bundle.status.risk` | reconciler, own status |
| Per-agent deployment records | Spec 28's `DeploymentLog` reconciler writes `DeploymentLog.status.entries` (this spec adds each entry's `riskTier`) | reconciler, own status |
| Track record | A track-record reconciler sums an agent's entries into `AgentIdentity.status.trackRecord` | reconciler, own status |
| Wave pacing | Fleet targets get wave edges and per-wave bake in the builder; admission is the existing compact-shape CEL | kro node (compact `def` CEL) |

Answers to the three AGENTS.md questions for the score: not a Watch node (the commits are not a
Kubernetes object); an Owned node whose reconciler writes status (yes); not a CEL extension (a git
read is I/O). The reconciler adds one G8 row (§19). No status is written across CRDs: every row
above writes its own kind and reads others.

## 4. ChangeRisk

### 4.1 Where the score is computed

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | The PolicyGate reconciler computes risk while it builds the gate context | No new CRD | Git or SCM I/O in the gate hot path (AGENTS.md anti-pattern); recomputed per gate and per recheck; no record |
| (b) | The translator computes it when it builds the Graph | Available before any node exists | The translator would do I/O; a Graph rebuild would re-score |
| (c) | A `ChangeRisk` owned node per Bundle, scored by its reconciler from a Job's output | I/O out of the controller and off the hot path; one immutable record that evidence and receipts can cite; the ImageVerification shape (`pkg/graph/imageverify.go:180-219`) | One more CRD and a Job per Bundle |

**Choice: (c).**

### 4.2 Where the diff comes from

The diff is the change between what the final environment runs now and what the version brings:

- **Image Bundle**: the application source, `base..head`, where `head` is
  `spec.provenance.commitSHA` and `base` is the commit of the version the final environments run.
- **Config Bundle**: the GitOps DRY repository (`spec.configRef.gitRepo` or the Pipeline's
  `spec.git.url`), `base..head` of `configRef.commitSHA`.
- **Mixed**: both, summed; each part recorded.

`base` is chosen by the ChangeRisk reconciler before it creates the Job: for each final environment
(`finalEnvironments`, `pkg/reconciler/pipeline/metrics.go:237`, every fleet target included), the
newest Bundle Verified there; among those, the oldest. That is the largest diff any production
target will receive. It is recorded in `status.base` with the environments it came from. No base
(a first deployment, or the base Bundle has no commit) gives factor `baseMissing`.

Options for reading it:

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | The PromotionStep's own git clone (`pkg/steps/steps/git_clone.go`) | No new clone | That clone is the GitOps repository: for an image Bundle the diff is one tag. It runs in the first environment's step, after the score is needed, and in the controller process |
| (b) | A Job owned by the ChangeRisk, as RenderRun runs `kardinal-render` (`pkg/reconciler/renderrun`, `pkg/renderjob`): it fetches `base` and `head` and diffs the trees | Full fidelity for any number of files; one implementation for every git host; sandboxed (no controller credentials, a NetworkPolicy, limits); the result is read from the Pod's termination message as RenderRun does (`pkg/renderjob/renderjob.go:41-44,154-184`) | A Pod per Bundle (seconds to minutes); clone size; the source repository's credentials must be in the namespace |
| (c) | The SCM compare API from the ChangeRisk reconciler (`GET /repos/{o}/{r}/compare/{base}...{head}` on GitHub) | One HTTP call, no clone | GitHub returns "up to 300 changed files for the entire comparison" (<https://docs.github.com/en/rest/commits/commits#compare-two-commits>), so the largest diffs are the ones it truncates; a compare implementation per provider (GitHub, GitLab, Forgejo/Gitea, Bitbucket, Azure DevOps in `pkg/scm`); API quota |
| (d) | CI writes a diff summary into the Bundle | No I/O at all | Asserted by whoever creates the Bundle; an agent would report its own risk |

**Choice: (b).** It is the only option that scores a large change correctly and does not trust the
author. The Job image is the render image with a `risk` mode (`kardinal-render risk`), so no new
image is shipped.

What it cannot do, and what would break it:

- It cannot read a repository it has no credentials for. Then the ChangeRisk fails with
  `SourceUnavailable` and the tier is `onFailure` (default `red`, 4.4): fail closed.
- For an image Bundle, `head` is `spec.provenance.commitSHA`, which CI asserts. An agent that
  creates a Bundle could name a small commit. When the Bundle's ImageVerification verified SLSA
  provenance (spec 20, #1963), the reconciler uses the verified source commit
  (`ImageVerification.status.attestations[].slsa.sourceCommit`,
  `api/v1alpha1/imageverification_types.go:525-533`; `attestations.<name>.slsa.sourceCommit` in gate
  CEL) and records `sourceVerified: true`. Otherwise a person's or a bot's version gets the
  `sourceUnverified` points (4.6), and an agent's is not scored at all (`SourceUnverified`, the
  `onFailure` tier, §14.1): the score is only as true as `head`, and the author must not name it.
- go-git v5.19.2 (`go.mod`) has no partial-clone filter (no `Filter` in its `CloneOptions`), so the
  Job fetches `head` and `base` with depth 1 each. Fetching a commit by SHA depends on the server
  allowing it (GitHub, GitLab and Forgejo do for reachable commits). The implementation verifies
  this per provider in the SCM contract tests and falls back to a branch fetch deepened in steps up
  to the Job's limit. (`RebaseOnRemote` deepens once, to `rebaseBaseDepth` 200 commits,
  `pkg/scm/git_rebase.go:64-68,139-151`, then fails; the Job keeps deepening until `limits`.)
- The termination message is at most 4096 bytes (`pkg/renderjob/renderjob.go:43-44`). Totals and
  class counts are exact; the path list is cut to fit and says so (`pathsTruncated`).

### 4.3 RiskProfile: who sets the weights

The weights decide what is green. Whoever can lower them can make their own changes green, so
they must not live where an agent or a team under the policy can edit them alone.

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `Pipeline.spec.risk` holds the weights and classes | One object | Whoever edits the Pipeline sets their own risk |
| (b) | A cluster-scoped profile | Only admins write it | One profile for every team; a team cannot add its own paths |
| (c) | A namespaced `RiskProfile`, in the Pipeline's namespace or an org policy namespace (`--policy-namespaces`), referenced by the Pipeline; the reference is a CEL variable, so org gates can require the org's profile | Teams write their own, orgs pin theirs; the same trust split org gates already use (`metricsNamespace`, `pkg/reconciler/policygate/reconciler.go:777-804`) | Two places to look; an org that cares must write the pin |

**Choice: (c).**

```yaml
apiVersion: kardinal.io/v1alpha1
kind: RiskProfile
metadata:
  name: default
  namespace: platform-policies
spec:
  classes:                         # at most 32; matched against every changed path in the Job
    - name: migrations
      globs: ["db/migrations/**", "**/*.sql"]
      points: 30
    - name: infra
      globs: ["**/*.tf", "charts/**/values-prod.yaml"]
      points: 25
    - name: security
      globs: ["**/rbac/**", "**/networkpolicy*.yaml"]
      points: 40
    - name: docs
      globs: ["docs/**", "**/*.md"]
      points: 0
    - name: frozen                 # RADAR-style scope exclusion: always red
      globs: ["billing/ledger/**"]
      forceTier: red
  weights:                         # every field optional; defaults in 4.6
    sizeBands: [{lines: 25, points: 5}, {lines: 75, points: 10}, {lines: 175, points: 15}, {lines: 375, points: 20}, {lines: 775, points: 25}]
    fileBands: [{files: 3, points: 2}, {files: 10, points: 5}, {files: 30, points: 8}, {files: 100, points: 10}]
    classesMax: 40
    actor: {human: 0, bot: 5, agent: 10, controller: 0, unknown: 10}
    sourceUnverified: 5            # a person's or a bot's version only; see requireVerifiedSource
    binaryFileLines: 100           # each changed binary file counts as this many lines in size
    baseMissing: 15
    historyPerFailure: 10
    historyMax: 20
  history:
    deployments: 30                # the window, matching deploymentMetrics (metricsLookbackBundles)
    directoryDepth: 2              # "apps/payments/x.go" → key "dir:apps/payments"
  tiers: {amber: 30, red: 60}      # score < 30 green, < 60 amber, else red
  onFailure: red                   # tier when the change cannot be scored: red | amber | green
  requireVerifiedSource: [agent]   # actor kinds whose version is scored only from verified provenance (§14.1)
  rollback: exempt                 # exempt | score
  limits: {timeout: 5m, maxRepoBytes: 2Gi}
status:
  ready: true
  conditions: [...]                # Ready; reasons Valid, InvalidGlob, InvalidBands
  observedGeneration: 4
```

A RiskProfile reconciler validates what the schema cannot (each glob with the `path.glob` rules,
§5.2; bands increasing) and writes `status.ready`. A ChangeRisk refuses a profile that is not ready
for its current generation (`ProfileNotReady`, then `onFailure`).

`Pipeline.spec.risk` holds only the reference and the source:

```yaml
spec:
  risk:
    profileRef: {namespace: platform-policies, name: default}   # own namespace or an org policy namespace
    source:                       # the application repository, for image Bundles
      repoURL: https://github.com/example/checkout
      secretRef: {name: checkout-src}   # labelled kardinal.io/referenceable=true, read by name
```

### 4.4 The ChangeRisk CR

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeRisk
metadata:
  name: checkout-checkout-7f3c2a-risk     # boundedName, as ImageVerificationName
  namespace: checkout
  labels: {kardinal.io/pipeline: checkout, kardinal.io/bundle: checkout-7f3c2a, kardinal.io/bundle-uid: "…"}
spec:                                      # written by the Graph from the Bundle; immutable
  pipelineName: checkout
  bundleName: checkout-7f3c2a
  bundleUID: "…"
  profileRef: {namespace: platform-policies, name: default}
  sources:
    - kind: source                         # source | config
      repoURL: https://github.com/example/checkout
      secretName: checkout-src
      head: 3b9c…
status:
  phase: Scored                            # Pending | Running | Scored | Failed; Scored and Failed are terminal
  message: ""
  jobName: checkout-checkout-7f3c2a-risk-job
  jobUID: "…"
  profile: {namespace: platform-policies, name: default, generation: 4, hash: 9e1d…}
  base:
    commits: [{kind: source, base: a17e…, head: 3b9c…}]
    bundle: checkout-61aa90                # the version the final environments run
    environments: [prod-eu, prod-us]
  diff:
    additions: 412
    deletions: 37
    files: 14
    binaryFiles: 0
    classes: [{name: migrations, files: 2}]
    keys: ["class:migrations", "dir:db/migrations", "dir:apps/checkout"]   # at most 32, for history
    paths: ["db/migrations/0042_add_index.sql", "apps/checkout/order.go"]  # at most 64, by churn
    pathsTruncated: true
  sourceVerified: false
  score: 80
  tier: red
  percentile: 93                           # among the Pipeline's last 30 scored Bundles; -1 under 10
  factors:                                 # each factor's points; the sum, clamped to 0..100, is the score
    - {name: size, value: "449 lines", points: 20}
    - {name: files, value: "14 files", points: 5}
    - {name: classes, value: "migrations", points: 30}
    - {name: actor, value: bot, points: 5}
    - {name: sourceUnverified, value: "provenance not verified", points: 5}
    - {name: history, value: "1 failed deployment touched class:migrations", points: 10}
  forcedBy: ""                             # the class name when forceTier set the tier
  scoredAt: "…"
  conditions: [...]                        # Ready; reasons Scored, SourceUnavailable, SourceUnverified, ProfileNotReady, JobReplaced, Timeout, TooLarge
```

20 + 5 + 30 + 5 + 5 + 10 = 75, red: a CI job's (bot's) version, whose provenance is not verified. An
agent's version without verified provenance is not scored at all (`SourceUnverified`, §14.1). §4.6
has more worked examples.

Rules:

- **Run once.** As RenderRun: the Job is created once, owned with a controller ownerReference (so
  garbage collection removes its Pods, avoiding ledger G12), `status.jobUID` pins it, `Scored` and
  `Failed` are terminal (a CRD transition rule refuses changing them), and a spec change after the
  Job started is ignored and recorded (`SpecHash`).
- **Failure is a tier.** `Failed` writes `tier: <onFailure>` and `score: -1`. Gates see
  `risk.scored == false` and the tier, so a policy written for red applies.
- **Rollback.** With `rollback: exempt`, a rollback Bundle whose `rollbackOf` was Verified in the
  final environment (the check `lifecycle.VerifyHeldRollback` already makes for holds) gets no Job,
  `score: 0`, `tier: green`, factor `rollback`. Otherwise it is scored like any version. Exempt is
  the default because the soak and approvals of a red tier would lengthen time to restore.

### 4.5 The Job

Inputs: the ChangeRisk spec, `status.base`, the profile's classes and limits, as environment and a
mounted Secret (as `pkg/renderjob` takes them). It has no Kubernetes credentials
(`automountServiceAccountToken: false`), a NetworkPolicy of its own that allows DNS and the git
host only, created by the reconciler and owned by the ChangeRisk as AgentRun creates one per run
(`pkg/reconciler/agentrun/reconciler.go:568-610`; the render Jobs' chart NetworkPolicy,
`render.networkPolicy.enabled`, is off by default and is not relied on), the in-process network
lock RenderRun's Jobs use (`renderjob.LockNetwork`, `pkg/renderjob/network.go:93`: only git to the
one host), `runAsNonRoot`, read-only root filesystem, an emptyDir work volume
bounded by `maxRepoBytes`, and `activeDeadlineSeconds` from `timeout`.

Work: fetch `base` and `head` (4.2), diff the two trees (rename detection on), count added and
deleted lines of text files, count binary files (text or binary is decided from the content, as
git does, by a NUL byte in the first 8000 bytes; `.gitattributes` in the repository is ignored, since
the author writes it), match every changed path (both sides of a rename)
against every class with the `pkg/pathglob` matcher that also backs `path.glob` (5.2), derive the
directory keys, and write the result as JSON to the termination message, trimming `paths` from the
end until it fits. It never reads commit messages.

Output: `diff` in 4.4. The reconciler computes the score; the Job only measures. Reading the
history and the actor needs the API server, which the Job must not reach.

### 4.6 Scoring

| | Model | For | Against |
|-|-------|-----|---------|
| (a) | Additive points with bands, defaults documented, tuned per RiskProfile | Deterministic; every point is explained in `factors`; tunable without data | Not calibrated; the defaults are judgement |
| (b) | Logistic regression with shipped coefficients | DRS's baseline shape | The coefficients would come from nowhere (2.1); a probability nobody can explain is worse than points |
| (c) | A regression fitted per installation | Calibrated in principle | A Pipeline has a handful of failures; the fit would be noise, and it would change under the policy that reads it |
| (d) | An LLM or StepTemplate scorer | Reads semantics | Non-deterministic, I/O, reads attacker-written content (2.1) |

**Choice: (a).** Score = clamp(Σ points, 0, 100):

| Factor | Value | Default points | DRS / RADAR counterpart |
|--------|-------|----------------|-------------------------|
| `size` | additions + deletions, plus `binaryFileLines` (100) for each changed binary file | the points of the highest band the value reaches: 25 → 5, 75 → 10, 175 → 15, 375 → 20, 775 → 25; under 25 → 0 | log of added and deleted SLOC |
| `files` | files changed | 3 → 2, 10 → 5, 30 → 8, 100+ → 10 | log of the number of files |
| `classes` | matched classes | sum of their points, at most `classesMax` (40) | high-criticality service; RADAR scope lists |
| `actor` | `actor.kind` of the Bundle (spec 17) | human 0, bot 5, agent 10, controller 0, unknown 10 | RADAR source classification |
| `sourceUnverified` | head commit not from verified provenance, for an actor kind not in `requireVerifiedSource` (an agent's version is not scored then: `SourceUnverified`, §14.1) | 5 | (none) |
| `baseMissing` | no base | 15 | (none) |
| `history` | failed deployments among the Pipeline's last 30 that share a key with this change | 10 each, at most 20 | previous SEV in file and folder |

A class with `forceTier` sets the tier whatever the score, and `forcedBy` names it. Tiers: score
below `tiers.amber` (30) is green, below `tiers.red` (60) amber, else red.

Worked examples with the default profile above:

| Change | Points | Tier |
|--------|--------|------|
| A person changes 40 lines in 2 Go files | 5 + 0 | green (5) |
| A person changes 200 lines in 6 files | 15 + 2 | green (17) |
| A CI job (bot) changes 200 lines in 6 files, provenance unverified | 15 + 2 + 5 + 5 | green (27) |
| An agent changes 200 lines in 6 files, provenance verified | 15 + 2 + 10 | green (27) |
| The same agent change, provenance unverified | not scored: `SourceUnverified` | `onFailure` (red) |
| A person adds a 60-line migration in 1 file | 5 + 0 + 30 | amber (35) |
| The same migration by an agent (verified provenance), after a failed deployment that touched `class:migrations` | 5 + 0 + 30 + 10 + 10 | amber (55) |
| An agent (verified provenance) touches RBAC and a migration (classes 40 + 30, capped at 40), 500 lines, 12 files | 20 + 5 + 40 + 10 | red (75) |
| A docs-only change by an agent (verified provenance), 300 lines in 4 files | 15 + 2 + 0 + 10 | green (27); 8.4 has a docs fast path that does not depend on the score |

The defaults are judgement. The changelog says so, and §18 schedules a calibration report
(`kardinal risk report`: tier against later failure, per Pipeline) before any default changes.

### 4.7 Rollback history of paths

The `history` factor needs past outcomes per key. The ChangeRisk of an old Bundle is gone: kro
deletes it with its Graph when the Bundle is retired (ledger G15), and Bundles beyond
`historyLimit` (default 50, `api/v1alpha1/pipeline_types.go:81-87`) are deleted. So:

- The Bundle reconciler copies the scored result into `Bundle.status.risk` (score, tier, keys,
  profile), once, when the ChangeRisk turns terminal. It is its own status; it reads ChangeRisk.
- The ChangeRisk reconciler takes the Pipeline's last `history.deployments` (30) deployments, as
  the Pipeline's change failure rate defines them, from the Bundles still present, and counts the
  failed ones whose `status.risk.keys` intersect this change's keys.

The deployment and failure definitions are one function: spec 28's `pkg/delivery.Derive(pipeline,
bundles, steps) []Entry` (28-actor-analytics.md §5.5), onto which `computeStability`
(`pkg/reconciler/pipeline/metrics.go:316-498`) is refactored; it is used by the Pipeline metrics,
the DeploymentLog, the history factor and so the agent records (7). If this spec's risk score
(rows 1-8 of §18) lands before spec 28, it lands that refactor first, under spec 28's names.
The window is the same 30 deployments the change failure rate uses
(`metricsLookbackBundles`, `pkg/reconciler/pipeline/metrics.go:44`). With `historyLimit` below 30
the window is what is left; `factors[history].value` says how many deployments were read.

Keys are classes and directory prefixes, not files: a file-level history would need every path of
every past change, which neither the termination message nor a status can hold, and DRS's folder
feature is the same idea at a coarser grain.

### 4.8 The node in the Graph, and how dependents wait

The builder adds one template node, ID `changeRisk` (node shape and compact shape alike, next to
`imageVerify`), when the Pipeline sets `spec.risk`. Its spec is literal (the translator knows every
value). `ValidateNodeIDs` refuses an environment that maps to the same ID (`change-risk`) with a
message naming both (`pkg/graph/validate.go:103-150`); because `spec.risk` is opt-in no existing
Pipeline is affected.

```yaml
- id: changeRisk
  template: { apiVersion: kardinal.io/v1alpha1, kind: ChangeRisk, metadata: {...}, spec: {...} }
  readyWhen:
    - ${changeRisk.?status.?phase.orValue("") in ["Scored", "Failed"]}
```

How do the environments wait for it?

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | `readyWhen` on the node, and dependents wait | Graph-native | A standalone Graph does not gate dependents on `readyWhen` (ledger G1: `GateReadiness` is off for Graphs, `executor/simple.go:65-69`, `cmd/controller/graphengine.go:77`). It only affects Graph `Ready` |
| (b) | A cross-node `readyWhen` on each step (`changeRisk.status.phase == "Scored"`) | One line per step | The compiler refuses any other node in `readyWhen` (G3, `compiler/compiler.go:749-766`), and it would still not gate (G1) |
| (c) | Hold the root steps, as image verification does: `resolvableWhen` on the root step's values in the node shape (`pkg/graph/builder.go:903-908`), a live mirror and a Pending hold in the compact shape (`pkg/reconciler/promotionstep/hooks.go:102-110`) | Nothing starts before the score | Every promotion waits for a Job even when no policy reads `risk`; the first environment's lead time grows by the Job's run time |
| (d) | Gates whose expression or `approval.requiredWhen` reads `risk` hold until the ChangeRisk is terminal (the PolicyGate reconciler reads it, as `metrics.*`) | Only the consumers wait; no Graph edge; the gate's reason says why | The wait is in the reconciler, not the Graph (an existing G8 row, "Cross-CRD reads for the gate context") |

**Choice: (d), with the `readyWhen` of (a) for Graph readiness and `kubectl get graph`.** The
`readyWhen` accepts `Failed`, because a node that can never be ready keeps the Graph `Ready=False`
for good (ledger Notes, "The Graph is not Ready while any node is not Ready"). A gate that reads
`risk` while the ChangeRisk is `Pending` or `Running` is not ready, with reason
`waiting for change risk (Running)`; this is a hold, not an evaluation error, and it writes no new
AuditEvent until the gate's readiness flips (the PolicyGate reconciler writes `GateEvaluated` only on
the first evaluation and on each flip, `pkg/reconciler/policygate/audit.go:39-61`). A team that wants nothing to start before the score puts any gate
that reads `risk` on the first environment (for example `risk.tier != "red"`): it holds until the
ChangeRisk is terminal.

What would break this: a kro change that classified the ChangeRisk's status read as data-pending
for the gate nodes would not matter (gates do not read it through kro). A gate that reads `risk`
in a Pipeline without `spec.risk` is an evaluation error, `risk: Pipeline checkout has no
spec.risk`, and fails closed, as an unknown ChangeWindow does
(`pkg/reconciler/policygate/cel_evaluator.go:176-198`).

## 5. `path.glob()`

### 5.1 Is there an equivalent?

Checked:

- cel-go v0.31.0 `ext/`: bindings, comprehensions, encoders, formatting, guards, lists, math,
  native, network, protos, regex, sets, strings. No glob or path function (`grep -ri glob` finds only
  `isGlobalUnicast` and "global functions" in `ext/network.go`, and `GlobalEnum` in tests).
- `k8s.io/apiserver` v0.37.1 `pkg/cel/library`: authz, cidr, format, ip, jsonpatch, lists,
  quantity, regex, semver, urls. No glob.
- kro v0.10.0-rc.0 `pkg/cel/library`: hash, json, lists, maps, omit, random, runtime. No glob. The
  Graph environment (`pkg/cel/environment.go:128-156`) adds cel-go ext and the Kubernetes libraries
  above; kro's docs list the same (`website/versioned_docs/version-0.10.0-rc.0/docs/concepts/expressions/02-cel-libraries.md`).
- kardinal's PolicyGate environment (`pkg/reconciler/policygate/cel_evaluator.go:107-165`):
  `ext.Strings`, json, maps, lists, random, `changewindow.*`. No glob.

The closest equivalent is standard CEL `matches()` (RE2). It has the same power, and a glob can be
translated to a regex. It is not a substitute in practice: the glob semantics users already write
in kardinal (Subscription `pathGlob`, matched with doublestar, `pkg/source/git_remote.go:470-497`,
and RiskProfile classes) must agree with the gate's; `*` must stop at `/`, `**/` must match zero or
more directories, and dots and brackets must be escaped. A hand-written regex for
`charts/**/values-prod.yaml` is `^charts/(?:[^/]+/)*values-prod\.yaml$`, which policy reviewers
do not read reliably. So a function is added (design rule 2: no equivalent in cel-go ext or the kro
library).

### 5.2 Semantics

```
path.glob(pattern string, path string) -> bool
```

- A global function in the `path` namespace, like `json.marshal` and `hash.sha256`.
- Matches the whole `path` against `pattern`, `/`-separated:
  - `*` matches any run of characters except `/`; `?` one character except `/`;
  - `[abc]`, `[a-z]`, `[!a]`, `[^a]` a character class (no `/`);
  - `**` as a whole segment (`a/**/b`, `**/x`, `a/**`) matches zero or more segments;
    elsewhere `**` acts as `*` (doublestar's rule);
  - `\` escapes the next character.
- **No `{a,b}` alternatives.** doublestar expands them by recursion
  (`doublestar/v4/match.go:224-250`, the `case '{'` of `doMatchWithSeparator`), which is exponential in nested alternatives. Use `||` in CEL
  or two globs in a class. A pattern with an unescaped `{` is invalid.
- Case-sensitive. Paths are not cleaned: `a/./b` does not match `a/b`, and a leading `/` is
  significant. Paths from `risk.paths` are always repository-relative without a leading `/`.
- Errors (the call is an evaluation error, so a gate fails closed): an invalid pattern, a pattern
  longer than 256 bytes, a path longer than 4096 bytes, a non-string argument.
- When `pattern` is a literal, it is validated at compile time by a `cel.ASTValidators` entry
  (cel-go `cel/validator.go:93-116`, the same mechanism as `ValidateRegexLiterals`), so
  `policygate.ValidateExpression`, the UI's validate endpoint and `kardinal policy simulate` reject
  a bad pattern before a gate is written.

The matcher is `pkg/pathglob`: doublestar v4's `Match` (already a dependency, `go.mod:18`) behind
a pattern check that refuses `{` and the length limits. The RiskProfile reconciler and the Job use
the same package, so a class and a gate agree on every path.

### 5.3 Cost

Without alternatives doublestar keeps one backtrack point for `*` and one for `**`
(`doMatchWithSeparator`, `match.go:95`), so one call is O(len(pattern) × len(path)) in the worst
case. The library declares this to the cost checker
(`checker.OverloadCostEstimate` / `interpreter.OverloadCostTracker`, as cel-go's network library
does in `ext/network.go:309,337`):

- estimate: `ceil(maxLen(pattern) × maxLen(path) / 64) + 1`, with unknown sizes taken as the
  limits (256 × 4096 / 64 = 16,385);
- actual: `ceil(len(pattern) × len(path) / 64) + 1`.

`risk.paths.exists(p, path.glob("db/**", p))` over 64 paths of 100 bytes costs about 64 × 3 =
192, far below the PolicyGate limit of 1,000,000 per evaluation (`celCostLimit`,
`cel_evaluator.go:47`).

### 5.4 Registration

- **PolicyGate environment.** `pkg/cel/library/path.go`, a `cel.Lib` with `LibraryName()` `path`
  and a version option, shaped like kro's `hash.go` (`pkg/cel/library/hash.go:79-100` in kro), added
  to `newEvaluator` (`cel_evaluator.go:107`). Documented in `docs/reference/cel-context.md`
  and `docs/policy-gates.md`.
- **kro.** Proposed as the same library in kro's `pkg/cel/library` and `coreDeclarations`
  (`pkg/cel/environment.go:128-156`), with its cost functions. kro declares no cost functions for
  its libraries today (none in `pkg/cel/library`), and the Graph controller runs without a cost
  limit by default (`--cel-cost-limit` 0, `cmd/controller/main.go:149-152`), so the proposal must
  bound the function itself (the length limits), not rely on the limit. doublestar is already in
  kro's module graph (`go.mod:42`, indirect). Ledger **G23** has the entry; nothing is posted.
- kardinal does not need `path.glob` in Graph CEL for this spec: gates evaluate in the PolicyGate
  reconciler. It is proposed because a Graph user has the same need (an `includeWhen` over a path
  field), and so that one implementation serves both environments once gate CEL moves onto the
  Graph (ledger G8, "PolicyGate CEL").

## 6. `risk.*` CEL variables

Declared in `newEvaluator` as `DynType`, built in `buildContext` only when the expression or
`approval.requiredWhen` contains `risk`, read from the Bundle's ChangeRisk in the manager's cache.
Every key is always present.

| Variable | Type | Value |
|----------|------|-------|
| `risk.scored` | bool | phase `Scored` |
| `risk.phase` | string | `Pending`, `Running`, `Scored`, `Failed` |
| `risk.score` | int | 0 to 100; -1 when not scored |
| `risk.tier` | string | `green`, `amber`, `red`; the profile's `onFailure` when `Failed` |
| `risk.percentile` | int | 0 to 100 among the Pipeline's last 30 scored Bundles (`Bundle.status.risk`); -1 with fewer than 10 |
| `risk.profile` | string | `<namespace>/<name>` of the RiskProfile used |
| `risk.classes` | list(string) | matched class names |
| `risk.paths` | list(string) | up to 64 changed paths, most churn first |
| `risk.pathsTruncated` | bool | more paths changed than `risk.paths` holds |
| `risk.factors` | map | `lines`, `additions`, `deletions`, `files`, `binaryFiles` (int); `actorKind` (string); `history` (int, failures counted); `baseMissing`, `sourceVerified`, `rollback` (bool) |
| `risk.points` | map(string, int) | points per factor |
| `risk.forcedBy` | string | class name, or `""` |

A gate that reads `risk` while the phase is `Pending` or `Running` holds (4.8). The PolicyGate
controller watches ChangeRisks and maps an event to the Bundle's gate instances whose expression or
`requiredWhen` contains `risk`: `instanceGateRequestsWhere` (`pkg/reconciler/policygate/reconciler.go:1514`) with a `readsRisk` predicate over both fields, as `readsAgent` does for the AgentIndex watch (`actor_context.go:47-52,135-140`). (`instanceGateRequests`, `reconciler.go:1505`, the helper the MetricCheck watch uses, reads only the expression.)
`kardinal policy simulate` gains `--risk-tier` and `--risk-score` so a person or an MCP pre-flight
can ask "would a red version be allowed into prod now".

`risk.paths` is for questions that a complete list answers, so an expression over it must check
`risk.pathsTruncated`; classes are computed over every path and need no such check.

## 7. Earned autonomy

### 7.1 Definitions

The record is spec 28's: every figure is computed from the `DeploymentLog` entries of spec 28
(28-actor-analytics.md §5.4), with its definitions (§5.1) and its pure functions in `pkg/delivery`
(`Derive`, `Merge`, `Summarize`, §5.5), onto which `computeStability` is refactored. This spec
defines none of its own, so the agent's record and the Pipeline's `deploymentMetrics` cannot
disagree.

- **Deployment**: a Bundle whose change reached a final environment (a final-environment step
  started its health check with a change to apply), once per Bundle: one DeploymentLog entry. Not
  deployments: no-op promotions, steps a supersession cancelled, rollback Bundles
  (`pipeline_types.go:1485-1494`).
- **Failed deployment**: an entry with `failed: true`: a final-environment step ended `Failed`,
  `AbortedByAlarm` or `RollingBack` after its health check started, a rollback Bundle later rolled
  it back (`rollbackFrom`), or it was rejected after it was deployed (`pipeline_types.go:1496-1503`).

| Field | Definition |
|-------|------------|
| `deployments` | the agent's entries (bucket `agent:<name>`, 28 §5.2) with `deployedAt` in the window, across every DeploymentLog |
| `failedDeployments` | of those, failed |
| `changeFailureRate` | `failedDeployments / deployments` (DORA change fail rate) |
| `rollbacks` | the agent's entries that a rollback entry names in `rollbackFrom` (a subset of failed) |
| `reworkDeployments` | `Summarize`'s rework for the bucket: rollbacks the agent shipped, and fixes (`fixOf`, 28 §5.4) of a production failure (DORA deployment rework) |
| `reworkRate` | `Summarize`'s rework rate: rework deployments over all deployments by the agent, rollbacks included in both (28 §5.1) |

Rework is DORA's: the share of an agent's own deployments that were firefighting. A fixer agent
has a high rework rate by design, so policies should use it for agents that are not fixers, and
`changeFailureRate` and `rollbacks` for every agent. `fixOf` is asserted by the author (28 AA3), so
rework is the one figure an agent can raise or lower by what it claims; policies should not reward
a low rework rate.

**Not in this spec: a pre-production failure rate.** A DeploymentLog records only versions that
reached a final environment, so an agent whose versions a Pipeline keeps out of its final
environments (`environments[].agents: deny`, `pipeline_types.go:200-208`) has no record, and
`agent.trackRecordSufficient` stays false for it (which, in the policies of 7.5, asks for a person).
A rate over pre-production steps needs DeploymentLog to record pre-production outcomes too, a
follow-up of spec 28; reading live PromotionSteps instead would be a second, non-durable source.

**Window.** Time-based: the last `spec.trackRecord.windowDays` days (default 30, 7 to 90) by
`deployedAt`, and `trackRecordSufficient` requires at least `spec.trackRecord.minDeployments`
(default 10) deployments in it. A DeploymentLog keeps entries up to `maxAge` (90 days by default,
28 §5.4), so every allowed window is covered unless `maxEntries` dropped entries (below). The
Pipeline metrics use the last 30 deployments per Pipeline instead; that window cannot be summed
across Pipelines, so the agent window is by time, as spec 28's (28 §5.7). RADAR's per-source
lookback is 60 days with a minimum count; 30 is DORA's usual period.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
spec:
  trackRecord: {windowDays: 30, minDeployments: 10}
status:
  trackRecord:
    windowStart: "…"
    deployments: 42
    failedDeployments: 3
    changeFailureRateMillis: 71
    rollbacks: 1
    reworkDeployments: 4
    reworkRateMillis: 93
    byTier:                                  # from the entries' riskTier (7.2); "unscored" without one
      green: {deployments: 30, failedDeployments: 0}
      amber: {deployments: 10, failedDeployments: 2}
      red:   {deployments: 2, failedDeployments: 1}
    sufficient: true
    truncated: false                         # a counted DeploymentLog dropped or reseeded entries inside the window (7.2)
    lastFailureAt: "…"
    pipelines: 7
    computedAt: "…"
```

Rates are integer thousandths, as `changeFailureRateMillis` is (`pipeline_types.go:1505-1508`).

### 7.2 Where the records live

Bundles are deleted beyond `historyLimit`, so a 30-day window cannot be recomputed from Bundles.

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | The track-record reconciler recomputes from live Bundles and PromotionSteps in every namespace | No new state | Loses everything older than the newest 50 Bundles per Pipeline; lists Bundles and steps cluster-wide on every reconcile |
| (b) | The Pipeline reconciler writes a bounded record per agent deployment into `Pipeline.status.agentDeployments` | Durable past Bundle GC | The Pipeline status is the controller's most-written object (#1719): every byte added is written on every step change. Spec 28 rejected it as a store for the same records (28 §5.3 (b)) |
| (c) | Spec 28's `DeploymentLog` per Pipeline (28 §5.4), read by the track-record reconciler | Durable past Bundle GC (90 days, `historyLimit` guard 28 §5.6); one definition with the Pipeline metrics and the fleet view; written once per deployment transition; tenant-local | Earned autonomy waits for spec 28's log to land (merge order below); the log must carry each entry's risk tier |
| (d) | A new `DeploymentRecord` CR per deployment, with retention | Unbounded history; easy queries | One object per deployment; duplicates spec 28's log |
| (e) | Derive from AuditEvents (`PromotionSucceeded`, `PromotionFailed`, `RollbackStarted`) | Already durable, with the actor (spec 17) | A second, different definition of a deployment and a failure; AuditEvents have their own retention per Pipeline (`--audit-retention-max-per-pipeline`) |

**Choice: (c), spec 28's DeploymentLog.** (The first version of this spec chose (b); the
coordinator's decision after the spec 24 review moved it, since Pipeline status is not a store.)

**The exact mechanism, read path to gate:**

1. `pkg/reconciler/deploymentlog` (spec 28) writes `DeploymentLog.status.entries` from the
   Pipeline's Bundles and steps, in the Pipeline's namespace, only its own status. This spec adds
   one field to an entry, `riskTier` (`green`, `amber`, `red`, or empty), copied from
   `Bundle.status.risk.tier` (4.7) when the Bundle has one; the log reconciler already reads the
   Bundle. It is fixed once the entry is `final`, as the rest of the entry.
2. A new **track-record reconciler** (per AgentIdentity) lists the DeploymentLogs that hold its
   agent through a field index on `status.actors[].key` (`agent:<name>`, 28 §5.4) from the
   manager's cache, calls `pkg/delivery.Summarize` over the agent's entries in its window, and
   writes `AgentIdentity.status.trackRecord`: its own status, read from another kind's status, no
   cross-kind write. A DeploymentLog watch whose predicate fires only when
   `status.actors` or an agent's entries changed maps the event to the agents in the old and new
   logs. It requeues itself at the time the oldest counted entry leaves the window; reading the clock
   there is allowed, since it writes the result to its own status (`computedAt`).
3. The PolicyGate reconciler builds `agent.*` (7.3) from `AgentIdentity.status.trackRecord` read
   from the cache, as it builds the rest of `agent.*` today (`actor_context.go:69-99`). **No gate
   lists DeploymentLogs** or computes a rate: the gate path reads one status field, which a
   reconciler wrote before the gate ran (spec 28 §10 requires exactly this).

Neither a Graph node nor a Graph `ref` is involved: `agent.*` is the PolicyGate reconciler's
context, as today (`schedule.*` and `metrics.*` are too), and the track record is
reconciler-written status, the Graph-first form for data that must outlive every Bundle (a Graph is
per Bundle and is deleted with it, ledger G15).

- **Truncation.** A DeploymentLog drops entries beyond `maxEntries` (100 by default) and sets its
  `Truncated` condition (28 §5.4); a deleted log is recreated and seeded from the live objects with
  the condition `Reseeded` (28 §12). The agent's `trackRecord.truncated` is true when a counted log
  is `Truncated` and its oldest kept entry is newer than the window start, or is `Reseeded` with the
  condition's `lastTransitionTime` inside the window: in both, entries of the window may be gone.
  Policies can require `!agent.trackRecordTruncated`.
- **Not the IndexReconciler.** Today's AgentIdentity status writer is `agentidentity.IndexReconciler`,
  which handles one fixed request and runs one `actor.Evaluate` over every AgentIdentity, writing
  the AgentIndex and every AgentIdentity's status (`pkg/reconciler/agentidentity/reconciler.go:97-100,
  157,388-401`), and watches AgentIdentities with no predicate. Its watch gains a predicate that
  ignores `status.trackRecord` changes, so a track-record write does not rebuild the index, and the
  two reconcilers write disjoint status fields (`trackRecord`; `ready`, `reason`, `conditions`) with
  merge patches.
- **Sharding.** AgentIdentity is cluster-scoped. The track-record reconciler registers through
  `shard.Active().Complete`, so its cluster-scoped requests run only on the shard that
  `OwnsClusterScoped` (`pkg/shard/shard.go:242,286-291`); the DeploymentLog reconciler is sharded
  by namespace (28 §5.5). The manager's cache is cluster-wide unless `--watch-namespace` is set
  (`cmd/kardinal-controller/manager_options.go:165-177`); the shard gate filters reconciles, not the
  cache, so the owning shard reads every DeploymentLog. With `--watch-namespace` the record is that
  namespace's only; the docs say so.
- **Merge order.** Spec 28's DeploymentLog (its §5.4-5.6, `pkg/delivery`, the reconciler and the
  `historyLimit` guard) lands before this spec's earned autonomy (§18 rows 9 and 10). The risk score
  (§4-6) does not depend on it.
- **Ledger.** Spec 28's G8 row says of the log "Display data: no promotion decision reads it". With
  earned autonomy a decision reads it, through `AgentIdentity.status.trackRecord`; whichever PR lands
  second updates that row (§19).

**What it cannot do, and what would break it:**

- **No pre-production failure rate (CR19).** The log holds final-environment deployments only (28
  §2, "Deployment"), so an agent's failures in test or uat are not in its record, and an agent kept out of
  every final environment (`environments[].agents: deny`) has no record at all; its
  `trackRecordSufficient` stays false, which in the policies of 7.5 asks for a person. A
  pre-production rate needs spec 28 to log pre-production outcomes (§18 row 16); reading live
  PromotionSteps in the meantime would be a second, non-durable source with its own definition.
- **Not live.** The record is as fresh as two reconcilers: the log's, on a final-environment step's
  transition, then the track record's, on the log's change. A gate evaluated between a failure and
  the track-record write sees the previous record (`computedAt` says when it was made). Stopping an
  agent at once after a failure is spec 17's suspension, not this.
- **One cluster.** An agent's deployments through another cluster's controller are not counted;
  with `--watch-namespace`, only that namespace's (Sharding, above).
- **A deleted log loses its history.** Spec 28 reseeds it from the live objects, so failures older
  than the retained Bundles are gone. Spec 28's admission covers CREATE and UPDATE only (28 §11), so
  `<release>-risk-owners` also refuses an agent's DELETE of a DeploymentLog (§11), and a reseed
  inside the window sets `trackRecord.truncated` (above), so a person's deletion is visible to
  policies.
- **Asserted rework.** `fixOf` is the author's claim (28 AA3): the rework rate is the one figure an
  agent moves by what it says (7.1).
- **It starts short.** At upgrade the log holds what spec 28 seeds from the retained Bundles (§15).
- **What would break it:** a gate that lists DeploymentLogs or computes a rate (the gate path must
  read one status field); an entry's `riskTier` changing after the entry is `final`; the
  track-record reconciler writing anything but `AgentIdentity.status.trackRecord`, or the
  IndexReconciler reconciling on that write (a loop between the two).

The budgets item of v0.14 (spec 24) does not count from these records: it keeps its own ledger in
`AgentIdentity.status.usage` (24-agent-budgets.md §5.2). The two can be reconciled later if the
definitions converge.

### 7.3 CEL: `agent.*` additions

Added to the `agent` map of spec 18 §2.1, read from `AgentIdentity.status.trackRecord` (status
only, as the rest of `agent.*`). For a Bundle whose actor is not an agent, every value is zero,
`0.0` or `false`.

| Variable | Type |
|----------|------|
| `agent.deployments`, `agent.failedDeployments`, `agent.rollbacks`, `agent.reworkDeployments` | int |
| `agent.changeFailureRate`, `agent.reworkRate` | double (millis / 1000) |
| `agent.trackRecordSufficient`, `agent.trackRecordTruncated` | bool |
| `agent.tiers.<green\|amber\|red>.deployments`, `.changeFailureRate` | int, double |

Doubles, so `agent.changeFailureRate > 0.15` compares a double with a double. The PolicyGate
environment does not enable `cel.CrossTypeNumericComparisons` (cel-go `cel/options.go:966-967`),
so the spec does not rely on comparing an int with a double. Spec 18's AgentIndex watch
(`reconciler.go:1481-1483`) does not see status changes of the track record, so the
PolicyGate controller also watches AgentIdentities, with a predicate on `status.trackRecord`, and
maps the event to the gates of the agent's in-flight Bundles that read `agent.` (an index on
`spec.actor.agent` of Bundles).

### 7.4 How autonomy tightens and relaxes

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Policies read the record in CEL (`approval.requiredWhen`, gate expressions) | CEL only; any threshold; live; explainable in the gate reason | Each team writes its thresholds |
| (b) | A controller changes `spec.autonomy` from the record | One switch | A controller rewriting an admin's spec; flapping; the reason is lost |
| (c) | A new autonomy value `earned` in spec 18's `agents:` shorthand, which the translator compiles to (a) with default thresholds | Short for the common case | One more value to define; still (a) underneath |

**Choice: (a) now; (c) as a follow-up** once the thresholds have been used (§18). The policy
relaxes on its own when the record improves, which is what "earned" means.

### 7.5 Example policies

```yaml
# Prod: an agent needs a release manager unless its record earns it out.
approval:
  required: 1
  allowedGroups: [release-managers]
  excludeAuthor: true
  requiredWhen: >-
    actor.kind == 'agent' && (
      !agent.trackRecordSufficient ||
      agent.changeFailureRate > 0.15 ||
      agent.rollbacks > 1 ||
      risk.tier == 'red')
---
# A recent rollback stops an agent's red changes outright.
expression: "!(actor.kind == 'agent' && agent.rollbacks > 0 && risk.tier == 'red')"
message: "agent has a rollback in its window; red changes need a person"
---
# Gaming check: a record built only from green changes does not earn red autonomy.
requiredWhen: "actor.kind == 'agent' && risk.tier != 'green' && agent.tiers.amber.deployments + agent.tiers.red.deployments < 5"
```

## 8. Risk-adaptive soak and approvals

### 8.1 Soak

The variables exist: `upstream.<env>.soakMinutes` (minutes since this Bundle was Verified there,
`pkg/reconciler/policygate/reconciler.go:894-908`) and `bundle.upstreamSoakMinutes` (the minimum
over the gated environment's direct upstreams, `reconciler.go:635-647,744-775`; computed only when
the expression names it, not for `requiredWhen`). With `risk.*`:

```yaml
expression: 'upstream.uat.soakMinutes >= (risk.tier == "red" ? 720 : risk.tier == "amber" ? 180 : 60)'
message: "uat soak scales with change risk"
```

Nothing else is needed. The gate re-evaluates on its `recheckInterval` (default 5m,
`reconciler.go:73`) and on every ScheduleClock tick, as soak gates do today (the PolicyGate
controller has no Bundle watch; `docs/policy-gates.md` lists what wakes a gate). The `Bake` of an
environment (`pipeline_types.go:257-262, 496-529`) is a different thing, a contiguous healthy window inside the step; it stays static per
environment, and §9 makes it per wave.

### 8.2 Approvals by tier

Approvals are per Bundle and environment, not per gate (`api/v1alpha1/approval_types.go:22-34`),
so every approval gate of an environment counts the same Approvals.

| | Mechanism | For | Against |
|-|-----------|-----|---------|
| (a) | Two gates, each with spec 18's `approval.requiredWhen`: one `required: 1` always, one `required: 2` with `requiredWhen: risk.tier == 'red'` | No new field; `kardinal inbox` and `why` show each policy, waived or not (`ApprovalStandingOf`, `pkg/lifecycle/approval_tally.go:396`); the UI shows each gate's policy and tally (`cmd/kardinal-controller/ui_api.go:668`) | Two gates for one rule |
| (b) | `approval.requiredExpression`: a CEL int (`risk.tier == 'red' ? 2 : 1`) | One gate | A new field the CLI and UI must evaluate to show "1 of 2"; duplicates (a) |
| (c) | A gate expression `approvals.count >= (risk.tier == 'red' ? 2 : 1)` without `spec.approval` | No new field | The inbox and `why` do not see it as an approval waiting on someone; the one-party rule applies to approvals only through `spec.approval` (to verdicts through `spec.verify.excludeAuthor`) |

**Choice: (a).** The existing variables plus `requiredWhen` are enough:

```yaml
# prod-approval
approval: {required: 1, allowedGroups: [release-managers], excludeAuthor: true,
           requiredWhen: "risk.tier != 'green' || actor.kind != 'human'"}
---
# prod-approval-red
approval: {required: 2, allowedGroups: [release-managers, sre-leads], excludeAuthor: true,
           requiredWhen: "risk.tier == 'red'"}
```

The verify gate of spec 19 composes the same way: RADAR's LLM review stage becomes an AgentRun
verifier required for amber and red (`verify` with a `requiredWhen`, which spec 19's verify block
does not have yet; §18 adds it, the same semantics as the approval's).

### 8.3 DRS gating levels

The DRS weekend, yellow and red levels are combinations of variables kardinal already has:

```yaml
# Weekend: block the riskiest 5% (DRS "weekend"); everything else as usual.
expression: "!(schedule.isWeekend && risk.percentile >= 95)"
---
# A declared high-traffic period: a ChangeWindow "peak" blocks amber and red.
expression: "!(changewindow.isBlocked('peak') && risk.tier != 'green')"
```

`peak` is a `blackout` ChangeWindow over the high-traffic period. A ChangeWindow without
`spec.freeze` acts only through the gates that reference it (`api/v1alpha1/changewindow_types.go:15-20`),
so this gate blocks amber and red versions and lets green ones through. `peak` must not set
`spec.freeze` (`changewindow_types.go:42-46`, spec 18 §9): a freeze holds every version on its own.

### 8.4 A docs-only fast path

```yaml
expression: 'risk.scored && !risk.pathsTruncated && risk.paths.all(p, path.glob("docs/**", p) || path.glob("**/*.md", p))'
```

It is used as `requiredWhen: "!(<that>)"` on an approval, so a docs-only change needs none.
`!risk.pathsTruncated` is required: `all()` over a truncated list could pass for a change that also
touched code.

## 9. Fleet wave presets

### 9.1 Today

A fleet environment expands into one environment per target, in target order
(`pkg/graph/fleet.go:85-142`); `maxConcurrent` admits at most N targets in flight, ranked by
order (`pkg/graph/compact.go:347-366`); `maxUnavailable` stops new starts after N failures
(`compact.go:359-366`); an environment after the fleet waits for every target (`fleet.go:170-210`).
That is a sliding window, not waves: with `maxConcurrent: 3` the fourth target starts as soon as
any of the first three is Verified. There is no "this group must be done and baked before the next
starts", and the bake is one value for every target (`EnvironmentSpec.Bake`, copied to each target,
`fleet.go:127-139`). Fleets are only built in the compact shape (`compact.go:80-113`).

### 9.2 The AWS model mapped

| AWS (2.3) | kardinal |
|-----------|----------|
| Waves 1 to 5: 1 Region (low traffic), 1 Region (high traffic), 3, up to 12, the rest | Wave sizes over the fleet's targets in their order; the user lists the low-traffic target first |
| Regions of a wave in parallel | Targets of a wave admitted together (subject to `maxConcurrent` if set) |
| Bake after a wave: 12 h after the first regional wave, 2 to 4 h after the others | Per-wave bake: each target's step `Bake.minutes` (a contiguous healthy window before Verified); the next wave starts when every target of the wave is Verified |
| One-box stage before each wave, 1 h bake | Not a wave. It is a canary inside a target (an Argo Rollouts or Flagger health adapter, `docs/health-adapters.md`), or a separate one-target fleet environment before the fleet |
| Wait for a number of data points | A MetricCheck gate on the fleet environment (`metrics["create-requests"].value >= 100`); not part of the preset |
| Roll back on the aggregate alarm during the bake | `bake.policy: fail-on-alarm` with `onHealthFailure: rollback`, and `maxUnavailable: 1` |
| No deployments at night or on weekends | A ChangeWindow; not part of the preset |

### 9.3 The API

| | Shape | For | Against |
|-|-------|-----|---------|
| (a) | A named preset only: `fleet.preset: aws` | One line | Opaque; cannot be adjusted; a later change to the preset changes everyone's rollout |
| (b) | Explicit waves only: `fleet.waves: [{size, bake}, ...]` | Exact; reviewable | Verbose for the common case; every team copies the AWS numbers |
| (c) | Explicit `fleet.waves`, plus `fleet.wavePreset` that supplies the list when `waves` is empty | The common case is one line; the mechanism is the explicit list; `kardinal explain` prints the expanded list | Two fields; the preset values are an API promise |
| (d) | Split the fleet by hand into several fleet environments with `wave:` numbers (`pipeline_types.go:227-240`) | Works today | Fleet-wide pause, hold, rollback and `maxUnavailable` (#1835, shipped) stop at each environment; five copies of the fleet config |

**Choice: (c).**

```yaml
environments:
  - name: prod
    fleet:
      selector: {kind: ClusterProfile, matchLabels: {tier: prod}}
      wavePreset: aws          # aws | aws-fast; used when waves is empty
      waves:                   # explicit form; wins over the preset
        - {size: 1, bakeMinutes: 720}
        - {size: 1, bakeMinutes: 240}
        - {size: 3, bakeMinutes: 240}
        - {size: 12, bakeMinutes: 240}
        - {bakeMinutes: 240}   # no size: the rest
      maxUnavailable: 1        # the preset's default when unset
```

| Preset | Waves (size, bake) | Default `maxUnavailable` |
|--------|--------------------|--------------------------|
| `aws` | (1, 12 h), (1, 4 h), (3, 4 h), (12, 4 h), (rest, 4 h) | 1 |
| `aws-fast` | (1, 1 h), (1, 1 h), (3, 1 h), (12, 1 h), (rest, 1 h) | 1 |

`aws` takes the upper end of "two to four hours" and the article's 12 hours after the first wave.
`aws-fast` is for pre-production fleets and is not from the article. The values are frozen:
changing them is a breaking change with a new preset name.

Rules:

- `size` is a count (1 or more) or a percentage of the targets (`"10%"`, rounded up, at least 1), as
  `maxUnavailable` (`fleet.go:145-168`). The last wave may omit it; only the last.
- A fleet smaller than the waves fills them in order and drops the empty ones: 4 targets with `aws`
  give (1), (1), (2).
- `bakeMinutes` replaces the environment's `bake.minutes` for that wave's targets. The bake policy,
  `maxDuration` and health come from the environment (and a target's `health` override) as today.
  0 means no bake.
- `maxConcurrent` still caps the targets in flight, inside a wave too.
- Validation in the schema: at most 10 waves; sizes positive; only the last without a size;
  `waves` and `wavePreset` not both set.
- Targets of a selector fleet (Application, ClusterProfile) are ordered by name, as `status.fleets`
  resolves them (`pkg/reconciler/pipeline/fleets.go:46-48`); that order is the wave order. To put
  the low-traffic target first, use static targets (their list order) or a new
  `kardinal.io/fleet-order` label on the selected objects, which `fleetTargets` (`fleet.go:45-79`)
  would sort by, then by name, when present.

### 9.4 Mechanism

- **Builder** (`pkg/graph/fleet.go`): `fleetMembers` assigns each target its wave and sets the
  target's `spec.Bake.Minutes` from the wave (`fleet.go:127-139`, where the target's spec is built),
  so the PromotionStep reconciler, which reads the target's spec through `EnvironmentSpecFor`
  (`fleet.go:225-240`, `findEnv` at `pkg/reconciler/promotionstep/reconciler.go:3008-3015`), bakes it without a
  change. `expandFleets` (`fleet.go:176-210`) gives each target of wave k > 1 the targets of wave
  k − 1 as upstreams, in place of the fleet's upstreams. Every consumer of the DAG (`SoakUpstreams`,
  `pkg/graph/upstreams.go:44-56`, over `DirectUpstreams`, for `bundle.upstreamSoakMinutes`;
  `kardinal explain`; the UI) then sees the waves.
- **Compact admission** (`compact.go:326-366`): already waits for `upstreamGroups`, one per
  fleet, done when every member is Verified or Superseded and at least one is Verified
  (`compact.go:333-343`, `upstreamGroups` at `compact.go:617`). Grouping changes from "per fleet"
  to "per fleet and wave", so a wave-2 target's group is the wave-1 targets. `maxConcurrent`,
  `maxUnavailable` and `paused` apply unchanged. The DAG entries gain `wave` (shown, not read by
  CEL).
- **Status**: no new list. `Pipeline.status.environments` already has one entry per fleet target
  (a fleet environment expands to its targets, `pkg/reconciler/pipeline/environments.go:32-39`),
  with its `state` and `since`; each entry gains `fleetWave` (the target's wave; 0 outside a fleet
  with waves), which the UI and `kardinal get pipelines` group by (`prod  wave 2/5  1/1 Verified`).
  A separate list of each wave's target names would repeat every target name in the Pipeline
  status, the controller's most-written object (#1719), and `Pipeline.status.fleets[]` cannot hold
  it either: that list holds only resolved selector fleets (Application and ClusterProfile), not
  static fleets or `Target` selectors (`pkg/reconciler/pipeline/fleets.go:46-62`,
  `pipeline_types.go:1281-1287`). `fleetWave` is display only, as the rest of `environments` (no
  controller decision reads it, `pipeline_types.go:1289-1293`); the builder assigns waves from the
  spec and the targets.

What would break this: a failed target keeps its wave from completing, so the next wave never
starts, which is the intent (AWS stops on an alarm). A target removed from a selector fleet while
its wave is in flight is kept by `keepRemovedFleetTargets` (`pkg/reconciler/bundle/reconciler.go:1048-1056`).
A membership change rebuilds an in-flight Bundle's Graph in place (`pipeline_types.go:736-741`;
FLEET-04), so the wave assignment of its remaining targets shifts too; steps already started keep
their spec.

### 9.5 Risk-adaptive waves

A gate on the fleet environment applies to every target (`pipeline_types.go:242-249`), and with wave
edges `bundle.upstreamSoakMinutes` of a wave-k target is the soak of wave k − 1, so the bake between
waves can scale with risk without any Graph change:

```yaml
expression: 'bundle.upstreamSoakMinutes >= (risk.tier == "red" ? 720 : risk.tier == "amber" ? 240 : 60)'
```

For the first wave the upstream is the environment before the fleet, so the same gate also asks
that environment to soak. Choosing a different wave list per tier in the Graph would need the step
spec to read the ChangeRisk; a PromotionStep template node with a pending field is wholly Unresolved (ledger G14),
so all steps would wait on the score (option (c) of 4.8). Not done.

## 10. API sketch (Go)

```go
// api/v1alpha1/changerisk_types.go
type ChangeRiskSpec struct {
    PipelineName string            `json:"pipelineName"`
    BundleName   string            `json:"bundleName"`
    BundleUID    string            `json:"bundleUID"`
    ProfileRef   RiskProfileRef    `json:"profileRef"`
    Sources      []ChangeRiskSource `json:"sources"` // MaxItems=2
}
type ChangeRiskSource struct {
    Kind       string `json:"kind"`                 // Enum=source;config
    RepoURL    string `json:"repoURL"`
    SecretName string `json:"secretName,omitempty"`
    Head       string `json:"head"`                 // full SHA
}
type ChangeRiskStatus struct {
    Phase          string             `json:"phase,omitempty"` // Enum; terminal rule
    Message        string             `json:"message,omitempty"`
    JobName        string             `json:"jobName,omitempty"`
    JobUID         string             `json:"jobUID,omitempty"`
    SpecHash       string             `json:"specHash,omitempty"`
    Profile        *RiskProfileUsed   `json:"profile,omitempty"`
    Base           *ChangeRiskBase    `json:"base,omitempty"`
    Diff           *ChangeRiskDiff    `json:"diff,omitempty"`
    SourceVerified bool               `json:"sourceVerified,omitempty"`
    Score          int                `json:"score"`            // -1..100
    Tier           string             `json:"tier,omitempty"`   // Enum=green;amber;red
    Percentile     int                `json:"percentile"`       // -1..100
    Factors        []RiskFactor       `json:"factors,omitempty"` // MaxItems=16
    ForcedBy       string             `json:"forcedBy,omitempty"`
    ScoredAt       *metav1.Time       `json:"scoredAt,omitempty"`
    Conditions     []metav1.Condition `json:"conditions,omitempty"`
}
type ChangeRiskDiff struct {
    Additions, Deletions, Files, BinaryFiles int
    Classes        []RiskClassCount `json:"classes,omitempty"` // MaxItems=32
    Keys           []string         `json:"keys,omitempty"`    // MaxItems=32
    Paths          []string         `json:"paths,omitempty"`   // MaxItems=64
    PathsTruncated bool             `json:"pathsTruncated,omitempty"`
}
type RiskFactor struct{ Name, Value string; Points int }

// api/v1alpha1/riskprofile_types.go
type RiskProfileSpec struct {
    Classes   []RiskClass     `json:"classes,omitempty"` // MaxItems=32
    Weights   RiskWeights     `json:"weights,omitempty"`
    History   RiskHistory     `json:"history,omitempty"`
    Tiers     RiskTiers       `json:"tiers,omitempty"`
    OnFailure string          `json:"onFailure,omitempty"` // Enum=green;amber;red; default red
    Rollback  string          `json:"rollback,omitempty"`  // Enum=exempt;score; default exempt
    Limits    RiskLimits      `json:"limits,omitempty"`
}
type RiskClass struct {
    Name      string   `json:"name"`                // CEL identifier pattern
    Globs     []string `json:"globs"`               // 1..16, MaxLength=256
    Points    int      `json:"points,omitempty"`    // 0..100
    ForceTier string   `json:"forceTier,omitempty"` // Enum=amber;red
}

// Pipeline
type PipelineRisk struct {
    ProfileRef RiskProfileRef `json:"profileRef"`
    Source     *RiskSource    `json:"source,omitempty"`
}
type FleetWave struct {
    Size        *intstr.IntOrString `json:"size,omitempty"`
    BakeMinutes *int                `json:"bakeMinutes,omitempty"`
}
// FleetSpec gains: Waves []FleetWave (MaxItems=10); WavePreset string (Enum=aws;aws-fast)
// PipelineEnvironmentStatus gains: FleetWave int (display only; 0 outside a fleet with waves) (9.4)
// DeploymentLogEntry (spec 28) gains: RiskTier string (Enum=green;amber;red, optional) (7.2)
// BundleStatus gains: Risk *BundleRisk {Name, Phase, Score, Tier, Profile, Keys, ScoredAt}
// AgentIdentitySpec gains: TrackRecord *TrackRecordSpec {WindowDays, MinDeployments}
// AgentIdentityStatus gains: TrackRecord *TrackRecordStatus (7.1)
```

## 11. RBAC and admission

- **ChangeRisk**: written by kro. The chart adds `changerisks` to the Graph applier ClusterRole and to
  the kro-watch ClusterRole (`chart/kardinal-promoter/templates/graph-rbac.yaml:24,51`), as for
  ImageVerification; the IdentityProvisioner (`pkg/graph/identity.go:33-49,80-90`), which only
  binds that applier role, is unchanged. Status only by the controller; the chart's user roles grant
  no `changerisks/status`. `changerisks` joins `$graphKinds` of the `<release>-graph-objects`
  admission policy (`identity-admission.yaml:538`, the ledger G17 workaround), which refuses a
  ChangeRisk created or changed by anyone but kro and the controller, so a team cannot pre-create a
  green record under the expected name; the spec is immutable by a CRD `self == oldSelf` rule, as
  ImageVerification's (`imageverification_types.go:348`).
- **RiskProfile**: written by `<release>-admin-extra` (aggregated to the built-in `admin`); the
  chart has no `-edit` role. An org profile lives in an org policy namespace, where teams have no
  write. `riskprofiles` and `changerisks` join `userRoles.readRules` (`user-roles.yaml:66-68`),
  which `test/helm/chart_test.go` requires to list every CRD.
- **`<release>-risk-owners`** (new, §14.1): a ValidatingAdmissionPolicy, with the AgentIndex as
  params as the actor policies take it, that refuses a CREATE, UPDATE or DELETE of a RiskProfile,
  an UPDATE of a Pipeline that changes `spec.risk`, and a DELETE of a DeploymentLog (7.2: a reseeded
  log forgets failures older than the retained Bundles), by a requester the AgentIndex names as an
  agent. A person, a bot (CI applying GitOps) and the controller are not judged. Fail closed when
  the index is not ready, as `<release>-actor` (spec 17 §5).
- **Pipeline `spec.risk.profileRef`**: the namespace must be the Pipeline's own or an org policy
  namespace (CRD CEL rule for the shape; the controller checks the namespace list and sets
  `Ready=False` with `RiskProfileNamespace` otherwise). The third way to reach another namespace
  since v0.13, a Pipeline's `spec.policyNamespaces` with the owner's consent
  (`kardinal.io/policy-shareable`, `pipeline_types.go:89-100`), is not allowed for a profile: a
  profile is a trust anchor, and only an org policy namespace, which teams cannot write, may hold a
  profile another namespace uses.
- **Source Secret**: in the Pipeline's namespace, labelled `kardinal.io/referenceable=true`, read
  uncached by name; the controller's own SCM token Secret is refused with
  `scm.ControllerTokenSecret.RefusesSecret` (`pkg/scm/controller_secret.go:38-40`), as RenderRun
  does (`renderrun/reconciler.go:479`). Only the Job mounts it.
- **Job**: in the Pipeline's namespace, no ServiceAccount token, the NetworkPolicy of 4.5. The
  controller holds `get, list, watch, create, delete` on `jobs` (for HookRun and RenderRun,
  `_rbac.tpl:105-111`) and `get, create` on `networkpolicies` (for AgentRun, `_rbac.tpl:112-118`);
  the Job's NetworkPolicy is owned by the ChangeRisk and garbage-collected with it, as AgentRun's.
- **AgentIdentity `spec.trackRecord`**: cluster admins, as the rest of AgentIdentity.
  `status.trackRecord` only by the controller.
- **DeploymentLog**: spec 28's rules (28 §11): the controller only. The track-record reconciler
  reads it from the cache; nothing here writes it but spec 28's reconciler (`riskTier` included).
- **Fleet waves**: schema validation only.

## 12. Failure modes

| Failure | Effect | Why that is safe |
|---------|--------|------------------|
| Source repository unreachable, credentials missing or wrong | ChangeRisk `Failed`, `SourceUnavailable`; tier `onFailure` (red) | Fails closed; the reason is in `kardinal why` |
| Job exceeds `timeout` or `maxRepoBytes` | `Failed`, `Timeout` / `TooLarge`; tier `onFailure` | A change too big to measure is treated as risky |
| Job Pod deleted, Job re-created by someone | `jobUID` mismatch: `Failed`, reason `JobReplaced`, following RenderRun's rule (`renderrun/reconciler.go:185-196`; RenderRun's status has no reason field, only the message) | No second, different result |
| Controller restarts while Running | Reconciler reads the Job by name and UID and continues | Idempotent: the Job is created once |
| RiskProfile not ready or deleted | `Failed`, `ProfileNotReady` | Fails closed |
| RiskProfile edited after scoring | Score unchanged; `status.profile.generation` records the one used | A score is a record, not a live value |
| Pipeline has no `spec.risk`, gate reads `risk` | Gate evaluation error | Fails closed, as an unknown ChangeWindow |
| Base Bundle deleted between base choice and Job | Base commits are already in `status.base`; nothing changes | Base is recorded before the Job |
| `head` asserted wrongly by CI | Score of the wrong commit; `sourceVerified: false` adds points (a person's or a bot's version); an agent's is not scored | The limit is stated (4.2); attestations remove it |
| A DeploymentLog dropped entries (`maxEntries`) inside an agent's window | `trackRecord.truncated` true for that agent | Policies can require `!agent.trackRecordTruncated` |
| A person deletes a DeploymentLog (an agent's DELETE is refused, §11) | Spec 28 recreates and reseeds it from the live objects (condition `Reseeded`, 28 §12); failures older than the retained Bundles leave the agent's record | `trackRecord.truncated` is true while the reseed is inside the window (7.2); policies can require `!agent.trackRecordTruncated`, and fewer deployments make `trackRecordSufficient` false sooner, which asks for a person (7.5) |
| Track-record reconciler behind | `trackRecord.computedAt` old; policies see the last record | The record changes slowly; a suspension (spec 17) does not depend on it |
| Wave target fails | Wave never completes, next wave never starts; `maxUnavailable` stops starts | AWS behaviour: stop on alarm |

## 13. Scale

- **Per Bundle**: one ChangeRisk object (about 3 KB status), one Job run (seconds to minutes; a
  depth-1 fetch of two commits). A Pipeline that creates 200 Bundles a day starts 200 Jobs. The
  ChangeRisk reconciler limits concurrent Jobs with a `slots.Limiter` of its own (`pkg/slots`,
  shipped in #2123; MetricCheck and ChangeRequest each have one, `cmd/kardinal-controller/main.go:423-437`),
  flags `--change-risk-max-jobs` (default 8) and `--change-risk-max-jobs-per-namespace` (default 2). Supersession lowers the
  load: the reconciler does not create the Job of a Bundle that is Superseded or Rejected before it
  starts (phase `Failed`, reason `Superseded`, no tier needed: no gate of that Bundle will run).
- **Graph**: one node per Bundle; it adds one object to G9's per-walk cost and counts toward
  `MaxGraphObjects` (4,500, `pkg/graph/size.go:28`), with about 200 bytes of spec and a 260-byte
  inventory estimate toward `MaxGraphBytes` (1,200,000, `size.go:22,35`; G10).
- **Gates**: `risk` is read from the cache, only for gates that mention it.
- **History factor**: reads the Pipeline's retained Bundles from the cache (`ListPipelineBundles`,
  indexed), once per ChangeRisk.
- **Agents**: the track-record reconciler, a new per-agent reconciler (not the IndexReconciler,
  which evaluates every AgentIdentity in one pass and must not read logs), reads only the
  DeploymentLogs its field index names. For an agent active in 1,000 Pipelines that is 1,000 cached
  logs (spec 28 §13 budgets about 25 KB each) and up to 100,000 entries per reconcile; reconciles of
  one agent are coalesced by the work queue. A log holds at most `maxEntries` (100) entries, so the
  cost does not grow with time.
- **Fleets**: waves add no node; the admission expression is the existing one with a different
  grouping. Wave edges add at most (size of wave k − 1) upstreams per target of wave k: for the
  `aws` preset on 500 targets, wave 5 (483 targets) each lists 12 upstreams, about 6,000 entries in
  the DAG `def`, inside the existing G10 check (`pkg/graph/size.go`). Explicit waves can be larger (a wave
  of 100 followed by 400 targets would write 40,000 entries), so the DAG data stores each wave's
  member list once and each entry names its upstream wave by index, keeping the size O(targets).
  The implementation measures it in the scale suite.
- **Multi-tenant**: profiles, ChangeRisks, Jobs and Secrets stay in the Pipeline's namespace; an org
  profile is read-only to teams.

## 14. What this does not protect against

- **A wrong head commit.** Without verified provenance, the score is of the commit CI named (4.2).
- **Changes outside the diff.** A risky value in a Secret, a cluster setting, a dependency bumped in
  the base image: none are in the commit range.
- **Calibration.** Defaults are judgement (4.6). A green tier means "the profile gives few points",
  not "safe".
- **Gaming by splitting.** An agent can split a large change into small versions. Supersession and
  budgets (v0.14) limit the rate; the track record by tier (7.5) keeps small greens from earning red
  autonomy.
- **Padding the record.** Trivial real deployments count toward `deployments`. Use the per-tier
  counts.
- **Path names chosen by the author.** A file named `docs/x.md` that a build includes as code is
  matched as docs. Classes describe paths, not what the build does with them.

### 14.1 Can an agent lower its own risk score?

Every input of the score, who controls it, and what keeps the author of a version (an agent, or
the person or CI job behind it) from lowering it. "Author" is whoever created the Bundle.

| Input | Who sets it | Can the author lower it? | What stops it |
|-------|-------------|--------------------------|---------------|
| `actor` factor | the Bundle's actor stamp | No | Admission checks the stamp against the AgentIndex (`<release>-actor`, spec 17): an agent cannot stamp itself human or bot |
| `head` (image Bundle) | `spec.provenance.commitSHA`, written by the author | **Yes, without verified provenance**: it names a small commit, and the Job scores that | `requireVerifiedSource` (default `[agent]`): an agent's version is scored only from the source commit of verified SLSA provenance (spec 20); without it the ChangeRisk fails `SourceUnverified` and takes the `onFailure` tier (red). A person's or a bot's version gets the `sourceUnverified` points instead, a deliberate trade for installations without attestations. The provenance binds the image digest to the commit, so the author cannot build one commit and name another |
| `head` (config Bundle) | `configRef.commitSHA`, written by the author | It names a commit, but that commit is what is deployed: the GitOps change is the commit's tree | Nothing to stop: the score is of the commit that ships |
| `base` | the ChangeRisk reconciler (4.2) | No | Chosen from Bundles Verified in the final environments, recorded before the Job |
| Lines and files | the Job, from the two trees | Not by how it is committed: squashing, splitting into commits, or a rename change nothing, since the diff is of trees and both sides of a rename are matched | Text or binary is decided from content, not `.gitattributes`; a binary file counts `binaryFileLines` (100) lines, so a change cannot hide in a file with a NUL byte |
| Classes | paths, matched against the profile | Partly: by naming paths (the residual "Path names chosen by the author" above), and by moving code to directories without a history key | Moving a path is itself in the diff: both sides of a rename are matched, so moving a migration out of `db/migrations/**` still matches the class |
| Weights, classes, tiers | the RiskProfile | **Yes, if the author may edit the profile or `Pipeline.spec.risk`** | A new admission policy, `<release>-risk-owners` (§11): an agent may not create, change or delete a RiskProfile, nor change a Pipeline's `spec.risk`. Lowering the bar is a person's act, as letting a pipeline through a freeze is (spec 18). An org pins its profile with a gate on `risk.profile` (4.3), which a team cannot edit either |
| `history` | failed deployments of the Pipeline | Partly: by moving code to new directories (new keys); at most 20 points are at stake | Classes still match; the residual is stated above |
| The record | ChangeRisk spec and status | No | Graph-only admission: only kro and the controller create a ChangeRisk or change it (§11); the Job runs no repository code (it diffs, it does not build) and writes only its termination message, which the reconciler parses as data |
| The rollback exemption | `rollback: exempt` | No | The exemption needs `lifecycle.VerifyHeldRollback`'s check: the rollback Bundle's artifacts are exactly ones that were Verified in the final environment (`pkg/lifecycle/hold.go:209-231`), so new content cannot pass as a rollback |
| Removing `spec.risk` | the Pipeline | Not for an agent (`<release>-risk-owners`) | A gate that reads `risk` in a Pipeline without `spec.risk` fails closed (4.8) |
| Volume | how many versions it makes | Yes, by splitting a change into small versions | The residual "Gaming by splitting" above |

The track record (§7) is the other score an agent would want lower. It counts only Bundles that
reached a final environment, from the controller's own deployment and failure definitions; an agent
can pad it with small deployments (the residual "Padding the record"), which the per-tier counts
expose (7.5). It cannot delete its failures: the records live in `DeploymentLog.status`, which
only the controller writes (28 §11), and `<release>-risk-owners` refuses an agent's DELETE of a
DeploymentLog (§11), whose reseed would forget failures older than the retained Bundles (7.2).

## 15. Upgrade from v0.13, and the defaults

- New CRDs: `changerisks`, `riskprofiles`. New optional fields: `Pipeline.spec.risk`,
  `FleetSpec.waves`, `FleetSpec.wavePreset`, `Pipeline.status.environments[].fleetWave`, `Bundle.status.risk`,
  `AgentIdentity.spec.trackRecord`, `AgentIdentity.status.trackRecord`, and `riskTier` on spec 28's
  DeploymentLog entries. New CEL: `risk.*`, `agent.*` additions, `path.glob`. New admission policy:
  `<release>-risk-owners`.
- Graphs built before the upgrade have no ChangeRisk; gates reading `risk` for those Bundles fail
  closed with `risk: Bundle checkout-7f3c2a has no ChangeRisk` (the Graph is rebuilt in place on the
  Pipeline spec change, #626, so in-flight Bundles of a Pipeline that turns risk on get the node).
- The track record reads spec 28's DeploymentLog, which spec 28 seeds from the retained Bundles at
  its upgrade (28 §14): an agent's record starts with what the cluster still holds, and
  `trackRecordSufficient` is false until `minDeployments` deployments are in the window. The
  release notes say so; a policy written as 7.5 asks for approval meanwhile, which is the safe
  direction.
- Adding `waves` to a fleet rebuilds the Graphs in flight (Pipeline spec change); targets with a step
  keep it, and the waves apply to the targets not started.
- `computeStability` is refactored onto spec 28's `pkg/delivery`; the seven existing
  `TestComputeDeploymentMetrics*` tests (`pkg/reconciler/pipeline/metrics_test.go`) must pass
  unchanged, so the Pipeline's figures do not move.

**Off by default.** Every feature here is inert until an admin opts in:

| Setting | Default | With the default |
|---------|---------|------------------|
| `Pipeline.spec.risk` | unset | No ChangeRisk node, no Job, no `risk.*`: the Pipeline behaves as in v0.13. A gate that reads `risk` fails closed (4.8) |
| RiskProfile | none shipped; the chart installs no profile | A Pipeline that sets `spec.risk` must name one; its ChangeRisk fails `ProfileNotReady` until it exists |
| `RiskProfile.onFailure` | `red` | An unscorable change is risky |
| `RiskProfile.requireVerifiedSource` | `[agent]` | An agent's version without verified provenance is not scored (`SourceUnverified`, red); a person's or a bot's gets 5 points (§14.1) |
| `RiskProfile.rollback` | `exempt` | A rollback to verified artifacts is green, to keep time to restore short |
| `RiskProfile.tiers` | amber 30, red 60 | Judgement (4.6), to be calibrated by `kardinal risk report` (§18) |
| `fleet.waves`, `fleet.wavePreset` | unset | The v0.13 sliding window (`maxConcurrent`, `maxUnavailable`) |
| `AgentIdentity.spec.trackRecord` | 30 days, at least 10 deployments | Computed for every agent once spec 28's DeploymentLog exists, but read by no decision until a policy names an `agent.*` track-record variable |
| `--change-risk-max-jobs`, `--change-risk-max-jobs-per-namespace` | 8, 2 | Only in effect for Pipelines with `spec.risk` |
| `<release>-risk-owners` | installed and bound with the chart, like the actor policies | Judges only agents' writes to RiskProfiles and `Pipeline.spec.risk` and their deletes of DeploymentLogs; without registered agents it refuses nothing |

## 16. Decisions

| # | Question | Options | Choice and why |
|---|----------|---------|----------------|
| CR1 | Where risk is computed | gate context; translator; ChangeRisk node | **ChangeRisk node** (4.1): I/O off the hot path, one immutable record |
| CR2 | Where the diff comes from | PromotionStep clone; owned Job; SCM compare API; CI-asserted | **Owned Job, RenderRun pattern** (4.2): no file cap, any git host, not author-asserted |
| CR3 | Who sets the weights | Pipeline; cluster profile; namespaced RiskProfile with org pinning | **Namespaced RiskProfile** (4.3): teams tune, orgs pin, the org-gate trust split |
| CR4 | Scoring model | points; shipped regression; per-install fit; LLM | **Additive points** (4.6): deterministic, explained; no data to fit |
| CR5 | Path history | per file; per class and directory; none | **Class and directory keys over the last 30 deployments** (4.7): fits the records, DRS's folder feature, the CFR window |
| CR6 | How dependents wait | `readyWhen`; cross-node `readyWhen`; hold root steps; gates that read `risk` hold | **Gates hold** (4.8), `readyWhen` for Graph readiness: G1 and G3 rule out the first two; only consumers wait |
| CR7 | Paths in CEL | `matches()`; `path.glob` in kardinal; plus kro proposal | **`path.glob` in kardinal and proposed to kro** (5): no glob in cel-go ext or kro; one matcher for classes and gates; ledger G23 |
| CR8 | Glob alternatives `{a,b}` | allow; allow with a depth limit; refuse | **Refuse** (5.2): bounded cost; `||` covers it |
| CR9 | Agent record storage | recompute from Bundles; Pipeline status ring + index; spec 28's DeploymentLog; DeploymentRecord CRD; AuditEvents | **Spec 28's DeploymentLog** (7.2), folded by a track-record reconciler into `AgentIdentity.status.trackRecord`: Pipeline status is the controller's most-written object (#1719, spec 28 §5.3), and one log serves the metrics, the fleet view and autonomy. Changed from the ring after the spec 24 review (coordinator's decision) |
| CR10 | Agent window | last 30 per Pipeline; 30 days; 60 days | **30 days, configurable 7 to 90, minimum 10** (7.1): summable across Pipelines; DORA's period; RADAR's minimum count |
| CR11 | How autonomy changes | CEL policies; controller edits `autonomy`; `agents: earned` | **CEL policies now, `earned` later** (7.4) |
| CR12 | Approval quorum by tier | two gates with `requiredWhen`; `requiredExpression`; expression over `approvals.count` | **Two gates with `requiredWhen`** (8.2): no new field, the inbox shows both |
| CR13 | Wave API | named preset; explicit waves; both; split environments | **Explicit `waves` plus `wavePreset`** (9.3) |
| CR14 | Wave mechanism | new admission CEL; wave edges in the expanded DAG; separate environments | **Wave edges** (9.4): one change in `expandFleets`, every DAG consumer sees it, compact admission reused |
| CR15 | Risk-adaptive waves | per-tier wave lists in the Graph; soak gate between waves | **Soak gate** (9.5): a step reading the score would hold every step (G14) |
| CR16 | An agent's unverified `head` | score it with points; force a tier; refuse to score (fail closed) | **Refuse to score for kinds in `requireVerifiedSource`, default `[agent]`** (§14.1): the author names the commit, so points alone let it choose its score; people and bots keep the points, for installations without attestations |
| CR17 | Who may lower the bar | anyone with RiskProfile edit; org pin only; refuse agents | **Refuse agents** (`<release>-risk-owners`, §11, §14.1), plus the org pin: lowering weights is a person's act, as a freeze exemption is |
| CR18 | Binary files | not counted; counted as files only; counted as lines | **`binaryFileLines` (100) each, text decided by content not `.gitattributes`** (4.5, 4.6): otherwise a change hides in a binary file |
| CR19 | Pre-production failure rate | from DeploymentLog; from live PromotionSteps; drop | **Drop for now** (7.1): DeploymentLog holds only final-environment deployments, and live steps are a second, non-durable source; a spec 28 follow-up can add it |

## 17. Test plan

Each layer below names where the test lives today. Live suites are the first column of
`hack/e2e/matrix.txt`; coverage rows are `test/e2e/coverage.tsv`'s seven columns (`id tier suite
status area feature source`, `test/e2e/README.md`), claimed by a test's `Covers ID.` comment.

**Unit** (table-driven, testify, `-race`):

- `pkg/pathglob` and the `path.glob` library (`pkg/cel/library`, registered in `newEvaluator`,
  `cel_evaluator.go:107-165`): the semantics of 5.2 (each operator, `**` at start, middle, end,
  escaping, classes, case), refusals (`{`, lengths), the compile-time literal check, cost estimate
  and actual cost, and agreement with doublestar on a corpus (fuzz `FuzzPathGlob` against
  `doublestar.Match` for patterns without `{`).
- Scoring (`pkg/reconciler/changerisk`, modelled on `renderrun` and `agentrun`): every row of the
  worked examples (4.6), band edges, `classesMax`, `forceTier`, clamps, `onFailure`,
  `rollback: exempt` with and without a Verified `rollbackOf`, percentile with 9 and 10 samples,
  `binaryFileLines`, and `requireVerifiedSource` (an agent's version without verified provenance
  fails `SourceUnverified`; a person's gets the points).
- History: keys intersecting, failures counted only within 30 deployments, `historyLimit` below 30.
- `pkg/delivery` (spec 28): every existing `computeStability` case returns the same figures (the
  seven `TestComputeDeploymentMetrics*` tests, `pkg/reconciler/pipeline/metrics_test.go`,
  unchanged), and `riskTier` copied into an entry and fixed once it is `final`.
- ChangeRisk reconciler: base choice across final environments and fleet targets; Job built
  restricted (no token, its own NetworkPolicy, limits); termination message parsing and truncation;
  `jobUID` mismatch (`JobReplaced`); terminal phases; supersession before start; idempotency (a
  second reconcile writes nothing); the `slots.Limiter` budget.
- Track-record reconciler: window edges, minimum, a `Truncated` log inside and outside the window,
  a `Reseeded` log inside and outside the window (`truncated`), by tier, and the IndexReconciler
  not reconciling on a `trackRecord` write (its new predicate).
- PolicyGate (`pkg/reconciler/policygate`): `risk.*` and the new `agent.*` keys always present;
  hold while Pending with the reason and no new AuditEvent; evaluation error without `spec.risk`;
  the ChangeRisk watch (as the ChangeRequest watch, `reconciler.go:1479`) through
  `instanceGateRequestsWhere` with `readsRisk` over the expression and `requiredWhen`; the
  AgentIdentity `status.trackRecord` watch (gates wake on the AgentIndex today, `reconciler.go:1483`).
- Builder (`pkg/graph`: `builder_test.go`, `compact_test.go`, `fleet_test.go` with its compact
  simulator, `upstreams_test.go`, `size_test.go`): the `changeRisk` node in both shapes; node ID
  collision with `change-risk`; wave assignment (counts, percentages, small fleets, rest), per-wave
  bake on target specs, wave edges in `expandFleets`, `upstreamGroups` per wave, `SoakUpstreams`
  across waves.
- Admission (`test/helm`, as `identity_admission_test.go`'s GRAPH-OBJECTS rows):
  `<release>-graph-objects` refuses a ChangeRisk a team creates or changes; `<release>-risk-owners`
  refuses an agent's RiskProfile create, update and delete, its change to `Pipeline.spec.risk` and
  its delete of a DeploymentLog, admits a person's and a bot's, and fails closed while the
  AgentIndex is not ready.

Safety assertions that must be shown to fail (the QA rule): the Graph-only admission refusing a
pre-created ChangeRisk; `<release>-risk-owners` refusing an agent's profile edit; an agent's
unverified version not scored green; a binary file not hiding its size; a gate reading `risk` not
passing while `Running`; `onFailure: red` applied on `SourceUnavailable`; a wave-2 target not
admitted while a wave-1 target is baking. Each PR breaks the property on purpose and shows the test
failing.

**Fake-client journeys** (real builder and reconcilers on a fake client, Pipeline test → uat →
prod, and a 20-target fleet). Named RJ, since J1-J9 are the project's journeys
(`docs/aide/definition-of-done.md`, `TestJourney1Quickstart`...). RJ1-RJ6 and RJ9 go in a new
`test/e2e/risk_journey_test.go`, as `test/e2e/actor_policy_journey_test.go`, with scenario variants
in `test/scenario/policy`; RJ7 and RJ8 in `pkg/graph/fleet_journey_test.go`, as
`TestJourney_FleetIncident`.

| # | Case | Expect |
|---|------|--------|
| RJ1 | Human, small change, green; gate `risk.tier != 'red'` on prod | prod promotes |
| RJ2 | Agent, migration class, red; two approval gates (8.2) | prod waits for 2 approvals; `why` names both gates |
| RJ3 | Source Secret missing | ChangeRisk Failed; tier red; prod held by the red gate |
| RJ4 | Soak gate scaled by tier, red | uat soak must reach 720 before prod |
| RJ5 | Agent with 10 DeploymentLog entries, 2 failed (CFR 0.2), policy 7.5 | approval required; after 10 more successes in the window, not required |
| RJ6 | Agent with 3 entries | `trackRecordSufficient` false; approval required |
| RJ7 | Fleet `wavePreset: aws`, 20 targets | waves 1, 1, 3, 12, 3; wave k+1 admitted only after wave k Verified after its bake |
| RJ8 | Fleet waves, a wave-3 target fails, `maxUnavailable: 1` | wave 4 never starts; targets in flight finish |
| RJ9 | Docs-only change, `pathsTruncated` false / true | approval skipped / required |
| RJ10 | Agent creates a version without verified provenance; gate `risk.tier != 'red'` | ChangeRisk Failed `SourceUnverified`; prod held |
| RJ11 | Agent edits the Pipeline's RiskProfile to lower `tiers.red` | refused by `<release>-risk-owners`; the score is unchanged |

**Live e2e** (kind, `COUNT=2`, CI dispatch with the affected suites only). Rows to add to
`test/e2e/coverage.tsv` when implemented. Test names must match the `core` suite's run regex
(`hack/e2e/up.sh:128`: `TestGate_`, `TestGraph_`, ...): RISK and AGENT-TR rows as `TestGate_Risk*`,
FLEET-WAVE rows as `TestGraph_FleetWave*` in `test/e2e/live/fleet_test.go`. There is no `fleet` suite.

| id | tier | suite | status | area | feature |
|----|------|-------|--------|------|---------|
| RISK-01 | live | core | todo | gates | ChangeRisk scored from a real git range on the in-cluster Forgejo; `status.factors` and tier shown by `kardinal why` and in the PR evidence |
| RISK-02 | live | core | todo | gates | A gate reading `risk` holds while the Job runs, then passes or blocks by tier |
| RISK-03 | live | core | todo | gates | Missing source credentials: ChangeRisk Failed, tier red, prod blocked |
| RISK-04 | live | core | todo | gates | The Job Pod has no ServiceAccount token and cannot reach the API server (the Job probes it and the test reads the result) |
| RISK-05 | live | core | todo | gates | `path.glob` in a gate expression |
| RISK-06 | live | ui | todo | ui-api | An invalid literal glob refused by the UI validate endpoint (`TestUI_`, beside UIAPI-VALIDATECEL-01) |
| RISK-07 | live | core | todo | actors | An agent's write to a RiskProfile and to `Pipeline.spec.risk`, and its delete of a DeploymentLog, refused by `<release>-risk-owners`; a person's admitted |
| RISK-08 | live | core | todo | gates | An agent's version without verified provenance: `SourceUnverified`, tier red; with an attested image (the core suite's signing fixtures), scored from the verified source commit |
| AGENT-TR-01 | live | core | todo | actors | An agent's track record in `AgentIdentity.status` from its DeploymentLog entries after deployments and a rollback; a `requiredWhen` on `agent.changeFailureRate` requires approval |
| FLEET-WAVE-01 | live | core | todo | graph | `wavePreset: aws` with bake minutes shortened by explicit `waves` (1 minute) on 6 targets: waves 1, 1, 3, 1 in order |
| FLEET-WAVE-02 | live | core | todo | graph | A failing wave-2 target stops wave 3 with `maxUnavailable: 1` |

(The `source` column, a `docs/...#anchor`, is the user page of §18 row 13.)

**Upgrade.** A new `upgrade-v0130` suite (a `kardinal-v0130.sh` component, as `kardinal-v0123.sh`),
once v0.13.0 is tagged: it enables `spec.risk` on an existing Pipeline with a Bundle in flight and
checks the rebuilt Graph has the node, and that agents' records start from the seeded DeploymentLog.

**Soak.** The enterprise-day soak (#2403, `TestSoak_EnterpriseDay` in its new heavy `soak` suite;
one cluster, several tenants, about 6 Bundles a minute for an hour, invariants checked every 3
minutes by `framework/invariants`, a mid-run `helm upgrade`) is where change risk is tested under
load and next to the other v0.14 features. Once this spec is implemented, team-a's Pipeline (agents
shipping and verifying) sets `spec.risk` with an org-pinned profile and a `risk.tier`-scaled soak
gate, and the soak adds invariants:

- every gate that reads `risk` passed only after its Bundle's ChangeRisk was terminal;
- no RiskProfile or `Pipeline.spec.risk` write by an agent was admitted;
- no agent version was scored without verified provenance;
- every agent's `trackRecord` agrees with `pkg/delivery.Summarize` over its DeploymentLog entries at
  the time of the check;
- the ChangeRisk Jobs in flight never exceed `--change-risk-max-jobs`, and none is left after its
  Bundle is gone.

## 18. Work breakdown (issues to open under a v0.14.0 epic)

None of these exists yet (audited 2026-10-21: no code, no issue). Specs 17-21 are on main, so they
are no longer dependencies.

| # | Issue | Size | Depends on |
|---|-------|------|------------|
| 1 | `pkg/pathglob` and the `path.glob` CEL library in the PolicyGate environment, docs, literal validator | s | none |
| 2 | `pkg/delivery` (`Derive`, `Merge`, `Summarize`) with `computeStability` refactored onto it, unchanged figures: spec 28's refactor, done by whichever spec starts first | s | none |
| 3 | `RiskProfile` CRD and reconciler; `riskprofiles` in `userRoles.readRules` | s | 1 |
| 4 | `ChangeRisk` CRD, Job mode in `kardinal-render`, reconciler, its own NetworkPolicy, `slots.Limiter` budget | l | 1, 3 |
| 5 | Graph node in both shapes; `changerisks` in the Graph applier and kro-watch roles and in `$graphKinds` | m | 4 |
| 6 | Scoring (incl. `binaryFileLines`, `requireVerifiedSource`), history, percentile, `Bundle.status.risk` mirror | m | 2, 4 |
| 7 | `risk.*` in the gate context, hold semantics, watch, `policy simulate --risk-*` | m | 6 |
| 8 | Evidence: PR body row, `kardinal why`, UI stage drawer | s | 6 |
| 9 | Spec 28's DeploymentLog (its §5.4-5.6, reconciler and `historyLimit` guard), plus `riskTier` on entries | m (spec 28's) | 2; lands before row 10 |
| 10 | Track-record reconciler: `AgentIdentity.spec/status.trackRecord`, the DeploymentLog field index and watch, the IndexReconciler predicate, `agent.*` additions and their gate watch | m | 9 |
| 11 | Fleet `waves` and `wavePreset`: schema, builder (waves, bake, edges), compact grouping, `status.environments[].fleetWave`, CLI | l | none |
| 12 | `verify.requiredWhen` for the verify gate (8.2) | s | none |
| 13 | `<release>-risk-owners` admission policy and its helm tests (§11, §14.1) | s | 3 |
| 14 | Docs: user page "Change risk and adaptive pacing", cel-context reference, recipes of §7.5 and §8, the gaming table of §14.1 | m | 7, 10, 11, 13 |
| 15 | Live e2e rows of §17, and the soak invariants in #2403 | m | each feature; #2403 |
| 16 | Follow-ups: `agents: earned` shorthand; `kardinal risk report` (tier against later failure); a pre-production failure rate once DeploymentLog records it (CR19) | m | 10, 6 |

Ledger work in this PR: entry **G23** (the `path.glob` proposal to kro) and a G8 row for the
ChangeRisk reconciler (planned; the G8 row was approved by the coordinator as the owner's delegate on 2026-10-15). Reserved number G24 is not used: the
other kro behaviours this spec depends on are existing entries (G1, G3, G10, G12, G14, G15).

## 19. Ledger entries

- **G23** in [16-graph-capability-ledger.md](16-graph-capability-ledger.md#g23-graph-cel-has-no-path-glob):
  Graph CEL has no path glob; the proposed `path` library with cost bounds.
- **G8 row** "Change risk scoring": the ChangeRisk reconciler and its Job, external git reads before
  any gate, writing only ChangeRisk status. Approved 2026-10-15.
- **Spec 28's G8 row** "Per-actor deployment log" says "Display data: no promotion decision reads
  it". Once row 10 lands, a decision reads it, through `AgentIdentity.status.trackRecord` (7.2):
  that PR updates the row to say so, and adds a G8 row for the track-record reconciler (it reads
  DeploymentLogs, a cross-kind read, and writes only `AgentIdentity.status.trackRecord`).
