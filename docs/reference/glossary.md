# Glossary

kardinal has four core words: **version**, **pipeline**, **environment** and **gate**
([Concepts](../concepts.md)). Every other term maps to one of them or to an object they use.

| Term | Kubernetes kind or field | Meaning |
|---|---|---|
| Version | `Bundle` | One immutable snapshot of what to deploy: images, a chart version or a config commit, with its provenance. The CLI and the UI also say Bundle |
| Pipeline | `Pipeline` | The promotion path of one application: its Git repository and its environments, in order |
| Environment | `Pipeline` `spec.environments[]` | One place a version is deployed to, such as `test`, `uat` or `prod`; its directory or branch in the GitOps repository |
| Stage, promotion step | `PromotionStep` | The promotion of one version into one environment: commit or pull request, merge, health check. kardinal creates them |
| Gate | `PolicyGate` | A CEL expression that holds a version out of an environment until it is true |
| Org gate, team gate | `PolicyGate` in a `--policy-namespaces` namespace, or in the Pipeline's | An org gate applies to every Pipeline and teams cannot remove it; a team gate adds to it |
| Approval gate | `PolicyGate` `spec.approval`, `Approval` | A gate that passes once enough people ran `kardinal approve` |
| Override | `PolicyGate` `spec.overrides[]` | A time-limited, recorded force-pass of a gate (`kardinal override`) |
| Change window | `ChangeWindow` | A cluster-wide blackout or allowed-hours window a gate can read |
| Metric check | `MetricCheck` | A metric query with a threshold a gate can read (`metrics.<name>`) |
| Soak, bake | `bundle.upstreamSoakMinutes`, `bake` | How long a version has been healthy upstream; `bake` requires continuous health before Verified |
| Health check | `health` | How a promotion step decides its environment runs the version healthily (Deployment, Argo CD, Flux, Argo Rollouts, Flagger) |
| Hook | `hooks`, `HookRun` | A Job that runs before a promotion or after its health check |
| Wave | `wave` | A number that promotes environments with the same number in parallel |
| Fleet | `fleet` | One environment expanded to many targets, promoted with a pace |
| Supersede | phase `Superseded` | A newer version of the same type replaces an unfinished older one ([details](../architecture.md#bundle-supersession)) |
| Reject | `spec.rejected` | Stop a version everywhere for good (`kardinal reject`) |
| Hold | Pipeline `spec.holds` | Keep an environment on a rolled-back version (`kardinal rollback --hold`) |
| Pause | Pipeline `spec.paused` | Stop new promotions of a Pipeline (`kardinal pause`) |
| PR evidence | | The provenance, gate results and upstream health kardinal writes into a promotion pull request |
| Subscription | `Subscription` | Watches a registry, Git or a chart repository and creates versions |
| Rendered manifests | `layout: branch`, `RenderRun` | kardinal renders an environment and commits plain YAML to its branch |
| Graph | kro `Graph` | The DAG kardinal builds for each version; kro creates its gates and promotion steps ([Architecture](../architecture.md)) |
| Retire | condition `GraphRetired` | Delete a finished version's Graph, keeping a record of its steps ([details](../architecture.md#graph-retirement)) |
| Audit event | `AuditEvent` | An immutable record of a promotion transition ([Audit logging](../guides/security.md#audit-logging)) |
