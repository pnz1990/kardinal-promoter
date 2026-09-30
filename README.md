<p align="center">
  <a href="https://pnz1990.github.io/kardinal-promoter/">
    <img src="kardinal-logo.png" alt="Kardinal Promoter" width="280" />
  </a>
</p>

<h1 align="center">kardinal-promoter</h1>

<p align="center">
  Kubernetes-native promotion controller with DAG pipelines and visible policy gates.<br/>
  <a href="https://pnz1990.github.io/kardinal-promoter/">Docs</a> ·
  <a href="https://pnz1990.github.io/kardinal-promoter/quickstart/">Quickstart</a> ·
  <a href="https://pnz1990.github.io/kardinal-promoter/comparison/">Comparison</a>
</p>

---

Kubernetes-native promotion controller built on [kro's Graph primitive](https://kro.run/next/docs/concepts/graph/overview/). Moves versioned artifact bundles through environment pipelines using Git pull requests as the approval mechanism, with policy gates expressed as CEL and represented as visible nodes in the promotion DAG.

## How it works

1. CI builds an image and creates a **Bundle** (a versioned artifact snapshot with build provenance).
2. The controller generates a **Graph** (a kro DAG) from the Pipeline definition, injecting **PolicyGate** nodes based on org and team policies.
3. The Graph controller creates **PromotionStep** CRs in dependency order.
4. For each step, the kardinal-controller writes manifests to Git, opens a PR with promotion evidence (provenance, upstream metrics, policy compliance), and monitors health.
5. PolicyGates block downstream steps until their CEL expressions evaluate to true. They are visible as nodes in the DAG.
6. When all environments are verified, the promotion is complete. On failure, the Graph stops downstream nodes. Automatic rollback is opt-in: with `onHealthFailure: rollback` on the environment (the default is `none`), a health failure creates a rollback Bundle that is promoted like any other. [Rollback](docs/rollback.md) lists when `onHealthFailure` applies.

All state lives in Kubernetes CRDs. There is no external database.

## Key properties

- **Graph-native pipelines.** Even linear pipelines run as kro Graphs internally. Parallel fan-out, conditional steps, and multi-service dependencies are native.
- **Policy gates as DAG nodes.** CEL-powered gates are visible in the UI and debuggable via `kardinal explain`. A team Pipeline cannot remove or weaken org-level gates; `kardinal override` force-passes one for a limited time and records the reason.
- **Pluggable integrations.** SCM providers (GitHub, GitLab, Forgejo, Gitea, Bitbucket Cloud, Azure DevOps; one per controller, chosen with `--scm-provider`), manifest update strategies (Kustomize, Helm, Argo CD), health adapters (Argo CD, Flux, Deployment), and delivery delegation (Argo Rollouts, Flagger) are Go interfaces. Adding a provider is one interface implementation.
- **PR-native approval.** Promotion PRs contain artifact provenance, upstream verification, and policy gate compliance. Human approval for production is merging the PR.
- **Multi-cluster.** Argo CD hub-spoke: health checks read the Applications in the hub. Remote-cluster health checks for Flux and bare Kubernetes (`health.cluster` kubeconfig Secrets) are not implemented yet.

## Status

**v0.9.0-rc.1** (release candidate; latest final release: v0.8.1) — alpha, in active development. APIs (`kardinal.io/v1alpha1`) may change between minor releases. See the [changelog](docs/changelog.md) for what shipped.
See the [full documentation](https://pnz1990.github.io/kardinal-promoter/) and [changelog](docs/changelog.md).

## Documentation

[https://pnz1990.github.io/kardinal-promoter/](https://pnz1990.github.io/kardinal-promoter/)

## Design

See [Architecture](docs/architecture.md) for how the controller works and [Concepts](docs/concepts.md) for the resource model.
