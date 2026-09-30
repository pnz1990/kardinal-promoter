# 02: Pipeline-to-Graph Translator

> Status: Historical (checked against the code on 2026-09-29). Several parts differ from the
> code; `pkg/translator/translator.go` and `pkg/graph/builder.go` are the reference. Known
> differences:
> - The package holds `doc.go` and `translator.go`; the Graph is built in `pkg/graph`.
> - `propagateWhen` and `now()` do not exist in kro. Dependents wait through `resolvableWhen`
>   guards in their templates (ledger G1).
> - PromotionStep carries `spec.upstreamStates` and `spec.requiredGates`, not
>   `upstreamVerified`/`upstreamEnvironment`.
> - Skip permission is the PolicyGate `spec.skipPermission` bool, checked in Go
>   (`pkg/graph/skip.go`); a denied skip fails the Bundle. There is no `SkipDenied` node.
> - A Pipeline change mid-flight updates the Graph in place; it is not immutable (ledger G6).
> - Per-region steps use the `region` variable, not `${item}`.
> - [design-v2.1](design-v2.1.md), cited below, is itself superseded.
> Depends on: 01-graph-integration
> Blocks: 03-promotionstep-reconciler, 04-policygate-reconciler

## Purpose

The Pipeline-to-Graph translator is the core logic that reads a Pipeline CRD, a Bundle CRD, and PolicyGate CRDs from the cluster, and produces a Graph spec that the Graph controller will execute. This is the bridge between the user-facing Pipeline abstraction and the underlying Graph execution engine.

## Input and Output

**Input:**
- `Pipeline` CRD (the user-authored promotion topology)
- `Bundle` CRD (the artifact to promote, with intent)
- `PolicyGate` CRDs (from `--policy-namespaces` + Pipeline namespace)

**Output:**
- A `Graph` CRD spec (`kro.run/v1alpha1/Graph`) with:
  - One node per environment (PromotionStep template)
  - One node per matching PolicyGate per gated environment (PolicyGate instance template)
  - Correct CEL reference edges between nodes
  - `readyWhen` expressions on each node
  - Nodes excluded based on `intent.targetEnvironment` and `intent.skipEnvironments`

## Go Package Structure

```
pkg/
  translator/
    translator.go       # Main translation function
    environment.go      # Environment ordering and dependsOn resolution
    gates.go            # PolicyGate collection and matching
    skip.go             # Skip-permission validation
    graph.go            # Graph spec assembly
    translator_test.go  # Unit tests
```

## Translation Algorithm

### Step 1: Resolve environment ordering

Read `spec.environments` from the Pipeline CRD. Build a dependency graph:

- If `dependsOn` is specified on an environment, use it.
- If `dependsOn` is omitted, the environment depends on the previous one in the list.
- The first environment has no dependencies.

Validate: no circular dependencies. No references to non-existent environment names.

Output: a map of `environmentName -> []dependsOnNames`.

### Step 2: Filter environments by Bundle intent

Read `spec.intent` from the Bundle CRD.

- `intent.targetEnvironment`: only include environments up to and including the target in the dependency graph. Walk the graph from the first environment to the target, including all environments on any path.
- `intent.skipEnvironments`: remove skipped environments from the graph. Before removing, validate skip permissions (see Step 3).
- Default (no intent): include all environments.

Output: a filtered list of environment names to include in the Graph.

### Step 3: Validate skip permissions

Implemented in `pkg/graph/skip.go` (`ValidateSkipPermissions`, `skipPermissionGates`).

For each environment in `intent.skipEnvironments`:

1. Collect the org gates that apply to it through `kardinal.io/applies-to`. An org gate is a gate in an org policy namespace (`--policy-namespaces`) or one labelled `kardinal.io/scope: org`.
2. If any org gate applies, the skip needs a permission: a PolicyGate labelled `kardinal.io/type: skip-permission` and `kardinal.io/applies-to: <env>`, with `spec.skipPermission: true`, **in an org policy namespace**. A gate in the Pipeline's namespace or in `spec.policyNamespaces` never grants a skip, even when labelled `scope: org`.
3. If no permission exists, Build returns `skip denied for environment "<env>": ...` (wrapping `graph.ErrInvalid`). No Graph is created and the Bundle goes to phase `Failed` with the reason in its conditions.
4. If a permission exists, its expression is not evaluated at translation time (the translator has no CEL). Instead an instance of the permission gate (annotated `kardinal.io/skipped-environments`) is put in front of each kept environment that depended on the skipped one. The PolicyGate reconciler evaluates it like any gate, and that environment waits until the expression is true.

