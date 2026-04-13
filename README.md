# kardinal-promoter

Kubernetes-native promotion controller built on [kro's Graph primitive](https://github.com/ellistarn/kro/tree/krocodile/experimental). Moves versioned artifact bundles through environment pipelines using Git pull requests as the approval mechanism, with policy gates expressed as CEL and represented as visible nodes in the promotion DAG.

## How it works

1. CI builds an image and creates a **Bundle** (a versioned artifact snapshot with build provenance).
2. The controller generates a **Graph** (a kro DAG) from the Pipeline definition, injecting **PolicyGate** nodes based on org and team policies.
3. The Graph controller creates **PromotionStep** CRs in dependency order.
4. For each step, the kardinal-controller writes manifests to Git, opens a PR with promotion evidence (provenance, upstream metrics, policy compliance), and monitors health.
5. PolicyGates block downstream steps until their CEL expressions evaluate to true. They are visible as nodes in the DAG.
6. When all environments are verified, the promotion is complete. On failure, the Graph stops downstream nodes and rollback PRs are opened.

All state lives in Kubernetes CRDs. There is no external database.

## Key properties

- **Graph-native pipelines.** Even linear pipelines run as kro Graphs internally. Parallel fan-out, conditional steps, and multi-service dependencies are native.
- **Policy gates as DAG nodes.** CEL-powered gates are visible in the UI and debuggable via `kardinal explain`. Org-level gates cannot be bypassed by teams.
- **Pluggable integrations.** SCM providers (GitHub, GitLab), manifest update strategies (Kustomize, Helm), health adapters (Argo CD, Flux, Deployment), and delivery delegation (Argo Rollouts, Flagger) are Go interfaces. Adding a provider is one interface implementation.
- **PR-native approval.** Promotion PRs contain artifact provenance, upstream verification, and policy gate compliance. Human approval for production is merging the PR.
- **Multi-cluster.** Argo CD hub-spoke (out of the box), Flux and bare K8s (via kubeconfig Secrets).

## Status

**v0.4.0** — Production-ready. Stages 0–17 complete (all Phase 1 and Phase 2 features shipped).
See the [full documentation](https://pnz1990.github.io/kardinal-promoter/) and [changelog](docs/changelog.md).

## Documentation

[https://pnz1990.github.io/kardinal-promoter/](https://pnz1990.github.io/kardinal-promoter/)

## Design

See [kardinal-promoter Technical Design Document v2.1](docs/design/design-v2.1.md).
