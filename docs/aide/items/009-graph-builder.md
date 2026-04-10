# Item 009: Graph Builder — Pipeline-to-Graph Translator

> **Queue**: queue-004 (Stage 3)
> **Branch**: `009-graph-builder`
> **Depends on**: 008 (merged — PropagateWhen + API group fix)
> **Dependency mode**: merged
> **Assignable**: immediately (008 is done)
> **Contributes to**: J1, J3, J7 (foundational for all journeys)
> **Priority**: HIGH — Stage 4 (PolicyGate CEL) depends on this

---

## Goal

Implement `pkg/graph/` and `pkg/translator/` packages:

1. **`pkg/graph/client.go`** — Graph CR CRUD operations via dynamic client
2. **`pkg/graph/builder.go`** — Builds a Graph spec from Pipeline + Bundle + PolicyGates  
3. **`pkg/translator/translator.go`** — Full Pipeline-to-Graph translation algorithm
4. **Extend `BundleReconciler`** — on `status.phase = Available`, call translator and create Graph

Design spec: `docs/design/01-graph-integration.md` + `docs/design/02-pipeline-to-graph-translator.md`

---

## Deliverables

### 1. `pkg/graph/client.go`

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

// GraphClient handles Graph CR CRUD operations via the dynamic client.
// Does NOT import any kro Go module — uses the dynamic client only.
type GraphClient struct {
    dynamic dynamic.Interface
    log     zerolog.Logger
}

func NewGraphClient(dynamic dynamic.Interface, log zerolog.Logger) *GraphClient

// Create creates a Graph CR in the given namespace.
// Returns nil if already exists (idempotent).
func (c *GraphClient) Create(ctx context.Context, graph *Graph) error

// Get retrieves a Graph CR by name and namespace.
func (c *GraphClient) Get(ctx context.Context, namespace, name string) (*Graph, error)

// Delete deletes a Graph CR. Returns nil if not found.
func (c *GraphClient) Delete(ctx context.Context, namespace, name string) error

// List lists all Graph CRs in a namespace with the given labels.
func (c *GraphClient) List(ctx context.Context, namespace string, labels map[string]string) ([]*Graph, error)
```

Use `GraphGVR` from `pkg/graph/types.go` (authoritative: `experimental.kro.run/v1alpha1/graphs`).
All operations use structured logging via `zerolog.Ctx(ctx)`. Errors wrapped with `fmt.Errorf("graph.Create: %w", err)`.

### 2. `pkg/graph/builder.go`

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

// BuildInput contains everything needed to generate a Graph spec.
type BuildInput struct {
    Pipeline   *v1alpha1.Pipeline
    Bundle     *v1alpha1.Bundle
    PolicyGates []v1alpha1.PolicyGate  // all gates from all policy namespaces + pipeline NS
}

// BuildResult is the output of the graph builder.
type BuildResult struct {
    Graph       *Graph
    // NodeCount is the total number of nodes generated.
    NodeCount   int
}

// Builder builds Graph specs from Pipeline + Bundle + PolicyGates.
type Builder struct{}

func NewBuilder() *Builder

// Build generates a Graph spec. Returns an error if the Pipeline is invalid
// (circular deps, unknown target env, etc.). Does NOT write to Kubernetes.
func (b *Builder) Build(input BuildInput) (*BuildResult, error)
```

The builder implements the full translation algorithm from `docs/design/02-pipeline-to-graph-translator.md`:

- Step 1: resolve environment ordering (sequential + dependsOn fan-out)
- Step 2: filter by Bundle intent (targetEnvironment, skipEnvironments)
- Step 3: validate skip permissions (PolicyGate type: skip-permission)
- Step 4: collect and match PolicyGates by `kardinal.io/applies-to` label
- Step 5: build PromotionStep and PolicyGate nodes with correct readyWhen + propagateWhen
- Step 6: wire gate edges (requiredGates on PromotionStep, upstreamEnvironment on PolicyGate)
- Step 7: assemble Graph with ownerReferences pointing to Bundle

**Graph naming**: `<pipeline-name>-<bundle-version-slug>` where slug replaces `.` and `+` with `-`.
Truncate to 63 chars. If collision (timestamp suffix needed): `<slug>-<unix-ts-last-4>`.

**Node naming**:
- PromotionStep: `<pipeline>-<bundle-slug>-<env-name>`
- PolicyGate:    `<pipeline>-<bundle-slug>-<gate-name>-<env-name>`

**CEL reference edges** (per design-v2.1.md):
- PromotionStep upstreamVerified: `${<upstream-env>.status.state}` for each dependsOn
- PolicyGate upstreamEnvironment: `${<upstream-env>.status.state}`
- PromotionStep requiredGates: `["${<gate-id>.metadata.name}", ...]`

### 3. `pkg/translator/` (thin adapter)

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