### Step 4: Collect and match PolicyGates

Read all PolicyGate CRDs from:
- Each namespace in `--policy-namespaces` controller flag (default: `platform-policies`)
- The Pipeline's own namespace

For each PolicyGate:
1. Read `kardinal.io/applies-to` label. Split by comma. Each value is an environment name.
2. Read `kardinal.io/type` label. Only process `gate` type (skip `skip-permission`, which was handled in Step 3).
3. For each environment name, if that environment is in the filtered list from Step 2, add the PolicyGate to that environment's gate list.

Output: a map of `environmentName -> []PolicyGate`.

### Step 5: Build Graph nodes

For each environment in the filtered list (in dependency order):

**PromotionStep node:**
```yaml
- id: <environment-name>
  readyWhen:
    - ${<environment-name>.status.state == "Verified"}   # health signal for UI
  propagateWhen:
    - ${<environment-name>.status.state == "Verified"}   # gates downstream data flow
  template:
    apiVersion: kardinal.io/v1alpha1
    kind: PromotionStep
    metadata:
      name: <pipeline>-<bundle-version>-<environment-name>
      labels:
        kardinal.io/pipeline: <pipeline-name>
        kardinal.io/bundle: <bundle-name>
        kardinal.io/environment: <environment-name>
        kardinal.io/shard: <shard-value>   # if shard is set on the environment
    spec:
      pipeline: <pipeline-name>
      environment: <environment-name>
      bundleRef: <bundle-name>
      path: <environment-path>
      git:
        url: <pipeline.spec.git.url>
        provider: <pipeline.spec.git.provider>
        secretRef: <pipeline.spec.git.secretRef.name>
      update:
        strategy: <environment.update.strategy>
      approval: <environment.approval>
      health: <environment.health>
      delivery: <environment.delivery>
      upstreamVerified: ${<upstream-env>.status.state}   # creates dependency edge
      requiredGates: [...]                               # filled in Step 6
```

The `upstreamVerified` field references the upstream environment's status. For the first environment (no dependencies), this field is omitted (no upstream). For environments with multiple `dependsOn`, multiple references are included.

**PolicyGate nodes** (for each gate matching this environment):
```yaml
- id: <gate-name>-<environment-name>
  readyWhen:
    - ${<gate-id>.status.ready == true}   # health signal
  propagateWhen:
    - ${<gate-id>.status.ready == true}   # gates data flow to prod PromotionStep
    - ${timestamp(<gate-id>.status.lastEvaluatedAt) > now() - duration("<recheck-interval * 2>")}  # freshness check
  template:
    apiVersion: kardinal.io/v1alpha1
    kind: PolicyGate
    metadata:
      name: <pipeline>-<bundle-version>-<gate-name>
      labels:
        kardinal.io/pipeline: <pipeline-name>
        kardinal.io/bundle: <bundle-name>
        kardinal.io/environment: <environment-name>
        kardinal.io/gate-template: <original-gate-name>
    spec:
      expression: <gate.spec.expression>
      message: <gate.spec.message>
      recheckInterval: <gate.spec.recheckInterval>
      upstreamEnvironment: ${<upstream-env>.status.state}  # creates dependency edge
```

> **`propagateWhen` is how PolicyGates block promotion.** Per the pre-upstream Graph controller design docs,
> `readyWhen` is a health signal that does not gate downstream execution. `propagateWhen` controls
> when a node's data flows to dependents. When `propagateWhen` is unsatisfied on a PolicyGate node,
> the downstream PromotionStep retains its Pending state. See design-v2.1.md Section 3.5.

### Step 6: Wire gate edges

For each gated environment (an environment with one or more PolicyGates):

1. The PolicyGate nodes depend on the upstream environment (via `upstreamEnvironment` reference).
2. The PromotionStep node for the gated environment depends on all its PolicyGate nodes (via `requiredGates` list).

