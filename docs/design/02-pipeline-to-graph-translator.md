# 02: Pipeline-to-Graph Translator

> Status: Comprehensive
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
  - Nodes excluded based on `intent.target` and `intent.skip`

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

- `intent.target`: only include environments up to and including the target in the dependency graph. Walk the graph from the first environment to the target, including all environments on any path.
- `intent.skip`: remove skipped environments from the graph. Before removing, validate skip permissions (see Step 3).
- Default (no intent): include all environments.

Output: a filtered list of environment names to include in the Graph.

### Step 3: Validate skip permissions

For each environment in `intent.skip`:

1. Collect all org-level PolicyGates (`kardinal.io/scope: org`) that match this environment via `kardinal.io/applies-to`.
2. If any org gate applies, check for a SkipPermission PolicyGate:
   - Scan PolicyGates with `kardinal.io/type: skip-permission` and `kardinal.io/applies-to` matching the skipped environment.
   - Evaluate the SkipPermission's CEL expression against the Bundle context (synchronously, at translation time).
   - If the expression evaluates to `true`, the skip is permitted.
   - If no SkipPermission exists or all evaluate to `false`, the skip is denied.
3. If denied: set Bundle `status.phase = "SkipDenied"` with reason. Do not create a Graph. Return.

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
      steps: <environment.steps>           # if custom steps specified
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

> **`propagateWhen` is how PolicyGates block promotion.** Per krocodile/experimental design docs,
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
| intent.target names an environment not in the Pipeline | Error: Bundle set to Failed with reason "Unknown target environment." |
| intent.skip removes all environments | Error: Bundle set to Failed with reason "All environments skipped." |
| PolicyGate applies-to matches no environment in the Pipeline | Gate is ignored (not injected). No error. |
| Two PolicyGates with the same name in different namespaces | Both are injected. Node IDs include the namespace to prevent collisions. |
| dependsOn references a skipped environment | Error: Bundle set to Failed with reason "dependsOn references skipped environment." |
| Circular dependsOn | Error: Bundle set to Failed with reason "Circular dependency detected." |

## Unit Tests

Test cases for `translator.go`:

1. Linear 3-env pipeline, no gates, default intent: verify 3 PromotionStep nodes with sequential dependencies.
2. Linear 3-env pipeline with 2 org gates on prod: verify 3 PromotionStep nodes + 2 PolicyGate nodes, gates between staging and prod.
3. Fan-out pipeline (staging -> [prod-us, prod-eu]): verify parallel nodes with shared dependency on staging.
4. intent.target = staging: verify only dev and staging nodes, no prod.
5. intent.skip = [staging] with SkipPermission: verify staging removed, dev -> prod directly.
6. intent.skip = [staging] without SkipPermission: verify Bundle set to SkipDenied.
7. Pipeline with shard on prod: verify shard label on prod PromotionStep.
8. Pipeline with custom steps on prod: verify steps field on prod PromotionStep.
9. Config Bundle: verify different default step sequence (config-merge instead of kustomize-set-image).
10. Empty Pipeline: verify error.
11. Circular dependency: verify error.
