# Ship automatically from images, charts and Git

A `Subscription` watches an OCI registry, a Git repository or a Helm chart repository and
creates a version for each new artifact it finds, so it is promoted without a CI step.

## Overview

Without a Subscription, versions must be created manually (`kardinal create bundle`) or
via CI using the Bundle webhook. A Subscription handles this automatically by watching
an artifact source on a configurable interval, or at once when the registry or SCM
posts a [webhook](#webhooks).

## Supported Sources

| Type | Source | Trigger | Version type |
|---|---|---|---|
| `image` | OCI registry repository: Docker Hub, GHCR, Quay, Harbor, ECR, GCR/Artifact Registry, ACR, Artifactory, any distribution registry, a plain-HTTP in-cluster registry; public or private | A new artifact among the tags that pass the [tag filters](#tag-selection) | `image` |
| `git` | Git repository over HTTP(S) or SSH, public or private | A new commit on the watched branch, optionally only one that changes [`pathGlob`](#path-filtering-pathglob) | `config` |
| `helm` | Helm chart repository: HTTP(S) `index.yaml` or OCI | A new chart version that passes the [tag filters](#tag-selection) | `chart` |

Private sources need a Secret in the Subscription's namespace (see
[Credentials](#credentials)). Every request has a 30-second timeout, and one poll at most 3 minutes (image), 2 minutes
(Git, SSH connections included) or 1 minute (Helm). Four Subscriptions poll at once.

The controller refuses to poll a registry, token realm, chart repository or Git server
on a loopback (`localhost` is the controller's own pod), link-local, cloud metadata,
unspecified or multicast address: the Subscription goes to phase `Error` with
`destination address is not allowed` in its message (see
[Outbound requests to user URLs](guides/security.md#outbound-requests-to-user-urls)).
Private addresses, such as in-cluster Services, are allowed. With the controller's egress
allowlist (chart value `egress.allowlist`, flag `--egress-allowlist`), a Subscription may
reach only the hosts and CIDRs listed there (see
[the egress allowlist](guides/security.md#outbound-requests-to-user-urls)). With the chart's
`networkPolicy.enabled`, egress is open only on 443 and 6443: add an `extraEgress` rule
for SSH (22) or a registry on another port.

A Subscription creates versions only in its **own namespace**, for the Pipeline named
in `spec.pipeline` in that namespace. `spec.namespace` is deprecated: leave it empty.
Any value other than the Subscription's own namespace puts the Subscription in phase
`Error` and creates no version.

## Example: OCI Image Subscription

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-image
  namespace: default
spec:
  type: image
  pipeline: my-app-pipeline
  image:
    registry: ghcr.io/myorg/my-app      # no tag, no digest, no credentials
    tagFilter: '^v\d+\.\d+\.\d+$'        # semantic version tags
    interval: 5m                        # poll every 5 minutes (values under 30s are raised to 30s)
```

`registry` is a repository reference: `host/path`, a Docker Hub short name
(`nginx`, `myorg/app`, `docker.io/library/nginx`), or a URL with an explicit
`http://` or `https://` scheme for a registry such as an in-cluster one
(`http://registry.registry.svc.cluster.local:5000/my-app`).

When `v1.4.0` is pushed after `v1.3.2`, the controller creates:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: my-app-image-v1-4-0-3f2a9c1b     # <subscription>-<tag>-<first 8 digest chars>
  labels:
    kardinal.io/pipeline: my-app-pipeline
    kardinal.io/subscription: my-app-image
    kardinal.io/source-digest: 3f2a9c1b...  # first 63 characters of the digest, without "sha256:"
spec:
  type: image
  pipeline: my-app-pipeline
  images:
    - repository: ghcr.io/myorg/my-app
      tag: v1.4.0
      digest: sha256:3f2a9c1b...
  provenance:                             # only when the build recorded its commit
    commitSHA: 7a98d10da0777741569e3c6bfb7e04d02c091005
```

`provenance.commitSHA` is the source commit the image's build recorded in
`org.opencontainers.image.revision`, as `docker buildx --label` and the OCI metadata actions
write it. kardinal looks, in order, at the annotation of the image's manifest; for a
multi-architecture index, at the index's own annotation, then the annotation of its
`linux/amd64` image (or of the first platform when there is none); and then at the label of
that image's config. The first revision found decides. It counts only when it is a full commit
SHA, 40 or 64 hex characters (lowercased); a tag such as `sha-1a2b3c4`, a short SHA, a build
number or a branch is not a commit. An image without one gets no `provenance`, and its PR
evidence shows `—` for the commit. The digest is in `images[0].digest` only. Versions created
before v0.12.0 may still hold the digest in `provenance.commitSHA`.

Version names are lowercased and made DNS-safe (`Build_42` becomes `build-42`) and are
at most 63 characters. The digest suffix means a re-pushed mutable tag (`latest`,
`main`) gets a new version rather than colliding with the previous one. A digest that
comes back after another one gets a `-2`, `-3`, ... suffix (see
[Deduplication](#deduplication)).

## Tag selection

The watcher lists the repository's tags (for a Helm Subscription, the chart's
versions) and applies the filters in this order. Each one is optional:

| Field | Keeps |
|---|---|
| `allowTags` | Only the listed tags (at most 100) |
| `tagFilter` | Tags that match the regular expression (RE2) |
| `excludeTagFilter` | Tags that do not match the regular expression (for example `-rc\.|-debug$`) |
| `ignoreTags` | Every tag but the listed ones (at most 100) |
| `semverConstraint` | Semantic versions (optional `v` prefix) that satisfy the constraint, such as `>=1.4.0 <2.0.0`, `^1.4` or `~1.4.2` ([Masterminds/semver](https://github.com/Masterminds/semver#checking-version-constraints) syntax). A constraint without a pre-release part excludes pre-releases. Every other tag is dropped |

Then `strategy` picks one tag among the remaining ones. Selection is deterministic: the
same tag list always picks the same tag.

| `strategy` | Selected | Registry requests per poll |
|---|---|---|
| `Auto` (default), exactly one tag (for example `tagFilter: "^main$"`) | That tag; a new version is created whenever its digest changes | tag list + 1 |
| `Auto`, all are semantic versions (`1.2.3`, `v1.2.3`, pre-releases) | The highest version | tag list + 1 |
| `Auto`, anything else, up to `discoveryLimit` tags | The most recently built image (the image config `created` time); when several tags point to it, the first listed tag. Different images with the same newest build time give phase `Error` ("same build time") | tag list + about 2 per tag |
| `Auto`, anything else, more than `discoveryLimit` tags | Nothing: phase `Error` asking you to narrow `tagFilter` | tag list |
| `SemVer` | The highest semantic version; other tags are ignored | tag list + 1 |
| `Lexical` | The tag that sorts last, for dated tags such as `2026-10-08.1` | tag list + 1 |
| `NewestBuild` | The most recently built image, at most `discoveryLimit` tags | tag list + about 2 per tag |
| No tag remains | Nothing: phase `Error` ("no tag ... matches tagFilter", or "passes the tag filters" with the filters listed) | tag list |

`discoveryLimit` (default 50, at most 200) bounds newest-build ordering, which reads every
remaining image on every poll. Tags such as `sha-abc1234` are not ordered, so a busy
repository soon passes it. Prefer semantic version tags or a single moving tag. A moving
tag is safe to promote: the version records the digest, and the kustomize step writes that
digest, not only the tag. Registries rate-limit clients (Docker Hub counts manifest
reads), so keep the interval reasonable, or poll rarely and use a
[webhook](#webhooks).

```yaml
spec:
  type: image
  pipeline: my-app-pipeline
  image:
    registry: ghcr.io/myorg/my-app
    semverConstraint: "^1.4"             # 1.x from 1.4 on, no pre-releases
    ignoreTags: ["1.6.0"]                # a release that must not ship
    interval: 10m
```

When a multi-architecture index is used, the digest is the index digest and the build
time comes from its `linux/amd64` image (or the first platform when there is none).

## Credentials

`spec.image.secretRef`, `spec.git.secretRef` and `spec.helm.secretRef` name a Secret
in the Subscription's own namespace (a Subscription cannot read a Secret in another
namespace). The Secret must carry the label `kardinal.io/referenceable: "true"`, so a user
who can create Subscriptions can use only the Secrets someone labelled for it, not every
Secret of the namespace:

```bash
kubectl label secret pull kardinal.io/referenceable=true
```

The controller reads the Secret on every poll, so a rotated credential is used at the next
poll. A Secret that is missing, not labelled, or has none of the keys below puts the
Subscription in phase `Error` with the `Ready` condition `False` (reason `SecretNotFound`,
`SecretNotReferenceable` or `SecretHasNoCredentials`) and nothing is sent; values never
appear in a message or a log.

With credentials attached, a redirect from `https` to `http` is refused, and so is a
redirect to another host, except a registry blob download (registries redirect those to
object storage), which is followed without the `Authorization` header.

| Source | Secret keys |
|---|---|
| Image, OCI chart | `.dockerconfigjson` (a `kubernetes.io/dockerconfigjson` Secret, as `kubectl create secret docker-registry` writes it; the entry for the registry host is used, with `username`/`password`, `auth`, `identitytoken` or `registrytoken`), or `username` and `password` |
| HTTP chart repository | `username` and `password` (basic auth) |
| Git over HTTP(S) | `token` (sent as the password of user `git`, or of `username` when set), or `username` and `password` |
| Git over SSH | `ssh-privatekey` (an unencrypted key, as in a `kubernetes.io/ssh-auth` Secret) and `known_hosts` (required: the host key is always checked) |

The watcher answers the registry's challenge: a `Bearer` challenge gets a token from the
realm with the credentials (Docker Hub, GHCR, Harbor, Quay, GCR and Artifact Registry, ACR,
distribution with token auth), a `Basic` challenge sends them with every request (ECR,
distribution with htpasswd, Artifactory). Without a Secret the watcher reads anonymously,
and a private repository gives phase `Error` saying `set secretRef`.

### Registry recipes

```bash
# Docker Hub (an access token), GHCR (a token with read:packages), Quay (a robot account),
# Harbor (a robot account), Artifactory, or any distribution registry:
kubectl create secret docker-registry pull -n default \
  --docker-server=ghcr.io --docker-username=my-bot --docker-password="$TOKEN"
```

```yaml
spec:
  type: image
  pipeline: my-app-pipeline
  image:
    registry: ghcr.io/myorg/private-app
    tagFilter: '^v\d+\.\d+\.\d+$'
    secretRef:
      name: pull
```

Cloud registries issue short-lived tokens. kardinal makes no cloud API calls: keep the
Secret fresh with a tool that writes a `dockerconfigjson` Secret, such as the
[External Secrets Operator](https://external-secrets.io/latest/api/generator/) generators
or a CronJob:

| Registry | Credential in the Secret | Lifetime | Keep it fresh with |
|---|---|---|---|
| Amazon ECR | user `AWS`, the password from `aws ecr get-login-password` | 12 hours | ESO `ECRAuthorizationToken` generator, or a CronJob that runs `kubectl create secret docker-registry ... --dry-run=client -o yaml \| kubectl apply -f -` every 6 hours |
| Google Artifact Registry, GCR | user `oauth2accesstoken`, an access token; or user `_json_key` and a service account key (no expiry) | 1 hour (access token) | ESO `GCRAccessToken` generator |
| Azure ACR | a service principal's client ID and secret; or user `00000000-0000-0000-0000-000000000000` and an ACR refresh token as `identitytoken` | 3 hours (refresh token) | ESO `ACRAccessToken` generator |

### Private Git repositories

```bash
kubectl create secret generic config-repo -n default --from-literal=token="$TOKEN"
# SSH: a deploy key and the server's host key
ssh-keyscan -t ed25519 github.com > known_hosts
kubectl create secret generic config-repo-ssh -n default --type=kubernetes.io/ssh-auth \
  --from-file=ssh-privatekey=deploy_key --from-file=known_hosts=known_hosts
```

```yaml
spec:
  type: git
  pipeline: my-app-pipeline
  git:
    repoURL: git@github.com:myorg/my-config.git    # or ssh://git@host:2222/org/repo.git
    secretRef:
      name: config-repo-ssh
```

An SSH host that is not in `known_hosts`, or whose key differs, is refused. The
`known_hosts` line names the host as it appears in `repoURL` (`[host]:port` for a port
other than 22).

## Example: Git Repository Subscription

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-config-git
  namespace: default
spec:
  type: git
  pipeline: my-app-pipeline
  git:
    repoURL: https://github.com/myorg/my-config-source
    branch: main
    interval: 5m
```

Every new commit on `branch` creates a `config` version named
`<subscription>-<first 8 SHA chars>`. Over HTTP(S) the repository must speak the Git smart
HTTP protocol (GitHub, GitLab, Gitea, Forgejo, Bitbucket, `git http-backend`); over SSH the
server runs `git-upload-pack`. An empty advertisement or a missing branch is an error.

Watch a repository, or a branch, that the Pipeline does not write to, or set `pathGlob` to
paths kardinal does not write. Without it, a Subscription on the Pipeline's own
`spec.git.url` and `spec.git.branch` creates a new version for every promotion commit
kardinal makes there. The version's `configRef.gitRepo` is the Subscription's `repoURL`, and
`config-merge` copies each environment's path (`environments[].path`, default
`environments/<name>`) from that commit, so the source repository uses the same layout.

### Path filtering (pathGlob)

```yaml
  git:
    repoURL: https://github.com/myorg/monorepo
    pathGlob: "services/web/**"     # also "*.yaml", "{base,overlays}/**"
    discoveryLimit: 20
```

With `pathGlob`, only a commit that adds, changes, deletes or renames a file matching the
glob counts (`**` matches any number of directories; the old and the new path of a rename
both count). A poll that sees a new branch head reads the branch's first-parent history
back from the head, at most `discoveryLimit` commits (default 20, at most 200) and only as
far as the head the previous poll saw (`status.lastSeenRevision`). The version is for the
newest matching commit, so later commits outside the glob do not change it. The first poll
records the newest matching commit within reach, or the head when there is none.

The read is a shallow fetch without file contents when the server supports partial clone
(GitHub, GitLab, Gitea, Forgejo do), into memory: at most 64 MiB on the wire, at most 32 MiB
for one object and 128 MiB for all of them inflated (a pack that inflates past these, such
as a compression bomb, is an error before anything is kept); it happens only when the
head moved. Adding or editing `pathGlob` records a new baseline (the newest matching
commit, in `status.observedPathGlob` and `status.lastSeenDigest`) and creates no version. A matching commit more than `discoveryLimit` commits behind the head when it is
first seen is not found: raise `discoveryLimit` or poll more often for busy branches.

## Example: Helm Chart Subscription

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: podinfo-chart
  namespace: default
spec:
  type: helm
  pipeline: my-app-pipeline
  helm:
    repoURL: https://stefanprodan.github.io/podinfo   # serves index.yaml
    # repoURL: oci://ghcr.io/stefanprodan/charts      # an OCI repository; oci+http:// for a plain-HTTP registry
    chart: podinfo
    semverConstraint: ">=6.0.0 <7.0.0"
    interval: 10m
```

For an HTTP(S) repository the watcher reads `<repoURL>/index.yaml` and identifies a version
by its index `digest`. For an OCI repository the chart is the repository
`<repoURL path>/<chart>`, its tags are the versions (Helm writes `+` as `_` in a tag), and
the version is identified by its manifest digest. The highest semantic version that passes
the [tag filters](#tag-selection) wins; versions that are not semantic versions are
ignored. Without `semverConstraint` pre-releases count. Some servers (GitHub, Gitea,
Forgejo) answer 404 for a private repository read without credentials.

A new chart version creates a `chart` Bundle:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: podinfo-chart-6-15-0-1a2b3c4d
  labels:
    kardinal.io/subscription: podinfo-chart
spec:
  type: chart
  pipeline: my-app-pipeline
  chart:
    repoURL: https://stefanprodan.github.io/podinfo
    name: podinfo
    version: 6.15.0
    digest: sha256:1a2b3c4d...
```

### Promoting a chart version

A chart version needs `update.strategy: helm` in every environment it promotes; any other
strategy fails the version when its Graph is built, and so does an environment with
`layout: branch` (its render Job does not get the chart version yet). The `helm-set-image` step writes
`spec.chart.version` at `update.helm.chartVersionPath` in `update.helm.chartVersionFile`
(relative to the environment path), then commits and pushes (or opens a PR) as for images:

| Where the chart version lives | `chartVersionFile` | `chartVersionPath` |
|---|---|---|
| Umbrella chart, the dependency named after the chart (the default) | `Chart.yaml` | `.dependencies[name=<chart>].version` |
| Argo CD Application with a Helm source | `application.yaml` | `.spec.source.targetRevision` |
| Flux HelmRelease | `helmrelease.yaml` | `.spec.chart.spec.version` |
| kustomize `helmCharts` | `kustomization.yaml` | `.helmCharts[name=podinfo].version` |

```yaml
  environments:
    - name: prod
      update:
        strategy: helm
        helm:
          chartVersionFile: helmrelease.yaml
          chartVersionPath: .spec.chart.spec.version
```

A numeric path segment indexes an existing list element and `[field=value]` selects the
element whose field has that value (an umbrella chart without a dependency of the chart's
name fails the step: `dependencies has no element with name "podinfo"`); missing mapping
keys are created, a missing list element or a path through a scalar fails the step. Comments and key order
are kept. The chart repository is not rewritten: the file keeps its own repository
reference. Rolling back a chart version restores the previous chart version Verified in the
environment. `kardinal create bundle` does not create chart versions; the Bundle API does
(`"type": "chart"` with `chart.name` and `chart.version`).

## Refreshing at once

A Subscription polls every `interval`. A webhook from the registry or SCM makes it poll at
once (see [Subscription webhooks](#webhooks)), and so does setting the
`kardinal.io/refresh` annotation:

```bash
kubectl annotate subscription my-app-image kardinal.io/refresh="$(date -u +%FT%TZ)" --overwrite
```

The poll answers the request in `status.lastRefreshRequest`. A request less than 10 seconds
after the previous poll waits until 10 seconds have passed, so a burst of pushes costs one
poll.

Complete manifests for the image and git sources are in
[`examples/subscription/`](https://github.com/pnz1990/kardinal-promoter/tree/main/examples/subscription).

## Status Fields

| Field | Description |
|---|---|
| `status.phase` | `Watching` \| `Error` |
| `status.lastCheckedAt` | RFC3339 timestamp of last poll |
| `status.lastBundleCreated` | Name of the last version created |
| `status.lastSeenDigest` | Digest, SHA or chart digest from the last successful check. The first check only records it. |
| `status.lastSeenTag` | The tag, short SHA or chart version of `lastSeenDigest` |
| `status.lastSeenRevision` | With `pathGlob`: the branch head the last poll read up to |
| `status.lastRefreshRequest` | The `kardinal.io/refresh` value the last poll answered |
| `status.observedPathGlob` | The `pathGlob` of the last successful poll |
| `status.observedSelection` | A hash of the source and tag selection (`registry`, `repoURL`, `chart`, `branch`, `strategy`, the tag filters, `discoveryLimit`) `lastSeenDigest` was chosen with |
| `status.conditions[type=Ready]` | `True` while polling; `False` with the reason (`SecretNotReferenceable`, `SecretNotFound`, `SecretUnreadable`, `SecretHasNoCredentials`, `WatchFailed`, `InvalidSpec`) |
| `status.message` | Error details when phase=Error |

## Deduplication

The first poll records the current digest in `status.lastSeenDigest` as a baseline and
creates no version: an artifact that already existed when the Subscription was created is
not promoted. Each later poll that sees a different digest creates one version.

Editing the source or what selects the artifact also records a new baseline and creates no
version:

- For an image Subscription: `registry`, `strategy`, `tagFilter`, `excludeTagFilter`,
  `allowTags`, `ignoreTags`, `semverConstraint` and `discoveryLimit`.
- For a Helm Subscription: `repoURL`, `chart` and its filters.
- For a Git Subscription: `repoURL` and `branch`. The new branch is read from its head.

The poll after the edit records what the new selection picks, in `status.lastSeenDigest`
and `status.observedSelection` (a hash of those fields). The next new artifact under it is a
version. A new selection can pick an older artifact, for example `strategy: Lexical` instead
of `NewestBuild`, or a mirror that lags. Promoting it would roll the environments back.

An edit that picks the same way is not a new baseline. Examples are `interval`, the order of
`allowTags`, or a default spelled out. The same applies to `pathGlob` (see
[Path filtering](#path-filtering-pathglob)).

An artifact pushed between the edit and that poll becomes the baseline too. It is promoted
only when the next artifact arrives. To promote it at once, create its version with
`kardinal create bundle`, or push again.

A Subscription whose baseline predates `status.observedSelection` keeps it, and its first
poll after an upgrade records the selection.

Before creating a version the controller lists the Subscription's versions (label
`kardinal.io/subscription`). When the newest one is already for the digest (label
`kardinal.io/source-digest`), it creates nothing, so a restart or two replicas polling at
once do not create duplicates. A digest that comes back after another one, such as a
moving tag pushed back to an earlier image or a branch reset to an earlier commit, is a
new change: it gets a new version, named after the first one with `-2` the second time,
`-3` the third, and so on. If a version with the generated name already exists for a
different digest, the Subscription goes to phase `Error`.

The `kardinal.io/source-digest` value is the digest without its `sha256:` prefix, cut to
the first 63 characters (the label value limit), so you can find the versions of a digest:

```bash
kubectl get bundles -l kardinal.io/source-digest=$(echo "${DIGEST#sha256:}" | cut -c1-63)
```

Versions carry labels, not owner references: deleting a Subscription does not delete
the versions it created or stop their promotions.

## Checking Subscription Status

```bash
kubectl get subscriptions
# NAME              TYPE    PIPELINE            PHASE     LAST-BUNDLE            AGE
# my-app-image      image   my-app-pipeline     Watching  my-app-image-v1-4-0-3f2a9c1b   5m

kubectl describe subscription my-app-image
```

## Architecture

The Subscription reconciler is an owned-node-style reconciler: it polls the source,
writes the result to the Subscription's own status, and creates versions. A Subscription
is not part of a Graph (Graphs are per version, and a kro `ref` node can only read
Kubernetes objects, not a registry); the versions it creates are built into Graphs like any
other. The webhook receiver only sets the `kardinal.io/refresh` annotation; the reconciler
does the poll.

## Relationship to CI

A Subscription is an alternative to CI-based version creation. Use one or the other:

| Approach | When to use |
|---|---|
| CI webhook (`POST /api/v1/bundles`) | CI already builds and tags images |
| `kardinal create bundle` CLI | Manual or scripted promotions |
| Subscription CRD | CI-less discovery; image or chart already published externally |

## Webhooks

A [Subscription](subscription.md) polls its source every `interval`. A registry or SCM
webhook makes it poll at once: push an image, a chart or a commit, and the version is
created within seconds instead of at the next poll. With webhooks in place, a long
interval (`1h`) is enough as a fallback, which also keeps registry rate limits away.

The receiver is on the controller's webhook port (`:8083`, the port of `/webhook/scm`
and the Bundle API):

```
POST /webhook/subscriptions/<namespace>/<subscription>/<provider>[/<token>]
```

`<provider>` is one of `dockerhub`, `ghcr` (or `github`), `harbor`, `quay`,
`artifactory` and `generic`. Expose the port the way you expose `/webhook/scm`, for
example with an Ingress for `/webhook/`.

### What a delivery does

An authenticated delivery that announces a push sets the Subscription's
`kardinal.io/refresh` annotation to the current time and gets `202 Accepted`. The
Subscription reconciler sees the annotation, polls the source with the Subscription's
filters and credentials, creates a version when there is a new artifact, and records the
request it answered in `status.lastRefreshRequest`. The receiver itself never reads the
source and never creates a version, so a forged or replayed payload can at most cause a
poll, and the payload's tag or digest is never trusted.

| Answer | When |
|---|---|
| `202 {"status":"refresh requested"}` | The annotation was set |
| `202 {"status":"refresh already pending"}` | A refresh is already pending (the annotation differs from `status.lastRefreshRequest`): duplicate and redelivered events are not written again |
| `200 {"status":"ignored: <event>"}` | Authenticated, but not a push: a GitHub `ping`, a Harbor delete, an Artifactory `deleted` |
| `400` | The payload is not the provider's format |
| `401 {"status":"unauthorized"}` | Any authentication failure, a Subscription that does not exist, or one without `spec.webhook`: the answer does not reveal which Subscriptions exist |
| `404` | The path is not `/webhook/subscriptions/<namespace>/<name>/<provider>[/<token>]` |
| `413` | The payload is over 1 MB |
| `429` | More than 10 requests a second (bursts of 20) from one source address, or 5 a second (bursts of 10) for one Subscription. Behind an Ingress or load balancer every sender has the Ingress's address, so all senders share one per-address bucket, and the per-Subscription limit is shared by everyone who posts to that Subscription: neither separates one sender from another. Give each Subscription its own token Secret ([Enabling it on a Subscription](#enabling-it-on-a-subscription)), so a leaked token reaches one Subscription only, and rate-limit per client at the Ingress |

The reconciler polls at most once every 10 seconds per Subscription, however many
deliveries arrive.

### Enabling it on a Subscription

Each Subscription opts in with `spec.webhook.secretRef`, a Secret in its namespace whose
key `token` (at least 16 characters) authenticates the deliveries. Like every Secret a
Subscription reads, it must be labelled `kardinal.io/referenceable: "true"`:

```bash
kubectl create secret generic my-app-webhook -n default --from-literal=token="$(openssl rand -hex 24)"
kubectl label secret my-app-webhook -n default kardinal.io/referenceable=true
```

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-image
  namespace: default
spec:
  type: image
  pipeline: my-app-pipeline
  image:
    registry: docker.io/myorg/my-app
    semverConstraint: ">=1.0.0"
    interval: 1h                 # a fallback; the webhook triggers the polls
  webhook:
    secretRef:
      name: my-app-webhook
```

The token is read on every delivery: rotate it by updating the Secret and the sender.

### Setting up each sender

In the URLs below, `$KARDINAL` is the controller's webhook address
(`https://kardinal.example.com`) and `$TOKEN` the Secret's `token`.

| Sender | Where | URL | Authentication |
|---|---|---|---|
| Docker Hub | Repository, Webhooks | `$KARDINAL/webhook/subscriptions/default/my-app-image/dockerhub/$TOKEN` | The token in the URL (Docker Hub cannot sign) |
| Quay | Repository settings, Notifications, "Push to Repository", Webhook POST | `$KARDINAL/webhook/subscriptions/default/my-app-image/quay/$TOKEN` | The token in the URL |
| Harbor | Project, Webhooks, event "Artifact pushed" (or "Chart uploaded"), HTTP | `$KARDINAL/webhook/subscriptions/default/my-app-image/harbor` | Auth Header: `$TOKEN` (sent as the `Authorization` header) |
| GHCR | Repository or organization Settings, Webhooks, event "Packages" (or "Registry packages"; "Pushes" for a git Subscription), content type JSON | `$KARDINAL/webhook/subscriptions/default/my-app-image/ghcr` | Secret: `$TOKEN` (GitHub signs `X-Hub-Signature-256`; a token in the URL is not accepted) |
| Artifactory | Administration, Webhooks, Docker or Artifact domain, event "pushed" or "deployed" | `$KARDINAL/webhook/subscriptions/default/my-app-image/artifactory` | Secret token: `$TOKEN` (`X-JFrog-Event-Auth`, plain or with "use secret for payload signing") |
| Anything else (CI, a script, GitLab, Gitea) | | `$KARDINAL/webhook/subscriptions/default/my-app-image/generic/$TOKEN` | The token in the URL, or no token in the URL and `X-Kardinal-Signature-256: sha256=<hex HMAC-SHA256 of the body with $TOKEN>` |

A token in the URL is as secret as the token itself: use HTTPS (`tls.enabled` or an
Ingress that terminates TLS), and prefer a signing sender where you have one.

```bash
# generic, signed
body='{"image":"myorg/my-app","tag":"v1.4.0"}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$TOKEN" | cut -d' ' -f2)
curl -X POST -H "X-Kardinal-Signature-256: sha256=$sig" -d "$body" \
  "$KARDINAL/webhook/subscriptions/default/my-app-image/generic"
```

The generic provider accepts any body. The others check that the payload is the
provider's push event: Docker Hub `push_data`, Quay `updated_tags`, Harbor `type`
`PUSH_ARTIFACT`, `pushImage` or `UPLOAD_CHART`, Artifactory `event_type` `pushed` or
`deployed`, GitHub `X-GitHub-Event` `package`, `registry_package` or `push`.

### Troubleshooting

- A refusal for a Subscription that does not exist, or has no `spec.webhook`, reads a
  placeholder Secret and checks the token against a random key, so its 401 takes about as
  long as a wrong token's. A sender that can measure latency precisely may still tell the
  two apart (the Secret read of an existing and a missing Secret differ slightly); the
  Subscription names are not secret in most clusters, but do not put anything sensitive in
  them.
- Every refusal is logged by the controller with the reason (`subscription webhook
  refused`), never with the token: a missing `spec.webhook`, a missing Secret, a token
  under 16 characters, or no valid token or signature.
- `kubectl get subscription my-app-image -o jsonpath='{.metadata.annotations.kardinal\.io/refresh} {.status.lastRefreshRequest}'`
  shows the last request and the last one a poll answered. Equal values mean the poll ran;
  check `status.phase` and `status.message` for its result.
- A delivery that the reconciler answered without a version means the source had no new
  artifact that passes the filters: the receiver does not choose the tag, the poll does.
