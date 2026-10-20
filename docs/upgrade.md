# Upgrade

**Upgrading from v0.12.0, v0.12.1 or v0.12.2** to v0.12.3: v0.12.3 changes no CRD, value or
admission policy, so apply the CRDs and upgrade the chart in place with the commands below
(`--version 0.12.3`), and the CLI with it. The [changelog](changelog.md)'s v0.12.3, v0.12.2 and
v0.12.1 sections list the fixes. On its first start v0.12.3 re-translates the Graph of each
promoting version once, which changes nothing for a Graph that was working (#2271).
**From v0.12.x** to the next release: follow [Upgrading from v0.12.0](#upgrading-from-v0120).
**From v0.11.0** to v0.12.3: follow [Upgrading from v0.11.0](#upgrading-from-v0110).
**From v0.10.0:** follow [Upgrading from v0.10.0](#upgrading-from-v0100). **From v0.9.0:**
follow [Upgrading from v0.9.0](#upgrading-from-v090).

**Requires Kubernetes 1.35 or later.** Check the cluster before you upgrade, and upgrade
Kubernetes first on an older one: the chart's `kubeVersion` refuses it, and `hack/install-kro.sh`
exits 1 there.

```bash
kubectl version | grep Server
```

Helm does not update CRDs on upgrade. Apply the new chart's CRDs first:

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 \
  | kubectl apply --server-side --force-conflicts -f -
```

`--force-conflicts` is needed when Helm installed the CRDs (a release installed from the chart,
which ships them): Helm owns their fields, and without it the apply stops with
`conflict with "helm" ... .spec.versions`. kubectl then owns the CRDs; Helm never changes them
after the install anyway.

Then upgrade the release:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 \
  --namespace kardinal-system \
  --reset-then-reuse-values
```

`--reset-then-reuse-values` (Helm 3.14 or later) keeps the values you set and takes every other
value from the new chart. `--reuse-values` also keeps the old chart's defaults: a new default is
never applied, and a key the new chart removed fails its schema.

`helm upgrade` upgrades the kardinal-promoter controller only. Upgrade kro separately by
re-running `hack/install-kro.sh` from the matching kardinal-promoter release.

The Pipeline CRD rejects an environment named `time` (reserved for a kro Graph node ID; see the
reserved names in the [Pipeline reference](pipeline-reference.md)).

## Downgrading to v0.9.0

The v0.9.0 controller does not know the compact Graph shape (Pipelines with more
than 100 environments, or the `kardinal.io/graph-shape: compact` annotation; see
[Large Pipelines](pipeline-reference.md#large-pipelines)). When it rebuilds the Graph of a
Bundle in flight (on any change to the Pipeline's spec, or a deleted Graph) it builds the node
shape, and kro then deletes every PromotionStep the compact Graph created: the Bundle promotes
again from its first environment. Before you downgrade, let the Bundles of those Pipelines finish, or delete them.

**From v0.12.0.** A controller older than v0.12.0 does not know the reader labels v0.12.0 puts
on Graphs (`reads.kardinal.io/<namespace>` and `kardinal.io/reader-labels`), and keeps them when
it updates a Graph. If it then adds a ref node that reads another namespace, the labels no longer
name every namespace the Graph reads, and after you upgrade again a reader RoleBinding that
Graph needs could be pruned until its next translation binds it again. Before you upgrade again
after a downgrade, remove the marker, so the controller reads those Graphs in full:
`kubectl label graphs.kro.run -A --all kardinal.io/reader-labels-`.

**From v0.13.0** (not released yet). A controller older than v0.13.0 puts a health ref node in
each Graph it builds, read through the `<fullname>-graph-reader` ClusterRole, which the v0.13.0
chart no longer ships. Downgrade the chart along with the controller (`helm rollback`), so the
ClusterRole is back. When you upgrade again, the controller removes those nodes and their reader
RoleBindings at startup.

## Upgrading from v0.12.0

!!! note "v0.13.0 is not released yet"
    These steps take a v0.12.0 installation to the next release, v0.13.0, once it is released:
    `<version>` in the commands is that release's chart version. Until then v0.12.3 is the latest
    release, and there is nothing to upgrade to.

The same steps take a v0.12.1, v0.12.2 or v0.12.3 installation: none of them changes a CRD, value
or admission policy.

The upgrade suites run these steps as written (`TestUpgradeV0120_Estate` from v0.12.0 and
`TestUpgradeV0121_Estate` from v0.12.1, on the oldest and newest supported Kubernetes minors)
while versions are in flight: bakes, open promotion PRs, a rollback, gates holding versions with
approvals and Verdicts recorded, a running AgentRun, a freeze and, from v0.12.1, an automatic
rollback. They keep their objects and finish.

What v0.12.0 stored keeps working. Versions in flight keep their Graphs, PromotionSteps,
PRStatuses and gate instances: at its first start the new controller removes the health ref nodes
from those Graphs in place, and it rebuilds a version's gates only when one of its gates changes.
The new kinds, PromotionReceipt and ChangeRequest, are only installed: receipts stay off until
you set `receipts.enabled`, and no change request is opened until a gate reads one. EKS health
credentials (`health.kubeconfigSecretRef.eks`, a new Pipeline field) stay off too: no Pipeline
may name an IAM role until you allow it in `health.eks.allowedRoleARNs` or
`health.eks.allowedRoleARNsByNamespace`, which are empty by default
([Remote clusters on EKS](health-adapters.md#remote-clusters-on-eks)).

v0.13.0 targets the same kro as v0.12.0 (`v0.10.0-rc.0`), so kro is not upgraded.

**1. Read the upgrade notes.** The changelog's [Unreleased](changelog.md#unreleased) section
lists every change since v0.12.0. Its "Before you upgrade" entries are the ones that can change
something for you:

- **`graph.readerNamespaces` is removed.** Setting it fails the upgrade
  (`graph.readerNamespaces was removed (#1283)`). With `--reset-then-reuse-values` a value you
  never set is dropped for you, v0.12.0's default (`argocd`, `flux-system`) included. If
  `helm get values` shows it, you set it: write your values without it, and pass
  `-f values-new.yaml` in place of `--reset-then-reuse-values` in step 3.

    ```bash
    helm get values kardinal-promoter --namespace kardinal-system -o yaml \
      | yq 'del(.graph.readerNamespaces)' > values-new.yaml
    ```

    The new chart deletes the `<fullname>-graph-reader` ClusterRole. At its first start the new
    controller removes the health ref nodes from existing Graphs and deletes the reader
    RoleBindings v0.12.0 recorded. Between the chart upgrade and that start, kro cannot read those
    nodes and may hold the Graphs that have them; their promotions go on once the nodes are
    removed, within about a minute. `kardinal_health_ref_cleanup_pending` is `0` once nothing is
    left ([Health ref cleanup after an upgrade](guides/monitoring.md#health-ref-cleanup-after-an-upgrade)).
- **A rollback changes its target environment alone** when its images (and chart) are pinned by
  digest and another version with the same artifacts was Verified in every environment upstream
  of the target: `kardinal rollback <pipeline> prod` then leaves test and uat on what they
  run. If your process relied on a rollback also rolling the upstream environments back, roll
  each of them back on its own. Rollbacks created before the upgrade still go through the
  upstream environments ([Multi-Environment Rollback](rollback.md#multi-environment-rollback)).
- **Gate changes reach the versions in flight.** At the upgrade, the controller renders the Graph
  of every version still promoting again, once, so its gates become copies of the current
  templates. A gate created, edited or deleted under v0.12 after the version started (v0.12 left
  those versions on the gates they started with) applies to it from then on, and a `GateChanged`
  AuditEvent that says it happened on upgrade records it. Later changes reach the versions in
  flight at once, and are recorded the same way. If you relied on a version in flight keeping an
  old gate, use `kardinal pause` or `kardinal hold` instead
  ([When a new or edited gate takes effect](policy-gates.md#when-a-new-or-edited-gate-takes-effect)).
- **`provenance.ciRunURL` refuses credential parameters.** Once the CRDs are applied, the Bundle
  CRD refuses a CI run URL with user info, a credential query or fragment parameter (a name such
  as `token`, `sig` or `X-Amz-Signature`) or a percent-encoded parameter name, and the Bundle API
  (HTTP 400) and `kardinal create bundle` refuse a credential parameter. Remove it from the URL your
  CI sends. A Bundle already stored with one stays writable while the URL is unchanged
  ([Provenance](ci-integration.md#provenance)).
- **Only the controller changes its own rollback markers, and labels no longer make a rollback.**
  On a Bundle the controller created, the `<release>-bundle-creator` admission policy refuses
  anyone else's add, change or removal of the `kardinal.io/rollback-from` annotation and the
  `kardinal.io/rollback` and `kardinal.io/pipeline` labels, so a script or GitOps tool that
  relabels those Bundles gets an error. New rollbacks record the version they roll back from in
  `spec.provenance.rollbackFrom` (a new field, step 2); rollbacks v0.12.0 created have none and
  are still found by their name. An automatic rollback adopts only a Bundle the controller created
  as that rollback, never another Bundle at its name or one that only carries the markers. The
  controller's own automatic rollbacks are never rolled back again, as before; any other version
  labelled `kardinal.io/rollback` is exempt from an automatic rollback only when every artifact it
  deploys was Verified in that environment before. v0.12.2 already adopts and exempts so (#2187);
  from v0.12.0 or v0.12.1 both are new. This applies to what is in flight when you upgrade: the
  admission rule covers the Bundles the controller created before the upgrade too, and a labelled
  version already promoting whose artifacts were never Verified there is rolled back
  automatically when its health check fails, where v0.12.0 and v0.12.1 stopped it for a person
  ([What Happens in Git](rollback.md#what-happens-in-git)).
- **kardinal's PR branch is kardinal's.** A PR create the SCM refuses counts as a duplicate only on
  the provider's own answer, and an open PR of kardinal's branch is adopted only when its head is,
  or contains, the commit kardinal pushed. git-push no longer force-pushes over a commit someone
  else pushed to kardinal's PR branch. Where v0.12.0 adopted such a PR or overwrote the commit, the
  step now fails against its retry budget with a `PRBranchForeignCommit` Warning Event that names
  the foreign head; any other refused create fails with the SCM's own message. This applies to
  steps already in flight when you upgrade: kardinal still recognises their own pushes (v0.12.0
  recorded them), but one that pushes again, or opens its PR again, over a commit someone else
  pushed to its branch now fails instead of overwriting the commit or adopting the PR
  ([PR evidence](pr-evidence.md), [SCM providers](scm-providers.md#supported-providers)).

**2. Apply the new CRDs.** v0.13.0 adds the `changerequests` and `promotionreceipts` CRDs, and
fields to Bundles, Pipelines, PolicyGates, PromotionSteps, AuditEvents, Approvals and
ImageVerifications. Helm never upgrades CRDs, and Helm owns the fields of the CRDs the v0.12.0
chart installed, so `--force-conflicts` is required:

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version> | kubectl apply --server-side --force-conflicts -f -
```

**3. Upgrade the chart.** `--reset-then-reuse-values` keeps the values you set and takes every
new default from the new chart.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version> \
  --namespace kardinal-system --reset-then-reuse-values --wait --timeout 3m
```

The new chart also installs the `<release>-receipts` admission policy (only the controller may
write a PromotionReceipt, whether or not receipts are on) and, with `rbac.userRoles.enabled`, the
`<release>-auditor` and `<release>-auditor-cluster` ClusterRoles, which nothing binds until you do
([User roles](guides/security.md#user-roles)).

**4. Check the result.**

```bash
kardinal doctor
kubectl get pipelines -A
kubectl get bundles -A
```

`kardinal doctor` passes every check, and every Pipeline is Ready.

## Upgrading from v0.11.0

The upgrade suite runs these steps as written (`TestUpgradeV0110_Estate`, on the oldest and newest
supported Kubernetes minors), on an estate of 30 Pipelines in 5 namespaces. Everything stored
under v0.11.0 keeps working:

- Versions in flight keep their Graphs, PromotionSteps, PRStatuses and gate instances, and
  finish. An open promotion PR stays the same PR.
- Approvals recorded before the upgrade still count toward their gate's quorum.
- Paused Pipelines stay paused and held environments stay held, until you resume or release them.

v0.12.0 targets the same kro as v0.11.0, so kro is not upgraded.

**1. Read the upgrade notes.** The changelog's [From v0.11.0](changelog.md#from-v0110) lists
what can change for you, and its v0.12.0 section every change since v0.11.0:

- **Kubernetes 1.35 or later.** The chart's `kubeVersion` refuses an older cluster. Check the
  server version first:

  ```bash
  kubectl version
  ```

- **Sync on promotion is scoped to the Pipeline's own objects.** The controller now asks Argo
  CD or Flux to sync only an Application or Kustomization in the Pipeline's namespace or
  deploying there. Applications that deploy to other namespaces, objects in `argocd` or
  `flux-system` that name no destination namespace, and Applications for another cluster are
  skipped, and their steps wait for the tool's poll. Admit them with `health.refresh.namespaces`
  (for example `default=my-app-*`, or `default=argocd`) in the values you upgrade with, or
  `["*=*"]` for v0.11.0's behaviour ([Sync on promotion](health-adapters.md#sync-on-promotion)).
- **Image Subscriptions' `provenance.commitSHA`** is the commit the build recorded in
  `org.opencontainers.image.revision`, or empty, instead of the image digest. A gate or PR
  template that reads it on those versions should read `images[0].digest` for the digest.
- **Free text has length limits**: a Bundle's `provenance.author` (256 characters) and
  `ciRunURL` (2048), a gate override's `reason` (1024) and a ChangeWindow's `reason` (1024).
  Objects already stored over a limit stay writable while that field is unchanged.
- **Agents are new.** Nothing changes for agents until you register one with an
  `AgentIdentity`; a registered agent's versions then wait for a person in each environment
  unless its `autonomy` is `auto` ([Actor-aware policy](agentic/policy.md#autonomy-per-environment)).

**2. Apply the new CRDs.** Helm never upgrades CRDs, and Helm owns the fields of the CRDs the
v0.11.0 chart installed, so `--force-conflicts` is required:

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 | kubectl apply --server-side --force-conflicts -f -
```

**3. Upgrade the chart.** `--reset-then-reuse-values` keeps the values you set and takes every
new default from the new chart.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 \
  --namespace kardinal-system --reset-then-reuse-values --wait --timeout 3m
```

**4. Check the result.**

```bash
kardinal doctor
kubectl get pipelines -A
kubectl get bundles -A
```

`kardinal doctor` passes every check, and every Pipeline is Ready.

## Upgrading from v0.10.0

These steps take a v0.10.0 installation to this release. The upgrade suite runs them as written
(`TestUpgradeV0100_FromV0100`, on the oldest and newest supported Kubernetes minors).
Promotions in flight keep running: their Graphs, PromotionSteps, PRStatuses and gate instances
are kept.
This release targets the same kro as v0.10.0, so kro is not upgraded.

**1. Read the upgrade notes.** The changelog's [From v0.10.0](changelog.md#from-v0100) lists
every change for existing objects and clients.

**2. Label the Pipelines' git Secrets.** A Secret named by `spec.git.secretRef` must carry
`kardinal.io/referenceable: "true"`: this release does not use an unlabeled one. Its Pipeline is
`Ready=False` with reason `SecretNotReferenceable`, and its git steps wait until it is labeled.
This labels the git Secret of every Pipeline:

```bash
kubectl get pipelines -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.spec.git.secretRef.name}{"\n"}{end}' |
  sort -u | while read -r ns name; do
    [ -z "$name" ] || kubectl -n "$ns" label secret "$name" kardinal.io/referenceable=true --overwrite
  done
```

**3. Take the `kardinal.io/referenceable` label off the controller's SCM token Secret.** No
Pipeline may use the controller's own token Secret, and the controller warns while that Secret
carries the label. This removes it, if it is there:

```bash
SECRET=$(kubectl -n kardinal-system get deployment kardinal-promoter \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="KARDINAL_SCM_TOKEN_SECRET_NAME")].value}')
[ -z "$SECRET" ] || kubectl -n kardinal-system label secret "$SECRET" kardinal.io/referenceable-
```

A Pipeline whose `spec.git.secretRef` names that Secret is `Ready=False` with reason
`ValidationFailed` after the upgrade. Give it a Secret of its own, labeled
`kardinal.io/referenceable=true`, before you upgrade. The chart's demo Pipeline uses
`demo.secretRef.name`.

**4. Move NotificationHook Authorization headers into Secrets.** This release has no
`spec.webhook.authorizationHeader`: the API server drops it, and the hook then posts without the
header. This moves each one into a Secret `<hook>-auth` (key `authorization`, labeled
`kardinal.io/referenceable=true`) and points the hook's `spec.webhook.secretRef` at it. It prints
nothing when no hook sets the field.

```bash
kubectl get notificationhooks -A -o json \
  | jq -r '.items[] | select((.spec.webhook.authorizationHeader // "") != "") | "\(.metadata.namespace) \(.metadata.name)"' \
  | while read -r ns hook; do
      value=$(kubectl -n "$ns" get notificationhook "$hook" -o jsonpath='{.spec.webhook.authorizationHeader}')
      kubectl -n "$ns" create secret generic "$hook-auth" --from-literal=authorization="$value"
      kubectl -n "$ns" label secret "$hook-auth" kardinal.io/referenceable=true
      kubectl -n "$ns" patch notificationhook "$hook" --type json -p '[{"op":"remove","path":"/spec/webhook/authorizationHeader"},{"op":"add","path":"/spec/webhook/secretRef","value":{"name":"'"$hook"'-auth"}}]'
    done
```

**5. Apply the new CRDs.** Helm never upgrades CRDs. The v0.10.0 chart installed them, so Helm
owns their fields: `--force-conflicts` is required, or the apply stops with
`conflict with "helm" ... .spec.versions`.

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 | kubectl apply --server-side --force-conflicts -f -
```

**6. Upgrade the chart.** Use `--reset-then-reuse-values`, which keeps the values you set and
takes every new default from the new chart.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 \
  --namespace kardinal-system --reset-then-reuse-values --wait --timeout 3m
```

If `helm get values kardinal-promoter -n kardinal-system` shows `validatingAdmissionPolicy` or
`rbac.integrationTestJobs`, add `--set validatingAdmissionPolicy=null` or
`--set rbac.integrationTestJobs=null`: the chart has neither, and `--reset-then-reuse-values`
would keep the value you set, which the schema rejects.

**7. Check the result.**

```bash
kardinal doctor
kubectl get pipelines -A
kubectl get bundles -A
```

`kardinal doctor` passes every check. Within a minute, `kubectl get pipelines` shows each
Pipeline's SUMMARY: what runs where. `kubectl get bundles` shows the VERSION of every Bundle that
is promoting or created after the upgrade; a Bundle that finished before it shows none. A Verified
Bundle stays Verified, every Bundle that was promoting finishes, and a new Bundle promotes end to
end.

## Upgrading from v0.9.0

These steps take a v0.9.0 installation to this release. The upgrade suite runs them as written
(`TestUpgradeV090_FromV090`, on the oldest and newest supported Kubernetes minors). Promotions
in flight keep running: their PromotionSteps, PRStatuses and gate instances are kept, and the
Graph of every Bundle that is still promoting is rebuilt in place by the new controller.

**1. Read the upgrade notes.** The changelog's [Before you upgrade](changelog.md#from-v090)
lists every change for existing objects and clients.

**2. Rename any environment named `time`.** The Pipeline CRD rejects it: `time` is reserved for
a kro Graph node ID. Renaming gives the environment new PromotionSteps and PR branches.

**3. Label the Pipelines' git Secrets.** A Secret named by `spec.git.secretRef` must carry
`kardinal.io/referenceable: "true"`: this release does not use an unlabeled one. Its Pipeline is
`Ready=False` with reason `SecretNotReferenceable`, and its git steps wait until it is labeled.
This labels the git Secret of every Pipeline:

```bash
kubectl get pipelines -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.spec.git.secretRef.name}{"\n"}{end}' |
  sort -u | while read -r ns name; do
    [ -z "$name" ] || kubectl -n "$ns" label secret "$name" kardinal.io/referenceable=true --overwrite
  done
```

**4. Move NotificationHook Authorization headers into Secrets.** This release has no
`spec.webhook.authorizationHeader`: the API server drops it, and the hook then posts without the
header. This moves each one into a Secret `<hook>-auth` (key `authorization`, labeled
`kardinal.io/referenceable=true`) and points the hook's `spec.webhook.secretRef` at it. It prints
nothing when no hook sets the field.

```bash
kubectl get notificationhooks -A -o json \
  | jq -r '.items[] | select((.spec.webhook.authorizationHeader // "") != "") | "\(.metadata.namespace) \(.metadata.name)"' \
  | while read -r ns hook; do
      value=$(kubectl -n "$ns" get notificationhook "$hook" -o jsonpath='{.spec.webhook.authorizationHeader}')
      kubectl -n "$ns" create secret generic "$hook-auth" --from-literal=authorization="$value"
      kubectl -n "$ns" label secret "$hook-auth" kardinal.io/referenceable=true
      kubectl -n "$ns" patch notificationhook "$hook" --type json -p '[{"op":"remove","path":"/spec/webhook/authorizationHeader"},{"op":"add","path":"/spec/webhook/secretRef","value":{"name":"'"$hook"'-auth"}}]'
    done
```

**5. Apply the new CRDs.** Helm never upgrades CRDs. Apply them before the new controller starts.
The v0.9.0 chart installed them, so Helm owns their fields: `--force-conflicts` is required, or
the apply stops with `conflict with "helm" ... .spec.versions`.

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 | kubectl apply --server-side --force-conflicts -f -
```

**6. Upgrade kro.** Run this from a checkout of kardinal-promoter at this release; it installs
the kro version the release targets, and leaves a kro that is already at it as it is.

```bash
KUBE_CONTEXT=<your-context> bash hack/install-kro.sh
```

**7. Upgrade the chart.** Use `--reset-then-reuse-values`, which keeps the values you set and
takes every new default from the new chart.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 \
  --namespace kardinal-system --reset-then-reuse-values --wait --timeout 3m
```

If `helm get values kardinal-promoter -n kardinal-system` shows `validatingAdmissionPolicy` or
`rbac.integrationTestJobs`, add `--set validatingAdmissionPolicy=null` or
`--set rbac.integrationTestJobs=null`: the chart has neither, and `--reset-then-reuse-values`
would keep the value you set, which the schema rejects.

**8. Check the result.**

```bash
kardinal doctor
kubectl get graphs.kro.run -A
kubectl get bundles -A
```

`kardinal doctor` passes every check. Every Bundle that was Promoting still has its Graph, and
finishes; a Verified Bundle stays Verified; a new Bundle promotes end to end.

v0.9.0 kept the Graph of every Bundle. This release retires the Graph of a finished Bundle
([Graph retirement](architecture.md#graph-retirement)) after a delay that starts when the
controller first finds the Bundle settled and sets its `GraphRetired` condition to `Scheduled`.
v0.9.0 never set that condition, so for a Bundle that finished under v0.9.0 the delay starts at the
new controller's first reconcile: its Graph goes one full delay after the upgrade (by default an
hour for a Verified Bundle still deployed, a minute for one a newer Bundle replaced). Each such
Bundle keeps its steps in `status.retiredSteps` and its phase; `kardinal get bundles`,
`kardinal history` and `kardinal rollback` read those records.

## Upgrading from v0.8.1

v0.8.1 ran its own Graph controller (krocodile, `experimental.kro.run`) from the kardinal chart. This release runs on upstream kro (`kro.run`), which you install separately. `helm upgrade --reuse-values` fails. A `helm upgrade` that gets past the values check deletes the `kro-system` namespace. Follow the steps below instead.

Tested on kind with Kubernetes 1.35 and 1.37, Helm 3.14, and Argo CD health checks with GitHub PRs. Promotions pause for about one minute (from step 3 until the new controller holds its leader lease). No Pipeline, Bundle, PromotionStep, PRStatus or PolicyGate is lost.

You need:

- Helm 3.14 or later, for `--reset-then-reuse-values`.
- `kubectl`, `jq`, `yq` and the new `kardinal` CLI.
- A checkout of kardinal-promoter at this release, for `hack/install-kro.sh`.

Check your Kubernetes version first: this release needs 1.35 or later. On an older cluster, upgrade Kubernetes before step 1 (see [Kubernetes version](#kubernetes-version)).

```bash
kubectl version | grep Server
```

### Steps

**1. Find stored objects the new CRDs reject.** This is required: some writes to these objects fail even with CRD validation ratcheting (see [Kubernetes version](#kubernetes-version)). The command prints one line per problem, and nothing when the cluster is clean.

```bash
kubectl get pipelines -A -o json | jq -r '
  ["api-version","kind","metadata","namespace","spec","status","graph","graphengine","kro","each","item","items","object","self","this","context","true","false","null","in","as","break","const","continue","else","for","function","if","import","let","loop","package","return","var","void","while","bundle","time"] as $reserved
  | .items[] | "pipeline \(.metadata.namespace)/\(.metadata.name)" as $p
  | ( ((.spec.environments // []) | group_by(.name)[] | select(length > 1) | "\($p): duplicate environment name \(.[0].name)"),
      ((.spec.environments // [])[] |
        (select(.autoRollback != null) | "\($p) env \(.name): autoRollback"),
        (select(.name as $n | ($reserved | index($n)) != null or ($n | test("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$") | not) or ($n | length) > 63) | "\($p) env \(.name): invalid or reserved name")) )'
kubectl get policygates -A -o json | jq -r '.items[] | "policygate \(.metadata.namespace)/\(.metadata.name)" as $g
  | (select((.metadata.name | length) > 63 and .spec.generated != true
      and ((.metadata.labels // {}) | (has("kardinal.io/bundle") or has("kardinal.io/gate-template") or .["kardinal.io/freeze"] == "true") | not))
      | "\($g): name longer than 63 characters")'
```

The new CRDs do not have Pipeline `spec.policyGates`, environment `steps`, `promotionTemplate` and `shard`, or PolicyGate `spec.selector`: the API server drops them from stored objects, and every environment runs the default step sequence. The finder does not list them, and they need no fix. Org gates apply through the `kardinal.io/applies-to` label.

Gate instances and pause freeze gates can have longer names. Kardinal created them, and the new controller sets `spec.generated` on them, the field that exempts them from the name limit, before it writes their status. The finder skips them.

Fix each line it prints. In the commands below, `<i>` is the environment's position in `spec.environments`, counting from 0.

| Finding | Fix |
|---|---|
| `autoRollback` | `kubectl -n <ns> patch pipeline <name> --type json -p '[{"op":"remove","path":"/spec/environments/<i>/autoRollback"}]'`. Use `onHealthFailure` instead (see [Rollback](rollback.md)). |
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

The new chart has no `krocodile`, `validatingAdmissionPolicy` or `rbac.integrationTestJobs` key, and its schema rejects them. If your values contain one, see [Helm values](#helm-values) before step 8.

Then label the Pipelines' git Secrets: this release uses a Secret named by `spec.git.secretRef`
only when it carries `kardinal.io/referenceable: "true"`. Without it the Pipeline is
`Ready=False` with reason `SecretNotReferenceable`, and its git steps wait.

```bash
kubectl get pipelines -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.spec.git.secretRef.name}{"\n"}{end}' |
  sort -u | while read -r ns name; do
    [ -z "$name" ] || kubectl -n "$ns" label secret "$name" kardinal.io/referenceable=true --overwrite
  done
```

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

On Kubernetes older than 1.35 the script exits 1 and installs nothing (see [Kubernetes version](#kubernetes-version)).

**7. Apply the new kardinal CRDs.** The v0.8.1 chart shipped no CRDs, and Helm never upgrades CRDs, so apply all 18 by hand; this adds the CRDs v0.8.1 did not have. Apply them before the new controller starts. `--force-conflicts` takes over fields another field manager (Helm, or an earlier `kubectl apply`) set.

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 | kubectl apply --server-side --force-conflicts -f -
```

**8. Upgrade the chart.** Use `--reset-then-reuse-values`, not `--reuse-values`. With `--reuse-values`, the v0.8.1 `krocodile` default is carried over and fails the new schema.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3 \
  --namespace kardinal-system --reset-then-reuse-values --wait --timeout 3m
```

Helm deletes the objects that only v0.8.1 needed:

- the `graph-controller` Deployment and ServiceAccount in `kro-system`;
- the `kardinal-graph-controller` ClusterRole and ClusterRoleBinding;
- the CRDs `graphs.experimental.kro.run` and `graphrevisions.experimental.kro.run`;
- with `validatingAdmissionPolicy.enabled` (the v0.8.1 default), the ValidatingAdmissionPolicies and bindings `kardinal-pipeline-validation`, `kardinal-policygate-validation` and `kardinal-bundle-validation`.

It adds:

- the ClusterRoles `kardinal-promoter-graph-applier` and `-kro-watch`;
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
- At startup the logs show three warnings: `scm.allowedRepositories` is not set, SCM webhooks are disabled without `--webhook-secret`, and UI API authentication is off (see [Other notes](#other-notes)). A Pipeline whose environments write the same path of the same repository and branch as another Pipeline's also logs `environments write overlapping paths` (its `PathConflict` condition); give each environment its own path.

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

### What happens to in-flight Bundles

- **Promoting Bundles continue.** The new controller builds a `kro.run` Graph for each one and takes over its existing PromotionSteps, PRStatuses and gate instances. The objects keep the same name, uid and creationTimestamp.
    - Verified environments stay Verified.
    - A step held by a gate keeps waiting.
    - Once the gate passes, the step opens its PR, and the Bundle finishes after the merge.
- **Superseded and finished Bundles** are left as they are. They get no new Graph (see step 11 for their children).
- **Gate instances follow their templates.** After the upgrade, editing a PolicyGate template reaches the Bundles still promoting: kardinal copies the gate again into each of them ([When a new or edited gate takes effect](policy-gates.md#when-a-new-or-edited-gate-takes-effect)). To release a Bundle a gate holds without changing the gate, override it:

    ```bash
    kardinal override <pipeline> --env <env> --gate <gate> --reason "<why>" --expires-in 1h
    ```

- **Check that Verified environments have the image.** v0.8.1 can report an environment Verified without writing a commit. If an environment branch has no kardinal commit for a Bundle that v0.8.1 reported Verified, create a new Bundle for the same image after the upgrade.
- **Bundles rebuilt from the start** only if the new CRDs were applied while v0.8.1 was running (see step 3). Nothing is lost, but steps that had already run are run again.

### Kubernetes version

This release needs Kubernetes 1.35 or later. `hack/install-kro.sh` checks the server version first and exits 1 on an older cluster, and the chart's `kubeVersion` makes `helm upgrade` refuse it. Upgrade Kubernetes first, then follow the steps above.

CRD validation ratcheting lets most writes to a stored object through when the new schema rejects one of its fields, but some writes fail, so step 1's finder is required. What each finding blocks once the new CRDs are applied:

| Stored value | What fails |
|---|---|
| env `autoRollback` | only edits to that environment |
| reserved environment name (e.g. `graph`) | only edits to that environment |
| environment name that is not a DNS label (e.g. `Test`) | nothing |
| PolicyGate name longer than 63 characters | **every write**, including labels and status |
| duplicate environment name | nothing; the Pipeline is `Ready=False` `ValidationFailed` |

If you already applied the CRDs, run the fixes from step 1 now. The patches and the gate copy succeed, and the objects recover.

Among the v0.8.1 examples, `custom-step` and `integration-test` set `steps`, and `multi-cluster-fleet` sets `shard`; the new CRDs drop both.

### Helm values

- **`krocodile`.** Remove it. If `helm get values` in step 2 shows it, you set it yourself, and `--reset-then-reuse-values` keeps the values you set, so the schema rejects it again. Pass your values file without the key instead, with `-f values-new.yaml` in place of `--reset-then-reuse-values` in step 8:

    ```bash
    yq 'del(.krocodile)' values.yaml > values-new.yaml
    ```

- **`validatingAdmissionPolicy` and `rbac.integrationTestJobs`.** The chart has neither. If `helm get values` in step 2 shows one, `--reset-then-reuse-values` keeps it and the schema rejects it: add `--set validatingAdmissionPolicy=null` or `--set rbac.integrationTestJobs=null` to step 8's command, which drops the value. The chart always installs its ValidatingAdmissionPolicies: the identity policies ([Verified identity](guides/security.md#verified-identity)), the hold-writes policy (only `pipelines/hold` may change `spec.holds`), the fleets policy (each `spec.pausedFleets` entry names an environment with a fleet) and the gitops-writes policy (the controller changes Argo CD and Flux objects only by their refresh annotation, see [Sync on promotion](health-adapters.md#sync-on-promotion)). It grants `batch/jobs` (create, get, list, watch, delete) for [hooks](hooks.md) and [rendered manifests](rendered-manifests.md).
- **`--reuse-values`** fails with `additional properties ... 'krocodile' not allowed` (Helm before 3.18.5: `Additional property krocodile is not allowed`), even when you never set `krocodile`. Use `--reset-then-reuse-values`.

### Other notes

- **Custom RBAC.** If you manage the controller's RBAC yourself, allow `create` and `patch` on `events.k8s.io` events.
- **UI.** With no UI auth mode set, `/api/` answers only local clients (`kubectl port-forward`). Set `ui.auth.tokenReview=true` or `ui.auth.tokenSecretRef.name` if the UI is reached another way.
- **SCM token scope.** v0.8.1 had no `scm.allowedRepositories`, so after the upgrade any Pipeline without its own `git.secretRef` can still have the controller's SCM token open PRs in any repository the token can write, and the controller warns at startup. Set `scm.allowedRepositories` to the repositories your Pipelines use (see [The shared SCM token](guides/security.md#the-shared-scm-token-and-scmallowedrepositories)).
- **Graph reader bindings.** v0.8.1 bound the reader role in every namespace a health `ref` named, and did not record those bindings. To v0.12.0, whose chart still ships the reader ClusterRole: in cluster mode its sweep deletes them once no Graph reads through them, and a binding that a Graph made by v0.8.1 still reads through stays until that Graph's Bundle is replaced. From v0.13.0 (not released yet), whose chart ships no reader ClusterRole: they grant nothing, but the controller cannot find them to delete them. In both cases, list them, and delete the ones you do not want, as [RBAC requirements](guides/security.md#rbac-requirements) shows.

- **Notes in the [changelog](changelog.md) that don't apply to v0.8.1:**
    - `promotionTemplate`, `PromotionStep.spec.inputs` and the `promotiontemplates` CRD don't exist in v0.8.1.
    - The `update.strategy: argocd` with `approval: pr-review` note doesn't apply: v0.8.1 allows only `kustomize` and `helm`.
    - The renamed examples (flux, flagger, argo-rollouts, github) are not in v0.8.1.

### If something goes wrong

- **`helm upgrade` fails with `additional properties 'krocodile' not allowed`** (Helm before 3.18.5: `Additional property krocodile is not allowed`). Nothing was changed. Rerun step 8 with `--reset-then-reuse-values`, or with `-f` and a values file without `krocodile`.
- **`kro-system` was deleted** (step 5 skipped). kro was deleted with it. Rerun `KUBE_CONTEXT=<your-context> bash hack/install-kro.sh`. The Graphs are in the Bundles' namespaces and survive; kro picks them up again.
- **`graphs.experimental.kro.run` is stuck `Terminating`** (step 4 skipped). Run the step 4 command. The CRD then finishes deleting (`kubectl wait --for=delete crd/graphs.experimental.kro.run --timeout=60s`), and no kardinal object is lost.
- **Steps ran again after the upgrade.** The CRDs were applied while v0.8.1 was running. Nothing is lost, and the Bundles finish.
- **`... is invalid` errors in the controller log.** Run the step 1 finder and fixes now.
- **Old PromotionSteps, PRStatuses or gates without a Bundle.** Run the step 11 command.
- **Rolling back to v0.8.1** was not tested.

---
