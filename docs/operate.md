# Operate at scale

Sizing, concurrency and shutdown for clusters with many Pipelines and Bundles. To split the work
between several controllers, see [Sharding controllers](sharding.md).

## Scale and limits

What kardinal is tested at, and the limits that bound it. Supported: **up to 4,000
AgentIdentities per cluster**. That is about 80% of the agent index's bound, and admission
stays under 50 ms (below). The rest of this section is the measured numbers.

Measured with the agent scale scenario (`test/scenario/enterprise/agents/scale`, full size,
which the release gate runs). Every write goes through the API server's
ValidatingAdmissionPolicy plugin with the chart's policies.

- 5,000 AgentIdentities, 200 Pipelines in 50 tenant namespaces, and 98 agents shipping 3
  bursts of versions at once.
- People approve prod and review PRs.
- Each burst converges in 23 controller rounds: every version Verified in every
  environment.

| Write | Admission p50 | Admission p99 |
|---|---|---|
| A Bundle created by an agent | 42 ms | 47 ms |
| An Approval created by a person | 42 ms | 47 ms |
| A gate instance created or updated by kro | 0.05 ms | 0.2 ms |

The p99 is each write's fastest of three admissions, on an otherwise idle core. A loaded
API server or a garbage-collection pause makes one request slower; the cost of admitting it
is the same.

### The agent index

