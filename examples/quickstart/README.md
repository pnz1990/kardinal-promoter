# Example: Quickstart

A minimal 3-environment promotion pipeline (test, uat, prod) for
[kardinal-test-app](https://github.com/pnz1990/kardinal-test-app), promoted through the
[kardinal-demo](https://github.com/pnz1990/kardinal-demo) GitOps repo.
Equivalent to the [Kargo Quickstart](https://docs.kargo.io/quickstart), reimplemented using kardinal-promoter.

## What this example does

1. CI builds a new kardinal-test-app image and creates a Bundle (artifact snapshot with provenance).
2. kardinal-promoter promotes the Bundle through test, uat, and prod via Git PRs.
3. PolicyGates block production promotion on weekends and enforce a 30-minute uat soak.
4. Argo CD syncs each environment from the GitOps repo.
5. Health verification uses Argo CD Application status.

## Pipeline topology

```
test (auto) --> uat (auto) --> [no-weekend-deploys] --> prod (pr-review)
                               [require-uat-soak]   /
```

## Prerequisites

- Kubernetes cluster with kardinal-promoter and kro installed (kro with the GraphKind
  feature gate, at the version pinned in `hack/install-kro.sh`)
- Argo CD installed and running
- A GitOps repo with a Kustomize overlay per environment. The manifests use
  [pnz1990/kardinal-demo](https://github.com/pnz1990/kardinal-demo), which has this layout:
  ```
  environments/
    test/
      deployment.yaml
      kustomization.yaml
    uat/
      ...
    prod/
      ...
  ```
  To promote into your own copy, fork it and change `spec.git.url` in `pipeline.yaml`
  and `repoURL` in `argocd-applications.yaml` to the fork.
- A GitHub PAT with write access to that repo

## Setup

### 1. Create the Git credentials Secret

```bash
kubectl create secret generic github-token \
  --namespace=default \
  --from-literal=token="$GITHUB_PAT"
```

### 2. Create the Argo CD Applications

```bash
kubectl apply -f argocd-applications.yaml
```

### 3. Create the PolicyGates (org-level)

`policy-gates.yaml` also creates the `platform-policies` namespace the org gates live in.

```bash
kubectl apply -f policy-gates.yaml
```

### 4. Create the Pipeline

```bash
kubectl apply -f pipeline.yaml
```

### 5. Create your first Bundle

Pick a kardinal-test-app image that CI has built:

```bash
IMAGE=ghcr.io/pnz1990/kardinal-test-app:sha-$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
kardinal create bundle kardinal-test-app --image "$IMAGE"
```

In CI, the create-bundle action (see `docs/ci-integration.md`) creates the Bundle
and records the commit SHA and CI run URL as provenance.

Or apply the Bundle directly:

```bash
kubectl apply -f bundle.yaml
```

## What happens next

1. kardinal-promoter generates a Graph for this Bundle.
2. kro creates a PromotionStep for test from the Graph.
3. kardinal-controller updates `environments/test/kustomization.yaml` with the new image tag, pushes directly (auto approval).
4. Argo CD syncs the test Application. Health adapter verifies `Application.status.health = Healthy`.
5. Graph advances to uat. Same flow.
6. Graph creates PolicyGate instances (no-weekend-deploys, require-uat-soak). kardinal-controller evaluates them.
7. When gates pass, Graph creates PromotionStep for prod.
8. kardinal-controller opens a PR with promotion evidence. A human reviews and merges.
9. Argo CD syncs prod. Health adapter verifies. Bundle marked Verified.

## Observing the promotion

```bash
# See the pipeline status
kardinal get pipelines

# See promotion steps and policy gates
kardinal get steps kardinal-test-app

# See why prod is waiting
kardinal explain kardinal-test-app --env prod

# Watch the promotion live
kardinal explain kardinal-test-app --env prod --watch
```

## Files in this example

| File | What it is |
|---|---|
| `pipeline.yaml` | The Pipeline CRD (3 environments) |
| `policy-gates.yaml` | Org-level PolicyGates (no-weekend-deploys, require-uat-soak) |
| `bundle.yaml` | A sample Bundle for manual creation |
| `argocd-applications.yaml` | Argo CD ApplicationSet for the 3 environments |
