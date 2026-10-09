# Subscription CRD — CI-less Bundle Creation

The `Subscription` CRD automatically creates Bundle CRDs when new artifacts are detected
in an OCI registry, a Git repository or a Helm chart repository. This removes the CI
dependency for artifact discovery.

## Overview

Without a Subscription, Bundles must be created manually (`kardinal create bundle`) or
via CI using the Bundle webhook. A Subscription handles this automatically by watching
an artifact source on a configurable interval, or at once when the registry or SCM
posts a [webhook](subscription-webhooks.md).

## Supported Sources

| Type | Source | Trigger | Bundle |
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

A Subscription creates Bundles only in its **own namespace**, for the Pipeline named
in `spec.pipeline` in that namespace. `spec.namespace` is deprecated: leave it empty.
Any value other than the Subscription's own namespace puts the Subscription in phase
`Error` and creates no Bundle.

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
```

Bundle names are lowercased and made DNS-safe (`Build_42` becomes `build-42`) and are
at most 63 characters. The digest suffix means a re-pushed mutable tag (`latest`,
`main`) gets a new Bundle rather than colliding with the previous one. A digest that
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
| `Auto` (default), exactly one tag (for example `tagFilter: "^main$"`) | That tag; a new Bundle is created whenever its digest changes | tag list + 1 |
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
tag is safe to promote: the Bundle records the digest, and the kustomize step writes that
digest, not only the tag. Registries rate-limit clients (Docker Hub counts manifest
reads), so keep the interval reasonable, or poll rarely and use a
[webhook](subscription-webhooks.md).

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

Every new commit on `branch` creates a `config` Bundle named
`<subscription>-<first 8 SHA chars>`. Over HTTP(S) the repository must speak the Git smart
HTTP protocol (GitHub, GitLab, Gitea, Forgejo, Bitbucket, `git http-backend`); over SSH the
server runs `git-upload-pack`. An empty advertisement or a missing branch is an error.

Watch a repository, or a branch, that the Pipeline does not write to, or set `pathGlob` to
paths kardinal does not write. Without it, a Subscription on the Pipeline's own
`spec.git.url` and `spec.git.branch` creates a new Bundle for every promotion commit
kardinal makes there. The Bundle's `configRef.gitRepo` is the Subscription's `repoURL`, and
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
far as the head the previous poll saw (`status.lastSeenRevision`). The Bundle is for the
newest matching commit, so later commits outside the glob do not change it. The first poll
records the newest matching commit within reach, or the head when there is none.

The read is a shallow fetch without file contents when the server supports partial clone
(GitHub, GitLab, Gitea, Forgejo do), into memory: at most 64 MiB on the wire, at most 32 MiB
for one object and 128 MiB for all of them inflated (a pack that inflates past these, such
as a compression bomb, is an error before anything is kept); it happens only when the
head moved. Adding or editing `pathGlob` records a new baseline (the newest matching
commit, in `status.observedPathGlob` and `status.lastSeenDigest`) and creates no Bundle. A matching commit more than `discoveryLimit` commits behind the head when it is
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

A new version creates a `chart` Bundle:

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

A chart Bundle needs `update.strategy: helm` in every environment it promotes; any other
strategy fails the Bundle when its Graph is built. The `helm-set-image` step writes
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
reference. Rolling back a chart Bundle restores the previous chart version Verified in the
environment. `kardinal create bundle` does not create chart Bundles; the Bundle API does
(`"type": "chart"` with `chart.name` and `chart.version`).

## Refreshing at once

A Subscription polls every `interval`. A webhook from the registry or SCM makes it poll at
once (see [Subscription webhooks](subscription-webhooks.md)), and so does setting the
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
| `status.lastBundleCreated` | Name of the last Bundle created |
| `status.lastSeenDigest` | Digest, SHA or chart digest from the last successful check. The first check only records it. |
| `status.lastSeenTag` | The tag, short SHA or chart version of `lastSeenDigest` |
| `status.lastSeenRevision` | With `pathGlob`: the branch head the last poll read up to |
| `status.lastRefreshRequest` | The `kardinal.io/refresh` value the last poll answered |
| `status.observedPathGlob` | The `pathGlob` of the last successful poll |
| `status.conditions[type=Ready]` | `True` while polling; `False` with the reason (`SecretNotReferenceable`, `SecretNotFound`, `SecretHasNoCredentials`, `WatchFailed`, `InvalidSpec`) |
| `status.message` | Error details when phase=Error |

## Deduplication

The first poll records the current digest in `status.lastSeenDigest` as a baseline and
creates no Bundle: an artifact that already existed when the Subscription was created is
not promoted. Each later poll that sees a different digest creates one Bundle.

Before creating a Bundle the controller lists the Subscription's Bundles (label
`kardinal.io/subscription`). When the newest one is already for the digest (label
`kardinal.io/source-digest`), it creates nothing, so a restart or two replicas polling at
once do not create duplicates. A digest that comes back after another one, such as a
moving tag pushed back to an earlier image or a branch reset to an earlier commit, is a
new change: it gets a new Bundle, named after the first one with `-2` the second time,
`-3` the third, and so on. If a Bundle with the generated name already exists for a
different digest, the Subscription goes to phase `Error`.

The `kardinal.io/source-digest` value is the digest without its `sha256:` prefix, cut to
the first 63 characters (the label value limit), so you can find the Bundles of a digest:

```bash
kubectl get bundles -l kardinal.io/source-digest=$(echo "${DIGEST#sha256:}" | cut -c1-63)
```

Bundles created by earlier releases carry the last 63 characters instead. The controller
still matches those, so upgrading does not create a second Bundle for the same digest.

Bundles carry labels, not owner references: deleting a Subscription does not delete
the Bundles it created or stop their promotions.

## Checking Subscription Status

```bash
kubectl get subscriptions
# NAME              TYPE    PIPELINE            PHASE     LAST-BUNDLE            AGE
# my-app-image      image   my-app-pipeline     Watching  my-app-image-v1-4-0-3f2a9c1b   5m

kubectl describe subscription my-app-image
```

## Architecture

The Subscription reconciler is an owned-node-style reconciler: it polls the source,
writes the result to the Subscription's own status, and creates Bundles. A Subscription
is not part of a Graph (Graphs are per Bundle, and a kro `ref` node can only read
Kubernetes objects, not a registry); the Bundles it creates are built into Graphs like any
other. The webhook receiver only sets the `kardinal.io/refresh` annotation; the reconciler
does the poll.

## Relationship to CI

A Subscription is an alternative to CI-based Bundle creation. Use one or the other:

| Approach | When to use |
|---|---|
| CI webhook (`POST /api/v1/bundles`) | CI already builds and tags images |
| `kardinal create bundle` CLI | Manual or scripted promotions |
| Subscription CRD | CI-less discovery; image or chart already published externally |
