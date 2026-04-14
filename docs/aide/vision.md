# kardinal-promoter: Vision

> Created: 2026-04-09
> Status: Active
> License: Apache 2.0

## ⚠️ Current Priority Order (read before generating any queue)

**1. PDCA Validation Infrastructure** — the agent must be able to use the product for real.
Before generating any feature queue, ensure the validation infrastructure exists:
- `pnz1990/kardinal-test-app` exists at https://github.com/pnz1990/kardinal-test-app ✅
- `make setup-e2e-env` creates a kind cluster with krocodile + ArgoCD + test app deployed
- The full promotion loop can be tested: real image → real ArgoCD sync → real health check

**2. Complete remaining backlog** — see milestone pages for open issues.

**3. PDCA loop** — after each batch, the standalone agent:
- Spins up the E2E environment (`make setup-e2e-env`)
- Creates a Bundle referencing a real `ghcr.io/pnz1990/kardinal-test-app` image
- Exercises complex promotion scenarios (pause, rollback, policy gates, multi-env)
- Opens bugs for any failures, improvements for any UX issues
- Tears down the cluster when done

---

## PDCA Architecture: The Agent Uses the Product

**The agent is not just a code writer. It is a customer of the product.**

Every few cycles (configurable via `product_validation_cycles` in `otherness-config.yaml`),
the standalone agent uses kardinal-promoter from the outside — the way a real platform
engineer would. It does NOT rely on unit tests alone.

### Test Infrastructure

| Component | Location | Purpose |
|---|---|---|
| Test application | `github.com/pnz1990/kardinal-test-app` | Real app with Dockerfile; CI pushes to `ghcr.io/pnz1990/kardinal-test-app` |
| E2E environment | `make setup-e2e-env` | kind + krocodile + ArgoCD + app in test/uat/prod namespaces |
| Pipeline | `examples/quickstart/pipeline.yaml` | 3-stage promotion: test → uat → prod |
| Policy gates | `examples/quickstart/policy-gates.yaml` | No-weekend-deploys gate for prod |

### PDCA Validation Scenarios (beyond standard journeys)

See `AGENTS.md` §Product Validation Scenarios for the full list.

