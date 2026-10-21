# Design: an agent index whose admission cost does not grow with every agent (#2071)

> Status: **Decided (2026-10-20)**: v0.14.0 ships design C, compact entries in the one index
> (#2071). The per-namespace index, PodTemplate params and a ConfigMap opt-in are rejected
> (§6). The upstream issue is on the owner's list (Appendix A).
> Implemented for v0.14.0 (#2071): the format, its readers and its upgrade path are in
> [The index format](../actors.md#the-index-format), the measured numbers in
> [Scale and limits](../operate.md#scale-and-limits).
> Created: 2026-10-20
> Depends on: [17-actors.md](17-actors.md) §5 (the actor policy and the AgentIndex) and
> [18-actor-policy.md](18-actor-policy.md).
> Verified against: this repository at `main` (`0601198f1`), and `k8s.io/apiserver` v0.37.1
> (the version in `go.mod`). Source references are `path:line` in those trees. The
> measurements are from #2071's comments of 2026-10-17 and 2026-10-19, and ENGINEER-5's of
> 2026-10-20.

---

## 1. Problem

Every write the actor policies check pays for the whole agent index:

- an agent's Bundle create;
- a person's Approval create;
- a gate override;
- a freeze exemption.

The API server converts the policy's param, the cluster-scoped AgentIndex `<release>`, into a
CEL value on every request. It converts all of it, whatever the policy then reads.

- **The cost per request.** A write's admission takes a p50 of **47.7 ms** at 2,000
  AgentIdentities (a 407 KiB index). It takes about 0.35 ms against an empty index
  (`TestEnterprise_AgentDifferential`, CI's full-size job, #2071). The conversion alone takes
  27.1 ms at 5,000 identities.
- **The bound.** The index is one object, bounded at 1 MiB of entries (`pkg/actor/registry.go:19`)
  and about 209 bytes per AgentIdentity. It fills at about 5,000, and then every checked write
  is refused (IndexFull).

v0.12 documents the tested limit, 4,000 AgentIdentities ("Scale and limits", `docs/operate.md`).
The goal for v0.14 is the one the coordinator set: **the per-request cost of the actor
policies should not grow with every agent in the cluster**, and the bound should stop being
one object for the whole cluster.

The coordinator asked for a per-namespace agent index. Section 3 designs one and shows what it
would take to keep it correct. It doesn't meet the goal. Section 4 compares the designs that
do, and section 5 recommends one.

## 2. How admission reads the index today

### 2.1 The policies

The chart's actor policies (`chart/kardinal-promoter/templates/actor-admission.yaml`) are
`<release>-actor`, `-actor-records` and `-actor-overrides`.

- **Param.** Each has `paramKind: AgentIndex` (cluster-scoped), and a binding with
  `paramRef.name: <release>` (`:183`, `:256`, `:327`).
- **Matching** (`:29-35`). The policy finds the requester's index entries by username,
  `request.userInfo.username in params.status.users`, and then by group,
  `variables.groups.filter(g, g in params.status.groups)`.
- **Classifying.** `variables.kind` (`:39-42`) is `agent` when an entry matched. Otherwise a
  `system:serviceaccount:` principal is a `bot`, and any other principal is a `human`.
- **Checks.** Readiness is `params.status.conditions` Ready (`:96-99`). An agent must stamp,
  must stay in the namespaces, Pipelines and environments of its entry, and may not let a
  pipeline through a freeze (`:100-156`).

**The invariant (I1).** "Not an agent" is decided from **absence**. If a principal registered as
an agent is missing from the param a request reads:

- a User or Group subject is read as a **human**, and may stamp itself human. That is identity
  forgery.
- a ServiceAccount is read as a **bot**. Its write is admitted with no scope check at all
  (`:144`: the scope check runs only for an agent). That is a scope bypass.

**The second invariant (I2).** The API server refuses every write a policy matches while its
param is missing ("no params found"), and it does so before any expression runs. So neither
the controller's exemption nor the `check` variable can help then: kro's gate instances and the
controller's own writes are refused too (#1929; the chart comment at `:365-376`). That's why
the chart creates the index itself, and why `<release>-agent-index` refuses to delete it.

### 2.2 Why the cost is the whole index

- `validating/validator.go:123` builds `cel.OptionalVariableBindings{VersionedParams: ...}` for
  every request. `cel/activation.go:76-77` then sets
  `paramsVal = common.SchemalessTypedToVal(inputs.VersionedParams)`.
- **Eager for CRD params.** `SchemalessTypedToVal` (`pkg/cel/common/valuesschemalesstyped.go`)
  converts eagerly when the value can convert itself to unstructured. A CRD param is served as
  `*unstructured.Unstructured`, so `cacheEntry.CanConvertToUnstructured()` is true and
  `ToUnstructured` walks the whole object (`:89-103`).
- **Lazy for built-in params.** A typed built-in object takes the reflective path below
  `:104`, which is lazy: only the fields the expression reads are converted.
- **matchConditions convert too.** They build the same activation with the param
  (`validating/validator.go:103`). So a policy that routes requests with matchConditions still
  pays the conversion of its param before it can say "not mine". This is the flaw that ruled
  out #2071's design B (comment of 2026-10-17).

Measured per call at 5,000 identities (#2071, 2026-10-17):

| Param | Conversion cost |
|---|---|
| AgentIndex, an object per identity (today) | 27.1 ms, 10.5 MB, 190k allocs |
| AgentIndex, one compact string per identity | 9.6 ms, 5.9 MB, 25k allocs |
| ConfigMap, conversion plus `u in params.data` and `params.data[u]` | **1.1 µs**, 1 KB, 10 allocs |

At full enterprise size the cost grows a little faster than the index, as size^1.14 (#2071,
2026-10-20).

### 2.3 How the API server finds a param

`generic/policy_dispatcher.go:296-310` (`CollectParams`) picks the namespace:

- **Namespaced `paramKind`, `paramRef.namespace` unset:** the param is looked up in **the
  request's namespace**.
- **Cluster-scoped request:** the binding fails with "cannot use namespaced paramRef in policy
  binding that matches cluster-scoped resources" (`:303-306`).
- **Built-in `paramKind`:** the param comes from the admission plugins' shared informer
  (`generic/policy_source.go:426`, `informerFactory.ForResource`). That factory is
  `clientgoinformers.NewSharedInformerFactory(client, 10*time.Minute)`, with no namespace
  option (`pkg/server/options/coreapi.go:79`). So its informer watches **every object of that
  kind in the cluster**.

  Measured on kind 1.37 with 20,063 ConfigMaps of 1 KiB unrelated to kardinal: the API server's
  RSS rose by **at least 48 MiB** when the first such policy was created, and was still rising
  (#2071, 2026-10-19).

## 3. A per-namespace AgentIndex

### 3.1 The design

- **A new namespaced kind, `NamespaceAgentIndex`.** The controller writes one per namespace.
  Its entries are the agents whose `spec.scope.namespaces` match that namespace: an exact name,
  a `prefix*`, or `*`.
- **Policy bindings.** The policies change `paramKind` to `NamespaceAgentIndex`, and their
  bindings drop `paramRef.name` and `namespace`, so each request reads the index of its own
  namespace (§2.3).
- **Cluster-scoped kinds.** The policy also checks ChangeWindows (freeze exemptions), which are
  cluster-scoped. So the cluster-scoped request path keeps a separate policy, bound to the
  cluster-wide AgentIndex as today.

### 3.2 What it needs to stay correct

1. **Every agent principal in every namespace (I1).**
   - An agent writing outside its scope must still be classified as an agent, so its scope check
     refuses it.
   - So every namespace's index must hold every agent principal and agent group of the cluster.
     Agents scoped there get their full entry; the others get at least their name and agent,
     with no namespaces in scope.
   - So the index of every namespace grows with every agent in the cluster: the per-request
     cost and the bound shrink only by the detail left out.
2. **An index in every namespace before anything is written there (I2).**
   - A namespace without its index refuses every write the policies match, kro's gate instances
     and the controller's own included, until the controller has written it.
   - `parameterNotFoundAction: Allow` would fail open instead: an agent's first write in a new
     namespace is unchecked.
   - Either way, every namespace creation opens a window: refused, or open.
3. **The controller watches Namespaces.**
   - It needs `list` and `watch` on Namespaces cluster-wide. That's granted today only with
     `controller.namespaceShard` (`_rbac.tpl`, the shard block); otherwise the controller only
     `get`s them.
   - It needs `create`/`update` on `NamespaceAgentIndex` in every namespace, and a delete guard
     like `<release>-agent-index`'s, per namespace.
4. **Write amplification.** An agent scoped `*` is in every namespace's index. Changing,
   suspending or adding it rewrites one object per namespace: 2,000 writes on a cluster of
   2,000 namespaces.

### 3.3 What it buys

Take a cluster of 5,000 agents, each scoped to its team's namespace (about 100 per namespace).
Each namespace's index then holds about:

- 100 full entries (about 0.5 ms to convert);
- 4,900 names (at most 9.6 ms, the compact-string figure in §2.2).

That's about 10 ms, against 27 ms today. The bound moves from about 5,000 identities to the
number of compact names that fit 1 MiB, which is unmeasured and estimated at 2 to 3×. **That's
the same order as compacting the single cluster-wide index (§4, C)**, which needs none of
§3.2's machinery.

### 3.4 Verdict

The per-namespace index does **not** meet the goal. Because of I1, its cost still grows with
every agent in the cluster, and because of I2, it brings back #1929's refused or open window
for every new namespace. Its added machinery is:

- a new kind;
- namespace-wide RBAC;
- one index object per namespace;
- write amplification;
- a second, cluster-scoped policy path.

That's for a gain (about 3×) that design C gets alone. I recommend not building it.

## 4. The designs that bound the cost

| | Per-request cost at 5k | Grows with agents? | Bound | API server memory | New moving parts |
|---|---|---|---|---|---|
| Today | 27 ms | yes, about linearly | 1 object, about 5k | none | none |
| **C** compact entries in the one AgentIndex | 9.6 ms | yes, linear, about 2.8× less | 1 object, about 1.5 to 2× more (estimate) | none | the entry encoding, parsed in CEL |
| **A** per-namespace index (§3) | about 10 ms | yes (I1) | 1 object per namespace, but every name in each | none | §3.2's four |
| **B-cm** ConfigMap shards, routed (#2071) | N × about 2 µs | **no** | N × 1 MiB | a watch on **every ConfigMap** in the cluster: at least 48 MiB at 20k | shards, routing (`perf/agent-index-shards`), a write guard on the ConfigMaps |
| **D** PodTemplate shards, routed | N × about 2 µs (expected, the same lazy path) | **no** | N × 256 KiB of annotations | a watch on every PodTemplate in the cluster, usually none (unmeasured) | as B-cm, on an unusual kind |
| **U** upstream: lazy conversion of CRD params | the AgentIndex lookups only | **no** | unchanged | none | a Kubernetes change; kardinal needs no code |

Notes on each design:

- **C.** Each entry becomes one string, for example
  `agent|sponsor|autonomy|ns1,ns2|pipelines|environments|blocked`. The policy reads it with
  `split` (Kubernetes CEL's strings library), so the entry is still one map value per principal,
  and the conversion is a map of strings rather than a map of objects. This is the cheapest
  change, and it keeps every invariant as it is. **It does not meet the goal**: it's a constant
  factor.
- **B-cm.**
  - **Mechanism.** The params are ConfigMaps `<release>-agents-<i>` in the release namespace,
    selected by `paramRef.selector`. The policy runs once per ConfigMap, and each run first
    checks that the requester routes to that shard; any other run admits.
  - **Invariants.** I1 holds within the principal's own shard. I2 holds because the ConfigMaps
    always exist: the chart creates them, and `parameterNotFoundAction: Deny`.
  - **Routing.** It is written and pinned in Go and CEL on `perf/agent-index-shards` (90b1a9c8f).
  - **Why it was deferred.** It was measured and deferred under the coordinator's rule, because
    the ConfigMap informer is cluster-wide and its cost grows with ConfigMaps kardinal doesn't own.
- **D** is B-cm on a built-in kind that clusters rarely use.
  - `PodTemplate` is core/v1 and namespaced, so it's served typed and converted lazily (§2.2).
    Its informer is cluster-wide too, but clusters seldom hold any PodTemplates.
  - **The costs:**
    - annotations are bounded at 256 KiB per object (`TotalAnnotationSizeLimitB`,
      `k8s.io/apimachinery/pkg/api/validation/objectmeta.go:39`), so it takes about 5 shards per
      6,000 identities;
    - a workload kind used as a data store surprises admins, GitOps diffs and policy engines that
      inspect PodTemplates;
    - the chart needs a VAP that lets only the controller write those PodTemplates, as it does
      for the AgentIndex today.
  - **Unverified:** its per-request cost and the API server's memory.
- **U.** The fix is in `SchemalessTypedToVal`: convert a CRD param lazily, or cache its
  conversion per resourceVersion. That would make today's design cheap with no change to
  kardinal. A draft issue exists in the parity notes and hasn't been posted (external posts are
  the owner's). Kardinal supports Kubernetes from 1.35, so a fix landing in, say, 1.39 helps only
  clusters on that version or later.

## 5. Recommendation

> As proposed for review. §6 records the decisions: items 1, 2 and 4 stand; item 3 (D) was
> rejected without measuring.

1. **Don't build the per-namespace index** (§3.4).
2. **v0.14, first step: C**, the compact encoding in the one AgentIndex.
   - It's about 2.8× cheaper per request, and has more capacity per byte.
   - It needs no new kind, RBAC or object, and keeps I1 and I2 as they are.
   - The admission-path change: the policies' `matches` read `split(params.status.users[u], '|')`
     instead of an object's fields, and `pkg/actor` writes the encoding and keeps its Go twin of
     the policy (`Derive` and `Scope`) pinned to it by the existing transliteration tests.
3. **Measure D before choosing it.** On kind 1.37, measure:
   - the API server's RSS with D's policy on a cluster that has no PodTemplates, and on one with
     1,000;
   - the per-request cost at 5,000 and 20,000 identities;
   - an `-race` CI run of the admission tests.

   If its cost stays in microseconds and the memory delta is under 10 MiB, build D behind a chart
   value. The AgentIndex stays as the status and visibility object, and the routing comes from
   `perf/agent-index-shards`. Otherwise stop at C.
4. **Ask the owner to post U upstream.** It's the only design that removes the cost with no new
   kardinal parts, but its timeline isn't kardinal's.

## 6. Decisions

The coordinator decided on 2026-10-20:

1. **The per-namespace index is rejected**, for §3.4's reasons.
2. **v0.14 ships C**, compact entries in the single AgentIndex (about 2.8× cheaper). That is the
   plan of record, tracked in #2071.
3. **PodTemplates as the index (D) are rejected, and not to be measured.** Using a built-in kind
   for unrelated data confuses operators, policy engines (Kyverno and Gatekeeper act on
   PodTemplates), security scanners and RBAC reviews: an enterprise-hostile trick, however cheap.
4. **A ConfigMap opt-in (B-cm) is rejected.** Two index paths double the admission surface, for a
   gain only clusters with few ConfigMaps see.
5. **The upstream issue (U) goes on the owner's list**, as posting externally is theirs. The draft
   is Appendix A.

Still to measure when C is built: its bound for one cluster, which depends on the encoding's
average length, at 5,000 and 10,000 identities with the enterprise scenario's identities.

## Appendix A. Draft upstream issue (kubernetes/kubernetes), not posted

For the owner to post. It is the draft from #2071's v0.13 work, unchanged.

> **ValidatingAdmissionPolicy: custom-resource params are converted eagerly on every request, so
> admission cost grows with the param's size**
>
> **What happens.** For every request a policy matches, the param is turned into a CEL value
> with `common.SchemalessTypedToVal(inputs.VersionedParams)`
> (`staging/src/k8s.io/apiserver/pkg/admission/plugin/cel/activation.go`, `newActivation`).
> This happens before any expression runs, and also for the policy's matchConditions
> (`validating/validator.go`, `celMatcher.Match`, then `condition.ForInput`, then
> `newActivation`).
>
> - For a built-in param kind, the object comes from the typed informer and is read lazily
>   (`reflectSchemalessTypedStruct` and `Map`). The cost doesn't depend on its size.
> - A custom-resource param is an `*unstructured.Unstructured`. Its type entry
>   `CanConvertToUnstructured()`, so `cacheEntry.ToUnstructured(v)` runs, then `NativeToValue`
>   over the whole map. The cost is linear in the object.
>
> **Measured** (`SchemalessTypedToVal` alone, per call):
>
> - an Unstructured param with 5,000 map entries of five fields each: about 22 ms, 10 MB and
>   155k allocations;
> - a ConfigMap with 5,000 data entries, including one lookup: about 1.1 µs.
>
> **Why it matters.**
>
> - Routing a request to one of N shard params with matchConditions doesn't help, because each
>   shard policy pays its own conversion before its matchCondition rejects. A paramRef selector
>   matching N params doesn't help either.
> - The workaround, a built-in param kind, has its own cost: a ConfigMap param starts a
>   cluster-wide ConfigMap informer in kube-apiserver (`options/coreapi.go`
>   `NewSharedInformerFactory`, `policy_source.go` `ForResource`). With 20k ConfigMaps of 1 KiB,
>   kube-apiserver RSS grew by 48 MiB or more.
>
> **Ask.** Wrap `Unstructured` content lazily, as typed objects already are: a CEL map over
> `map[string]interface{}` that converts values on access. Or skip param conversion for a
> matchCondition that does not reference `params`.
