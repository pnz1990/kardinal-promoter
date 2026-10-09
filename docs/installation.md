# Installation

This guide covers installing kardinal-promoter in a Kubernetes cluster using Helm.

---

## Prerequisites

| Requirement | Version | Notes |
|---|---|---|
| Kubernetes | ≥ 1.30 | kind, EKS, GKE, or any conformant cluster. kro's CRDs use CRD `selectableFields`, which needs 1.30; `hack/install-kro.sh` and the chart refuse older clusters. CI tests 1.35, 1.36 and 1.37; the upgrade suite runs on 1.30 and 1.37 |
| kubectl | ≥ 1.29 | Within one minor of your cluster |
| Helm | ≥ 3.14 | `brew install helm`. 3.14 adds `--reset-then-reuse-values`, used by [Upgrade](#upgrade). On 3.14, pass `helm template` a `--kube-version` of 1.30 or later: its default, 1.29, fails the chart's `kubeVersion` |
| GitHub token | — | Personal access token with `repo` scope |
| kro | ≥ 0.10.0-rc.0 | Graph controller with the `GraphKind` feature gate — see [Install kro](#install-kro) |

!!! tip "Trying it out locally?"
    Use [kind](https://kind.sigs.k8s.io/) for a single-node local cluster.
    See the [Quickstart](quickstart.md) for the fast path.

---

## Install kro

kardinal-promoter renders one kro [Graph](https://kro.run/next/docs/concepts/graph/overview/)
(`kro.run/v1alpha1`) per Bundle. [kro](https://github.com/kubernetes-sigs/kro) is a separate
prerequisite; the kardinal-promoter chart does not bundle it.

```bash
# From a kardinal-promoter checkout
bash hack/install-kro.sh
```

The script installs the kro Helm chart (`oci://registry.k8s.io/kro/charts/kro`) into `kro-system`
with `config.featureGates.GraphKind=true` and `rbac.mode=aggregation`, then server-side applies the
kro CRDs. Override the version with `KRO_VERSION=<version>`.

The script also tunes kro for kardinal's Graphs:

| Helm value | kro default | Script default | Override |
|---|---|---|---|
| `config.graphConcurrentReconciles` | 1 | 8 | `KRO_GRAPH_CONCURRENT_RECONCILES` |
| `config.clientQps` | 100 | 300 | `KRO_CLIENT_QPS` |
| `config.clientBurst` | 150 | 500 | `KRO_CLIENT_BURST` |
| `deployment.resources.limits.memory` | 1024Mi | 2Gi | `KRO_MEMORY_LIMIT` |
| `deployment.resources.requests.memory` | 128Mi | 768Mi | `KRO_MEMORY_REQUEST` |

kro reconciles one Graph at a time by default, and each reconcile makes about three API calls per
object the Graph applies. With one worker, one large promotion (a Pipeline with 150 environments)
delays every other Graph in the cluster by several seconds, up to about 30 seconds while two such
promotions run. Eight workers keep that under half a second. If you install kro another way, set
the same values.

### Sizing kro

kro keeps every Graph it reconciles in memory: its compiled program and its watches, 3 to 6 MB
for a kardinal Graph whatever its size (#1492: OOMKilled at 337 Graphs in 1 GiB; 5.4 MB per Graph
measured for 14 KB Graphs under load). kardinal creates one Graph per
Bundle and retires it once the Bundle has finished: the Graph is deleted, and the Bundle keeps a
record of each PromotionStep in `status.retiredSteps`, which rollback, promote, history, metrics,
the CLI and the UI read. So the Graphs kro holds are:

| Graphs | How many |
|---|---|
| Bundles promoting | about one per Pipeline and Bundle type (a newer Bundle supersedes an older one) |
| Superseded Bundles, and Verified or Failed ones a newer Verified Bundle replaced in every environment they touched | those whose steps settled in the last `graph.retire.superseded` (1m), plus the ones the controller has not reached yet |
| Verified Bundles still deployed | those verified in the last `graph.retire.verified` (1h) |
| Failed Bundles not replaced yet (and Bundles with a step stopped by a health alarm) | those failed in the last `graph.retire.failed` (24h) |

Why these delays: nothing reads a Superseded Bundle's Graph once its steps have settled (a Graph
is retired only when every step is `Verified`, `Failed`, `RollingBack` or `AbortedByAlarm` and none
still holds a PR finalizer), so
its delay is only a grace period, and it is the one that grows with the Bundle rate. At 2 Bundles a
second, 1 minute keeps about 120 Superseded Graphs (about 650 MB); 10 minutes would keep about
1,200 (about 6.5 GB, over kro's 1 GiB default). A Verified Bundle still deployed keeps its Graph
an hour after it finished, and a Failed one a day, for a person to look at it before it becomes
final (a retired Failed Bundle no longer recovers).

Measured with the scale suite (`full` profile: 40 Pipelines, 2 Bundles a second for 10 minutes,
1,200 Bundles), with the default delays:

| Run | Live Graphs (peak) | kro working set (peak) |
|---|---|---|
| Steady load | 126 | 668 MiB |
| Same load, controller leader killed 13 times | 91 | 657 MiB |
| Before retirement had its own work queue, leader kills | about 300 | OOMKilled at 1 GiB |

`hack/install-kro.sh` sets 2Gi: about three times that steady state, enough for the Graphs a
restarted or lagging controller has not retired yet (some 300).

Set `KRO_MEMORY_LIMIT` to about 256 MiB plus 6 MB times the live Graphs, with headroom for bursts.
Per Pipeline, the live Graphs are about:

    1 (in flight) + 1 (deployed, for an hour after it verified)
      + B x 1m (Superseded: B Bundles a minute)
      + F x 24h (Failed Bundles a minute that no newer Verified Bundle replaced yet)

The failure term dominates for a Pipeline that fails often and is not fixed: 10 failures a day that
stay unreplaced keep 10 Graphs. For 200 Pipelines that each promote a few Bundles an hour and fail
one a day, about 600 Graphs, use 4Gi. A burst of
Bundles, for example 2 a second for 10 minutes over 40 Pipelines, holds a few hundred Graphs while
the controller catches up; size for the burst. A kro that
runs out of memory restarts and stops promoting every Pipeline until it has compiled every Graph
again. `kubectl get graphs -A --no-headers | wc -l` shows the live count. Shorter retirement delays
(chart `graph.retire.*`, or a Pipeline's `kardinal.io/graph-retire-after` annotation) hold fewer
Graphs; see [Bundle history](concepts.md#graph-retirement) for what a retired Bundle keeps.

!!! warning "Version compatibility"
    The kro version kardinal-promoter is tested against is pinned in `hack/install-kro.sh`.

---

## Install kardinal-promoter

### 1. Create the GitHub token secret

kardinal-promoter needs a GitHub personal access token to open and monitor pull requests.
The token requires the `repo` scope (contents read/write, pull requests read/write).

```bash
kubectl create namespace kardinal-system

kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=<your-github-token>
```

### 2. Install with Helm

```bash
helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system \
  --create-namespace \
  --set github.secretRef.name=github-token
```

This installs the kardinal CRDs (from the chart's `crds/` directory), the **kardinal-promoter
controller** in the `kardinal-system` namespace, and the ClusterRoles bound to the Graph identity
(`<fullname>-graph-applier`, `<fullname>-graph-reader`; see [RBAC requirements](#rbac-requirements)).

Helm installs `crds/` only on the first install and never updates or deletes it. See
[Upgrade](#upgrade) for how to update the CRDs.

The chart ships `values.schema.json`, which rejects unknown keys: a misspelled `--set` fails the
install instead of being ignored.

Verify both controllers are running:

```bash
kubectl get pods -n kardinal-system
# NAME                                              READY   STATUS    RESTARTS   AGE
# kardinal-promoter-6b6c8c8446-jc79g               1/1     Running   0          30s

kubectl get pods -n kro-system
# NAME                              READY   STATUS    RESTARTS   AGE
# kro-7d4b8f9f5-xk2pq               1/1     Running   0          30s
```

### 3. Install the CLI

Download the `kardinal` binary of the same release. The release has builds for Linux and
macOS (`amd64`, `arm64`) and `kardinal-windows-amd64.exe`.

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
curl -Lo kardinal "https://github.com/pnz1990/kardinal-promoter/releases/download/v0.9.0/kardinal-${OS}-${ARCH}"
chmod +x kardinal && sudo mv kardinal /usr/local/bin/
kardinal version
# CLI:        v0.9.0
# Controller: v0.9.0
# Graph:      kro v0.10.0-rc.0
```

---

### Sizing the controller

The controller keeps an informer cache of every Pipeline, Bundle, PromotionStep, PRStatus and
PolicyGate it watches, so its memory grows with the number of Pipelines times the Bundles each
keeps (`historyLimit`), more than with the promotion rate. Measured with the scale suite (`full`
profile, controller built without `-race`, 2 replicas; peak resident memory of the leader,
[#1553](https://github.com/pnz1990/kardinal-promoter/issues/1553)):

| Load | Controller memory (peak) |
|---|---|
| No Pipelines | 55-70 MiB |
| 200 Pipelines x 3 environments, one Bundle each | 166 MiB |
| 1,000 Bundles over 100 Pipelines | 175 MiB |
| 200 Pipelines, one Bundle each, latency run | 187 MiB |
| 2 Bundles a second for 10 minutes over 50 Pipelines (1,200 Bundles) | 366 MiB |
| 2 Bundles a second over 40 Pipelines, leader killed 12 times | 409 MiB |

The chart requests 256 MiB and limits the controller to 1 GiB: 2.5 times the largest load
measured. A controller that runs out of memory is OOMKilled, re-lists everything when it
restarts and can be killed again, and no promotion in the cluster moves while it is down. For
more Pipelines or a longer history, raise `resources.limits.memory` in proportion: about
0.5 MiB per Pipeline with one Bundle (the 200-Pipeline run above), plus what the kept Bundles
and their steps hold. Watch
`container_memory_working_set_bytes` of the controller Pod, or `process_resident_memory_bytes`
on its metrics port. The controller sets the Go runtime's soft memory limit (`GOMEMLIMIT`) to 90% of
its container limit, which the chart passes in from the downward API, so the garbage collector
works harder before the kernel would OOMKill it; set `GOMEMLIMIT` in `controller.extraEnv` to
choose another value. kro has its own budget: [Sizing kro](#sizing-kro).

## Helm values reference

### kardinal-promoter controller

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
| `github.secretRef.name` | `""` | Existing Secret (release namespace) holding the SCM token. Recommended |
| `github.secretRef.key` | `token` | Key in the Secret |
| `github.app.enabled` | `false` | The `github.secretRef.name` Secret holds GitHub App credentials (`githubAppID`, `githubAppInstallationID`, `githubAppPrivateKey`) instead of a token; see [GitHub App](scm-providers.md#github-app). Needs `github.secretRef.name` |
| `github.token` | `""` | Token value. The chart stores it in Secret `<fullname>-github-token` (`kardinal-promoter-github-token` for release `kardinal-promoter`); the value stays in the Helm release history. Setting both this and `secretRef.name` fails |
| `scm.provider` | `""` | `--scm-provider`: `github` (default), `gitlab`, `forgejo`, `gitea`, `bitbucket`, `azuredevops` |
| `scm.apiURL` | `""` | `--scm-api-url` for self-hosted SCM instances |
| `scm.allowedRepositories` | `[]` | `--scm-allowed-repositories`: `host/repository` globs (`github.com/acme/*`, `gitlab.example.com/team/**`) the controller's SCM token may act on. Every SCM call for another repository is refused, and a Pipeline that would need the token for one is `Ready=False/RepositoryNotAllowed`. Empty allows every repository. See [Security](guides/security.md#the-shared-scm-token-and-scmallowedrepositories) |
| `scm.instanceSigners` | `[]` | `--scm-instance-signers`: Forgejo/Gitea only, the names or emails the instance signs commits with (`repository.signing` `SIGNING_NAME` / `SIGNING_EMAIL`). [Image verification](image-verification.md#signed-commits) treats such a commit as a platform signature. Without it, a verified signer that is not a user of the instance is taken as the instance key |
| `scm.gatesCommitStatus.enabled` | `true` | `--gates-commit-status`: post the gate results of a waiting pr-review step as the commit status on the commit kardinal pushed to its PR ([Gate status check](pr-evidence.md#gate-status-check-kardinalgates)). `false` posts none. |
| `scm.gatesCommitStatus.context` | `kardinal/gates` | `--gates-status-context`: the status name branch protection requires; reserved for kardinal. |
| `webhook.secretRef.name` / `.key` | `""` / `secret` | Secret with the SCM webhook secret (`KARDINAL_WEBHOOK_SECRET`): the HMAC key, or for GitLab and Azure DevOps the plain token |
| `bundleAPI.tokenSecretRef.name` / `.key` | `""` / `token` | Secret with the Bundle API bearer token (`KARDINAL_BUNDLE_TOKEN`). `POST /api/v1/bundles` is off until this is set |
| `ui.auth.tokenSecretRef.name` / `.key` | `""` / `token` | Secret with a static UI API bearer token (`KARDINAL_UI_TOKEN`). With neither this nor `ui.auth.tokenReview` set, the UI API serves only local clients (`kubectl port-forward`) |
| `ui.auth.tokenReview` | `false` | `--ui-tokenreview-auth`: validate UI tokens with TokenReview; adds the RBAC it needs |
| `controller.accessLog.allRequests` | `false` | `--access-log-all-requests`: log every UI API and Bundle API request, not only logins, refusals and writes ([API access log](guides/security.md#api-access-log)) |
| `controller.accessLog.sourceIP` / `.trustedProxies` | `false` / `[]` | `--access-log-source-ip`, `--access-log-trusted-proxies`: add the client address; believe `X-Forwarded-For` only from these proxy CIDRs |
| `ui.corsAllowedOrigins` | `[]` | `--cors-allowed-origins` |
| `ui.allowedHosts` | `[]` | Extra host names for `--ui-allowed-hosts` (Ingress host, node IP). localhost and the Service DNS names are always allowed |
| `service.uiPort` | `8082` | UI and UI API port (container and Service) |
| `service.webhookPort` | `8083` | Webhook (`/webhook/scm`) and Bundle API port (container and Service) |
| `service.metricsPort` / `.healthPort` | `8080` / `8081` | Metrics and health probe Service ports |
| `metricsBindAddress` / `healthProbeBindAddress` | `:8080` / `:8081` | `--metrics-bind-address` / `--health-probe-bind-address` (the container ports) |
| `controller.watchNamespace` | `""` | Namespace-scoped mode (`--watch-namespace`). Must equal the release namespace |
| `controller.policyNamespaces` | `[]` | Namespaces with org-level PolicyGates (`--policy-namespaces`; default `platform-policies`) |
| `graph.compactAbove` | `null` | Environment count above which a Bundle's Graph uses the compact shape (`--graph-compact-above`; default `100`; `0` makes every Graph compact). See [Large Pipelines](pipeline-reference.md#large-pipelines) |
| `controller.gateStatusHeartbeat` | `""` | Longest a PolicyGate's status goes unwritten while its result does not change (`--gate-status-heartbeat`; default `10m`; `0s` writes on every evaluation). See [Policy gates](policy-gates.md#re-evaluation) |
| `controller.workers.promotionStep` / `.prStatus` / `.policyGate` / `.bundle` / `.pipeline` | unset (16 / 8 / 8 / 4 / 4) | How many objects of a kind are reconciled at once (`--promotionstep-workers`, ...). One object is never reconciled twice at once. See [Controller concurrency](#controller-concurrency) |
| `controller.tlsCertFile` / `tlsKeyFile` | `""` | TLS for the UI and webhook servers. Paths inside the container: mount the certificate Secret with `controller.extraVolumes` / `extraVolumeMounts`. Set both or neither: the chart refuses one alone, and a path that is not in a mounted `secret`, `projected` or `csi` volume (for certificates that come another way, set `KARDINAL_TLS_CERT_FILE` and `KARDINAL_TLS_KEY_FILE` with `controller.extraEnv`) |
| `controller.extraArgs` / `extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | Extra controller args, env vars, volumes and mounts |
| `rbac.argocdApplicationsWrite` | `false` | Grant `patch` on Argo CD Applications (the `argocd` update strategy) |
| `rbac.integrationTestJobs` | `false` | Deprecated, no effect, removed in v0.10. The `integration-test` step was removed. The chart grants `batch/jobs` for [hooks](hooks.md) whatever it says |
| `hooks.serviceAccounts` | `[default]` | ServiceAccounts a [hook](hooks.md)'s Pod may run as (`--hook-service-accounts`), in the Pipeline namespace. The Graph ServiceAccount is never allowed |
| `hooks.podSecurityLevel` | `baseline` | Pod Security Standard a [hook](hooks.md) Pod must meet (`--hook-pod-security-level`): `baseline`, `restricted` or `privileged` (no Pod checks); below `privileged`, `nodeName` and `hostPort` are refused too |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `1Gi` | Memory limit; see [Sizing the controller](#sizing-the-controller) |
| `resources.requests.cpu` | `10m` | CPU request |
| `resources.requests.memory` | `256Mi` | Memory request |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Pod tolerations |
| `affinity` | `{}` | Pod affinity |
| `topologySpread.enabled` | `true` | With `replicaCount` > 1, prefer spreading replicas across zones (`whenUnsatisfiable: ScheduleAnyway`) |
| `pdb.enabled` / `.minAvailable` | `true` / `1` | PodDisruptionBudget, created only when `replicaCount` > 1 |
| `terminationGracePeriodSeconds` / `shutdownDelaySeconds` | `60` / `5` | See [Graceful shutdown](#graceful-shutdown) |
| `networkPolicy.enabled` | `false` | NetworkPolicy for the controller Pod |
| `networkPolicy.ingressFrom.{metrics,health,ui,webhook}` | `[]` | Allowed peers per ingress port (empty admits any source) |
| `networkPolicy.extraEgress` | `[]` | Extra egress rules (e.g. Prometheus for MetricChecks) |
| `tracing.enabled` | `false` | Export OpenTelemetry traces over OTLP/HTTP ([Tracing](guides/monitoring.md#tracing-opentelemetry)) |
| `tracing.endpoint` | `""` | OTLP/HTTP endpoint URL or `host:port`; empty uses `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `tracing.insecure` | `false` | Plain HTTP to a `host:port` endpoint |
| `tracing.samplingRatio` | `0.1` | Fraction of traces recorded, decided at each trace's root; an inbound `traceparent` does not force recording |
| `egress.allowlist` | `[]` | Destinations NotificationHook, MetricCheck and Subscription requests may reach (`--egress-allowlist`): host names, `*.` wildcards, CIDRs. Empty allows any destination outside the always-refused loopback, link-local and metadata addresses. See [Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls) |
| `scheduleClock.enabled` / `.interval` | `true` / `"1m"` | ScheduleClock `kardinal-clock` in the release namespace. Each tick re-evaluates every PolicyGate instance |
| `validatingAdmissionPolicy.enabled` | `true` | Deprecated, no effect. The CRD schemas validate these fields |

The monitoring values (`serviceMonitor`, `prometheusRule`, `grafanaDashboard`) are described in
[Monitoring](guides/monitoring.md), and `demo.*` in the [Quickstart](quickstart.md).

### kro Graph integration

| Key | Default | Description |
|---|---|---|
| `graph.serviceAccountName` | `kardinal-graph` | ServiceAccount kro impersonates to apply each Graph's children (`spec.serviceAccountName`) |
| `graph.kroNamespace` | `kro-system` | Namespace kro runs in (NetworkPolicy egress; `""` drops the rule) |
| `graph.aggregateToKro` | `true` | Ship a ClusterRole aggregated into kro's controller role (kro with `rbac.mode=aggregation`) |
| `graph.readerNamespaces` | `[argocd, flux-system]` | `--graph-reader-namespaces`: namespaces, besides a Graph's own, where the Graph identity may be bound to the reader role for health `ref` nodes. A health ref into any other namespace is dropped from the Graph with a warning (health refs are observational; the PromotionStep reconciler still checks health). Add the namespaces your `health.resource` targets live in. `["*"]` allows every namespace; use it only when every Pipeline author may read every namespace. `kube-system`, `kube-public` and `kube-node-lease` are never allowed |
| `graph.retire.superseded` | `""` | How long the Graph of a Superseded Bundle, or of a Verified one a newer Bundle replaced in every environment, is kept before it is retired (`--graph-retire-superseded-after`; default `1m`; `0s` keeps it). See [Graph retirement](concepts.md#graph-retirement) |
| `graph.retire.verified` | `""` | The same for a Verified Bundle still deployed in an environment (`--graph-retire-verified-after`; default `1h`) |
| `graph.retire.failed` | `""` | The same for a Failed Bundle, which no longer recovers once retired (`--graph-retire-failed-after`; default `24h`) |

---

## Accessing the UI

The kardinal controller serves an embedded web UI at port `8082` (`--ui-listen-address`, Helm value `service.uiPort`).

### In-cluster access (recommended): kubectl port-forward

The chart's Service is `ClusterIP` and the chart creates no Ingress. Without an Ingress, `NodePort` or `LoadBalancer` Service of your own, use `kubectl port-forward`:

```bash
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082
```

Then open <http://localhost:8082/ui/> in your browser. If you installed with a
release name other than `kardinal-promoter`, the Service is named
`<release>-kardinal-promoter`.

!!! tip "Why port-forward?"
    Port-forwarding routes traffic through the Kubernetes API server over a secure
    tunnel — no Ingress or LoadBalancer needed. It is the recommended approach for
    platform engineers accessing the UI from their workstation.

!!! warning "Ingress, NodePort and LoadBalancer need a UI auth mode"
    With no UI auth mode set, the UI API answers only local clients, which is how
    `kubectl port-forward` connects. A client that comes through an Ingress, a `NodePort`,
    a `LoadBalancer` or another pod gets `403`. To serve those clients, set
    `ui.auth.tokenReview=true` or `ui.auth.tokenSecretRef.name`. An authenticating proxy
    in front of the UI can inject the shared token. With a service-mesh sidecar in the
    controller pod, set an auth mode as well: the sidecar makes mesh clients look local.
    See [UI API Access Control](guides/security.md#ui-api-access-control).

!!! warning "Avoid accessing the UI over plain HTTP from a remote address"
    If you expose port 8082 directly (e.g. via `NodePort`) without TLS, the UI will
    display a security warning. Use port-forward from localhost instead, or configure
    TLS with `--tls-cert-file` / `--tls-key-file`.

!!! note "Browsing to a name other than localhost"
    The UI API only accepts its own host names: localhost and the controller Service's
    DNS names. If you browse to an Ingress host or a node IP, add it to
    `ui.allowedHosts` (`--ui-allowed-hosts`) as well as setting an auth mode. See
    [Host names (DNS rebinding)](guides/security.md#host-names-dns-rebinding).

### What the UI shows

- **Fleet board** (the start page, and the kardinal logo from anywhere). Each Pipeline is a
  line of stations, one per environment in promotion order; environments promoted in parallel
  are stacked. A station shows the version the environment runs and when it was Verified.
  That is the newest promotion there whose change landed, the Bundle `kardinal status` reports
  as deployed. Image and config Bundles do not replace each other, so a station also shows, under
  `+`, what the environment runs from another Bundle: the config commit of the last config Bundle
  under an image Bundle, or the image tags of the last image Bundle under a config Bundle, as
  `kardinal status` does. A lit rail marks the active Bundle's version on its way into an environment:
  amber and moving while it promotes, waits for its PR or is health checked; amber and still
  while a PolicyGate holds it; red where it failed. A station opens its Pipeline. The board
  follows the sidebar's health filter.
- **Pipeline view.** The lane, the promotion graph, policy gates with their CEL expressions,
  the Bundle history and comparison, and pause, resume, promote, roll back and create bundle.
- **Step timings.** Selecting an environment step lists the steps of that promotion
  (`git-clone` … `health-check`) with their durations, and a bar for each that shows where it
  ran in the promotion's time. A slow health check or push stands out at once.
- **Bundle types.** The Bundle card names what the shown Bundle changes: `image`, `config`,
  `image + config` (a mixed Bundle) or `chart`. In the Bundle history only the Bundles that are
  not image Bundles carry the tag.
- **Keyboard.** The first Tab stop skips to the main content. Each fleet line, and the Bundle
  history, is a single Tab stop: the arrow keys move between its stations or Bundles (Home and
  End to the ends, Up and Down between fleet lines), and Enter opens the one in focus. `?` lists
  the shortcuts (`/` filter, `r` refresh, `Esc` close a panel). Every view is checked against
  WCAG 2.1 AA, colour contrast included, in both themes.
- **Dark and light themes.** The UI follows the operating system's setting until you pick one
  with the ☀ / ☾ button next to the refresh indicator; the choice is kept in the browser.

### With TLS (production)

If you configure TLS via `--tls-cert-file` / `--tls-key-file` (or the Helm values
`controller.tlsCertFile` / `controller.tlsKeyFile`), the UI is served over HTTPS
and no browser warning is shown. See the [Security guide](guides/security.md) for
a cert-manager example.

---

## Upgrade

Helm does not update CRDs on upgrade. Apply the new chart's CRDs first:

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version> \
  | kubectl apply --server-side -f -
```

Then upgrade the release:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system \
  --reset-then-reuse-values
```

`--reset-then-reuse-values` (Helm 3.14 or later) keeps the values you set and takes every other
value from the new chart. `--reuse-values` also keeps the old chart's defaults: a new default is
never applied, and a key the new chart removed fails its schema.

`helm upgrade` upgrades the kardinal-promoter controller only. Upgrade kro separately by
re-running `hack/install-kro.sh` from the matching kardinal-promoter release.

From v0.10.0 the Pipeline CRD rejects an environment named `time`: kro#1434 proposes reserving it
as a Graph node ID. Rename such an environment before you upgrade (see the reserved names in the
[Pipeline reference](pipeline-reference.md)); renaming gives it new PromotionSteps and PR branches.

### Downgrading

A controller from v0.9.0-rc.1 or earlier does not know the `kardinal.io/close-pr` finalizer
that this release puts on PromotionSteps that open a PR, and never removes it: such a step, its
Graph and its namespace then never finish deleting. After you downgrade, remove the finalizer
from every step that holds it with the command under [Uninstall](#uninstall). The older
controller does not close the PR of a deleted step, so close by hand the PRs of the steps you
delete after the downgrade.

A controller older than this release does not know the compact Graph shape (Pipelines with more
than 100 environments, or the `kardinal.io/graph-shape: compact` annotation; see
[Large Pipelines](pipeline-reference.md#large-pipelines)). When it rebuilds the Graph of a
Bundle in flight (on any change to the Pipeline's spec, or a deleted Graph) it builds the node
shape, and kro then deletes every PromotionStep the compact Graph created: the Bundle promotes
again from its first environment. Before you downgrade, let the Bundles of those Pipelines finish, or delete them.

### Upgrading from v0.8.1

v0.8.1 ran its own Graph controller (krocodile, `experimental.kro.run`) from the kardinal chart. This release runs on upstream kro (`kro.run`), which you install separately. `helm upgrade --reuse-values` fails. A `helm upgrade` that gets past the values check deletes the `kro-system` namespace. Follow the steps below instead.

Tested on kind with Kubernetes 1.30 and 1.37, Helm 3.14, and Argo CD health checks with GitHub PRs. Promotions pause for about one minute (from step 3 until the new controller holds its leader lease). No Pipeline, Bundle, PromotionStep, PRStatus or PolicyGate is lost.

You need:

- Helm 3.14 or later, for `--reset-then-reuse-values`.
- `kubectl`, `jq`, `yq` and the new `kardinal` CLI.
- A checkout of kardinal-promoter at this release, for `hack/install-kro.sh`.

Check your Kubernetes version first. v0.9.0 needs 1.30 or later; on an older cluster, upgrade Kubernetes before step 1 (see [Kubernetes version](#kubernetes-version)).

```bash
kubectl version | grep Server
```

#### Steps

**1. Find stored objects the new CRDs reject.** This is required: some writes to these objects fail even with CRD validation ratcheting (see [Kubernetes version](#kubernetes-version)). The command prints one line per problem, and nothing when the cluster is clean.

```bash
kubectl get pipelines -A -o json | jq -r '
  ["api-version","kind","metadata","namespace","spec","status","graph","graphengine","kro","each","item","items","object","self","this","context","true","false","null","in","as","break","const","continue","else","for","function","if","import","let","loop","package","return","var","void","while","bundle","time"] as $reserved
  | .items[] | "pipeline \(.metadata.namespace)/\(.metadata.name)" as $p
  | ( (select((.spec.policyGates // []) | length > 0) | "\($p): spec.policyGates"),
      ((.spec.environments // []) | group_by(.name)[] | select(length > 1) | "\($p): duplicate environment name \(.[0].name)"),
      ((.spec.environments // [])[] |
        (select((.steps // []) | length > 0) | "\($p) env \(.name): steps"),
        (select(.promotionTemplate != null) | "\($p) env \(.name): promotionTemplate"),
        (select(.autoRollback != null) | "\($p) env \(.name): autoRollback"),
        (select((.shard // "") != "") | "\($p) env \(.name): shard"),
        (select(.name as $n | ($reserved | index($n)) != null or ($n | test("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$") | not) or ($n | length) > 63) | "\($p) env \(.name): invalid or reserved name")) )'
kubectl get policygates -A -o json | jq -r '.items[] | "policygate \(.metadata.namespace)/\(.metadata.name)" as $g
  | (select(.spec.selector != null) | "\($g): spec.selector"),
    (select((.metadata.name | length) > 63 and .spec.generated != true
      and ((.metadata.labels // {}) | (has("kardinal.io/bundle") or has("kardinal.io/gate-template") or .["kardinal.io/freeze"] == "true") | not))
      | "\($g): name longer than 63 characters")'
```

Gate instances and pause freeze gates can have longer names. Kardinal created them, and the new controller sets `spec.generated` on them, the field that exempts them from the name limit, before it writes their status. The finder skips them.

Fix each line it prints. In the commands below, `<i>` is the environment's position in `spec.environments`, counting from 0.

| Finding | Fix |
|---|---|
| `steps` | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"remove","path":"/spec/environments/<i>/steps"}]'`. Every environment runs the default step sequence. |
| `promotionTemplate` | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"remove","path":"/spec/environments/<i>/promotionTemplate"}]'`. Only clusters that ran a build from `main` have it. |
| `autoRollback` | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"remove","path":"/spec/environments/<i>/autoRollback"}]'`. Use `onHealthFailure` instead (see [Rollback](rollback.md)). |
| `shard` | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"remove","path":"/spec/environments/<i>/shard"}]'`. Distributed mode was removed. |
| `spec.policyGates` | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"remove","path":"/spec/policyGates"}]'`. Label org gates with `kardinal.io/applies-to`. |
| PolicyGate `spec.selector` | `kubectl -n <ns> patch policygate <name> --type json -p '[{"op":"remove","path":"/spec/selector"}]'`. Use the `kardinal.io/applies-to` label. |
| Invalid, reserved or duplicate environment name | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"replace","path":"/spec/environments/<i>/name","value":"<new-name>"}]'`. Renaming gives the environment new PromotionSteps and PR branches. Do it when the Pipeline has no Bundle in flight, and update any `dependsOn` that names it. |
| PolicyGate name longer than 63 characters | Copy the gate under a shorter name, then delete the old one (command below). |

```bash
kubectl -n <ns> get policygate <long-name> -o yaml \
  | yq 'del(.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.generation,.metadata.managedFields,.metadata.annotations,.status) | .metadata.name = "<short-name>"' \
  | kubectl apply -f - && kubectl -n <ns> delete policygate <long-name>
```

Run the finder again until it prints nothing.

Editing a Pipeline's spec makes v0.8.1 rebuild that Pipeline's in-flight Bundles, which reruns their steps. If a Pipeline you need to fix has a Bundle in flight, make the fix after step 3.

**2. Check your Helm values.**

```bash
helm get values kardinal-promoter -n kardinal-system -o yaml
```

The new chart has no `krocodile` key, and its schema rejects it. If your values contain `krocodile`, see [Helm values](#helm-values) before step 8.

**3. Stop both v0.8.1 controllers.** Promotions pause from here until the end of step 8.

```bash
kubectl -n kardinal-system scale deploy/kardinal-promoter --replicas=0
kubectl -n kro-system scale deploy/graph-controller --replicas=0
kubectl -n kardinal-system wait --for=delete pod -l app.kubernetes.io/name=kardinal-promoter --timeout=90s
kubectl -n kro-system wait --for=delete pod -l app=graph-controller --timeout=90s
```

If v0.8.1 is still running when step 7 applies the new CRDs, it sees the new defaults (`historyLimit: 50`, `maxConcurrentPromotions: 0`) as a Pipeline change. It then rebuilds every in-flight Bundle from the start.

**4. Remove the krocodile finalizers from the old Graphs.**

```bash
kubectl get graphs.experimental.kro.run -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' \
  | while read ns n; do kubectl -n "$ns" patch graphs.experimental.kro.run "$n" --type merge -p '{"metadata":{"finalizers":null}}'; done
```

Step 8 deletes the `graphs.experimental.kro.run` CRD. The finalizer `experimental.kro.run/graph-controller` can only be cleared by krocodile, which is stopped, so without this step the CRD stays `Terminating`. Removing it doesn't touch any kardinal object, because PromotionSteps, PRStatuses and gate instances are not owned by these Graphs. GraphRevisions have no finalizer.

**5. Keep the `kro-system` namespace.** The v0.8.1 chart created `kro-system`. The new chart doesn't, so `helm upgrade` would delete the namespace, and kro with it.

```bash
kubectl annotate namespace kro-system helm.sh/resource-policy=keep
```

**6. Install kro.** Run this from a checkout of kardinal-promoter at this release. It installs kro v0.10.0-rc.0 into `kro-system` as the Helm release `kro`, together with the CRDs `graphs.kro.run`, `graphrevisions.internal.kro.run` and `resourcegraphdefinitions.kro.run`.

```bash
KUBE_CONTEXT=<your-context> bash hack/install-kro.sh
```

On Kubernetes older than 1.30 the script exits 1 and installs nothing (see [Kubernetes version](#kubernetes-version)).

**7. Apply the new kardinal CRDs.** The v0.8.1 chart shipped no CRDs, and Helm never upgrades CRDs, so apply all 12 by hand. This adds the new `notificationhooks.kardinal.io` CRD. Apply them before the new controller starts.

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 | kubectl apply --server-side -f -
```

If this reports field-manager conflicts, add `--force-conflicts`.

**8. Upgrade the chart.** Use `--reset-then-reuse-values`, not `--reuse-values`. With `--reuse-values`, the v0.8.1 `krocodile` default is carried over and fails the new schema.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system --reset-then-reuse-values --wait --timeout 3m
```

Helm deletes the objects that only v0.8.1 needed:

- the `graph-controller` Deployment and ServiceAccount in `kro-system`;
- the `kardinal-graph-controller` ClusterRole and ClusterRoleBinding;
- the CRDs `graphs.experimental.kro.run` and `graphrevisions.experimental.kro.run`;
- with `validatingAdmissionPolicy.enabled` (the v0.8.1 default), the ValidatingAdmissionPolicies and bindings `kardinal-pipeline-validation`, `kardinal-policygate-validation` and `kardinal-bundle-validation`.

It adds:

- the ClusterRoles `kardinal-promoter-graph-applier`, `-graph-reader` and `-kro-watch`;
- a leader-election Role and RoleBinding.

**9. Check the result.**

```bash
kardinal doctor
kubectl get graphs.kro.run -A
kubectl get bundles
kubectl -n kardinal-system logs deploy/kardinal-promoter | grep -E '"level":"(warn|error)"'
```

What to expect:

- `kardinal doctor` passes every check.
- There is one `graphs.kro.run` Graph for each Bundle that was Promoting. The controller logs `graph created` for each.
- The logs show only two warnings: SCM webhooks are disabled without `--webhook-secret`, and UI API authentication is off (see [Other notes](#other-notes)).

**10. Tidy up.**

```bash
kubectl annotate namespace kro-system helm.sh/resource-policy- meta.helm.sh/release-name- meta.helm.sh/release-namespace-
kubectl label namespace kro-system app.kubernetes.io/managed-by- app.kubernetes.io/component-
kubectl delete crd promotiontemplates.kardinal.io --ignore-not-found
```

- The first two commands remove the v0.8.1 release labels and annotations from `kro-system`, which still say `component: krocodile`.
- v0.8.1 has no `promotiontemplates.kardinal.io` CRD, so the delete is a no-op. It matters only on clusters that ran a build from `main`.

**11. Paused Pipelines and leftover children.**

- **Paused Pipelines.** A paused Pipeline stays paused: the new controller creates a `freeze-<pipeline>` PolicyGate for it. List them, and resume each one when you want it to continue:

    ```bash
    kubectl get pipelines -A -o custom-columns=NAME:.metadata.name,PAUSED:.spec.paused
    kardinal -n <ns> resume <pipeline>
    ```

- **Leftover children.** Bundles that were Superseded or finished at upgrade time get no new Graph. When such a Bundle is deleted later, its PromotionSteps, PRStatuses and gate instances stay behind. After you delete old Bundles, remove what they left with this command. It matches only objects labelled with a Bundle that no longer exists; PolicyGate templates carry no `kardinal.io/bundle` label and are never touched.

    ```bash
    kubectl get promotionsteps,prstatuses,policygates -A -l kardinal.io/bundle \
      -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.labels.kardinal\.io/bundle}{"\n"}{end}' | sort -u |
    while read ns b; do
      kubectl -n "$ns" get bundle "$b" >/dev/null 2>&1 && continue
      echo "Bundle $ns/$b is gone:"
      kubectl -n "$ns" delete promotionsteps,prstatuses,policygates -l "kardinal.io/bundle=$b"
    done
    ```

#### What happens to in-flight Bundles

- **Promoting Bundles continue.** The new controller builds a `kro.run` Graph for each one and takes over its existing PromotionSteps, PRStatuses and gate instances. The objects keep the same name, uid and creationTimestamp.
    - Verified environments stay Verified.
    - A step held by a gate keeps waiting.
    - Once the gate passes, the step opens its PR, and the Bundle finishes after the merge.
- **Superseded and finished Bundles** are left as they are. They get no new Graph (see step 11 for their children).
- **Gate instances keep their old expression.** A Bundle's gate instances are copied from the PolicyGate templates when the controller builds the Bundle's Graph, so editing a template after the upgrade doesn't change the instances of Bundles already in flight. To release a Bundle held by such a gate, override it:

    ```bash
    kardinal override <pipeline> --stage <env> --gate <gate> --reason "<why>" --expires-in 1h
    ```

- **Check that Verified environments have the image.** In our v0.8.1 run, v0.8.1 reported test and uat Verified without writing a commit, so the environments kept the old image. If an environment branch has no kardinal commit for a Bundle that v0.8.1 reported Verified, create a new Bundle for the same image after the upgrade. In our run, the new Bundle wrote the commits and reached prod in 90 seconds.
- **Bundles rebuilt from the start** only if the new CRDs were applied while v0.8.1 was running (see step 3). Nothing is lost, but steps that had already run are run again.

#### Kubernetes version

v0.9.0 needs Kubernetes 1.30 or later. kro's `graphrevisions.internal.kro.run` CRD uses CRD `selectableFields`, which older API servers reject. `hack/install-kro.sh` checks the server version first and exits 1 on an older cluster, and the chart's `kubeVersion` makes `helm upgrade` refuse it. v0.8.1 runs on 1.30, so on a v0.8.1 cluster on 1.29 or older, upgrade Kubernetes first, then follow the steps above.

From 1.30, CRD validation ratcheting lets most writes to a stored object through when the new schema rejects one of its fields. On 1.30 to 1.32, status writes to such an object still fail, including the controller's. Some writes fail on every version, so step 1's finder is required. What each finding blocks once the new CRDs are applied:

| Stored value | Kubernetes 1.30 to 1.32 | Kubernetes 1.33 and later |
|---|---|---|
| env `steps` / `autoRollback` | status writes and edits to that environment fail | only edits to that environment fail |
| reserved environment name (e.g. `graph`) | status writes and edits to that environment fail | only edits to that environment fail |
| `spec.policyGates`, PolicyGate `spec.selector` | status writes fail; other writes succeed, with a warning | writes succeed, with a warning |
| environment name that is not a DNS label (e.g. `Test`) | writes succeed | writes succeed |
| PolicyGate name longer than 63 characters | **every write fails**, including labels and status | **every write fails**, including labels and status |
| duplicate environment name | writes succeed; Pipeline `Ready=False` `ValidationFailed` | same |
| `shard` | writes succeed; Pipeline `Ready=False` `NotImplemented` | same |

If you already applied the CRDs, run the fixes from step 1 now. The patches and the gate copy succeed, and the objects recover.

Among the v0.8.1 examples, `custom-step` and `integration-test` set `steps`, and `multi-cluster-fleet` sets `shard`. Pipelines copied from them need step 1.

#### Helm values

- **`krocodile`.** Remove it. If `helm get values` in step 2 shows it, you set it yourself, and `--reset-then-reuse-values` keeps the values you set, so the schema rejects it again. Pass your values file without the key instead, with `-f values-new.yaml` in place of `--reset-then-reuse-values` in step 8:

    ```bash
    yq 'del(.krocodile)' values.yaml > values-new.yaml
    ```

- **`validatingAdmissionPolicy.*`.** Deprecated, with no effect. The CRD schemas validate the kardinal fields; the chart's only ValidatingAdmissionPolicies are the identity policies ([Verified identity](guides/security.md#verified-identity)) and the hold-writes policy (only `pipelines/hold` may change `spec.holds`), which are always installed.
- **`rbac.integrationTestJobs`.** Deprecated, with no effect, and removed in v0.10. The chart grants `batch/jobs` (create, get, list, watch, delete) for [hooks](hooks.md) whatever it says.
- **`--reuse-values`** fails with `additional properties 'krocodile' not allowed` (Helm before 3.18.5: `Additional property krocodile is not allowed`), even when you never set `krocodile`. Use `--reset-then-reuse-values`.

#### Other notes

- **Custom RBAC.** If you manage the controller's RBAC yourself, allow `create` and `patch` on `events.k8s.io` events.
- **UI.** With no UI auth mode set, `/api/` answers only local clients (`kubectl port-forward`). Set `ui.auth.tokenReview=true` or `ui.auth.tokenSecretRef.name` if the UI is reached another way.
- **Notes in the [changelog](changelog.md) that don't apply to v0.8.1:**
    - `promotionTemplate`, `PromotionStep.spec.inputs` and the `promotiontemplates` CRD don't exist in v0.8.1.
    - The `update.strategy: argocd` with `approval: pr-review` note doesn't apply: v0.8.1 allows only `kustomize` and `helm`.
    - The renamed examples (flux, flagger, argo-rollouts, github) are not in v0.8.1.

#### If something goes wrong

- **`helm upgrade` fails with `additional properties 'krocodile' not allowed`** (Helm before 3.18.5: `Additional property krocodile is not allowed`). Nothing was changed. Rerun step 8 with `--reset-then-reuse-values`, or with `-f` and a values file without `krocodile`.
- **`kro-system` was deleted** (step 5 skipped). kro was deleted with it. Rerun `KUBE_CONTEXT=<your-context> bash hack/install-kro.sh`. The Graphs are in the Bundles' namespaces and survive; kro picks them up again.
- **`graphs.experimental.kro.run` is stuck `Terminating`** (step 4 skipped). Run the step 4 command. The CRD then finishes deleting (`kubectl wait --for=delete crd/graphs.experimental.kro.run --timeout=60s`), and no kardinal object is lost.
- **Steps ran again after the upgrade.** The CRDs were applied while v0.8.1 was running. Nothing is lost, and the Bundles finish.
- **`... is invalid` errors in the controller log.** Run the step 1 finder and fixes now.
- **Old PromotionSteps, PRStatuses or gates without a Bundle.** Run the step 11 command.
- **Rolling back to v0.8.1** was not tested.

---

## Controller concurrency

Each controller reconciles several objects at once. A PromotionStep holds its worker through
every git and SCM round trip, so one worker made the steps of every Pipeline in the cluster wait
for each other: one slow repository or git host slowed every promotion. The defaults below
were measured with the scale suite's `full` profile (`TestScale_LoadPipelines`: 200 Pipelines
x 3 automatic environments, one Bundle each, started together; `TestScale_LoadBurst`: 1,000
Bundles over 100 Pipelines; controller built with `-race`, 4 CPU, 2 replicas):

| Controller | 200 Pipelines: step p50 / p99 | Bundle end to end p50 / p99 | All settled | 1,000 Bundles: Bundle end to end p50 / p99 |
|---|---|---|---|---|
| 1 worker each (before) | 65 s / 81 s | 245 s / 319 s | 324 s | 201 s / 221 s |
| the worker defaults | 1 s / 3 s | 110 s / 314 s | 325 s | 90 s / 153 s |
| the defaults, and Graph translations of one namespace no longer list its Graphs under one lock for the whole controller | 3 s / 6 s | 52 s / 82 s | 87 s | 16 s / 26 s |

With the defaults the `full` profile meets the latency objective of `TestScale_LatencySLO`
(test/e2e/README.md, Latency SLO): automatic steps p50 2 s and p99 5 s, Bundles p99 92 s,
against 10 s, 30 s and 2 minutes.

With 1.5 s of latency on every git round trip and 2 Bundles a second over 40 Pipelines for 10
minutes, one worker brought 180 steps to `Verified` (step p99 67 s, PromotionStep queue 84);
the defaults brought 597 (step p99 20 s, queue 22). The controller's memory was the same with
one worker and with the defaults (peak resident about 700 MiB in that run with the race
detector, which inflates it), so in that run the workers added no memory of note. That is not a
sizing guide: the controller's memory grows with the number of Pipelines, Bundles and steps it
caches, and the chart's default limit is too small for the `full` profile ([#1553](https://github.com/pnz1990/kardinal-promoter/issues/1553)).

| Value | Flag | Default | Why |
|---|---|---|---|
| `controller.workers.promotionStep` | `--promotionstep-workers` | `16` | git clone, commit, push, PR and health checks: almost all waiting on the network |
| `controller.workers.prStatus` | `--prstatus-workers` | `8` | one SCM call per poll |
| `controller.workers.policyGate` | `--policygate-workers` | `8` | CEL evaluation and a status write; the compiled programs are shared |
| `controller.workers.bundle` | `--bundle-workers` | `4` | Graph creation; the Graph identity of one namespace is bound under that namespace's lock, so Bundles of different namespaces translate in parallel |
| `controller.workers.pipeline` | `--pipeline-workers` | `4` | status and history |

One object is never reconciled by two workers at once: the work queue serializes it. Bundles
of one Pipeline with `maxConcurrentPromotions` count the free slots under a per-Pipeline lock
(as does a Failed Bundle that recovers into a slot), so more workers never promote past the
cap. Environments that promote in parallel to one branch (waves, a fan-in) push at the same
time and rebase onto each other's commits (git-push retries a moved branch), so a wave of 50
regions on one branch takes about a minute even when each step is fast. Raise `promotionStep` for many Pipelines on slow
git hosts. Each step that runs at once holds one shallow clone of its repository in the
controller's memory and its working directory on disk.

## Graceful shutdown

When Kubernetes deletes the controller Pod (a rolling update, a scale-down or a node drain),
the Pod first keeps serving for `shutdownDelaySeconds` (default **5**). Services stop sending
it new connections in that time, so the UI, SCM webhooks and the Bundle API keep answering
through a rollout instead of refusing or dropping requests on a node whose routes still point
at the old Pod. Then the controller gets `SIGTERM` and shuts down in this order:

1. The webhook and UI servers stop accepting connections and give the requests in flight up
   to **20 seconds** to finish. Reconciles keep running meanwhile.
2. The controller stops starting reconciles and cancels the ones in flight. A git push or SCM
   API call in progress is cancelled, not finished: the step logs `step failed, will retry`
   with `context canceled`.
3. The metrics and health probe servers stop, waiting for the requests in flight.
4. A leader releases its leader Lease, so another replica takes over within seconds instead
   of waiting for the Lease to expire (15 seconds).

With no request in flight the controller usually exits within a second. The whole shutdown
is bounded at **30 seconds**: when a request is still open then, the controller logs
`failed waiting for all runnables to end within grace period of 30s` and exits.

This leaves no inconsistent state. After the restart the step runs again from its last saved
step. A `pr-review` step force-pushes its branch `kardinal/<namespace hash>/<bundle>/<env>`, so a step stopped
after its push and before its PR opens one PR with one commit, and the base branch changes
only when the PR is merged.

The Helm chart sets `terminationGracePeriodSeconds: 60` so Kubernetes sends `SIGKILL` only
after the shutdown delay and the controller's full 30 seconds to shut down. The delay counts
against the grace period: keep `terminationGracePeriodSeconds` above `shutdownDelaySeconds`
plus 30. A leader keeps its Lease while it waits, so the delay also postpones the new
leader's takeover by as much.

```yaml
# values.yaml
shutdownDelaySeconds: 0  # SIGTERM at once: new connections can still reach the Pod as it stops
```

The 30-second shutdown timeout is fixed in the controller. `terminationGracePeriodSeconds` only sets
when Kubernetes sends `SIGKILL`, so a value above 30 does not give reconciles more time.
A value below 30 can cut the shutdown short, and `0` kills the Pod at once:

```yaml
# values.yaml
terminationGracePeriodSeconds: 0  # no graceful shutdown: SIGKILL at once
```

---

## Uninstall

Delete your Bundles, and wait for their PromotionSteps to go, before you uninstall the
controller. A step whose environment was `pr-review` when the step started carries the
`kardinal.io/close-pr` finalizer while it is `Promoting` or `WaitingForMerge`, and on delete the
controller closes its PR with a comment before it lets the step go. Without the controller the
finalizer stays, and the step, its Graph, its namespace and the PromotionStep CRD never finish
deleting.

In namespace mode (`controller.watchNamespace`), deleting the release namespace is the same as
uninstalling first: the controller goes with the Bundles, so the finalizers stay and the
namespace stays `Terminating` until you remove them by hand, as shown below.

```bash
kubectl delete bundles.kardinal.io --all -A
# The controller closes the open promotion PRs. It gives up on a PR after about 5 minutes of
# SCM errors (see Troubleshooting), so the steps are gone within about 6 minutes.
kubectl wait --for=delete promotionsteps.kardinal.io --all -A --timeout=6m
helm uninstall kardinal-promoter -n kardinal-system
```

If a step still holds the finalizer once the controller is gone (the wait timed out, or the
controller was uninstalled first), remove the finalizer by hand. The controller did not close
that step's PR, so the PR stays open: close it in your SCM, since merging it would change the
environment with no PromotionStep tracking it. This prints each step and its PR (`-` when the
step recorded none), then removes the finalizer:

```bash
kubectl get promotionsteps.kardinal.io -A -o go-template='{{range .items}}{{$s := .}}{{range $i, $f := .metadata.finalizers}}{{if eq $f "kardinal.io/close-pr"}}{{$s.metadata.namespace}} {{$s.metadata.name}} {{$i}} {{or $s.status.prURL "-"}}{{"\n"}}{{end}}{{end}}{{end}}' |
while read -r ns name i pr; do
  echo "$ns/$name: close PR $pr by hand"
  kubectl patch promotionsteps.kardinal.io "$name" -n "$ns" --type=json -p \
    "[{\"op\":\"test\",\"path\":\"/metadata/finalizers/$i\",\"value\":\"kardinal.io/close-pr\"},{\"op\":\"remove\",\"path\":\"/metadata/finalizers/$i\"}]"
done
```

See also [Troubleshooting: deletion hangs](troubleshooting.md#a-promotionstep-graph-or-namespace-never-finishes-deleting).
Then remove the CRDs and kro if you want:

```bash
# Optional: remove kardinal CRDs (deletes all Pipelines, Bundles, PolicyGates, etc.)
# promotiontemplates.kardinal.io exists only on clusters that ran a pre-release build from main.
kubectl delete crd --ignore-not-found \
  pipelines.kardinal.io \
  bundles.kardinal.io \
  promotionsteps.kardinal.io \
  policygates.kardinal.io \
  prstatuses.kardinal.io \
  rollbackpolicies.kardinal.io \
  metricchecks.kardinal.io \
  scheduleclocks.kardinal.io \
  changewindows.kardinal.io \
  subscriptions.kardinal.io \
  notificationhooks.kardinal.io \
  promotiontemplates.kardinal.io \
  auditevents.kardinal.io \
  approvals.kardinal.io

# Optional: remove kro and its CRDs (only if nothing else uses kro)
helm uninstall kro -n kro-system
kubectl delete crd \
  graphs.kro.run \
  graphrevisions.internal.kro.run \
  resourcegraphdefinitions.kro.run
kubectl delete namespace kro-system
```

---

## RBAC requirements

The chart creates the controller's ServiceAccount (`kardinal-promoter`) and its RBAC. In summary:

| Resources | Verbs |
|---|---|
| All `kardinal.io` kinds and their `/status` | Full CRUD, except `auditevents` (get, list, watch, create) and `changewindows` (get, list, watch; get, update, patch on `/status`) |
| `graphs.kro.run` | Full CRUD; get on `graphs/status` |
| `serviceaccounts`, `rolebindings` | get, create; get, list, create, update, delete (Graph identity; `delete` removes reader bindings no Graph needs, `list` finds them for the sweep) |
| `namespaces` | get, limited to `controller.watchNamespace` in namespace mode (lets go of a Graph whose namespace is being deleted) |
| `clusterroles` | `bind`, limited to `<fullname>-graph-applier` and `<fullname>-graph-reader` |
| `deployments`, Argo CD `applications` and `rollouts`, Flux `kustomizations`, Flagger `canaries` | get, list, watch (health adapters) |
| `replicasets` | get only: the `resource` and `flux` adapters read the ReplicaSet a Deployment's `ProgressDeadlineExceeded` names |
| `pods` | list only: while a Deployment's replicas are unavailable, the `resource` adapter lists the pods of its new ReplicaSet to name why one is not ready. In the default cluster mode `list` covers **every pod in the cluster** (pod specs, including literal `env` values, not Secrets); `controller.watchNamespace` limits it to one namespace. Without it, health messages leave the pod out |
| `secrets` | get only: the controller reads each Secret by name and never lists or watches them. In the default cluster mode `get` covers **every Secret in the cluster**. The release-namespace Role adds `get` on the SCM token Secret by name |
| `configmaps` | None in the watched namespaces. The leader-election Role reads and writes the `kardinal-version` ConfigMap by name |
| `leases` | Leader election, through a Role in the release namespace |
| `events` | get, list, watch, create, patch |

Optional rules: `rbac.argocdApplicationsWrite` (patch Applications) and `ui.auth.tokenReview`
(create TokenReviews and SubjectAccessReviews). The full list of objects and rules is in
[Security: Controller RBAC](guides/security.md#controller-rbac). Set `controller.watchNamespace`
to turn the namespaced rules into a Role in one namespace.

kro does not apply a Graph's children with its own identity. It impersonates the Graph's
`spec.serviceAccountName` (default `kardinal-graph`) in the Graph's namespace. The kardinal-promoter
controller creates that ServiceAccount and binds it with RoleBindings to `<fullname>-graph-applier`
(in the Graph namespace) and `<fullname>-graph-reader` (in each namespace a health `ref` node reads,
limited to the Graph's own namespace and `graph.readerNamespaces`). Each reader binding is named
`<fullname>-graph-reader-<graph namespace>`: with release `kp`, `kp-kardinal-promoter-graph-reader-<graph namespace>`.
`<fullname>` is the chart's full name: `fullnameOverride` if set, else the release name, plus
`-kardinal-promoter` unless the release name contains it. Reader bindings that no Graph
in the namespace needs any more are deleted: when a Bundle is translated, when a Graph is deleted,
and, in cluster mode, by a sweep at controller startup and every 10 minutes that also catches the
bindings of namespaces that are gone. The sweep lists only RoleBindings labeled
`app.kubernetes.io/managed-by=kardinal-promoter`.
See G5 in the [Graph capability ledger](design/16-graph-capability-ledger.md).

**Upgrading:** earlier versions bound the reader role in every namespace a health `ref` named,
and did not record those bindings. In cluster mode the sweep deletes them once no Graph reads
through them. A binding that a Graph made by the old version still reads through stays until
that Graph's Bundle is replaced. To revoke such bindings at once, list them and delete any in
a namespace that is not the Graph's own and not in `graph.readerNamespaces`:

```bash
kubectl get rolebindings -A -l app.kubernetes.io/managed-by=kardinal-promoter \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name | grep graph-reader-
```

The Helm chart creates all of these ClusterRoles, Roles and bindings.

---

## Next steps

- [Quickstart](quickstart.md) — apply your first Pipeline and promote a Bundle
- [Concepts](concepts.md) — understand Pipelines, Bundles, PolicyGates
- [Policy Gates](policy-gates.md) — write CEL expressions for promotion policies
- [Troubleshooting](troubleshooting.md) — diagnose installation issues with `kardinal doctor`
