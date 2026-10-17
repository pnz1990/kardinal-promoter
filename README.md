<p align="center">
  <a href="https://pnz1990.github.io/kardinal-promoter/">
    <img src="kardinal-logo.png" alt="Kardinal Promoter" width="280" />
  </a>
</p>

<h1 align="center">kardinal-promoter</h1>

<p align="center">
  <strong>GitOps Governance for the Agentic Era</strong><br/>
  Promote every version of your app through test, uat and prod, with gates you can read.<br/>
  <a href="https://pnz1990.github.io/kardinal-promoter/">Docs</a> ·
  <a href="https://pnz1990.github.io/kardinal-promoter/quickstart/">Quickstart</a> ·
  <a href="https://pnz1990.github.io/kardinal-promoter/comparison/">Comparison</a>
</p>

---

Your CI builds a version. kardinal moves it through your environments by writing to Git, and
**gates** decide when it may go further: no deploys on weekends, 30 minutes in uat first,
two approvers for prod. With `approval: pr-review`, each prod change is a pull request that
shows what is shipping, which gates passed and how the earlier environments did. It works with Argo CD, Flux
or plain Kubernetes.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    secretRef: { name: github-token }
  environments:
    - name: test
    - name: uat
    - name: prod
      approval: pr-review
```

By default each environment deploys the `environments/<name>` directory of the repository, and
kardinal checks the Deployment `my-app` in the namespace `<name>`.

```bash
kardinal create bundle my-app --image ghcr.io/myorg/my-app:1.29.0   # ship a version
kardinal explain my-app --env prod                                   # why is it not in prod yet?
```

Try it in about 10 minutes with the [Quickstart](https://pnz1990.github.io/kardinal-promoter/quickstart/).

## Why kardinal

- **Gates you can read.** Rules are CEL expressions over the version, the clock, soak time,
  metrics and approvals. `kardinal explain` shows which gate holds a version and why;
  `kardinal policy simulate` tries a rule at any time. Platform teams set org gates that a team
  pipeline cannot remove.
- **Soak time that requires health.** `bake` requires continuous health, and a failed health
  check restarts the clock.
- **Pull requests with evidence.** Provenance, gate results and upstream health go into every
  production PR. Merging it promotes.
- **Parallel by design.** Fan out to regions with `dependsOn` or `wave`, or to many clusters
  with fleets.
- **Rollback and incident controls.** Roll back, hold an environment on the rolled-back
  version, reject a version everywhere or pause a pipeline.
- **No database or extra services.** The UI ships inside the controller, and all state is in
  Kubernetes.
- **Any Git host, any GitOps tool.** GitHub, GitLab, Forgejo, Gitea, Bitbucket Cloud and Data
  Center, Azure DevOps; health from Deployments, Argo CD, Flux, Argo Rollouts or Flagger.

See how it compares with Kargo and GitOps Promoter in the
[comparison](https://pnz1990.github.io/kardinal-promoter/comparison/).

## Status

Alpha, in active development ([releases](https://github.com/pnz1990/kardinal-promoter/releases)).
The `kardinal.io/v1alpha1` API may change between minor releases; the
[changelog](docs/changelog.md) lists every change.

## Documentation

[pnz1990.github.io/kardinal-promoter](https://pnz1990.github.io/kardinal-promoter/): start with the
[Quickstart](docs/quickstart.md), then [Concepts](docs/concepts.md). Contributors: see
[CONTRIBUTING.md](CONTRIBUTING.md) and [Architecture](docs/architecture.md).
