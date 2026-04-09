# Example: Quickstart

A minimal 3-environment promotion pipeline (test, uat, prod) for an Nginx application.
Equivalent to the [Kargo Quickstart](https://docs.kargo.io/quickstart), reimplemented using kardinal-promoter.

## What this example does

1. CI builds a new Nginx image and creates a Bundle (artifact snapshot with provenance).
2. kardinal-promoter promotes the Bundle through test, uat, and prod via Git PRs.
3. PolicyGates block production promotion on weekends and enforce a 30-minute staging soak.
4. Argo CD syncs each environment from the GitOps repo.
5. Health verification uses Argo CD Application status.

## Pipeline topology

```
test (auto) --> uat (auto) --> [no-weekend-deploys] --> prod (pr-review)
                               [require-uat-soak]   /
```

## Prerequisites

- Kubernetes cluster with kardinal-promoter and Graph controller installed
- Argo CD installed and running
- A GitOps repo with Kustomize overlays per environment:
  ```
  base/
    deployment.yaml
    kustomization.yaml
  environments/
    test/
      kustomization.yaml
    uat/
      kustomization.yaml
    prod/
      kustomization.yaml
  ```
- A GitHub PAT with repo write access

## Setup

### 1. Create the Git credentials Secret

```bash
export GITOPS_REPO_URL=https://github.com/<your-username>/kardinal-demo
export GITHUB_USERNAME=<your-username>
export GITHUB_PAT=<your-pat>
```

```bash
kubectl create secret generic github-token \
  --namespace=default \
  --from-literal=token=$GITHUB_PAT
```

### 2. Create the Argo CD Applications

```bash
kubectl apply -f argocd-applications.yaml
```

### 3. Create the PolicyGates (org-level)

```bash
kubectl apply -f policy-gates.yaml
```

### 4. Create the Pipeline

```bash
kubectl apply -f pipeline.yaml
```

### 5. Create your first Bundle

After your CI builds and pushes `ghcr.io/<your-username>/nginx-demo:1.29.0`:

```bash
kardinal create bundle nginx-demo \
  --image ghcr.io/<your-username>/nginx-demo:1.29.0 \
  --commit abc123 \
  --ci-run https://github.com/<your-username>/nginx-demo/actions/runs/12345
```

Or apply the Bundle directly:

```bash
kubectl apply -f bundle.yaml
```

## What happens next

1. kardinal-promoter generates a Graph for this Bundle.
2. Graph controller creates a PromotionStep for test.
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
kardinal get steps nginx-demo

# See why prod is waiting
kardinal explain nginx-demo --env prod

# Watch the promotion live
kardinal explain nginx-demo --env prod --watch
```

## Files in this example

| File | What it is |
|---|---|
| `pipeline.yaml` | The Pipeline CRD (3 environments) |
| `policy-gates.yaml` | Org-level PolicyGates (no-weekend-deploys, require-uat-soak) |
| `bundle.yaml` | A sample Bundle for manual creation |
| `argocd-applications.yaml` | Argo CD ApplicationSet for the 3 environments |
