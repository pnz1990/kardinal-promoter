# Sharding controllers by namespace

One kardinal controller (with its leader-elected replicas) reconciles every
namespace by default. A large cluster can split the work across several
controller installations, one per **shard**, by labelling namespaces:

```bash
kubectl label namespace team-payments kardinal.io/shard=payments
```

Each installation runs with `controller.namespaceShard` (the
`--namespace-shard` flag):

| Shard | Reconciles |
|---|---|
| `default` | namespaces without `kardinal.io/shard` (or labelled `default`), and the cluster-scoped kinds (ChangeWindow) and cluster-wide sweeps |
| any other name, for example `payments` | namespaces labelled `kardinal.io/shard=payments` |
| unset (the default) | everything: sharding is off |

Every controller of a sharded cluster needs a shard name, and exactly one of them is
`default`. A namespace labelled with a shard that no controller runs is not reconciled.

```bash
# The default shard, in kardinal-system (unlabelled)
helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  -n kardinal-system --set controller.namespaceShard=default ...

# A second shard in its own namespace, labelled with its shard
kubectl create namespace kardinal-payments
kubectl label namespace kardinal-payments kardinal.io/shard=payments
helm upgrade --install kardinal-payments oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  -n kardinal-payments --set controller.namespaceShard=payments --skip-crds ...
```

Install each shard in its own namespace and label that namespace with the shard, so the
installation's ScheduleClock is reconciled by its own controller. Each release has its own
ClusterRoles (named after the release). `--namespace-shard` cannot be combined with
`controller.watchNamespace`.

## How a namespace changes hands

The label says where a namespace should go; a Lease says who has it. A shard
reconciles a namespace only while its leader holds the Lease `kardinal-shard` in
that namespace (`kubectl get lease kardinal-shard -n team-payments` shows the
holder, `kardinal-shard/<shard>`).

When you relabel a namespace:

1. The shard that holds it stops starting reconciles there within about 5 seconds,
   waits for the reconciles already running (a git push or a PR in progress is not
   repeated by another controller), and releases the Lease.
2. The shard the label now names takes the Lease and enqueues every kardinal object
   of the namespace. In-flight promotions continue where they are: a step waiting
   for its PR keeps the same PR.

There is never a moment with two owners. A shard whose controller is down keeps its
namespaces until their Leases expire (60 seconds without renewal); then the shard the
label names takes them. A new leader of the same shard takes its Leases at once.

Every replica of every shard serves the UI, the Bundle API and the webhooks: a
webhook only writes an object (a PRStatus, a Subscription annotation), and the
shard that owns its namespace reconciles it.

## What sharding does not split

- **The cache**: every controller still watches the kardinal kinds in every
  namespace; memory is not divided. Only the work is.
- **kro**: one kro Graph controller reconciles the Graphs of all shards from one
  queue, so sharding kardinal does not raise the Graph throughput ceiling
  ([ledger G13](design/16-graph-capability-ledger.md#g13-the-graph-controller-cannot-be-sharded)).
  Tune kro with `hack/install-kro.sh` (`config.graphConcurrentReconciles`).

## RBAC

With `controller.namespaceShard` set the chart also grants `list` and `watch` on
Namespaces and `get`, `list`, `watch`, `create` and `update` on Leases cluster-wide.
