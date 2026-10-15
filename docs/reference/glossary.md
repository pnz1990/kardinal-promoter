# Glossary

kardinal has four core words: **version**, **pipeline**, **environment** and **gate**
([Concepts](../concepts.md)). Every other term maps to one of them or to an object they use.

| Term | Kubernetes kind or field | Meaning | Shown as |
|---|---|---|---|
| Version | `Bundle` | A version of your app: one immutable snapshot of what to deploy (images, a chart version or a config commit) with its provenance. The `kardinal create bundle` and `get bundles` commands act on versions |  |
| Pipeline | `Pipeline` | The promotion path of one application: its Git repository and its environments, in order |  |
| Environment | `Pipeline` `spec.environments[]` | One place a version is deployed to, such as `test`, `uat` or `prod`; its directory or branch in the GitOps repository |  |
| Stage, promotion step | `PromotionStep` | The promotion of one version into one environment: commit or pull request, merge, health check. kardinal creates them | [its state](#promotion-step): Queued, Live, ... |
| Gate | `PolicyGate` | A CEL expression that holds a version out of an environment until it is true | [its state](#gate): Passing, Blocking, ... |
| Org gate, team gate | `PolicyGate` in a `--policy-namespaces` namespace, or in the Pipeline's | An org gate applies to every Pipeline and teams cannot remove it; a team gate adds to it |  |
| Approval gate | `PolicyGate` `spec.approval`, `Approval` | A gate that passes once enough people ran `kardinal approve` |  |
| Override | `PolicyGate` `spec.overrides[]` | A time-limited, recorded force-pass of a gate (`kardinal override`) |  |
| Change window | `ChangeWindow` | A cluster-wide blackout or allowed-hours window a gate can read |  |
| Metric check | `MetricCheck` | A metric query with a threshold a gate can read (`metrics.<name>`) |  |
| Soak, bake | `upstream.<env>.soakMinutes`, `bake` | How long a version has been Verified in an upstream environment. `upstream.<env>.soakMinutes` names the environment; `bundle.upstreamSoakMinutes` is shorthand for the direct upstream (the minimum on fan-in). `bake` requires continuous health before Verified |  |
| Health check | `health` | How a promotion step decides its environment runs the version healthily (Deployment, Argo CD, Flux, Argo Rollouts, Flagger) |  |
| Hook | `hooks`, `HookRun` | A Job that runs before a promotion or after its health check |  |
| Wave | `wave` | A number that promotes environments with the same number in parallel |  |
| Fleet | `fleet` | One environment expanded to many targets, promoted with a pace |  |
| Supersede | phase `Superseded` | A newer version of the same type replaces an unfinished older one ([details](../architecture.md#bundle-supersession)) | Replaced by newer version |
| Reject | `spec.rejected` | Stop a version everywhere for good (`kardinal reject`) | Rejected (a version), Version rejected (a gate) |
| Hold | Pipeline `spec.holds` | Keep an environment on what runs there (`kardinal hold`) or on a rolled-back version (`kardinal rollback --hold`) | Held by rollback |
| Pause | Pipeline `spec.paused`, `spec.pausedFleets` | Stop new promotions of a Pipeline, or of one fleet (`kardinal pause`, `--env <fleet>`) | Paused |
| PR evidence | | The provenance, gate results and upstream health kardinal writes into a promotion pull request |
| Subscription | `Subscription` | Watches a registry, Git or a chart repository and creates versions |  |
| Rendered manifests | `layout: branch`, `RenderRun` | kardinal renders an environment and commits plain YAML to its branch |  |
| Graph | kro `Graph` | The DAG kardinal builds for each version; kro creates its gates and promotion steps ([Architecture](../architecture.md)) |  |
| Retire | condition `GraphRetired` | Delete a finished version's Graph, keeping a record of its steps ([details](../architecture.md#graph-retirement)) |  |
| Audit event | `AuditEvent` | An immutable record of a promotion transition ([Audit logging](../guides/security.md#audit-logging)) |  |

## States and how they are shown

The CLI's tables and the UI show each state as a label; `-o json`, `-o yaml`, the Kubernetes
objects and the UI API keep the API value. The UI API serves this table at
`GET /api/v1/ui/display-states`.

### Pipeline

| API value | Shown as |
|---|---|
| `Unknown` | No version yet |
| `Promoting` | Promoting |
| `Ready` | Up to date |
| `Degraded` | A version failed |
| `Paused` | Paused |

### Version

| API value | Shown as |
|---|---|
| `Available` | Ready to promote |
| `Promoting` | Promoting |
| `Verified` | Verified |
| `Failed` | Failed |
| `Superseded` | Replaced by newer version |
| `Rejected` | Rejected |

### Promotion step

| API value | Shown as |
|---|---|
| `NotStarted` | Not reached |
| `Pending` | Queued |
| `Promoting` | Promoting |
| `WaitingForMerge` | Waiting for PR merge |
| `HealthChecking` | Checking health |
| `Verifying` | Verifying |
| `Verified` | Live |
| `Failed` | Failed |
| `AbortedByAlarm` | Stopped by alarm |
| `RollingBack` | Rolling back |
| `Superseded` | Replaced by newer version |

An environment whose step waits for a gate is shown as "Held by gate <gate>", and one held on a
rolled-back version as "Held by rollback".

### Sub-step

The steps inside a promotion step (`status.steps[]`: git-clone, open-pr, health-check, ...).

| API value | Shown as |
|---|---|
| `Pending` | Not started |
| `InProgress` | In progress |
| `Completed` | Done |
| `Failed` | Failed |

### Gate

| API value | Shown as |
|---|---|
| `Pending` | Not evaluated yet |
| `Waiting` | Not ready |
| `Pass` | Passing |
| `Block` | Blocking |
| `Superseded` | Replaced by newer version |
| `Rejected` | Version rejected |