Key scenarios:
1. **Happy path promotion** — create Bundle, verify test→uat automated, verify prod PR opened
2. **Pause blocks promotion** — create Bundle, pause pipeline, verify it stays at test
3. **Weekend gate blocks prod** — `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED
4. **Rollback** — `kardinal rollback` opens PR with rollback label and evidence body
5. **Health check failure** — deploy an app that fails readiness, verify promotion blocks
6. **Concurrent bundles** — create two Bundles simultaneously, verify correct supersession

---

## ⚠️ Immediate Goal: Execute Workshop 1

**The standalone agent must execute this workshop end-to-end on a live kind cluster:**

> https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo

This is tracked as GitHub epic #123 / milestone `workshop-1-executed`. That milestone
closes ONLY when the agent has run every workshop step, recorded the actual terminal
output on issue #123, and confirmed every step produced the documented result.

The agent does this itself — it is not a human task. The coordinator assigns the
workshop execution as an item once code gaps #115, #116, #117 are merged.

**Agents: do not plan or implement Workshop 2 scope (Argo Rollouts, multi-cluster,
GitLab, distributed mode) until epic #123 is closed. If the coordinator generates a
queue with Workshop 2 items while #123 is open, post `[NEEDS HUMAN]`.**

---

## ⚠️ Second Objective: Graph Purity (milestone v0.2.1)

**After Workshop 1 is executed, the team's second objective is eliminating all logic leaks that do NOT require krocodile changes.**

This is milestone `v0.2.1`. See `docs/design/11-graph-purity-tech-debt.md` for the full list and agent instructions. 41 leaks are fixable in kardinal alone. Start with issue #133 (PRStatus CRD — eliminates 6 API call paths).

Issues blocked on krocodile (#130, #132, #136, #138) must NOT be worked on. They are labeled `blocked-on-krocodile`.

**No new logic leaks are permitted going forward.** Any new `time.Now()`, external HTTP call, or cross-CRD mutation in a reconciler requires explicit human approval before implementation. QA must block such PRs.

---

## Project Overview


> https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo

This is tracked as GitHub epic #123 / milestone `workshop-1-executed`. That milestone
closes ONLY when the agent has run every workshop step, recorded the actual terminal
output on issue #123, and confirmed every step produced the documented result.

The agent does this itself — it is not a human task. The coordinator assigns the
workshop execution as an item once code gaps #115, #116, #117 are merged.

**Agents: do not plan or implement Workshop 2 scope (Argo Rollouts, multi-cluster,
GitLab, distributed mode) until epic #123 is closed. If the coordinator generates a
queue with Workshop 2 items while #123 is open, post `[NEEDS HUMAN]`.**

The three code gaps blocking Workshop 1 execution:
- #115: `kardinal get pipelines` per-environment columns
- #116: `kardinal explain` label mismatch (shows zero PolicyGates)
- #117: `kardinal explain` missing CEL expression display

---

## Project Overview

kardinal-promoter is a Kubernetes-native promotion controller that moves versioned artifact bundles through environment pipelines using Git pull requests as the approval mechanism, with policy gates expressed as CEL and represented as visible nodes in the promotion DAG.

The execution engine is kro's Graph primitive, a general-purpose Kubernetes DAG reconciler. Graph handles dependency ordering, parallel execution, conditional inclusion, and teardown. kardinal-promoter handles the promotion-specific logic: Git writes, PR lifecycle, policy evaluation, health verification, and delivery delegation.

All state lives in Kubernetes CRDs. There is no external database, no dedicated API server, and no state outside of etcd. The CLI, UI, and webhook endpoints create and read CRDs. A user can operate the entire system with kubectl.

### Why this project exists

The Kubernetes promotion landscape has an orchestration gap. GitOps tools (Argo CD, Flux) synchronize a single cluster with Git, but they do not understand the relationship between environments. Moving an artifact from dev to staging to prod falls back on CI scripts, manual interventions, or tools that lock you into a specific ecosystem.

Kargo (by Akuity) fills this gap but requires Argo CD, introduces 6+ concepts, and stores state in a dedicated API server. GitOps Promoter fills it with PR-native approval but lacks artifact bundling, DAG pipelines, and governance. No tool combines DAG-structured pipelines, PR-native approval with evidence, visible policy gates, and GitOps-tool agnosticism into a single declarative system.

### Relationship to kro

kro's Graph primitive (`kro.run/v1alpha1/Graph`) is the core DAG engine. Within the kro ecosystem:

- **Graph** is the core DAG primitive. Creates, reconciles, and tears down Kubernetes resources in dependency order.
- **RGD** (ResourceGraphDefinition) is a higher-level concept built on Graph for resource composition.
- **kardinal-promoter** is a separate concept built on Graph for promotion orchestration and policy gating.

Building on Graph directly (rather than on RGD) avoids a translation shim. The controller generates a Graph spec whose nodes are exactly the PromotionStep and PolicyGate CRDs that kardinal-promoter needs. No intermediate abstraction, no unused resource-composition semantics.

Reference: [ellistarn/kro — krocodile branch](https://github.com/ellistarn/kro/tree/krocodile/experimental)

### kro Tracking and Contribution Policy

The krocodile/experimental branch is under **autonomous, continuous development** —
commits land multiple times per day, entire subsystems are rewritten in a single PR,
and breaking API changes arrive without deprecation windows. This is intentional:
the project is pre-1.0 and moving fast. Our agents must treat krocodile as a
first-class dependency that requires active stewardship, not a stable library.

**Every engineer and coordinator must:**

1. **Check the krocodile git log before implementing any Graph integration.** Run:
   ```bash
   gh api 'repos/ellistarn/kro/commits?sha=krocodile&per_page=20' \
     --jq '.[] | {sha: .sha[0:8], message: .commit.message[0:80], date: .commit.committer.date}'
   ```
   Look for changes to `experimental/docs/design/`, `experimental/crds/`, and
   `experimental/controller/`. If more than 10 new commits have landed since our
   pinned commit, treat it as a mandatory upgrade task before proceeding.

2. **Read the design docs before implementing.** The canonical source of truth for
   Graph semantics is `experimental/docs/design/` in the krocodile branch, not our
   own docs. When they disagree, the krocodile docs win.

3. **Contribute upstream rather than work around.** If Graph lacks a capability
   kardinal-promoter needs, open a PR to krocodile first. A contribution that lands
   upstream eliminates a workaround from our codebase permanently. Workarounds are
   accepted only when a contribution would block progress for more than one sprint.
   See §Upstream Issue and PR Protocol below for how to do this.

4. **Pin the Graph CRD version in our Helm chart and test infrastructure.** Install
   krocodile at a specific commit hash in all test clusters (CI and local kind) via
   `hack/install-krocodile.sh`. When krocodile updates break our tests, file a GitHub
   issue labelled `kind/bug,area/graph` with the breaking commit and impact before it
   blocks a sprint.

5. **Update our design docs when Graph semantics change.** The sections most likely
   to drift are `design-v2.1.md` §3.5 (`readyWhen` vs `propagateWhen`), §3.6
   (dependency edge mechanism), spec 01 (Graph CRD schema), and spec 02 (node
   templates). Stale design docs are a bug.

### Periodic krocodile Review (Coordinator responsibility)

**Every batch cycle**, before generating the work queue, the coordinator must:

```bash
# 1. Check new commits since our pin
PINNED=$(grep "KROCODILE_COMMIT:-" hack/install-krocodile.sh | grep -o '[a-f0-9]\{7,\}')
gh api 'repos/ellistarn/kro/commits?sha=krocodile&per_page=50' \
  --jq ".[] | select(.sha | startswith(\"$PINNED\") | not) | \
    {sha: .sha[0:8], message: .commit.message[0:100]}"

