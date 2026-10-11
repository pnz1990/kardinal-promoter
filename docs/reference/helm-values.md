# Helm values

## kardinal-promoter controller

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Number of controller replicas |
| `image.repository` | `ghcr.io/pnz1990/kardinal-promoter/controller` | Controller image |
| `image.tag` | Chart `appVersion` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `imagePullSecrets` | `[]` | Image pull secrets for private registries |
| `nameOverride` / `fullnameOverride` | `""` | Override the chart name / the full name (`<fullname>`) used in object names |
| `serviceAccount.create` / `.name` / `.annotations` | `true` / `""` / `{}` | Create the controller ServiceAccount; its name (default `<fullname>`) and annotations |
| `podAnnotations` | `{}` | Controller Pod annotations |
| `podSecurityContext` | `runAsNonRoot`, `RuntimeDefault` seccomp | Pod security context |
| `securityContext` | non-root, read-only root filesystem, no privilege escalation, all capabilities dropped | Container security context |
| `logLevel` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`). Sets both `--log-level` and `--zap-log-level` (`warn` maps to `error` there) |
| `leaderElect` | `true` | Enable leader election (required for HA) |
| `leaderElectionFlowSchema.enabled` | `true` | Create a FlowSchema that sends the controller's leader election Lease requests to the `leader-election` priority level, so a busy or throttled controller does not lose leadership ([Leader election under API pressure](../operate.md#leader-election-under-api-pressure)). Cluster-scoped: the installer needs create/get/update/delete on `flowschemas.flowcontrol.apiserver.k8s.io`; set `false` for GitOps installs that may not manage cluster resources |
| `leaderElectionFlowSchema.matchingPrecedence` | `90` | The FlowSchema's precedence: lower than any FlowSchema matching the controller's other requests |
| `leaderElectionFlowSchema.priorityLevel` | `leader-election` | Priority level for the Lease requests |
| `scm.secretRef.name` | `""` | Existing Secret (release namespace) holding the SCM token. Recommended. Never label it `kardinal.io/referenceable` and never name it in a Pipeline's `git.secretRef`: the controller refuses that ([Security](../guides/security.md#the-controllers-own-scm-token)) |
| `scm.secretRef.key` | `""` (`token`) | Key in the Secret; empty is `token` |
| `scm.token` | `""` | Token value. The chart stores it in Secret `<fullname>-github-token` (`kardinal-promoter-github-token` for release `kardinal-promoter`); the value stays in the Helm release history. Setting both this and `scm.secretRef.name` fails |
| `github.secretRef.*`, `github.token` | `""` | The old names of `scm.secretRef.*` and `scm.token`; they keep working. Setting an old and a new name to different values fails |
| `github.app.enabled` | `false` | The `scm.secretRef.name` Secret holds GitHub App credentials (`githubAppID`, `githubAppInstallationID`, `githubAppPrivateKey`) instead of a token; see [GitHub App](../scm-providers.md#github-app). Needs `scm.secretRef.name` |
| `scm.provider` | `""` | `--scm-provider`: `github` (default), `gitlab`, `forgejo`, `gitea`, `bitbucket`, `azuredevops` |
| `scm.apiURL` | `""` | `--scm-api-url` for self-hosted SCM instances |
| `scm.allowedRepositories` | `[]` | `--scm-allowed-repositories`: `host/repository` globs (`github.com/acme/*`, `gitlab.example.com/team/**`) the controller's SCM token may act on. Every SCM call for another repository is refused, and a Pipeline that would need the token for one is `Ready=False/RepositoryNotAllowed`. Empty allows every repository. See [Security](../guides/security.md#the-shared-scm-token-and-scmallowedrepositories) |
| `scm.instanceSigners` | `[]` | `--scm-instance-signers`: Forgejo/Gitea only, the names or emails the instance signs commits with (`repository.signing` `SIGNING_NAME` / `SIGNING_EMAIL`). [Image verification](../image-verification.md#signed-commits) treats such a commit as a platform signature. Without it, a verified signer that is not a user of the instance is taken as the instance key |
| `scm.gatesCommitStatus.enabled` | `true` | `--gates-commit-status`: post the gate results of a waiting pr-review step as the commit status on the commit kardinal pushed to its PR ([Gate status check](../pr-evidence.md#gate-status-check-kardinalgates)). `false` posts none. |
| `scm.gatesCommitStatus.context` | `kardinal/gates` | `--gates-status-context`: the status name branch protection requires; reserved for kardinal. |
| `webhook.secretRef.name` / `.key` | `""` / `secret` | Secret with the SCM webhook secret (`KARDINAL_WEBHOOK_SECRET`): the HMAC key, or for GitLab and Azure DevOps the plain token |
| `bundleAPI.tokenSecretRef.name` / `.key` | `""` / `token` | Secret with the Bundle API bearer token (`KARDINAL_BUNDLE_TOKEN`). `POST /api/v1/bundles` is off until this is set |
| `ui.auth.tokenSecretRef.name` / `.key` | `""` / `token` | Secret with a static UI API bearer token (`KARDINAL_UI_TOKEN`). With neither this nor `ui.auth.tokenReview` set, the UI API serves only local clients (`kubectl port-forward`) |
| `ui.auth.tokenReview` | `false` | `--ui-tokenreview-auth`: validate UI tokens with TokenReview; adds the RBAC it needs |
| `ui.auth.allowStaticTokenWithTokenReview` | `false` | Install with both a static UI token and `ui.auth.tokenReview` (the static token wins); refused otherwise |
| `rbac.userRoles.aggregateToDefaultRoles` | `false` | Aggregate the user roles into `view`, `edit` and `admin` (then everyone bound to `edit` can promote and approve) |
| `rbac.userRoles.directWrites` | `false` | Grant the promoter and approver `update` for `kardinal pause`, `kardinal rollback --hold` / `release-hold` and `kardinal override` from a kubeconfig, limited by the scoped-writes admission policy |
| `tokenReview.audiences` | `["kardinal-promoter"]` | `--tokenreview-audiences`: token audiences the UI API and Bundle API accept (`kubectl create token <sa> --audience kardinal-promoter`) |
| `tokenReview.acceptAPIServerAudience` | `false` | `--tokenreview-accept-apiserver-audience`: also accept kubeconfig and default ServiceAccount tokens |
| `controller.accessLog.allRequests` | `false` | `--access-log-all-requests`: log every UI API and Bundle API request, not only logins, refusals and writes ([API access log](../guides/security.md#api-access-log)) |
| `controller.accessLog.sourceIP` / `.trustedProxies` | `false` / `[]` | `--access-log-source-ip`, `--access-log-trusted-proxies`: add the client address; believe `X-Forwarded-For` only from these proxy CIDRs |
| `ui.corsAllowedOrigins` | `[]` | `--cors-allowed-origins` |
| `ui.allowedHosts` | `[]` | Extra host names for `--ui-allowed-hosts` (Ingress host, node IP). localhost and the Service DNS names are always allowed |
| `service.uiPort` | `8082` | UI and UI API port (container and Service) |
| `service.webhookPort` | `8083` | Webhook (`/webhook/scm`) and Bundle API port (container and Service) |
| `service.metricsPort` / `.healthPort` | `8080` / `8081` | Metrics and health probe Service ports |
| `metricsBindAddress` / `healthProbeBindAddress` | `:8080` / `:8081` | `--metrics-bind-address` / `--health-probe-bind-address` (the container ports) |
| `pprofAddress` | `""` | `--pprof-address`: serve Go pprof profiles. Off by default; a port alone (`:6060`) binds to 127.0.0.1. See [Memory profiles](../operate.md#memory-profiles) |
| `controller.watchNamespace` | `""` | Namespace-scoped mode (`--watch-namespace`). Must equal the release namespace |
| `controller.policyNamespaces` | `[]` | Namespaces with org-level PolicyGates (`--policy-namespaces`; default `platform-policies`) |
| `graph.compactAbove` | `null` | Environment count above which a Bundle's Graph uses the compact shape (`--graph-compact-above`; default `100`; `0` makes every Graph compact). See [Large Pipelines](../pipeline-reference.md#large-pipelines) |
| `controller.gateStatusHeartbeat` | `""` | Longest a PolicyGate's status goes unwritten while its result does not change (`--gate-status-heartbeat`; default `10m`; `0s` writes on every evaluation). See [Policy gates](../policy-gates.md#re-evaluation) |
| `controller.workers.promotionStep` / `.prStatus` / `.policyGate` / `.bundle` / `.pipeline` | unset (16 / 8 / 8 / 4 / 4) | How many objects of a kind are reconciled at once (`--promotionstep-workers`, ...). One object is never reconciled twice at once. See [Controller concurrency](../operate.md#controller-concurrency) |
| `controller.tlsCertFile` / `tlsKeyFile` | `""` | TLS for the UI and webhook servers. Paths inside the container: mount the certificate Secret with `controller.extraVolumes` / `extraVolumeMounts`. Set both or neither: the chart refuses one alone, and a path that is not in a mounted `secret`, `projected` or `csi` volume (for certificates that come another way, set `KARDINAL_TLS_CERT_FILE` and `KARDINAL_TLS_KEY_FILE` with `controller.extraEnv`) |
| `controller.extraArgs` / `extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | Extra controller args, env vars, volumes and mounts |
| `rbac.argocdApplicationsWrite` | `false` | Grant `patch` on Argo CD Applications (the `argocd` update strategy) |
| `rbac.integrationTestJobs` | `false` | Deprecated, no effect, removed in v0.11. The chart grants `batch/jobs` for [hooks](../hooks.md) whatever it says |
| `hooks.serviceAccounts` | `[default]` | ServiceAccounts a [hook](../hooks.md)'s Pod may run as (`--hook-service-accounts`), in the Pipeline namespace. The Graph ServiceAccount is never allowed |
| `hooks.podSecurityLevel` | `baseline` | Pod Security Standard a [hook](../hooks.md) Pod must meet (`--hook-pod-security-level`): `baseline`, `restricted` or `privileged` (no Pod checks); below `privileged`, `nodeName` and `hostPort` are refused too |
| `render.image.repository`, `render.image.tag`, `render.image.digest` | `ghcr.io/pnz1990/kardinal-promoter/render`, the chart's appVersion, none | The `kardinal-render` image the render Jobs of [`layout: branch`](../rendered-manifests.md#the-render-job) run (`--render-image`); with a digest, `repository@digest`. Renders never run in the controller. `image.digest` pins the controller image the same way. The render Pods get the chart's `imagePullSecrets`, which must exist in the Pipeline namespaces |
| `render.serviceAccountName` | `kardinal-render` | ServiceAccount of the render Pods; the controller creates it, without a token, in a Pipeline namespace that has none. Bind no role to it |
| `render.resources.limits.cpu`, `.memory` | `1`, `512Mi` | Limits of a render Job; a render that needs more memory fails |
| `render.timeout` | `5m` | How long a render Job may run |
| `render.networkPolicy.enabled`, `.namespaces`, `.gitEgress` | `false`, `[]`, `[]` | A NetworkPolicy in each listed namespace that lets the render Pods reach only DNS and the git host rules in `gitEgress` (a CNI that enforces NetworkPolicy is needed). Recommended |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `1Gi` | Memory limit; see [Sizing the controller](../operate.md#sizing-the-controller) |
| `resources.requests.cpu` | `10m` | CPU request |
| `resources.requests.memory` | `256Mi` | Memory request |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Pod tolerations |
| `affinity` | `{}` | Pod affinity |
| `topologySpread.enabled` | `true` | With `replicaCount` > 1, prefer spreading replicas across zones (`whenUnsatisfiable: ScheduleAnyway`) |
| `pdb.enabled` / `.minAvailable` | `true` / `1` | PodDisruptionBudget, created only when `replicaCount` > 1 |
| `terminationGracePeriodSeconds` / `shutdownDelaySeconds` | `60` / `5` | See [Graceful shutdown](../operate.md#graceful-shutdown) |
| `networkPolicy.enabled` | `false` | NetworkPolicy for the controller Pod |
| `networkPolicy.ingressFrom.{metrics,health,ui,webhook}` | `[]` | Allowed peers per ingress port (empty admits any source) |
| `networkPolicy.extraEgress` | `[]` | Extra egress rules (e.g. Prometheus for MetricChecks) |
| `tracing.enabled` | `false` | Export OpenTelemetry traces over OTLP/HTTP ([Tracing](../guides/monitoring.md#tracing-opentelemetry)) |
| `tracing.endpoint` | `""` | OTLP/HTTP endpoint URL or `host:port`; empty uses `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `tracing.insecure` | `false` | Plain HTTP to a `host:port` endpoint |
| `tracing.samplingRatio` | `0.1` | Fraction of traces recorded, decided at each trace's root; an inbound `traceparent` does not force recording |
| `egress.allowlist` | `[]` | Destinations NotificationHook, MetricCheck and Subscription requests may reach (`--egress-allowlist`): host names, `*.` wildcards, CIDRs. Empty allows any destination outside the always-refused loopback, link-local and metadata addresses. See [Outbound requests to user URLs](../guides/security.md#outbound-requests-to-user-urls) |
| `scheduleClock.enabled` / `.interval` | `true` / `"1m"` | ScheduleClock `kardinal-clock` in the release namespace. Each tick re-evaluates every PolicyGate instance |
| `validatingAdmissionPolicy.enabled` | `true` | Deprecated, no effect, removed in v0.11. The CRD schemas validate these fields |

The monitoring values (`serviceMonitor`, `prometheusRule`, `grafanaDashboard`) are described in
[Monitoring](../guides/monitoring.md), and `demo.*` in the [Quickstart](../quickstart.md).

## kro Graph integration

| Key | Default | Description |
|---|---|---|
| `graph.serviceAccountName` | `kardinal-graph` | ServiceAccount kro impersonates to apply each Graph's children (`spec.serviceAccountName`) |
| `graph.kroNamespace` | `kro-system` | Namespace kro runs in (NetworkPolicy egress; `""` drops the rule) |
| `graph.aggregateToKro` | `true` | Ship a ClusterRole aggregated into kro's controller role (kro with `rbac.mode=aggregation`) |
| `graph.readerNamespaces` | `[argocd, flux-system]` | `--graph-reader-namespaces`: namespaces, besides a Graph's own, where the Graph identity may be bound to the reader role for health `ref` nodes. A health ref into any other namespace is dropped from the Graph with a warning (health refs are observational; the PromotionStep reconciler still checks health). Add the namespaces your `health.resource` targets live in. `["*"]` allows every namespace; use it only when every Pipeline author may read every namespace. `kube-system`, `kube-public` and `kube-node-lease` are never allowed |
| `graph.retire.superseded` | `""` | How long the Graph of a Superseded Bundle, or of a Verified one a newer Bundle replaced in every environment, is kept before it is retired (`--graph-retire-superseded-after`; default `1m`; `0s` keeps it). See [Graph retirement](../architecture.md#graph-retirement) |
| `graph.retire.verified` | `""` | The same for a Verified Bundle still deployed in an environment (`--graph-retire-verified-after`; default `1h`) |
| `graph.retire.failed` | `""` | The same for a Failed Bundle, which no longer recovers once retired (`--graph-retire-failed-after`; default `24h`) |

---
