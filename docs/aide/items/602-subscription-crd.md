# Item 602: Subscription CRD — OCI + Git watching for CI-less bundle creation

> Queue: queue-019
> Issue: Stage 18 (no separate issue — create during implementation)
> Priority: medium
> Size: xl
> Milestone: v0.7.0 — Graph Purity
> Depends on: 601 (independent)

## Summary

Add a `Subscription` CRD for automatic Bundle creation from OCI registry and Git
repository watching. Removes the CI dependency for artifact discovery.

## New CRD types

`Subscription` CRD:
- `spec.type`: "image" | "git"
- `spec.image`: registry URL, tag filter regex, interval, pipeline, namespace
- `spec.git`: repo URL, branch, path glob, interval, pipeline, namespace
- `spec.pipeline`: target Pipeline name
- `spec.namespace`: target namespace for created Bundles
- `status.lastCheckedAt`: timestamp of last check
- `status.lastBundleCreated`: name of last Bundle created
- `status.phase`: Watching | Idle | Error

## Acceptance Criteria

- [ ] `Subscription` CRD type with spec.type=image|git, spec.image, spec.git, spec.pipeline
- [ ] `SubscriptionReconciler`:
  - For `type: image`: polls OCI registry for new tags matching filter regex; creates image Bundle
  - For `type: git`: polls Git repo for new commits touching watched path; creates config Bundle
  - De-duplicates: does not create a Bundle if one already exists for same digest/commit SHA
  - Writes status.lastCheckedAt and status.lastBundleCreated after each check
  - Requeues after spec.image.interval or spec.git.interval
- [ ] `source.Watcher` interface with `OCIWatcher` and `GitWatcher` stub implementations
- [ ] Unit tests: tag filter regex, deduplication, interval requeue, missing pipeline (error)
- [ ] `examples/subscription/` with image and git subscription examples
- [ ] `docs/concepts.md` updated with Subscription CRD section

## Package

`api/v1alpha1/subscription_types.go` — new CRD types
`pkg/source/watcher.go` — Watcher interface
`pkg/source/oci.go` — OCIWatcher stub (polls registry)
`pkg/source/git.go` — GitWatcher stub (polls git)
`pkg/reconciler/subscription/reconciler.go` — reconciler
`cmd/kardinal-controller/main.go` — register SubscriptionReconciler

## Architecture Note

The SubscriptionReconciler is an Owned node (Q2 pattern):
- It calls the OCI/Git APIs in the reconciler (which is allowed — Owned nodes can make
  external HTTP calls to compute a result and write it to their own CRD status)
- It only writes to `Subscription.status` — no cross-CRD mutations
- It creates Bundle CRDs (not a status mutation — creating a new object is allowed)
- `time.Now()` is used only inside the status write (compliant with Graph-first rules)

The Bundle creation is a standard Kubernetes CREATE call — not a status mutation.
This is explicitly permitted because creating an owned child resource is part of the
Owned node pattern (the reconciler owns the Bundles it creates via owner references).