# 2. Read the diff of the controller and types
cd /tmp && git clone -q --depth=200 https://github.com/ellistarn/kro.git kro-review -b krocodile
git -C /tmp/kro-review log $PINNED..HEAD --oneline -- experimental/controller/ experimental/docs/
```

For each batch that has ≥5 new krocodile commits, the coordinator **must** include
a `chore(graph): upgrade krocodile to <newsha>` item in the queue. This is not
optional. An unreviewed krocodile gap accumulates silent breakage.

### Primitive Rethink (Quarterly or after major krocodile releases)

**At least once per quarter**, a dedicated agent session must answer these questions:

> **Are our primitives still the right ones given what krocodile now provides?**

Specifically:

| Question | Why it matters |
|---|---|
| Can `PromotionStep` become a pure krocodile Watch node instead of an Owned reconciler? | Every new krocodile Watch capability potentially eliminates reconciler code |
| Can any `pkg/reconciler/*` be deleted because krocodile now handles that pattern natively? | krocodile adds Definition nodes, drift timers, CEL AST path extraction — these eliminate workarounds |
| Does `pkg/translator/translator.go` still need to hand-build Graph specs, or can krocodile generate from higher-level input? | The translator is our biggest complexity surface |
| Are our node ID schemes, CEL expression patterns, and propagateWhen conventions still idiomatic given the latest krocodile design docs? | Conventions drift silently |
| Do any of our current `blocked-on-krocodile` GitHub issues now have a solution in HEAD? | New krocodile features often close our blocked issues |

The output of a rethink session must be:
- At least one GitHub issue opened (`kind/enhancement,area/graph`) if a simplification is found
- A comment on the report issue (Issue #1) with findings, even if the answer is "no changes needed"
- Updated `docs/design/10-graph-first-architecture.md` if any architectural commitment changes

### Upstream Issue and PR Protocol

When kardinal-promoter hits a krocodile limitation or bug, agents must engage
upstream rather than work around it locally. The krocodile maintainer (Ellis Tarn,
`@ellistarn`) is responsive and the project benefits from real-world usage reports.

**When to open a krocodile issue:**
- A bug in krocodile causes a kardinal feature to fail (e.g., propagateWhen stuck state)
- An API contract changes in a way that breaks our integration (e.g., node ID format)
- krocodile's behavior differs from its own design docs

**When to open a krocodile PR:**
- A missing primitive forces a workaround that violates Graph-first architecture
- A validation is too strict or too loose for real-world use (e.g., node ID format enforcement)
- A bug has a clear, small fix that we can provide

**How to do it:**
```bash
# Clone at our pinned commit
git clone https://github.com/ellistarn/kro.git /tmp/kro-upstream -b krocodile
cd /tmp/kro-upstream

# For issues: use the GitHub CLI with the krocodile repo
gh issue create --repo ellistarn/kro \
  --title "<clear title referencing the specific controller file/function>" \
  --body "## Summary
<what kardinal-promoter observed>

## Root cause
<specific file:line in krocodile>

## Reproduction
<minimal Graph spec or test case>

## Suggested fix
<if known>"

# For PRs: create a branch, make the fix, open PR
git checkout -b fix/<descriptive-name>
# ... make the fix ...
gh pr create --repo ellistarn/kro \
  --title "fix: <description>" \
  --body "<summary, root cause, test coverage>"
```

**After upstream engagement:**
- Record the issue/PR URL in the kardinal issue that motivated it (cross-link)
- Label the kardinal issue `blocked-on-krocodile` if we must wait
- Check the upstream issue/PR every batch cycle and update when it lands
- When it lands: upgrade our pin, remove the workaround, close the kardinal issue

**Krocodile issue/PR history** (update this table as we engage):

| Date | krocodile issue/PR | Cardinal issue | Status |
|---|---|---|---|
| 2026-04-14 | [#109](https://github.com/ellistarn/kro/pull/109) DNS label validation | bdb6968 compat fix | Merged, we're on 948ad6c |
| 2026-04-14 | propagateWhen stuck state (reported via DM) | propagation bug | Under investigation by Ellis |

Key semantic facts as of 2026-04-14 (verify against krocodile before implementing):
- `readyWhen` = health signal (UI, `kubectl get graph`) — does NOT block downstream
- `propagateWhen` = data-flow gate — DOES block downstream when unsatisfied
- `spec.nodes` (not `spec.resources`) is the field name for the node list
- `experimental.kro.run` is the API group for Graph CRDs
- Node IDs must be valid DNS-1123 labels after `strings.ToLower()` — no underscores, no hyphens, ≤63 chars (PR #109)
- Drift timers (default 30min) replace unconditional 1s requeues — watch events are primary
- Propagation hash includes `propagateWhen` state — gate transitions trigger downstream (commit `0b609d0`)
- **Pinned krocodile commit: `948ad6c`** (validates DNS labels in node IDs, includes drift timers and propagation hash fix)

### Graph-First: The Core Architectural Commitment

**The world is a DAG. Everything in kardinal-promoter is a derivation of the krocodile Graph primitive.**

This is not aspirational. It is the governing constraint on every implementation decision.
See `docs/design/10-graph-first-architecture.md` for the full decision record.

The layer model:
```
L1: krocodile Graph API       — universal DAG primitive, CEL evaluation
L2: kardinal APIs             — PromotionStep, PolicyGate, Bundle, Pipeline CRDs
                                All expressed as Graph Watch or Owned nodes
L3: kardinal customer APIs    — Pipeline and PolicyGate definitions
```

**If a feature cannot be expressed as a Graph node** (Watch node, Owned node, or CEL
extension on the Graph environment), that is a signal krocodile is missing a primitive
that should be contributed upstream. Agents must STOP and escalate to human. No logic
may leak outside the Graph layer without explicit human approval.

**Pending upstream contributions that will eliminate current workarounds:**
1. `recheckAfter` on Graph nodes — eliminates the PolicyGate reconciler for time-based gates
2. Explicit `dependsOn` edges — eliminates data-flow-as-dependency hacks

**Known transitional exception (must not grow):**
- `pkg/cel/` — the standalone PolicyGate CEL evaluator. Exists because krocodile lacks
  `recheckAfter`. Must be deleted once `recheckAfter` is contributed upstream.
  See `docs/design/10-graph-first-architecture.md` §Known Exceptions.

## Release and Versioning Philosophy

**GA (`v1.0.0`) is not a near-term goal.** Do not plan for or reference GA. We are a
pre-release project — the focus is on shipping working, usable minor versions iteratively.

### Versioning approach

- **Minor versions (`v0.N.0`)** are substantial. Each minor covers many stages and
  multiple capability areas. Keep minor version numbers low — prefer `v0.2.0`, `v0.3.0`
  over jumping quickly to `v0.5.0`, `v0.6.0`. There is no upper bound, but progress
  should feel significant between minors. Minor versions are cut when the milestone's
  open issues reach zero and its associated journeys pass.

- **Patch versions (`v0.N.P`)** are bug fixes, security patches, and doc corrections.
  Cut them whenever needed — they do not require a full milestone to complete.

- **No `v1.0.0` planning.** Do not create a v1.0.0 milestone. When we are genuinely
  production-ready we will decide on GA deliberately, not as a roadmap item.

### Milestone structure — multiple epics per milestone

Each milestone should contain **5-7 epics** representing different capability workstreams,
similar to how kro structures its milestones. This keeps minor version numbers low and
makes each release feel substantial.

**How to structure epics within a milestone:**
- Each epic covers one coherent capability area (e.g. "Git operations", "Health adapters",
  "CLI", "PolicyGate engine") — not one stage
- Multiple stages may contribute to one epic
- Epics within the same milestone can be worked in parallel (different queue batches)
- An epic is closed when all its items are done and its acceptance criteria pass
- The milestone closes (and release is cut) when ALL epics in it are closed

**Example structure (reference — derive the actual epics from the roadmap):**

```
v0.2.0 milestone
├── Epic: Git operations and PR flow (Stage 5)
├── Epic: Full promotion loop — PromotionStep reconciler (Stage 6)
├── Epic: Health adapters — k8s native, ArgoCD, Flux (Stage 7)
├── Epic: CLI — core commands (Stage 8)
└── Epic: Docs and examples — quickstart working end-to-end
```

```
v0.3.0 milestone
├── Epic: Embedded React UI (Stage 9)
├── Epic: PR evidence, labels, webhook reliability (Stage 10)
├── Epic: GitHub Actions integration + kardinal init (Stage 11)
├── Epic: Helm strategy + config-only promotions (Stage 12)
└── Epic: Rollback and pause/resume (Stage 13)
```

### PM instructions for milestone creation

- Derive milestones from `docs/aide/roadmap.md` — group stages into minors with 5-7 epics each
- Each milestone title is `v0.N.0` (no v1.0.0)
- Each milestone description states: stages covered, what users can do, which journeys unlock
- Create 5-7 epic issues per milestone — one per capability area, not one per stage
- Future milestones beyond the next one get epics only (no full item specs)
- The currently active milestone gets full item specs for the current batch only
- When cutting a release: use `gh release create`, generate notes from closed issues,
  close the milestone, open the next one, post `[📋 PM] RELEASE: vX.Y.Z` on the report issue

## Goals and Objectives

1. Provide a declarative, Kubernetes-native promotion system where every object is a CRD and every state transition is observable via kubectl.
2. Make every promotion a Git pull request with rich evidence: artifact provenance, upstream verification, and policy gate compliance.
3. Represent policy gates as visible nodes in the promotion DAG, inspectable via CLI (`kardinal explain`) and UI, with org-level gates that teams cannot bypass.
4. Support DAG-structured pipelines (parallel fan-out, conditional steps, multi-service dependencies) via kro's Graph primitive.
5. Work with existing GitOps tools (Argo CD, Flux) via pluggable health adapters that auto-detect installed CRDs. Also work without a GitOps tool.
6. Support multi-cluster promotion via Argo CD hub-spoke, Flux with remote kubeconfig, or bare Kubernetes.
7. Provide pluggable promotion steps so teams can inject custom logic (tests, approvals, notifications) between built-in Git and health operations.
8. Support both image promotions (new container versions) and config-only promotions (resource limits, env vars, feature flags) through the same pipeline.
9. Enable distributed deployments where agents run behind firewalls and connect outbound to a control plane.

## Target Users

### Platform engineers (primary)

Define promotion pipelines and PolicyGates for their organization. Configure health adapters, Git providers, and delivery delegation. Manage org-level policies that application teams inherit.

### Application developers (secondary)

Create Bundles (via CI webhook or CLI) to trigger promotions. Review and merge promotion PRs. Use `kardinal explain` to understand why a promotion is blocked. Use the UI to monitor promotion progress.

### SREs and operators (secondary)

Use `kardinal rollback` and `kardinal pause` during incidents. Monitor promotion health via the UI and controller metrics. Configure shard assignments for distributed deployments.

**The UI is a control plane, not a status display.** Operators act during incidents without switching tools. All of the following are shipped:
- Fleet-wide health dashboard: blocked pipelines, CI red, human interventions pending — all scannable in one table
- Per-pipeline operations view: sortable health columns (inventory age, last merge, blockage time, interventions/deploy)
- Per-stage detail: bake countdown, integration test pass rates, override history, alarm events
- In-UI actions: approve, pause, resume, rollback, override gate (with mandatory reason), restart failed step
- Bundle promotion timeline: artifact history with diff links, rollback records, override audit trail
- Policy gate detail: current CEL variable values, evaluation history, time until unblocked

A user who has never seen the CLI can operate kardinal entirely from the UI during an incident.

## Core Features

### F1: Pipeline CRD

A user-facing CRD that defines the promotion path for one application. Lists environments in order with Git configuration. The controller translates this into a kro Graph, injecting PolicyGate nodes.

Environments promote sequentially by default. For parallel fan-out, use the `dependsOn` field. Each environment specifies approval mode (`auto` or `pr-review`), update strategy (`kustomize` or `helm`), health adapter, and optional delivery delegation.

### F2: Bundle CRD

An immutable, versioned snapshot of what to deploy. Types: `image` (container images), `config` (Git commit with configuration changes), `mixed` (both). Carries build provenance (commit SHA, CI run URL, author, digest).

Created by CI webhook, CLI, kubectl apply, or Subscription CRD. Includes intent (target environment, skip list with permission enforcement).

Phases: Available, Promoting, Verified, Failed, Superseded. Per-environment evidence (metrics, gate results, approver, timing) is stored in Bundle status for durable audit.

### F3: PolicyGate CRD

CEL-powered policy checks represented as nodes in the promotion Graph. Platform teams define org-level gates (in `platform-policies` namespace) that are automatically injected into every Pipeline targeting matching environments. Teams can add their own gates but cannot remove org gates.

CEL context includes: bundle metadata, schedule (isWeekend, hour, dayOfWeek), environment info, metrics, upstream soak time.

Re-evaluation via `recheckInterval` for time-based gates. `lastEvaluatedAt` freshness prevents stale gate state.

SkipPermission gates control whether `intent.skip` is allowed on gated environments.

### F4: Promotion Steps Engine

Configurable step sequence per environment. Default sequence inferred from update strategy and approval mode. Custom steps via HTTP webhook.

Built-in steps: `git-clone`, `kustomize-set-image`, `helm-set-image`, `kustomize-build`, `config-merge`, `git-commit`, `git-push`, `open-pr`, `wait-for-merge`, `health-check`.

Custom steps: any `uses` value not matching a built-in step dispatches as HTTP POST to the configured URL. Returns pass/fail. Steps pass outputs to subsequent steps via an accumulator.

### F5: Health Adapters

Pluggable, auto-detected health verification. Available adapters: Deployment condition (`resource`), Argo CD Application health+sync (`argocd`), Flux Kustomization Ready (`flux`), Argo Rollouts (`argoRollouts`), Flagger (`flagger`).

Auto-detection: controller checks for CRDs on startup and periodically. Priority: argocd, flux, resource. Remote cluster support via kubeconfig Secrets.

### F6: PR Evidence

For `pr-review` environments, the controller opens a PR with structured body: policy gate compliance table, artifact provenance with links, upstream verification timestamps, commit range diff. Labels for filtering (`kardinal`, `kardinal/promotion`, `kardinal/rollback`, `kardinal/emergency`). Merge detection via webhook with startup reconciliation fallback.

### F7: Distributed Architecture

Control plane (kardinal-controller) handles Pipeline reconciliation, Graph generation, PolicyGate evaluation. Agents (kardinal-agent) handle PromotionStep reconciliation per shard. Agents run behind firewalls, connect outbound to control plane. Shard routing via `kardinal.io/shard` label. Credential isolation: Git tokens and health credentials stay in agent clusters.

Standalone mode (Phase 1): single binary handles everything. Distributed mode (Phase 2+): controller + agents. Same reconciler code in both modes.

### F8: kardinal-ui

Embedded React UI served by the controller binary via `go:embed`. Read-only. Renders the promotion DAG with per-node state (PromotionStep: green/amber/red; PolicyGate: pass/fail/pending). Shows Bundle provenance, PR links, policy evaluation details. Backend API proxy at `/api/v1/ui/` reads CRDs from the Kubernetes API server.

### F9: CLI

Single static Go binary. Commands: `init`, `get pipelines/steps/bundles`, `create bundle`, `promote`, `explain` (with `--watch`), `rollback` (with `--emergency`), `pause`, `resume`, `history`, `policy list/test/simulate`, `diff`, `version`. All commands create or read CRDs.

### F10: Config-Only Promotions

Bundle `type: config` references a Git commit SHA. The `config-merge` step applies changes via cherry-pick or overlay. Config Bundles go through the same Pipeline, PolicyGates, and PR flow. Config and image Bundles coexist independently (different types do not supersede each other). Phase 3: mixed Bundles (image + config), Git Subscription for passive config watching.

### F11: Subscription CRD (Phase 3)

Declarative registry and Git watcher. Image subscriptions watch OCI registries for new tags. Git subscriptions watch repositories for config changes. Auto-creates Bundles when new artifacts are discovered.

## Technical Architecture

### Stack

- **Language:** Go 1.23+
- **Kubernetes:** controller-runtime, dynamic client, CRDs via kubebuilder
- **DAG engine:** kro Graph primitive (`kro.run/v1alpha1/Graph`)
- **Policy expressions:** CEL via `google/cel-go`
- **Git:** go-git or shell exec for clone/push, SCM provider interface for PRs
- **UI:** React 19, TypeScript, Vite, embedded via `go:embed`
- **CLI:** cobra

### CRDs

| CRD | User-created? | Purpose |
|---|---|---|
| Pipeline | Yes | Promotion topology, Git config, environments |
| Bundle | Yes (via CI/CLI/kubectl) | Artifact snapshot with provenance and intent |
| PolicyGate | Yes (platform/team) | CEL policy check, DAG node template |
| PromotionStep | No (created by Graph) | Per-environment promotion execution state |
| MetricCheck | Yes (optional, Phase 2) | Prometheus/Datadog query template |
| Subscription | Yes (optional, Phase 3) | Registry/Git watcher |
| Graph | No (created by controller) | kro DAG spec (auto-generated from Pipeline) |

### Pluggable Interfaces

| Interface | Purpose | Phase 1 impl | Future impls |
|---|---|---|---|
| `scm.SCMProvider` | PR lifecycle (create, merge, comment) | GitHub | GitLab, Bitbucket |
| `scm.GitClient` | Git operations (clone, push) | go-git/shell | Same across providers |
| `update.Strategy` | Manifest update | Kustomize | Helm, replace, OCI |
| `health.Adapter` | Health verification | Deployment, Argo CD, Flux | Argo Rollouts, Flagger |
| `delivery.Delegate` | Progressive delivery watch | none (instant) | Argo Rollouts, Flagger |
| `metrics.Provider` | Metric queries | (Phase 2) | Prometheus, Datadog |
| `source.Watcher` | Artifact discovery | (Phase 3) | OCI registry, Git |
| `steps.Step` | Promotion step | 10 built-in | Custom webhooks |

### Multi-Cluster Model

- Argo CD hub-spoke: controller reads Application health from hub cluster, no cross-cluster API calls
- Flux per-cluster: remote kubeconfig Secret for health verification
- Bare Kubernetes: remote kubeconfig Secret
- Parallel fan-out: `dependsOn` on Pipeline environments creates parallel Graph nodes

### Graph Integration

The controller generates a Graph spec per Bundle. Dependency edges are inferred from CEL `${}` references between node templates. Each PromotionStep and PolicyGate carries fields (`upstreamVerified`, `upstreamEnvironment`, `requiredGates`) that serve both reconciler logic and edge creation.

Per-Bundle Graph lifecycle: created on Bundle promotion start, owned by Bundle via ownerReferences, cascade-deleted on Bundle GC.

## Non-Functional Requirements

### Performance

- Controller startup in under 10 seconds
- Pipeline-to-Graph translation in under 1 second
- PolicyGate CEL evaluation in under 10ms
- PR creation in under 5 seconds (GitHub API dependent)
- Health check polling every 10 seconds during HealthChecking state
- Git clone from cache in under 2 seconds (shallow clone, shared work trees)

### Scalability

- Designed for fewer than 50 Pipelines per control plane in Phase 1
- Distributed mode (Phase 2) scales horizontally via sharded agents
- Git rate limits: no polling, webhook-only merge detection with startup reconciliation
- PolicyGate recheck: ~20 writes/minute at 50 pipelines with `recheckInterval: 5m`

### Security

- Pod security: runAsNonRoot, readOnlyRootFilesystem, drop ALL capabilities
- RBAC: minimum required verbs per CRD, org PolicyGates protected by namespace RBAC
- Webhook auth: X-Hub-Signature-256 (SCM), Bearer+HMAC (Bundles), rate limited
- Credential isolation: in distributed mode, Git tokens stay in agent clusters
- UI: read-only, separate port available via `--ui-listen-address`

### Reliability

- All reconcilers are idempotent (safe to re-run after crash)
- Leader election via Kubernetes Lease (15s failover)
- Graph controller down: existing steps continue, new steps paused
- Agent down: sharded steps paused, resume on restart
- PR merge detection: webhook primary, startup reconciliation fallback
- PolicyGate staleness: `lastEvaluatedAt` freshness check prevents stale advancement

## Constraints and Assumptions

### Dependencies

- kro's Graph controller must be available in the cluster (experimental, API may change)
- A GitOps tool (Argo CD, Flux, or equivalent) must be syncing from the Git repository
- CI must create Bundles (via webhook, CLI, or kubectl). Subscription CRD (planned) removes CI requirement.

### Assumptions

- Teams have an existing GitOps repository with Kustomize or Helm per-environment directories
- Environment-specific configuration (secrets, resource limits) is already managed in the GitOps repo
- GitHub and GitLab are the supported Git providers. Other providers are planned.

### Constraints

- The controller never mutates workload resources (Deployments, Services, etc.)
- CEL is the only expression language (no OPA/Rego, no Cedar)
- Rollback is a forward promotion of a prior Bundle (no separate rollback mechanism)

## Out of Scope

| Excluded | Reason |
|---|---|
| Traffic management (canary weights, blue-green switching) | Delegated to Argo Rollouts or Flagger |
| GitOps sync (cluster reconciliation from Git) | Handled by Argo CD or Flux |
| CI pipelines (building images, running tests) | CI creates Bundles; kardinal-promoter does not build |
| Secret management | Managed in GitOps repo via External Secrets, Sealed Secrets, or SOPS |
| Native canary or blue-green delivery | Delegated |
| External database | Kubernetes is the database |
| Graph primitive development | kro team maintains Graph |
| Formal policy analysis (automated reasoning) | CEL does not support this |

## Success Criteria

### Delivered (v0.4.0 — Stages 0–17 complete)

- A user can apply a Pipeline CRD and a Bundle, and see the promotion flow through 3 environments (test, uat, prod) with correct ordering.
- PolicyGates block production promotion on weekends and enforce upstream soak time and metrics.
- PRs contain promotion evidence (provenance, upstream verification, policy compliance).
- `kardinal explain` shows which gates are blocking and why.
- kardinal-ui renders the promotion DAG with per-node state.
- Health verification works with Argo CD, Flux, bare Kubernetes, Argo Rollouts, and Flagger (auto-detected).
- Multi-cluster health via remote kubeconfig Secrets.
- `kardinal init` generates a Pipeline from 8 lines of config.
- GitHub Action and GitLab CI create Bundles from CI.
- Argo Rollouts and Flagger health delegation.
- Custom promotion steps via webhook.
- Config-only Bundles with config-merge step.
- Rollback CLI and automatic rollback on failure.
- MetricCheck CRD with Prometheus.
- `kardinal policy simulate`.
- GitLab support.

### Next (v0.5.0)

- Contiguous healthy soak (`bake:` stage field, reset-on-alarm)
- Pre-deploy gate type (`when: pre-deploy` on PolicyGate)
- Auto-rollback with ABORT vs ROLLBACK distinction
- ChangeWindow CRD for fleet-wide freeze management
- Deployment metrics on Bundle and Pipeline status

### Planned (v0.6.0+)

- Wave topology (`wave:` field on stages)
- Integration test step (Kubernetes Job as promotion step)
- PR review gate (`bundle.pr().isApproved()`)
- `kardinal override` with audit record
- Cross-stage history CEL functions
- Subscription CRD for registry and Git watching
- Security hardening and production readiness

## Competitive Landscape

| Dimension | kardinal-promoter | Kargo (v1.9.5) | GitOps Promoter (v0.26.x) |
|---|---|---|---|
| Pipeline model | Graph DAG (fan-out, conditional) | Stage pipeline (sequential) | Linear branch promotion |
| Policy governance | PolicyGate Graph nodes (CEL, cross-stage) | Manual approval only | CommitStatus webhook checks |
| Cross-stage policy | Yes — gate reads upstream soak, metrics, history | No | No |
| GitOps integration | ArgoCD, Flux, bare K8s (auto-detected) | ArgoCD primary | Any (commit statuses) |
| PR approval | Evidence (provenance, metrics, policy table) | None — tracked in Kargo UI | Git diff only |
| Artifact bundling | Bundle CRD (images, Helm, Git ref, provenance) | Freight (images, charts, Git refs, digests) | None (raw Git diff) |
| Artifact discovery | Bundle created by CI/CLI | Warehouse (automatic OCI/git scanning) | Git commit-based |
| Rollback | Forward promotion of prior Bundle (auto or manual) | Manual re-promote | Manual git revert |
| Auto-rollback | Yes (RollbackPolicy CRD) | No | No |
| Multi-cluster | ArgoCD hub-spoke, Flux via kubeconfig | ArgoCD hub-spoke | Branch structure |
| CLI | Full command set | Full command set | None |
| UI | Embedded React (DAG, gate states, timeline) | Polished Kargo UI | None |
| Maturity | v0.4.0, active development | v1.9.x, production-grade, commercial | v0.26.x, experimental |

### Key differentiators

1. **Cross-stage policy** — gates can read upstream soak time, metrics, and bundle history across the entire pipeline. No competitor has this.
2. **Graph-native DAG** — fan-out, conditional branches, arbitrary dependencies. Kargo is sequential; GitOps Promoter has no DAG.
3. **GitOps-tool agnostic** — ArgoCD, Flux, bare Kubernetes, all auto-detected. Kargo requires ArgoCD.
4. **Contiguous healthy soak** (v0.5.0) — timer resets if health fails, not just elapsed time.
5. **Auto-rollback** — automated rollback Bundle on health failure.
6. **Structured PR evidence** — gate results, soak time, provenance in every production PR.
7. **ChangeWindow CRD** (v0.5.0) — one object freezes all pipelines fleet-wide.
8. **Deployment metrics** (v0.5.0) — time-to-production, rollback rate, operator interventions per pipeline.

## Workshop Benchmarks

The design has been validated against two AWS workshops:

1. [Platform Engineering on EKS: Production Deploy with Kargo](https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo)
2. [Fleet Management on Amazon EKS: Promote App to Prod Clusters](https://github.com/aws-samples/fleet-management-on-amazon-eks-workshop/tree/mainline/patterns/kro-eks-cluster-mgmt)

Both scenarios (multi-cluster promotion with parallel prod fan-out and Argo Rollouts canary) are achievable with the Pipeline CRD, PolicyGate, and health adapter architecture. Working examples are in `examples/quickstart/` and `examples/multi-cluster-fleet/`.

## Reference Documents

| Document | Location |
|---|---|
| Technical design | `docs/design/design-v2.1.md` |
| Implementation specs | `docs/design/01-graph-integration.md` through `09-config-only-promotions.md` |
| User quickstart | `docs/quickstart.md` |
| Core concepts | `docs/concepts.md` |
| CLI reference | `docs/cli-reference.md` |
| Pipeline reference | `docs/pipeline-reference.md` |
| Policy gates guide | `docs/policy-gates.md` |
| Health adapters guide | `docs/health-adapters.md` |
| Rollback guide | `docs/rollback.md` |
| CI integration guide | `docs/ci-integration.md` |
| PR evidence guide | `docs/pr-evidence.md` |
| Troubleshooting | `docs/troubleshooting.md` |
| Quickstart example | `examples/quickstart/` |
| Multi-cluster example | `examples/multi-cluster-fleet/` |
| Constitution | `.specify/memory/constitution.md` |
