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

The label says where a namespace should go; two kinds of Lease say who has it and
whether that shard is alive:

- **The token**, Lease `kardinal-shard` in each namespace, names the shard that holds it
  (`kubectl get lease kardinal-shard -n team-payments` shows the holder,
  `kardinal-shard/<shard>`, and the annotation `kardinal.io/shard-heartbeat` names the
  holder's namespace). It is written only when the namespace changes hands, and always
  with the version read, so two shards cannot both take it. Do not delete token Leases:
  a namespace whose token is gone is taken by the shard its label names at once, without
  waiting for the old holder.
- **The heartbeat**, Lease `kardinal-shard-heartbeat-<shard>` in each shard's own
  namespace, is renewed by the shard's leader every 10 seconds. That is the only write a shard makes in
  steady state, however many namespaces it holds: with 1000 namespaces, 0.1 writes per
  second per shard, and no API reads beyond the renewal (namespaces and tokens come from
  the watch cache).

When you relabel a namespace:

1. The shard that holds it stops starting reconciles there within about 5 seconds,
   waits for the reconciles already running, however long they take (a git push or a
   PR in progress is not repeated by another controller; its heartbeat keeps the token
   valid meanwhile), and gives the token up.
2. The shard the label now names takes the token and enqueues every kardinal object
   of the namespace. In-flight promotions continue where they are: a step waiting
   for its PR keeps the same PR. Until then it retries the namespace's objects with a
   delay that grows from 5 seconds to 2 minutes.

What is guaranteed:

- A shard whose controller is down keeps its namespaces until its heartbeat has not
  changed for 60 seconds. The shard taking over measures that on its own clock, from
  when it saw the heartbeat change, as Kubernetes leader election does, so a clock that
  is off on either host does not shorten the wait. A new leader of the same shard takes
  its tokens at once.
- A shard that cannot renew its heartbeat (the API server is unreachable, or it cannot
  list namespaces and so cannot see a relabel) fences itself 44 seconds after the start
  of its last renewal, on its own ticker (every API call of the Lease loop times out after
  5 seconds, so a hung call cannot delay it): it starts no reconcile and cancels the ones
  running. Once it renews again it re-reads its tokens and gives up those another shard
  took meanwhile.
- So during an API partition the old owner stops at least 13 seconds before a new owner
  can start (60 s × 0.99 − 44 s × 1.01 − 1 s ≈ 13.96 s, allowing 1% clock rate difference
  between hosts). Two owners overlap only if a reconcile keeps writing more than 13 seconds after
  its context was cancelled. Even then the side effects repeat safely: a push to the PR
  branch is a force-push of the same change, a push to the base branch never forces, and
  `open-pr` adopts the open PR of its branch instead of opening a second one on every
  provider (GitHub, GitLab, Forgejo and Gitea, Bitbucket and Azure DevOps).
- Each shard name runs once. A second installation of a shard name, in another namespace,
  does not take a namespace the live first one holds; it emits a `ShardHomeConflict`
  Warning Event on that namespace.

A namespace labelled with a shard that no controller runs is not reconciled. The
default shard emits a `ShardNotRunning` Warning Event on such a namespace (once, a
minute after it starts), and logs it:

```bash
kubectl get events -A --field-selector reason=ShardNotRunning
```

Every replica of every shard serves the UI, the Bundle API and the SCM webhook, for
every namespace. The UI actions and the Bundle API create or edit kardinal objects,
which the namespace's shard then reconciles. The webhook is the exception that
writes reconciler state: a merged-PR event marks the matching PRStatus
`status.merged` in whichever namespace and shard it is, after confirming the merge
with the SCM credentials of the shard that received it. Configure one webhook URL
for all shards only if they share the SCM token; otherwise point each repository's
webhook at the shard whose namespaces use it. A missed or refused event costs only
latency: the owning shard's PRStatus poll records the merge.

## What sharding does not split

- **The cache**: every controller still watches the kardinal kinds in every
  namespace; memory is not divided. Only the work is.
- **kro**: one kro Graph controller reconciles the Graphs of all shards from one
  queue, so sharding kardinal does not raise the Graph throughput ceiling
  ([ledger G13](design/16-graph-capability-ledger.md#g13-the-graph-controller-cannot-be-sharded)).
  Tune kro with `hack/install-kro.sh` (`config.graphConcurrentReconciles`).

## RBAC

With `controller.namespaceShard` set the chart also grants, cluster-wide: `list` and
`watch` on Namespaces; `get`, `list`, `watch` and `update` on Leases named
`kardinal-shard` (the controller lists and watches them by that name); `get` and `list`
on Leases (the other shards' heartbeats, whose names include shard names the chart does
not know; read-only, no watch); and `create` on Leases, which RBAC cannot limit by name.
Its own heartbeat is written through the release namespace's Role. Shard names must be
lowercase DNS labels.
