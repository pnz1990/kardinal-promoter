# 02: Pipeline-to-Graph Translator

> Status: Outline
> Depends on: 01-graph-integration
> Blocks: 03-promotionstep-reconciler, 04-policygate-reconciler

The most complex single piece. Converts user-facing Pipeline CRDs into executable Graph specs.

## Scope

- Input: Pipeline CRD + Bundle CRD + PolicyGate CRDs (from multiple namespaces)
- Output: Graph spec (kro.run/v1alpha1/Graph)
- Algorithm: environment ordering (sequential default, dependsOn override)
- Gate matching: label selector (kardinal.io/applies-to) across --policy-namespaces + Pipeline namespace
- Gate injection: where in the DAG gates are wired (between upstream env and target env)
- Intent handling: target limits which nodes are included, skip removes nodes after permission check
- Skip-permission validation: synchronous evaluation before Graph creation
- Concurrency: two Bundles for the same Pipeline at the same time (both get their own Graph)
- Superseding: when to close old Graph and replace with new (pin support)
- Ownership: Graph owned by Bundle via ownerReferences, cascade GC
- Graph naming convention: {pipeline}-{bundle-short-version}
- What happens when a Pipeline spec changes while a Bundle is mid-flight
