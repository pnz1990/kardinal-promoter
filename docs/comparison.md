# Comparison: kardinal vs Kargo vs GitOps Promoter

This page compares kardinal-promoter with the two most similar tools in the GitOps promotion space.

!!! note "Objectivity"
    Surveyed from each project's source, docs and release notes: kardinal-promoter
    v0.12.0, Kargo v1.12.4 and GitOps Promoter v0.45.3. All three tools are actively developed;
    check each project's releases for the latest capabilities. "Kargo Enterprise" means the
    commercial edition (Kargo on the Akuity Platform); everything else in the Kargo column is open source.

---

## Feature Matrix

| Feature | kardinal-promoter | Kargo | GitOps Promoter |
|---|---|---|---|
| **Promotion model** | DAG (fan-out, fan-in, arbitrary `dependsOn`) | DAG of Stages (a Stage takes Freight from one or more upstream Stages; `availabilityStrategy: All` for fan-in) | Ordered environment list, or a DAG with `dependsOn` (v0.38+) |
| **Parallel environments** | Yes — native fan-out + `wave:` topology | Yes — several Stages with the same upstream | Yes — `dependsOn` |
| **Policy gates** | PolicyGate CRDs with CEL: schedule, upstream soak and history, metrics, change windows, PR review state, Bundle metadata, approvals | No gate object. Verification (AnalysisTemplates), `requiredSoakTime`, manual Freight approval; expr-lang checks inside a promotion (`if:`, `fail`, `http`) fail it rather than hold it. Kargo Enterprise: promotion windows and `wait-for-approval` (v1.12, beta) | CommitStatus gates: Argo CD health, soak time, schedule windows, expr-lang checks on commits and web requests; any controller can write a CommitStatus |
| **Cross-stage policy** | Yes — a gate can read upstream soak, promotion history and PR approval state | Partial — Freight must be verified (and soaked) in the upstream Stage; a step can read it with `freightStatus()` (v1.12) | Partial — `DependentsSuccessfulCommitStatus` waits for upstream success; a `WebRequestCommitStatus` can read the whole PromotionStrategy status |
| **Pre-deploy gates** | Yes — a step stays Pending until every gate passes on a result newer than the step, before its first git or Argo CD write. While a PR waits, the gates are re-checked and mirrored to it as the `kardinal/gates` commit status; that blocks the merge when branch protection requires the check | Freight availability (upstream verification and soak) before a Promotion is created; Kargo Enterprise promotion windows reject new Promotions, and a running one is not stopped | Yes — proposed commit statuses must pass before the PR merges, re-checked for every new commit |
| **PR evidence body** | Structured, in every PR kardinal opens (`approval: pr-review`): image provenance (digest, CI run, commit, author), each gate's result and reason, other environments' health-check times | The commit message plus a link to the Kargo UI; the `git-open-pr` description can be templated with expressions; no gate or verification results | Templated (v0.37+): deployed and proposed commits, a diff link, the promotion pipeline table; gate results appear as SCM checks, not in the body |
| **PR controls** | Per environment (`pr`): templated title, body (with the evidence sections), labels, reviewers, team reviewers and assignees (incl. the Bundle's author); SCM auto-merge with `merge`, `squash` or `rebase` and a templated commit message ([by provider](scm-providers.md#pr-controls)) | `git-open-pr`: title, description and labels as expressions; `git-merge-pr` merges inside the promotion with a provider-specific `mergeMethod`; no reviewers or assignees | Templated title and description, PR labels; `autoMerge` per environment; reviewers ([#2002](https://github.com/argoproj-labs/gitops-promoter/issues/2002)), assignees ([#373](https://github.com/argoproj-labs/gitops-promoter/issues/373)), squash ([#1580](https://github.com/argoproj-labs/gitops-promoter/issues/1580)) and commit message templates ([#1562](https://github.com/argoproj-labs/gitops-promoter/issues/1562)) are open requests |
| **GitOps engine support** | ArgoCD, Flux, raw Kubernetes (chosen per environment) | Argo CD (sync and health); can push Flux-compatible OCI artifacts (v1.12), with no Flux sync or health check | Argo CD first-class (Source Hydrator, health gate, UI extension); any engine that syncs the environment branches, with no health gate for it |
| **SCM providers** | GitHub (PAT or GitHub App, incl. Enterprise), GitLab (incl. self-managed), Forgejo/Gitea; Bitbucket Cloud, Bitbucket Data Center and Azure DevOps (newer, less tested); git over HTTPS or ssh (key and `known_hosts`) with every provider. Several per controller (ScmProvider, ClusterScmProvider) | GitHub, GitLab, Gitea, Bitbucket Cloud, Azure DevOps | GitHub (a GitHub App, incl. Enterprise), GitLab, Forgejo, Gitea, Bitbucket Cloud, Azure DevOps |
| **Health checks** | Deployment, ArgoCD, Flux, Argo Rollouts, Flagger | ArgoCD Application, plus AnalysisTemplate verification | ArgoCD Application (Healthy and Synced); others through a custom CommitStatus |
| **Rollback mechanism** | `kardinal rollback` promotes the previous artifact through the same pipeline, gates and PR flow | Re-promote older Freight; since v1.11 this pins the Stage so auto-promotion does not roll forward again | `RestoreActiveCommit` (v0.44): a new commit restores one environment branch to a version it ran, and blocks promotion there until released |
| **Auto-rollback on health failure** | Yes — `onHealthFailure: rollback \| abort \| none` per stage, or a RollbackPolicy | Kargo Enterprise only (beta): back to the last verified Freight when verification fails | No |
| **Contiguous healthy soak** | Yes — `bake.minutes` resets timer on health alarm | No — `requiredSoakTime` counts time since the Freight was verified upstream; health does not reset it | No — `TimedCommitStatus` counts time since the commit merged; health does not reset it |
| **Change freeze management** | Yes — cluster-scoped `ChangeWindow` (blackout or recurring), enforced by any gate that references it; `kardinal freeze` stops the line for everyone or holds only agents' versions, on every in-flight promotion, with audited exemptions | Kargo Enterprise only (promotion windows, v1.12, beta); open source ignores the field | `ScheduledCommitStatus` cron windows, global or per environment, per PromotionStrategy |
| **Pause in-flight promotions** | Yes — `kardinal pause` (CLI or UI): no new step starts, and a step still preparing its change holds before its next git step | Turn off auto-promotion per Stage; a manual re-promote pins the Stage (v1.11) | `autoMerge: false` per environment (PRs wait for a manual merge) |
| **Reject a release** | Yes — `kardinal reject`: the Bundle is never promoted again, its unfinished steps are cancelled and their PRs closed, and rollback never returns to it; the rejecting user is verified by the API server | No — rejecting Freight is an open request ([#6151](https://github.com/akuity/kargo/issues/6151)) | No reject action; close the PR by hand |
| **Supersede an older release in flight** | Yes — a newer Bundle supersedes an older one that is still promoting | No — promotions queue ([#3108](https://github.com/akuity/kargo/issues/3108), its most-requested open issue) | Yes — the newest commit wins; intermediate commits may be skipped |
| **Wave topology** | Yes — `wave:` field generates multi-region DAG edges automatically | No | No |
| **Fleets of targets** | Yes — `fleet:` expands an environment into targets (listed, or selected by labels from Argo CD Applications or cluster inventory ClusterProfiles), `maxConcurrent` at a time, stopped after `maxUnavailable` failures (a count or a percentage), targets added or removed mid-rollout; pause, roll back or hold the whole fleet with `--env <fleet>` ([Fleets](pipeline-reference.md#fleets)) | Kargo Enterprise fan-out to fleets of targets with a concurrency limit, on its main branch, unreleased | No |
| **CLI** | Full `kardinal` CLI incl. `explain`, `policy simulate`, `override`, `approve`, `reject`, `pause`, `rollback`, `metrics`, `logs`, `validate`, `status`, shell completion | `kargo` CLI (get, promote, approve, verify, grant, logs and more) | Minimal: `gitops-promoter dashboard`, `demo` and `version`; no operational commands |
| **Explain and simulate gates** | Yes — `kardinal why` (`-o wide` for every step and gate) shows why an environment is blocked and the `approve` or `override` command that gets past each gate; `kardinal policy simulate` runs the gates at a chosen time | No | No (a Go package can simulate `WebRequestCommitStatus` expressions) |
| **Scripting exit codes** | Yes — `kardinal ship --wait` exits 0 Verified, 1 Failed, 3 Held, 4 WaitingForMerge, 5 Superseded, Rejected or Deleted, 6 Timeout or Interrupted, with `-o json` | Partial — `kargo promote --wait` waits for the Promotion to finish and exits 0 whether it Succeeded, Failed, Errored or was Aborted; 1 only when the command fails | No |
| **UI dashboard** | Embedded UI: fleet board (the version each environment of every pipeline runs, and releases on their way), fleet health bar, ops table, a station map of each pipeline, bundle timeline and comparison, policy gates with CEL expressions, metrics bar, step timings, Bundle type badges, dark and light themes, keyboard navigation (WCAG 2.1 AA checked); create bundle, pause/resume, promote, approve (with an approvals inbox), reject a version, override a gate and roll back from the UI, offering only what the caller's role allows | Polished Kargo UI: pipeline graph, Freight timeline and diffs, drag-and-drop promotion, step logs | Read-only dashboard, plus an Argo CD UI extension; both show promotion history |
| **First run** | Partial — `kardinal init` writes a Pipeline from a few questions (`--scaffold-gitops` adds the environment directories, `--demo` a sample app); `kardinal doctor` checks the cluster. kro and the chart are installed separately | Partial — quickstart scripts (`curl … \| sh`) create a kind or k3d cluster with Argo CD and Kargo; the UI has a guided project creation | Yes — `gitops-promoter demo` asks for a personal access token, a GitHub App ID and the App's private key, installs Argo CD and GitOps Promoter in the current cluster, creates a demo GitHub repository, and port-forwards the Argo CD UI, where GitOps Promoter shows as an extension |
| **Why is it waiting** | Yes — CLI: `kardinal why <pipeline> <env> -o wide` shows the step and the gates it waits on, those not ready first, with each gate's expression, state and reason. UI: an environment's details have a "Why it's waiting" section, and selecting a gate on the map opens it | Partial — CLI: `kargo get stages` shows each Stage's health, readiness and status message. UI: in promote mode a Freight that cannot go to a Stage says "Soak" with the time left, or "Non-approved freight" | Partial — no CLI. The dashboard lists each environment's proposed change with its checks (commit statuses) and their state |
| **Deep links** | Yes — every screen has a URL: a pipeline, an environment's details, a version, a gate, the comparison panel, the tab, the fleet filter and the ops table; old links redirect | Yes — URLs for a project, Stage, Warehouse, Freight and Promotion | Partial — URLs for a PromotionStrategy and its history |
| **Command palette** | Yes — ⌘K or Ctrl+K: go to any pipeline or environment, or a version of the pipeline you are on, or switch theme, density, the ops table and refresh; it never promotes, pauses or rolls back | No | No |
| **Metric-gated promotions** | Yes — `MetricCheck` CRD: Prometheus, Datadog, CloudWatch, New Relic, or any JSON API (`web`, JSONPath and a threshold); Secret-backed credentials; per-promotion queries templated with the Bundle version and environment ([Metric Checks](metric-checks.md)); no Argo Rollouts needed. And `verification` runs Argo Rollouts AnalysisTemplates per promotion (every Rollouts provider, Job and plugins; needs Argo Rollouts installed, and fails closed without it; [Analysis](analysis.md)) | Yes — verification with AnalysisTemplates (Prometheus, Datadog, CloudWatch, New Relic and others; needs Argo Rollouts installed) | Indirect — a `WebRequestCommitStatus` can call a metrics API |
| **DORA metrics** | Yes — all four: deployment frequency, lead time, change failure rate and time to restore in `Pipeline.status.deploymentMetrics`, `kardinal metrics` and the UI; per-step timings in `PromotionStep.status.steps` | No — operational Prometheus metrics only (v1.12) | No — on its roadmap ([#574](https://github.com/argoproj-labs/gitops-promoter/issues/574), its most-requested open issue) |
| **Distributed tracing** | Yes — OpenTelemetry over OTLP/HTTP, off by default: reconciles, promotion steps, git, SCM API calls, NotificationHook deliveries (with `traceparent`), inbound webhooks and Bundle API | On main, unreleased: OpenTelemetry tracing for control plane components ([kargo#7353](https://github.com/akuity/kargo/pull/7353), merged 2026-09-29); not in v1.12.4 | No |
| **Audit trail** | `AuditEvent` CRD (promotions, rollbacks, supersession, gate results, approvals, overrides, rejections and holds) and Kubernetes Events; since v0.12.0, each record's verified actor, who paused, resumed, froze and lifted, who merged each `pr-review` PR, and `kardinal audit export` | Kubernetes Events; `record-audit-event` step in Kargo Enterprise (v1.12) | `ChangeTransferPolicyHistory` (last 20 promotions per environment, v0.41), git notes, Kubernetes Events |
| **Custom promotion steps** | Partly — the sequence itself is fixed (Bundle type, `update.strategy`, `approval`; [Promotion Steps](pipeline-reference.md#promotion-steps)); `update.strategy: yaml` sets any YAML paths in several files (Kargo's `yaml-update`), and each environment can run its own Jobs before it starts and after its health check ([hooks](hooks.md)); no custom git or render steps | Yes — about 35 built-in steps composed in a Stage's `promotionTemplate`, reusable PromotionTasks, conditions and retries; container steps in Kargo Enterprise (v1.10+) | No |
| **Rendered manifests (hydration)** | Yes — `layout: branch`: kustomize build or helm template in a sandboxed render Job per promotion (no credentials but the Pipeline's git Secret, resource and time limits), committed to `env/<name>` with the DRY commit in the trailers; pr-review PRs show rendered diffs; drift on the rendered branch fails the promotion; rollback re-renders the old DRY commit ([Rendered Manifests](rendered-manifests.md)) | Yes — `kustomize-build` and `helm-template` steps write to a stage branch in a promotion template | Yes — its core model: hydrated environment branches from the Argo CD Source Hydrator or any hydrator, with the DRY commit in hydrator metadata |
| **Integration test step** | Yes — a post-deploy [hook](hooks.md) Job runs after the health check; the environment is Verified, and the next one starts, only when it succeeded; a failure applies `onHealthFailure`. Pre-deploy hooks run migrations before the change | Yes — verification can run a Kubernetes Job (AnalysisTemplate); an `http` step can poll a test service | No (a Job gate is on its roadmap) |
| **Image signature verification** | Yes — `spec.imageVerification` checks cosign key and Sigstore keyless signatures (sigstore-go) before a Bundle is promoted, requires digest-pinned images, and can require the config commit to be signed (SCM-verified); pair it with admission-time verification in the workload cluster ([Image Signature Verification](image-verification.md)) | No (Kargo Enterprise's `jfrog-evidence` step can verify signed JFrog evidence) | No (git commit signature checks are on its roadmap) |
| **Approvals with quorum** | Yes — approval gates: N distinct approvers from allowed users and groups, a counted reject blocks, no self-approval; identity verified by the API server (ValidatingAdmissionPolicy); for `auto` environments too. `kardinal approve` and the UI (stage drawer and approvals inbox) | Manual Freight approval (`kargo approve`): one approver, no quorum. Kargo Enterprise: `wait-for-approval` with approver quorum (v1.12, beta) | Through the SCM: PR reviews and branch protection; no approval object of its own |
| **Emergency gate override** | Yes — `kardinal override`: time-limited, with a mandatory reason; recorded in the gate's `spec.overrides`, a `GateOverridden` AuditEvent and the PR evidence. The user name is the Kubernetes identity the API server authenticated, enforced by a ValidatingAdmissionPolicy | Manual Freight approval (`kargo approve`), with no reason or expiry | Merge the PR by hand; nothing is recorded |
| **Outbound event notifications** | Yes — `NotificationHook` CRD with native Slack (Block Kit) and Microsoft Teams (Adaptive Card) formats, JSON, or a templated body for any HTTP API; ten events (Bundle verified, failed, superseded, rollback started and done; gate blocked and unblocked; step failed, PR opened, waiting for approval); auth header and URL from a Secret; HMAC-signed requests with a replay-proof timestamp; CloudEvents 1.0 format; per-event dedupe key; controller egress allowlist; pipeline selector | Kargo Enterprise only: event routing to Slack, email or HTTP, and a `send-message` step (v1.8+). Open source emits Kubernetes Events, and an `http` step can call a webhook | Kubernetes Events only (a webhook CRD is in a draft PR) |
| **Multi-cluster** | Argo CD or Flux hub (health read from the hub's Applications or Kustomizations), or direct remote-cluster health through a kubeconfig Secret (`health.kubeconfigSecretRef`, inline credentials only) | Yes — through Argo CD; controllers can be sharded across clusters | Yes — the Argo CD gate reads remote clusters through kubeconfig Secrets |
| **Upstream soak time in gates** | Yes — `bundle.upstreamSoakMinutes >= 30` (minutes since the upstream was Verified) | Yes — `requiredSoakTime` (elapsed time) | Yes — a `TimedCommitStatus` on the upstream, which the ordering gate requires |
| **Cross-stage history in gates** | Yes — `upstream.<env>.recentSuccessCount`, `lastPromotedAt` | No | No |
| **Artifact discovery** | Bundle created by CI (HTTP API, GitHub Action) or the CLI; the Subscription CRD watches OCI images, Git (HTTPS or SSH, `pathGlob`) and Helm charts (HTTP index or OCI), public or private (pull Secrets; ECR, GCR and ACR through refreshed token Secrets), with semver, regex and allow/ignore filters, and webhook receivers for Docker Hub, GHCR, Harbor, Quay, Artifactory and generic senders | Warehouse: images, Git and Helm charts, with webhook receivers for registries and SCMs | Git commits from a hydrator (Argo CD Source Hydrator or any other) |
| **Multi-artifact bundle** | Yes (image + config in one Bundle) | Yes (Freight, with creation criteria since v1.8) | No |
| **Architecture** | Graph-first (one kro Graph per Bundle) | Controllers over Stage and Freight CRDs; v2.0 will replace the storage layer | Controller, plus an optional API server for the dashboard |
| **Maturity** | v0.12.0, active development; runs on kro's alpha Graph API (v0.10.0-rc.0) | v1.12.4, production-grade; v2.0.0 is next | v0.45.3, `v1alpha1` API, "experimental" per its README |
| **License** | Apache 2.0 | Apache 2.0 | Apache 2.0 |

---

## Agentic governance

How each tool governs what AI agents change, from each vendor's documentation as of October 2026.
Each ↗ links the page the cell rests on. "—" means we found no such control in the vendor's docs
(the ↗ is the main page we read), not that it cannot exist. kardinal's column describes main, which is not released yet: its local
agentic features (MCP, verified actors, actor-aware policy, verify gates, freezes) ship in v0.12.0. The [threat model](agentic/threat-model.md) maps real incidents to
these controls.

| | kardinal | Akuity Kargo | Harness | Octopus | GitHub | GitLab | Argo CD MCP | Flux MCP |
|---|---|---|---|---|---|---|---|---|
| **Enforced at the target** | **Yes** | Yes [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | Partial [↗](https://github.com/harness/mcp-server "Source: Harness") | Partial [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | Yes [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") | Partial [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | Partial [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | Partial [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Verified actor and sponsor** | **Yes** | Partial [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | Partial [↗](https://developer.harness.io/harness-ai/use-harness-ai/core-capabilities/in-your-pipelines/worker-agent/permissions "Source: Harness") | Partial [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | Yes [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") | Yes [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Separation of duties between agents** | **Yes** | — [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | — [↗](https://github.com/harness/mcp-server "Source: Harness") | — [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | Partial [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") [↗](https://docs.github.com/en/actions/how-tos/deploy/configure-and-manage-deployments/manage-environments "Source: GitHub") | Partial [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Agent approvals opt-in** | **Yes** | — [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | — [↗](https://github.com/harness/mcp-server "Source: Harness") | — [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | — [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") | — [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Agents as verifiers** | **Yes** | — [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | — [↗](https://github.com/harness/mcp-server "Source: Harness") | — [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | Partial [↗](https://docs.github.com/en/actions/how-tos/deploy/configure-and-manage-deployments/manage-environments "Source: GitHub") | — [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Budgets** | **No** | — [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | — [↗](https://github.com/harness/mcp-server "Source: Harness") | — [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | — [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") | — [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Kill switch** | **Yes** | Partial [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | — [↗](https://github.com/harness/mcp-server "Source: Harness") | Partial [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | Partial [↗](https://docs.github.com/en/copilot/how-tos/administer-copilot/manage-for-organization/add-copilot-cloud-agent "Source: GitHub") | Partial [↗](https://docs.gitlab.com/user/duo_agent_platform/turn_on_off/ "Source: GitLab") | Partial [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | Partial [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Receipts** | **Partial** | Partial [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | Partial [↗](https://github.com/harness/mcp-server "Source: Harness") | Partial [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | Partial [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") | Partial [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Pre-flight** | **Yes** | Partial [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | — [↗](https://github.com/harness/mcp-server "Source: Harness") | — [↗](https://octopus.com/docs/octopus-ai/mcp/remote "Source: Octopus") | — [↗](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations "Source: GitHub") | — [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | — [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | — [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |
| **Read-only by default** | **Yes** | Yes [↗](https://akuity.io/blog/agentic-control-plane-mcp-server "Source: Akuity Kargo") | Partial [↗](https://github.com/harness/mcp-server "Source: Harness") [↗](https://developer.harness.io/harness-ai/use-harness-ai/core-capabilities/in-your-pipelines/worker-agent/permissions "Source: Harness") | No [↗](https://github.com/OctopusDeploy/mcp-server "Source: Octopus") | No [↗](https://github.com/github/github-mcp-server "Source: GitHub") | — [↗](https://docs.gitlab.com/user/duo_agent_platform/composite_identity/ "Source: GitLab") | No [↗](https://github.com/argoproj-labs/mcp-for-argocd "Source: Argo CD MCP") | No [↗](https://fluxoperator.dev/docs/mcp/config/ "Source: Flux MCP") |

Akuity Kargo is its Agentic Control Plane and MCP server; GitHub is the Copilot cloud agent,
GitHub environments and the GitHub MCP server; GitLab is the Duo Agent Platform. A "Partial"
is narrower than the row: for example GitHub's and GitLab's separation of duties is between the
agent and the human who asked for the change, every "Receipts" partial is an unsigned audit
log, a "Kill switch" partial is a setting that turns the agent or MCP access off rather than a
hold on promotions, Kargo's "Pre-flight" is a server-side dry run of a change, not whether a
version would be admitted, and Harness's worker agents start read-only while its MCP server does
not.

What kardinal does for each row today:

| Control | kardinal on main |
|---|---|
| **Enforced at the target** | Yes: the API server's admission policies and the controller's gates judge every promotion, whoever started it |
| **Verified actor and sponsor** | Yes: an `AgentIdentity` registers each agent with its human sponsor and scope, and every version, Approval and override records its actor (human, agent with sponsor, bot or kardinal), verified at admission; model and session are kept apart as asserted ([Agents and the verified actor](actors.md)) |
| **Separation of duties between agents** | Yes: with `excludeAuthor` an approval gate counts parties, not people: an agent's sponsor cannot approve its work, an agent cannot approve its sponsor's, and two agents of one sponsor cannot approve each other's; verify gates never count the author agent's party ([one party, one vote](approvals.md#one-party-one-vote)) |
| **Agent approvals opt-in** | Yes: an agent's approval counts only on a gate with `approval.allowAgents` that lists it in `allowedAgents`, and never on its own party's work ([Agents as approvers](approvals.md#agents-as-approvers)) |
| **Agents as verifiers** | Yes: a verify gate holds an environment for `required` passing Verdicts on the version, from the agents it allows (never the author agent's party), with `maxFailures`, `onInconclusive` and a timeout; an AgentRun runs an admin-registered agent image in the cluster to write one ([Verify with agents](agentic/verify-with-agents.md)). Post hooks and Argo Rollouts analyses also gate an environment |
| **Budgets** | No: per-agent budgets are [planned](roadmap.md#v014-adaptive) |
| **Kill switch** | Yes: `kardinal freeze --agents` holds every promotion by agents, `kardinal freeze --everyone` stops the line, cluster-wide or for one namespace, enforced by the controller; a person's break-glass exemption is verified and audited, and never counts for an agent ([Freezes](agentic/freezes.md)). Pipelines and fleets also pause |
| **Receipts** | Partial: PR evidence and append-only AuditEvents; signed promotion receipts are [planned](roadmap.md#v013-evidence) |
| **Pre-flight** | Yes: the MCP `simulate` tool (`kardinal policy simulate --version`) says whether a version would be allowed into an environment now, and what would let it in |
| **Read-only by default** | Yes: `kardinal mcp` offers write tools only with `--allow-write` |

---

## Why kardinal-promoter

Unique to kardinal: CEL gates that combine schedule, soak, history, metrics and change windows;
the contiguous healthy soak; wave topology; gate results and provenance in the PR body; DORA metrics;
`explain` and `policy simulate`; and auto-rollback in open source.

Open requests in their trackers that kardinal already ships:

- Kargo: superseding queued promotions ([#3108](https://github.com/akuity/kargo/issues/3108)), time windows in open source ([#1328](https://github.com/akuity/kargo/issues/1328)), a system-wide stop ([#1422](https://github.com/akuity/kargo/issues/1422)), and provenance in PR bodies ([#2830](https://github.com/akuity/kargo/issues/2830)).
- GitOps Promoter: DORA metrics ([#574](https://github.com/argoproj-labs/gitops-promoter/issues/574)) and holding a promotion ([#1803](https://github.com/argoproj-labs/gitops-promoter/issues/1803)).

### Graph-native policy evaluation

kardinal PolicyGates are nodes in the kro DAG. A gate's CEL expression reads the Bundle, the
schedule, upstream soak and promotion history, metrics and change windows, not just the current stage:

```yaml
# In a prod PolicyGate — reads upstream uat stage's soak time
expression: "bundle.upstreamSoakMinutes >= 60"

# Read a MetricCheck result
expression: 'metrics["error-rate"].result == "Pass"'

# Combine schedule + soak + metadata
expression: '!schedule.isWeekend && bundle.upstreamSoakMinutes >= 30 && !(has(bundle.labels.hotfix) && bundle.labels.hotfix == "true")'
```

Kargo (`requiredSoakTime`) and GitOps Promoter (`TimedCommitStatus`) can wait a fixed time
after the upstream promotion. Neither resets the timer on a health alarm, or combines soak
time with schedule, metrics and Bundle metadata in one expression. Both use expr-lang
elsewhere: Kargo in promotion steps, which fail a promotion rather than hold it, and
GitOps Promoter in its commit and web-request gates.

### Structured PR evidence

Every PR kardinal opens (environments with `approval: pr-review`) has a body like this
(`pkg/scm/pr_template.go`; see [PR Evidence](pr-evidence.md)):

```markdown
## Promotion: my-app-x7k2p -> my-app/prod

### Artifact Provenance

| Image | Tag | Digest | CI Run | Commit SHA | Author |
|---|---|---|---|---|---|
| ghcr.io/myorg/my-app | 1.29.0 | sha256:a1b2c3d4 | [CI run](https://github.com/myorg/my-app/actions/runs/12345) | abc123d | engineer-name |

### Policy Gate Compliance

| Gate | Namespace | Result | Reason | Last Evaluated |
|---|---|---|---|---|
| no-weekend-deploys | my-app | Pass | !schedule.isWeekend = true (version 1.29.0) | 2026-04-14T14:00Z |

### Upstream Verification

| Environment | Health Checked At | Elapsed |
|---|---|---|
| staging | 2026-04-14T13:15Z | 45m |
```

Kargo's default PR body is the commit message and a link to the Kargo UI. You can template the
description with expressions, but Kargo adds no gate or verification results. GitOps Promoter's
default template lists the deployed and proposed commits, a diff link and the promotion pipeline
table; its gate results appear as SCM checks, not in the body.

### Contiguous healthy soak

`bake.minutes` counts *contiguous* healthy minutes — if a health alarm fires during the
soak window, the timer resets to zero. The deployment must survive a full `bake.minutes`
window with no alarms. Kargo's `requiredSoakTime` counts time since the Freight was verified
in the upstream Stage, and GitOps Promoter's `TimedCommitStatus` counts time since the commit
merged. Neither resets when the service turns unhealthy.

### Change freeze management

A `ChangeWindow` is a cluster-scoped object: a one-off blackout (`start`/`end`) or a
recurring allowed window (days, hours, timezone). Platform teams reference it from org-level
PolicyGates (`!changewindow.isBlocked("freeze")`, one gate per environment name such as `prod`),
so one object blocks every pipeline's matching environment during incidents, holidays, or
maintenance windows, with no per-pipeline changes. The freeze stops steps that have not
started yet. `kardinal freeze --everyone` (stop-the-line) or `--agents` needs no gate at all: a
ChangeWindow with `spec.freeze` holds every step it selects, including Graphs built before it, and
`kardinal override --freeze` lets one change through with an audited exemption
([Freeze agents, stop the line](agentic/freezes.md)). Kargo open source has no equivalent; Kargo Enterprise has promotion windows
(v1.12, beta), checked when a Promotion is created. GitOps Promoter has `ScheduledCommitStatus`,
set up per PromotionStrategy.

### Wave topology for multi-region rollouts

The `wave:` field on Pipeline environments generates DAG dependency edges automatically:
wave 2 cannot start until all wave 1 stages are verified. This makes the prod-wave-1 →
prod-wave-2 → prod-wave-3 pattern idiomatic in three lines of YAML. Kargo and GitOps
Promoter have no wave concept.

### Fleets

A `fleet:` environment promotes one release to many targets, such as clusters, regions or
tenants. The targets are listed, or selected by labels from Argo CD Applications or cluster
inventory ClusterProfiles. They are promoted `maxConcurrent` at a time, and the rollout stops once
`maxUnavailable` of them (a count or `"10%"`) have failed. Each target has its own PR, gates and
health check, and targets can be added or removed while a release rolls out. `kardinal pause`,
`rollback` and `hold` take the fleet's name to act on every target. Kargo's main branch has an unreleased
Kargo Enterprise fan-out to fleets of targets with a concurrency limit. GitOps Promoter has none.

### DORA metrics built-in

`Bundle.status.metrics` records `commitToProductionMinutes`, `bakeResets` and `operatorInterventions`
(the `kardinal override` entries on the Bundle's gates) for every promotion.
`Pipeline.status.deploymentMetrics` adds rollouts in the last 30 days, p50 and p90 lead time,
the automatic and overall rollback rates, the operator intervention rate, and the change failure
rate and mean time to restore over the last 30 deployments to the last environment. Lead time
starts at the commit when CI sets `provenance.commitTime`, else when the Bundle is created.
The `kardinal metrics` CLI surfaces these per pipeline. Kargo v1.12 added operational Prometheus
metrics, not DORA metrics; GitOps Promoter lists DORA metrics on its roadmap.

### Auto-rollback and health-failure policy

With `onHealthFailure: rollback` (the default is `none`), kardinal creates a rollback Bundle
when a promotion fails health verification. Each stage can independently configure `onHealthFailure: rollback | abort | none`.
Combined with `bake.policy: fail-on-alarm`, this gives fine-grained control: critical stages
abort and require human intervention; non-critical stages roll back automatically.
GitOps Promoter has no automated rollback. Kargo has it only in Kargo Enterprise (beta),
triggered when verification fails.

### Fan-out DAG topology

```station-map
title: A fan-out pipeline
caption: Staging fans out to two prod regions. The version is Verified once both are.
stations:
  test: verified
  uat: verified
  staging: verified
  prod-us: {state: verified, status: Verified}
  prod-eu: {state: promoting, status: Promoting}
edges:
  - test -> uat
  - uat -> staging
  - staging -> prod-us, prod-eu
thread: [test, uat, staging, prod-us, prod-eu]
```

All three tools can run this. kardinal models it natively, and `wave:` can generate the edges.
Kargo builds it from Stages that request Freight from upstream Stages. GitOps Promoter builds
it with `dependsOn` on its environments.

### Signed artifacts only

`spec.imageVerification` refuses to promote a Bundle whose images are not signed by an accepted
key or Sigstore keyless identity, or whose config commit is not signed. Neither Kargo (open
source) nor GitOps Promoter verifies signatures today; GitOps Promoter has commit signature checks
on its roadmap. See [Image Signature Verification](image-verification.md).

---

## Where Kargo or GitOps Promoter is ahead

- **Composable promotion steps** (Kargo). About 35 built-in steps (git, Helm, Kustomize, YAML,
  OCI, HTTP, Argo CD), reusable PromotionTasks, conditions and retries. kardinal runs a fixed
  sequence per environment, with your own Jobs only before and after it ([hooks](hooks.md)).
- **Access control and API** (Kargo). Projects with per-project roles, OIDC claim mapping,
  API tokens and a REST API.
- **Gates mirrored to the SCM** (GitOps Promoter). Commit statuses appear as SCM checks and are
  re-checked for every new commit until the PR merges. kardinal mirrors all of an environment's
  gates as one `kardinal/gates` status, on the commit it pushed to the PR branch.
- **Pluggable gates** (GitOps Promoter). Any controller can write a `CommitStatus`.
- **Monorepos** (GitOps Promoter). Several PromotionStrategies can share one environment
  branch (`activePath`).

## Known limitations

kardinal-promoter is the right tool for most of what is described above. There are cases
where a different approach may be a better fit — not because the competition is better,
but because the use case doesn't match what kardinal is designed for.

**You want native ArgoCD updates with a PR review.**
`update.strategy: argocd` patches the Argo CD Application directly, with no git commit (the
equivalent of Kargo's `argocd-update`), but it supports only `approval: auto`: the API server
rejects a Pipeline that sets `approval: pr-review` on the same environment. It promotes image
Bundles only. See [ArgoCD-Native Promotion](argocd-native-promotion.md).

**You need custom promotion steps.**
kardinal runs a fixed step sequence per environment. [Hooks](hooks.md) run Jobs before it
starts and after the health check (migrations, tests), but not between its git steps. If each
environment needs its own build, render or update steps, Kargo's promotion templates fit better.

**You want zero state outside Git.**
kardinal maintains state in Kubernetes CRDs (Pipeline, Bundle, PromotionStep, AuditEvent and others).
If your constraint is that every promotion artefact must be a git commit with no
Kubernetes-side state, kardinal is not the right model.

**You need a larger community and commercial support today.**
kardinal-promoter is pre-1.0 and in active development, on kro's alpha Graph API. Kargo has a
longer production track record and commercial backing from Akuity. If your organisation requires vendor
support or a larger existing community before adopting, that is a legitimate constraint.

**Your only gate requirement is "a human clicks approve."**
kardinal's DAG, CEL gates, and structured evidence add meaningful complexity. If your
promotion workflow is simply "CI passes, human approves PR," that complexity is overhead
you don't need.

---

## When to choose kardinal-promoter

- You want **expressive, cross-stage policy gates** — soak time, upstream history, metrics, schedule, bundle metadata, PR approval state — without writing webhook servers
- You need **contiguous healthy soak** — deployments must survive bake windows with zero health alarms, not just elapsed time
- You want **wave topology** for multi-region production rollouts — promote to 1 region, bake, then expand to the next wave
- You want a **centralized change freeze** — one `ChangeWindow` object, referenced by org-level gates, blocks every pipeline during incidents or holidays
- You use **ArgoCD + Flux mixed**, or neither — kardinal doesn't require a specific GitOps engine
- You want **structured PR evidence** so reviewers have full promotion context in the PR body
- You want **auto-rollback** triggered by health check failures, with per-stage abort vs. rollback vs. ignore policy, in open source
- You want a **newer release to supersede an older one** still in flight, and to **pause** a pipeline mid-promotion
- You want to **see why a promotion is blocked** (`kardinal why -o wide`) and test a policy before it applies (`kardinal policy simulate`)
- You are a **platform team** that needs org-level policies automatically applied to all pipelines, which a team Pipeline cannot remove (break-glass overrides are time-limited and recorded, and need `patch` on PolicyGates in the Pipeline namespace)
- You want **DORA metrics** — time-to-production, rollback rate, operator interventions — surfaced per pipeline
- You need a **time-limited emergency override** that records its reason on the gate and in the PR evidence, not a silent bypass

---

## Further Reading

- [Concepts](concepts.md) — kardinal-promoter's core model
- [Policy Gates](policy-gates.md) — CEL expression reference
- [Architecture](architecture.md) — system design
- [FAQ](troubleshooting.md#common-questions) — common questions
- [Coming from Kargo](coming-from-kargo.md) — each Kargo concept beside its kardinal counterpart
- [Migrating from Kargo](guides/migrating-from-kargo.md) — a migration walkthrough