Set `requiredGates` on the PromotionStep template:
```yaml
requiredGates:
  - ${<gate-1-id>.metadata.name}
  - ${<gate-2-id>.metadata.name}
```

This creates fan-in: multiple PolicyGates feed into one PromotionStep.

### Step 7: Assemble and create Graph

Combine all nodes into a Graph spec. Set metadata:
- Name: `<pipeline>-<bundle-version>`
- Namespace: Pipeline's namespace
- Owner: Bundle CR (ownerReferences)
- Labels: `kardinal.io/pipeline: <pipeline-name>`, `kardinal.io/bundle: <bundle-name>`

Create the Graph CR via the dynamic client (see 01-graph-integration).

## Concurrency

**Two Bundles for the same Pipeline at the same time:**

Each Bundle gets its own Graph with a unique name. Both Graphs execute independently. The Graph controller reconciles them in parallel. PromotionStep and PolicyGate CRs from different Bundles do not interfere because they have unique names (`<pipeline>-<bundle-version>-<env>`).

**Bundle superseding:**

When a new Bundle is created for a Pipeline that already has an active (Promoting) Bundle:
1. Check if the old Bundle is pinned (`kardinal.io/pin: "true"`). If pinned, both coexist.
2. If not pinned, and the old Bundle's Graph has no environment past HealthChecking state (no canary in progress), delete the old Graph (via Bundle ownerRef cascade) and mark the old Bundle as `Superseded`.
3. Create a new Graph for the new Bundle.

## Pipeline Spec Changes Mid-Flight

If the Pipeline CRD is updated while a Bundle is mid-flight:
- The existing Graph is NOT updated. It was generated at Bundle processing time and is immutable for that promotion run.
- The new Pipeline spec applies to all subsequent Bundles.
- This is documented, intentional behavior (Section 3.5 of design-v2.1.md).

## Edge Cases

| Case | Behavior |
|---|---|
| Pipeline has 0 environments | Error: Bundle set to Failed with reason "Pipeline has no environments." |
| intent.targetEnvironment names an environment not in the Pipeline | Error: Bundle set to Failed with reason "Unknown target environment." |
| intent.skipEnvironments removes all environments | Error: Bundle set to Failed with reason "All environments skipped." |
| PolicyGate applies-to matches no environment in the Pipeline | Gate is ignored (not injected). No error. |
| Two PolicyGates with the same name in different namespaces | Both are injected. Node IDs include the namespace to prevent collisions. |
| dependsOn references a skipped environment | Error: Bundle set to Failed with reason "dependsOn references skipped environment." |
| Circular dependsOn | Error: Bundle set to Failed with reason "Circular dependency detected." |

## Unit Tests

Test cases for `translator.go`:

1. Linear 3-env pipeline, no gates, default intent: verify 3 PromotionStep nodes with sequential dependencies.
2. Linear 3-env pipeline with 2 org gates on prod: verify 3 PromotionStep nodes + 2 PolicyGate nodes, gates between staging and prod.
3. Fan-out pipeline (staging -> [prod-us, prod-eu]): verify parallel nodes with shared dependency on staging.
4. intent.targetEnvironment = staging: verify only dev and staging nodes, no prod.
5. intent.skipEnvironments = [staging] with SkipPermission: verify staging removed, dev -> prod directly.
6. intent.skipEnvironments = [staging] without SkipPermission: verify Build fails with `skip denied` (ErrInvalid); with one, verify the permission instance holds prod.
7. Pipeline with shard on prod: verify shard label on prod PromotionStep.
8. Pipeline with custom steps or a promotionTemplate on prod: verify Build rejects it (not implemented yet).
9. Config Bundle: verify different default step sequence (config-merge instead of kustomize-set-image).
10. Empty Pipeline: verify error.
11. Circular dependency: verify error.

## Present

✅ Multi-region fan-out via Graph `forEach` (PR #612, 2026-04-22):
   `EnvironmentSpec.Regions []string` — when ≥2 regions are set, the translator emits
   a `forEach` Graph node. The Graph controller stamps out one PromotionStep per region; each
   instance receives `spec.region = "${item}"`. `PromotionStepSpec.Region string` carries
   the current region. Per-item `propagateWhen` (pre-upstream fork ≥ `745998f`) ensures all
   regional instances must be Verified before downstream environments proceed.

## Future

_(no items)_
