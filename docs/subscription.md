# Subscription CRD — CI-less Bundle Creation

The `Subscription` CRD automatically creates Bundle CRDs when new artifacts are detected
in an OCI registry or Git repository. This removes the CI dependency for artifact discovery.

!!! warning "Source watchers in active development"
    The OCI image watcher (`type: image`) and Git watcher (`type: git`) are currently stubs.
    A Subscription can be created and will enter `status.phase = Watching`, but it will not
    discover new tags or commits yet — `Changed: false` is always returned.

    Real polling is being implemented (GitHub issues
    [#491](https://github.com/pnz1990/kardinal-promoter/issues/491) for OCI,
    [#493](https://github.com/pnz1990/kardinal-promoter/issues/493) for Git).
    Until those land, create Bundles manually with `kardinal create bundle` or the CI webhook.

## Overview

Without a Subscription, Bundles must be created manually (`kardinal create bundle`) or
via CI using the Bundle webhook. A Subscription handles this automatically by watching
an artifact source on a configurable interval.

## Supported Sources

| Type | Source | Trigger |
|---|---|---|
| `image` | OCI registry (ghcr.io, ECR, etc.) | New tag matching optional regex filter |
| `git` | Git repository (HTTPS) | New commit on watched branch |

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
    registry: ghcr.io/myorg/my-app
    tagFilter: "^sha-"      # only tags starting with "sha-"
    interval: 5m            # poll every 5 minutes
```

When `ghcr.io/myorg/my-app` has a new tag matching `^sha-`, the controller creates:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: my-app-image-sha-abc1234
  labels:
    kardinal.io/pipeline: my-app-pipeline
    kardinal.io/subscription: my-app-image
spec:
  type: image
  pipeline: my-app-pipeline
  images:
    - repository: ghcr.io/myorg/my-app
      tag: sha-abc1234
      digest: sha256:abc123...
```

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
    repoURL: https://github.com/myorg/my-gitops-repo
    branch: main
    pathGlob: "config/**"   # only commits touching config/ files
    interval: 5m
```

## Status Fields

| Field | Description |
|---|---|
| `status.phase` | `Watching` \| `Error` |
| `status.lastCheckedAt` | RFC3339 timestamp of last poll |
| `status.lastBundleCreated` | Name of the last Bundle created |
| `status.lastSeenDigest` | Digest/SHA from last successful check (deduplication) |
| `status.message` | Error details when phase=Error |

## Deduplication

The controller tracks `status.lastSeenDigest`. A Bundle is only created when the digest
changes from the last known value. Multiple reconcile runs for the same digest do not
create duplicate Bundles.

## Checking Subscription Status

```bash
kubectl get subscriptions
# NAME              TYPE    PIPELINE            PHASE     LAST-BUNDLE            AGE
# my-app-image      image   my-app-pipeline     Watching  my-app-image-sha-abc   5m

kubectl describe subscription my-app-image
```

## Relationship to CI

A Subscription is an alternative to CI-based Bundle creation. Use one or the other:

| Approach | When to use |
|---|---|
| CI webhook (`POST /api/v1/bundles`) | CI already builds and tags images |
| `kardinal create bundle` CLI | Manual or scripted promotions |
| Subscription CRD | CI-less discovery; image already published externally |
