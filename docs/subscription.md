# Subscription CRD — CI-less Bundle Creation

The `Subscription` CRD automatically creates Bundle CRDs when new artifacts are detected
in an OCI registry or Git repository. This removes the CI dependency for artifact discovery.

## Overview

Without a Subscription, Bundles must be created manually (`kardinal create bundle`) or
via CI using the Bundle webhook. A Subscription handles this automatically by watching
an artifact source on a configurable interval.

## Supported Sources

| Type | Source | Trigger |
|---|---|---|
| `image` | Public OCI registry repository (ghcr.io, Docker Hub, Quay, a plain-HTTP local registry) | A new artifact among the tags matching `tagFilter` (see [Tag selection](#tag-selection)) |
| `git` | Public Git repository over smart HTTP(S) | New commit on the watched branch |

Only **public** repositories are supported. The OCI watcher uses the registry's
anonymous token flow and the Git watcher sends no credentials, so a private
repository (or a registry that always requires credentials, such as ECR) puts the
Subscription in phase `Error` with an "access denied" or "requires credentials"
message. For private artifacts, create Bundles from CI with the Bundle webhook
instead.

Every request has a 30-second timeout.

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
    interval: 5m                        # poll every 5 minutes (minimum 30s)
```

`registry` is a repository reference: `host/path`, a Docker Hub short name
(`nginx`, `myorg/app`, `docker.io/library/nginx`), or a URL with an explicit
`http://` or `https://` scheme for a local registry (`http://localhost:5000/my-app`).

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
`main`) gets a new Bundle rather than colliding with the previous one.

## Tag selection

The watcher lists the repository's tags, keeps those matching `tagFilter` (all tags
when empty), and picks one artifact:

| Matching tags | Selected | Registry requests per poll |
|---|---|---|
| Exactly one (for example `tagFilter: "^main$"`) | That tag; a new Bundle is created whenever its digest changes | tag list + 1 |
| All are semantic versions (`1.2.3`, `v1.2.3`, pre-releases) | The highest version | tag list + 1 |
| Anything else, up to 50 tags | The most recently built image (the image config `created` time) | tag list + about 2 per tag |
| Anything else, more than 50 tags | Nothing: phase `Error` asking you to narrow `tagFilter` | tag list |
| None | Nothing: phase `Error` ("no tag ... matches tagFilter") | tag list |

Tags such as `sha-abc1234` are not ordered, so "most recently built" has to read every
matching image and a busy repository soon passes the 50-tag limit. Prefer semantic
version tags or a single moving tag. A moving tag is safe to promote: the Bundle
records the digest, and the kustomize step writes that digest, not only the tag.
Registries rate-limit anonymous clients (Docker Hub counts manifest reads), so keep
the interval reasonable.

When a multi-architecture index is used, the digest is the index digest and the build
time comes from its `linux/amd64` image (or the first platform when there is none).

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
`<subscription>-<first 8 SHA chars>`. `pathGlob` is not implemented: a Subscription
that sets it goes to phase `Error` instead of silently creating a Bundle for every
commit. The repository must speak the Git smart HTTP protocol (GitHub, GitLab,
Gitea, `git http-backend`); an empty advertisement or a missing branch is an error.

Watch a repository, or a branch, that the Pipeline does not write to. Without path
filtering, a Subscription on the Pipeline's own `spec.git.url` and `spec.git.branch`
creates a new Bundle for every promotion commit kardinal makes there. The Bundle's
`configRef.gitRepo` is the Subscription's `repoURL`, and `config-merge` copies each
environment's path (`environments[].path`, default `environments/<name>`) from that
commit, so the source repository uses the same layout.

Complete manifests for both source types are in
[`examples/subscription/`](https://github.com/pnz1990/kardinal-promoter/tree/main/examples/subscription).

## Status Fields

| Field | Description |
|---|---|
| `status.phase` | `Watching` \| `Error` |
| `status.lastCheckedAt` | RFC3339 timestamp of last poll |
| `status.lastBundleCreated` | Name of the last Bundle created |
| `status.lastSeenDigest` | Digest/SHA from the last successful check. The first check only records it. |
| `status.message` | Error details when phase=Error |

## Deduplication

The first poll records the current digest in `status.lastSeenDigest` as a baseline and
creates no Bundle: an artifact that already existed when the Subscription was created is
not promoted. Each later poll that sees a different digest creates one Bundle.

Before creating a Bundle the controller also looks for an existing Bundle with the same
`kardinal.io/subscription` and `kardinal.io/source-digest` labels, so a restart or two
replicas polling at once do not create duplicates. If a Bundle with the generated name
already exists for a different digest, the Subscription goes to phase `Error`.

The `kardinal.io/source-digest` value is the digest without its `sha256:` prefix, cut to
the first 63 characters (the label value limit), so you can find a Bundle by digest:

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

## Relationship to CI

A Subscription is an alternative to CI-based Bundle creation. Use one or the other:

| Approach | When to use |
|---|---|
| CI webhook (`POST /api/v1/bundles`) | CI already builds and tags images |
| `kardinal create bundle` CLI | Manual or scripted promotions |
| Subscription CRD | CI-less discovery; image already published externally |
