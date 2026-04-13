# kardinal-promoter

**GitOps promotion pipelines with visible policy gates and PR evidence.**

kardinal-promoter is a Kubernetes-native controller that automates software promotion through environments (test → uat → prod) using a DAG of promotion steps, CEL-based policy gates, and structured PR evidence.

<div class="grid cards" markdown>

-   :material-clock-fast:{ .lg .middle } **Get started in 5 minutes**

    ---

    Install kardinal-promoter, apply a Pipeline, create a Bundle, watch it promote.

    [:octicons-arrow-right-24: Quickstart](quickstart.md)

-   :material-shield-check:{ .lg .middle } **Policy gates**

    ---

    Block production deployments on weekends, require soak time, enforce team approvals — all in CEL.

    [:octicons-arrow-right-24: Policy Gates](policy-gates.md)

-   :material-graph:{ .lg .middle } **DAG pipelines**

    ---

    Every promotion is a directed acyclic graph. Fan-out to parallel environments, gate on any condition.

    [:octicons-arrow-right-24: Concepts](concepts.md)

-   :material-source-pull:{ .lg .middle } **PR evidence**

    ---

    Every prod promotion opens a PR with structured evidence: image digest, CI run, gate results, soak time.

    [:octicons-arrow-right-24: PR Evidence](pr-evidence.md)

</div>

## Why kardinal-promoter?

| Feature | kardinal | Kargo | GitOps Promoter |
|---|---|---|---|
| DAG promotion pipelines | ✅ | ❌ linear only | ❌ linear only |
| CEL policy gates with kro library | ✅ | basic | ❌ |
| PR evidence body (structured) | ✅ | ❌ | ✅ basic |
| GitOps-agnostic (ArgoCD + Flux) | ✅ | ArgoCD only | Flux only |
| Graph-first architecture (krocodile) | ✅ | ❌ | ❌ |

## Quick install

```bash
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace
```

See [Installation](installation.md) for full prerequisites and configuration.