// Translator handles the full pipeline-to-graph creation flow:
// reads PolicyGates from cluster, calls graph.Builder, calls graph.GraphClient.Create.
type Translator struct {
    graphClient  *graph.GraphClient
    builder      *graph.Builder
    policyNS     []string  // controller flag: --policy-namespaces (default: platform-policies)
    log          zerolog.Logger
}

func New(graphClient *graph.GraphClient, builder *graph.Builder, policyNS []string, log zerolog.Logger) *Translator

// Translate translates a Pipeline+Bundle pair to a Graph CR and creates it.
// Idempotent: if Graph already exists with the correct name, returns without error.
func (t *Translator) Translate(ctx context.Context, pipeline *v1alpha1.Pipeline, bundle *v1alpha1.Bundle) error
```

### 4. Extend `BundleReconciler` (`pkg/reconciler/bundle/`)

Add translator call on Bundle Available → Promoting transition:

```go
// When bundle.Status.Phase == "Available":
result, err := t.translator.Translate(ctx, pipeline, bundle)
if err != nil {
    // patch bundle status to Failed with reason
    return ctrl.Result{}, fmt.Errorf("translator.Translate: %w", err)
}
bundle.Status.Phase = "Promoting"
bundle.Status.GraphRef = result.Graph.Name
// patch bundle status
```

The `BundleReconciler` must inject `Translator` via constructor (not package-level init).

### 5. Unit tests

Test file: `pkg/graph/builder_test.go`

Implement the 11 test cases from `docs/design/02-pipeline-to-graph-translator.md §Unit Tests`:

1. Linear 3-env pipeline, no gates: verify 3 PromotionStep nodes, sequential deps
2. Linear 3-env with 2 org gates on prod: 3 PromotionStep + 2 PolicyGate nodes
3. Fan-out: staging → [prod-us, prod-eu]: parallel nodes, shared dep on staging
4. `intent.targetEnvironment = staging`: only dev+staging nodes
5. `intent.skipEnvironments = [staging]` with SkipPermission: staging removed
6. `intent.skipEnvironments = [staging]` without SkipPermission: SkipDenied error
7. Shard label on prod PromotionStep
8. Custom steps on prod PromotionStep
9. Config Bundle: config-merge step sequence
10. Empty Pipeline: error returned
11. Circular dependency: error returned

Additionally:
- Test that `propagateWhen` is set correctly on PolicyGate nodes
- Test that `readyWhen` is set correctly on PromotionStep nodes
- Test Graph name truncation to 63 chars
- Test that ownerReferences point to Bundle

Target: **≥ 90% line coverage** on `pkg/graph/builder.go` (per roadmap Stage 3 acceptance criteria).

### 6. `examples/quickstart/graph-expected.yaml`

Add a YAML file showing the expected generated Graph for the quickstart Pipeline (3-env linear pipeline with 1 PolicyGate). This is documentation/test evidence, not applied to a cluster.

---

## Acceptance Criteria

- [ ] `pkg/graph/client.go`: Create/Get/Delete/List via dynamic client using `GraphGVR`
- [ ] `pkg/graph/builder.go`: Full translation algorithm implemented (Steps 1-7)
- [ ] `pkg/translator/translator.go`: Reads PolicyGates from cluster, calls builder, creates Graph
- [ ] `BundleReconciler` extended: Available → Promoting transition creates Graph
- [ ] `examples/quickstart/graph-expected.yaml` created
- [ ] Unit tests: all 11 test cases from design spec pass
- [ ] Unit test for propagateWhen on PolicyGate nodes
- [ ] `go test ./pkg/graph/... -race` passes with ≥ 90% coverage
- [ ] `go test ./... -race -count=1 -timeout 120s` passes
- [ ] `go build ./...` passes
- [ ] `go vet ./...` zero findings
- [ ] Copyright header on all new files
- [ ] No banned filenames (util.go, helpers.go, common.go)
- [ ] No kro import in go.mod (dynamic client only)
- [ ] All errors use `fmt.Errorf("context: %w", err)` pattern
- [ ] All logging uses `zerolog.Ctx(ctx)`, no fmt.Println

---

## Anti-patterns to Avoid

- Do NOT import any kro Go module — use dynamic client only
- Do NOT add `util.go`, `helpers.go`, or `common.go`
- Do NOT mutate Deployments/Services
- Use `GraphGVR` from `pkg/graph/types.go`, not inline string literals

---

## Notes

- The `BundleReconciler` already has integration tests in `pkg/reconciler/bundle/`. Extend them.
- The `BundleReconciler` constructor needs a `*translator.Translator` parameter added.
- The controller manager main.go needs to construct `GraphClient`, `Builder`, `Translator` and inject into `BundleReconciler`.
- For unit tests, mock the Kubernetes API using `sigs.k8s.io/controller-runtime/pkg/client/fake` (already in deps).
- Graph's `examples/quickstart/graph-expected.yaml` is purely documentation — it shows the expected output but is not applied to a cluster.