The actor policies read one object per installation, the
[agent index](actors.md#how-the-actor-is-verified), as their parameter.

- **Admission cost.** Admitting a Bundle or an Approval reads the whole index, so its cost
  grows with the number of AgentIdentities. The p99 of the whole admission was 44 ms at 4,200 on
  kube-apiserver (#1959), and 47 ms at 5,000 in the scale scenario (the table under
  [Scale and limits](#scale-and-limits)). kro's gate writes do not read it.
- **The size bound.** The index is bounded at **1 MiB** of entries. An AgentIdentity takes
  about 210 bytes: one ServiceAccount or user subject, a namespace scope and a sponsor. One
  that lists more subjects, namespaces, Pipelines or environments takes more.

| AgentIdentities | Index size | Of the 1 MiB bound |
|---|---|---|
| 1,000 | 202 KiB | 20% |
| 2,000 | 407 KiB | 40% |
| 4,000 | 815 KiB | 80% |
| 5,000 | 1,019 KiB | 99.5% |

At about **5,000** AgentIdentities of that size, the index is full.

- A full index is not Ready (`IndexFull`).
- Until AgentIdentities or subjects are removed, kardinal refuses a ServiceAccount's checked
  writes, and a person can stop (freeze, pause, hold, reject) but not go: a version is held,
  approvals don't count, and break-glass waits
  ([While the index isn't Ready](actors.md#while-the-index-isnt-ready)).
- Keep under 4,000 AgentIdentities, or fewer if yours list several subjects or scopes.
- Watch the index's Ready condition:
  `kubectl get agentindex kardinal-promoter -o jsonpath='{.status.conditions}'`.

The index reports how full it is: `status.bytes`, `status.maxBytes` and a `NearFull`
condition from 80% ([Register an agent](actors.md#register-an-agent)).

#### Why the bound stays at 4,000 for now

For each request a policy matches, the API server turns the policy's parameter into a CEL
value before any expression runs. It does that for the matchConditions too. For a custom
resource such as the agent index, it copies the whole object each time. For a built-in kind
such as a ConfigMap, it reads lazily and the cost doesn't grow with size. So admission cost
grows with the index, whatever the policy reads from it. Measured on its own at 5,000
AgentIdentities, that conversion takes about 22 to 27 ms per request: 21.7 ms and 27.1 ms in
the two benchmarks of the conversion alone on #2071. It is part of the 42 to
47 ms that the whole admission of a Bundle or an Approval takes at 5,000 (the table under
[Scale and limits](#scale-and-limits)), about half of it. The rest is the policies' own work and
the API server's. The two were measured on different hosts, so compare them only roughly.

Three ways to lift the bound were checked (#2071) and none ships in v0.13:

- **An index per namespace.** Absence from the index means "not an agent". An agent writing
  outside its own namespace would be absent from that namespace's index and pass as a person,
  so every index would still have to list every agent.
- **Shards routed by a match condition.** Each shard's policy converts its own shard before
  its match condition runs. A request still pays for the whole index, split N ways.
- **ConfigMap shards.** These are cheap per request, about 1 µs at 5,000 entries. But a
  ConfigMap parameter makes the API server watch every ConfigMap in the cluster, not only
  kardinal's. With 20,000 ConfigMaps of 1 KiB, kube-apiserver's memory grew by 48 MiB or more.
  On a cluster with larger ConfigMaps it would grow further, and kardinal can't bound that.

A lazy conversion for custom-resource parameters in the API server would let a sharded index
pay only for the shard a request reads. Until then, plan for up to 4,000 AgentIdentities
per cluster.

## Sizing kro

kro keeps every Graph it reconciles in memory: its compiled program and its watches, 3 to 6 MB
for a kardinal Graph whatever its size (about 5 MB per Graph under load). kardinal creates one Graph per
Bundle and retires it once the Bundle has finished: the Graph is deleted, and the Bundle keeps a
record of each PromotionStep in `status.retiredSteps`, which rollback, promote, history, metrics,
the CLI and the UI read. So the Graphs kro holds are:

| Graphs | How many |
|---|---|
| Bundles promoting | about one per Pipeline and Bundle type (a newer Bundle supersedes an older one) |
| Superseded and Rejected Bundles, and Verified or Failed ones a newer Verified Bundle replaced in every environment they touched | those whose steps settled in the last `graph.retire.superseded` (1m), plus the ones the controller has not reached yet |
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
Graphs; see [Bundle history](architecture.md#graph-retirement) for what a retired Bundle keeps.

!!! warning "Version compatibility"
    The kro version kardinal-promoter is tested against is pinned in `hack/install-kro.sh`.

---

## Sizing the controller

The controller keeps an informer cache of every Pipeline, Bundle, PromotionStep, PRStatus and
PolicyGate it watches, so its memory grows with the number of Pipelines times the Bundles each
keeps (`historyLimit`), more than with the promotion rate. Measured with the scale suite (`full`
profile, controller built without `-race`, 2 replicas; peak resident memory of the leader):

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

#### Memory profiles

To see what holds the controller's memory, turn on Go's pprof profiles with
`--pprof-address` (chart value `pprofAddress`). They are off by default. A port alone binds to
127.0.0.1 inside the Pod, so only a port-forward reaches it, and the chart adds no Service or
container port for it. The profiles show heap contents (object values, possibly tokens) and
stacks, so do not bind it to every interface (`0.0.0.0:6060`) where untrusted clients can connect.
The endpoint has no authentication or authorization: anyone who reaches the address can read
every profile. `/debug/pprof/cmdline` also returns the process's command line, so pass secrets
in environment variables, as the chart does for the SCM, UI and Bundle API tokens, never in
flags (`controller.extraArgs`). Only an empty `pprofAddress` turns it off.

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.13.0 --reset-then-reuse-values --set pprofAddress=:6060
kubectl -n kardinal-system port-forward deploy/kardinal-promoter 6060:6060
go tool pprof -sample_index=inuse_space http://localhost:6060/debug/pprof/heap
# Compare two snapshots: what grew between them.
curl -s localhost:6060/debug/pprof/heap > before.pb.gz   # ... later:
curl -s localhost:6060/debug/pprof/heap > after.pb.gz
go tool pprof -top -base before.pb.gz after.pb.gz
```

`kubectl port-forward deploy/...` picks one Pod; with two replicas, forward the leader's Pod
(the holder of the `kardinal-promoter-leader` Lease), which is the one reconciling.

## Controller concurrency

Each controller reconciles several objects at once. A PromotionStep holds its worker through
every git and SCM round trip, so the controllers run several workers. The defaults were sized
with the scale suite's `full` profile (`TestScale_LoadPipelines`: 200 Pipelines
x 3 automatic environments, one Bundle each, started together; `TestScale_LoadBurst`: 1,000
Bundles over 100 Pipelines; controller built with `-race`, 4 CPU, 2 replicas):

| Controller | 200 Pipelines: step p50 / p99 | Bundle end to end p50 / p99 | All settled | 1,000 Bundles: Bundle end to end p50 / p99 |
|---|---|---|---|---|
| defaults | 3 s / 6 s | 52 s / 82 s | 87 s | 16 s / 26 s |

With the defaults the `full` profile meets the latency objective of `TestScale_LatencySLO`
(test/e2e/README.md, Latency SLO): automatic steps p50 2 s and p99 5 s, Bundles p99 92 s,
against 10 s, 30 s and 2 minutes.

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

### Fair sharing between namespaces

The PromotionStep, PRStatus, PolicyGate, Bundle, Pipeline and MetricCheck work queues share
their workers between namespaces. An item's place in the queue is lowered, within its
priority, by how many items its namespace already has ready or in process (in a few steps: none,
1 to 3, 4 to 15, 16 to 63, 64 to 255, 256 or more), so an item of a namespace with little work
goes ahead of another namespace's large backlog. So one namespace's large promotion does not
make another team's steps wait for all of it.

- Only items that are due count: an item waiting for a requeue delay or a retry backoff counts
  once its time has come, and its place is computed again then. A namespace with many idle or
  failing objects is not pushed down.
- Aging: an item that has been due for more than 10 seconds goes back to its normal place, so a
  busy namespace still gets its share of the workers while many small namespaces keep the
  queue busy.
- Within one namespace and step, items are served in the order they became due.
- Priority levels stay apart: a step waiting for its branch's turn (see above) stays behind
  every step that can run.
- No worker waits while an item is ready, so a namespace alone on the controller gets every
  worker. The fairness reorders work; it does not add any.

### Kubernetes Events

The controller writes `events.k8s.io` Events for its transitions (`kubectl events`). client-go
starts a goroutine and an API write for every Event, so the controller rate-limits Events:

| Type | Bucket | Rate | Burst |
|---|---|---|---|
| Normal | one for the controller | `--event-qps` (20 a second) | `--event-burst` (100) |
| Warning | one per namespace (the 1,024 most recently used) | `--event-warning-qps` (5 a second) | `--event-warning-burst` (50) |

So neither a flood of Normal transitions nor one tenant's burst of Warnings drops another
namespace's Warning. The shard gate's Warnings regard Namespaces, which are cluster-scoped, so
they share one cluster-wide Warning bucket (and are otherwise log only: see the table below).
Every Event pays a token, repeats included. An Event over its bucket is dropped, never queued, so
a reconcile never waits on an Event, and `kardinal_events_dropped_total{recorder,type}` counts it. Set the flags with `controller.extraArgs`;
a rate of 0 removes that type's limit, and the controller refuses to start with a negative or
non-finite rate or a burst below 1.

**Events are best effort.** Alert on the durable record of each Warning, not on the Event:

| Warning Event | On | Durable record |
|---|---|---|
| `Rejected` | Bundle | `status.phase: Rejected`, `kardinal_bundles_total{phase="Rejected"}` |
| `Failed` | Bundle | `status.phase: Failed` and the `Ready` condition's reason, `kardinal_bundles_total{phase="Failed"}` |
| `PipelineNotFound`, `TranslationError` | Bundle | `Ready=False` condition with that reason |
| `GraphDeleted`, `GraphSyncFailed` | Bundle | `GraphSynced=False` condition |
| `InvalidRetireDelay` | Bundle | the `GraphRetired` condition's message names the ignored annotation |
| step `Failed`, `AbortedByAlarm`, `RollingBack` | PromotionStep | `status.state`, and the `PromotionFailed` (or `RollbackStarted`) AuditEvent |
| `GitCredentialMissing`, `SCMUnavailable` | PromotionStep | the condition of that name |
| `GatesStatusFailed` | PromotionStep | `status.outputs.gatesStatus` (`error:…` with the retry time) |
| `GatesStatusNoCommit` | PromotionStep | no `status.outputs.prHeadSHA`; the PR has no `kardinal/gates` status |
| `MergedWhileBlocked` | PromotionStep | `status.outputs.mergedWhileBlocked` |
| `PRBranchForeignCommit` | PromotionStep | `status.outputs.prForeignHead` and the step's message, which name the foreign head; once the step's retries are used up, `status.state: Failed` and the `PromotionFailed` AuditEvent ([PR evidence](pr-evidence.md)) |
| `GitOpsRefreshSkipped`, `GitOpsRefreshFailed` | PromotionStep | `status.refreshMessage`; the step then waits for the GitOps tool's own poll ([Sync on promotion](health-adapters.md#sync-on-promotion)) |
| `PRLeftOpen`, `ClosePRFailed` | PromotionStep (deleted) | `kardinal_pr_cleanup_failures_total{reason}` and the controller's error log: the step is gone, so close the PR by hand ([troubleshooting](troubleshooting.md)) |
| `Blocked` | PolicyGate | `Ready=False` condition (reason `Blocked`) and the `GateEvaluated` AuditEvent |
| `GateExempted` | PolicyGate | `status.reason` (`EXEMPT: …`) and the `GateEvaluated` AuditEvent |
| `HoldBundleMissing` | Pipeline | the `HoldBundleMissing` AuditEvent and `kardinal_hold_bundle_missing_total` |
| `PolicyNamespaceNotShared` | Pipeline | the `PolicyNamespacesShared=False` condition, which names the namespaces that have not agreed ([Sharing gates with other namespaces](policy-gates.md#sharing-gates-with-other-namespaces)) |
| `RollbackRefused` | RollbackPolicy | the `RollbackRefused` condition |
| `InvalidSpec` | ChangeWindow | `Valid=False` condition |
| `FreezeAuditFailed` | ChangeWindow | for a freeze's start, the `FreezeAudited` condition, missing until the `FreezeStarted` AuditEvent is written; for a lift whose `FreezeLifted` record still fails after 2 minutes, the controller's warning log only ([Freezes](agentic/freezes.md#who-froze-and-who-lifted-it)) |
| `StatusWriteRefused` | PromotionReceipt | the receipt itself: a refused signing write leaves it unsigned (`status.bundle` empty, phase not yet `Signed`), and a refused PR-comment write leaves `status.prComment` unset. `kardinal_receipt_status_writes_refused_total{step}` counts the refusals until the controller restarts, which is also how the receipt is tried again once you fix the cause ([Receipts](receipts.md)) |
| `NotificationDropped`, `NotificationTemplateFailed` | NotificationHook | `status.failureMessage` and `kardinal_notifications_dropped_total{reason}` |
| `ShardNotRunning`, `ShardHomeConflict` | Namespace | the controller's warning log only |
| `ControllerSecretReferenceable` | Secret (the controller's SCM token) | the `kardinal.io/referenceable=true` label on that Secret, and the controller's warning log |

### Leader election under API pressure

The leader renews its Lease every 2 seconds and gives up leadership when a
renewal has not succeeded within 10 seconds. The controller's reconcilers
can send many requests at once (see the worker table above), and a cluster
can throttle a ServiceAccount through API Priority and Fairness. Either way,
renewals could wait behind reconcile traffic and the leader would exit
("leader election lost"). Two things keep renewals separate:

- the chart's FlowSchema (`leaderElectionFlowSchema`) puts the
  controller ServiceAccount's Lease requests in its namespace into the
  built-in `leader-election` priority level, as Kubernetes does for its own
  controllers. Keep its `matchingPrecedence` below any FlowSchema that
  matches the controller's other requests;
- the controller also renews with a rate limiter of its own, so in-process
  reconcile traffic cannot take its tokens (controller-runtime appends
  `/leader-election` to the controller's user agent for these requests).

The FlowSchema is cluster-scoped: whoever installs the chart (Helm, or a
GitOps controller such as Argo CD or Flux) needs `create`, `get`, `update`
and `delete` on `flowschemas.flowcontrol.apiserver.k8s.io`. It needs the
`flowcontrol.apiserver.k8s.io/v1` API, which every Kubernetes kardinal
supports has; the chart skips it when the API is missing. Where the
installer may not manage cluster-scoped resources, set
`leaderElectionFlowSchema.enabled=false` and, if the cluster throttles the
controller, ask the cluster admin to create an equivalent FlowSchema.

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

## Tuning checklist

### 50+ environments / 100+ concurrent Bundles

The controller handles each Bundle independently via a dedicated Graph. For very large deployments, consider:

**1. Raise the controller's resource limits.** Extra replicas do not add throughput: the
controller runs with `--leader-elect`, so only one replica reconciles and the others are
standbys (`replicaCount`, default 1).
```yaml
# values.yaml
resources:
  limits:
    cpu: "2"
    memory: 2Gi
  requests:
    cpu: 500m
    memory: 512Mi
```

**2. Reconcile concurrency** is set per controller with `controller.workers.*` (see
[Controller concurrency](#controller-concurrency)); MetricCheck runs `--metriccheck-global-slots` plus 4 workers, at least 16.

**3. Reduce ScheduleClock tick frequency** if no gate needs minute-level `schedule.*`
re-evaluation. The chart owns the `kardinal-clock` ScheduleClock, so set it through Helm:
```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.13.0 \
  -n kardinal-system --reset-then-reuse-values --set scheduleClock.interval=5m
```

**4. Bundle history** — finished Bundles are garbage-collected per Pipeline. Set
`spec.historyLimit` on the Pipeline to keep fewer (see
[Pipeline reference](reference/pipeline.md#spechistorylimit)).

**5. Monitor controller performance:**
```bash
# Check reconcile queue depth (via Prometheus if PrometheusRule is installed)
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8080:8080
curl -s http://localhost:8080/metrics | grep "^workqueue_depth"

# Or use the built-in Prometheus alerts (install with --set prometheusRule.enabled=true)
kubectl get prometheusrule kardinal-promoter -n kardinal-system -o yaml
```
