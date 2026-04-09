# Quickstart

This guide walks you through setting up your first promotion pipeline with kardinal-promoter. By the end, you will have a working pipeline that promotes an Nginx application through test, uat, and prod environments using Git pull requests.

## What you will build

```
test (auto-promote) --> uat (auto-promote) --> prod (PR review required)
```

- Test and uat promote automatically when the upstream environment is verified.
- Prod requires a human to review and merge a PR.
- The PR includes promotion evidence: what image is being deployed, who built it, and what upstream verification looked like.

## Prerequisites

- A Kubernetes cluster (kind, Docker Desktop, EKS, GKE, or any distribution)
- [kardinal-promoter installed](#install-kardinal-promoter)
- [Argo CD installed](https://argo-cd.readthedocs.io/en/stable/getting_started/) (or Flux; this guide uses Argo CD)
- A GitHub account with a personal access token (PAT) that has repo write access
- A GitOps repository with Kustomize overlays (see [Set Up Your GitOps Repo](#set-up-your-gitops-repo))

## Install kardinal-promoter

kardinal-promoter requires the kro Graph controller to be available in the cluster.

```bash
# Install the Graph controller (if not already present)
# (instructions TBD based on Graph packaging)

# Install kardinal-promoter
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace
```

Verify the installation:

```bash
kubectl get pods -n kardinal-system
# NAME                                    READY   STATUS    RESTARTS   AGE
# kardinal-controller-7f8d9c-xxxxx        1/1     Running   0          30s

kardinal version
# CLI:        v0.1.0
# Controller: v0.1.0
```

## Set up your GitOps repo

Fork or create a repository with this structure:

```
base/
  deployment.yaml
  service.yaml
  kustomization.yaml
environments/
  test/
    kustomization.yaml      # patches for test
  uat/
    kustomization.yaml      # patches for uat
  prod/
    kustomization.yaml      # patches for prod
```

Each environment's `kustomization.yaml` references the base and applies environment-specific patches (replica count, resource limits, ingress hostnames, etc.).

Example `base/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-demo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx-demo
  template:
    metadata:
      labels:
        app: nginx-demo
    spec:
      containers:
        - name: nginx
          image: ghcr.io/<your-username>/nginx-demo:latest
          ports:
            - containerPort: 80
```

Example `environments/test/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base
```

## Create Argo CD Applications

Create an Argo CD ApplicationSet to manage the three environments:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: nginx-demo
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - env: test
          - env: uat
          - env: prod
  template:
    metadata:
      name: nginx-demo-{{env}}
    spec:
      project: default
      source:
        repoURL: https://github.com/<your-username>/kardinal-demo
        targetRevision: main
        path: environments/{{env}}
      destination:
        server: https://kubernetes.default.svc
        namespace: nginx-demo-{{env}}
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
          - CreateNamespace=true
EOF
```

## Create the Pipeline

First, create a Secret with your GitHub token:

```bash
kubectl create secret generic github-token \
  --from-literal=token=<your-github-pat>
```

Then create the Pipeline:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: nginx-demo
spec:
  git:
    url: https://github.com/<your-username>/kardinal-demo
    branch: main
    layout: directory
    provider: github
    secretRef:
      name: github-token
  environments:
    - name: test
      path: environments/test
      update:
        strategy: kustomize
      approval: auto
      health:
        type: argocd
        argocd:
          name: nginx-demo-test
    - name: uat
      path: environments/uat
      update:
        strategy: kustomize
      approval: auto
      health:
        type: argocd
        argocd:
          name: nginx-demo-uat
    - name: prod
      path: environments/prod
      update:
        strategy: kustomize
      approval: pr-review
      health:
        type: argocd
        argocd:
          name: nginx-demo-prod
EOF
```

Verify the Pipeline was created:

```bash
kardinal get pipelines
# PIPELINE     BUNDLE   TEST   UAT   PROD   AGE
# nginx-demo   --       --     --    --     10s
```

## Create your first Bundle

In a real setup, your CI pipeline creates Bundles after building and pushing images.
For this quickstart, create one manually:

```bash
kardinal create bundle nginx-demo \
  --image ghcr.io/<your-username>/nginx-demo:1.29.0
```

Or equivalently with kubectl:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: nginx-demo-v1-29-0
  labels:
    kardinal.io/pipeline: nginx-demo
spec:
  artifacts:
    images:
      - name: nginx
        reference: ghcr.io/<your-username>/nginx-demo:1.29.0
        digest: sha256:replace-with-actual-digest
  provenance:
    commitSHA: "manual-quickstart"
    author: "<your-username>"
EOF
```

## Watch the promotion

The promotion starts immediately. kardinal-promoter generates a Graph and begins promoting through environments.

```bash
# Watch the pipeline status
kardinal get pipelines
# PIPELINE     BUNDLE    TEST       UAT        PROD           AGE
# nginx-demo   v1.29.0  Verified   Promoting  Waiting        2m

# See individual steps
kardinal get steps nginx-demo
# STEP                          TYPE            STATE           ENV
# nginx-demo-v1-29-0-test       PromotionStep   Verified        test
# nginx-demo-v1-29-0-uat        PromotionStep   HealthChecking  uat
# nginx-demo-v1-29-0-prod       PromotionStep   Pending         prod

# Check why prod hasn't started
kardinal explain nginx-demo --env prod
# PROMOTION: nginx-demo / prod
#   Bundle: v1.29.0
#
# RESULT: WAITING
#   Upstream environment "uat" has not been verified yet.
```

Once uat is verified, the prod PromotionStep is created. Since prod uses `approval: pr-review`, kardinal-promoter opens a PR:

```bash
kardinal get steps nginx-demo
# STEP                          TYPE            STATE             ENV
# nginx-demo-v1-29-0-test       PromotionStep   Verified          test
# nginx-demo-v1-29-0-uat        PromotionStep   Verified          uat
# nginx-demo-v1-29-0-prod       PromotionStep   WaitingForMerge   prod
```

Go to your GitHub repo. You will see a PR titled:

> **[kardinal] Promote nginx-demo v1.29.0 to prod**

The PR body contains:
- The artifact being promoted (image reference, digest)
- Build provenance (commit SHA, CI run link, author)
- Upstream verification status (test and uat verified timestamps)
- Policy gate compliance (if any gates are configured)

**Merge the PR.** kardinal-promoter detects the merge via webhook, Argo CD syncs the prod Application, and the health adapter verifies it.

```bash
kardinal get pipelines
# PIPELINE     BUNDLE    TEST       UAT        PROD       AGE
# nginx-demo   v1.29.0  Verified   Verified   Verified   8m
```

The promotion is complete.

## Adding policy gates (optional)

To add a no-weekend-deploys gate to prod, create a PolicyGate in the platform-policies namespace:

```bash
kubectl create namespace platform-policies 2>/dev/null

cat <<EOF | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend-deploys
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
    kardinal.io/type: gate
spec:
  expression: "!schedule.isWeekend"
  message: "Production deployments are blocked on weekends"
  recheckInterval: 5m
EOF
```

The next Bundle promoted to prod will have this gate injected into its Graph. If it is a weekend, the gate blocks the promotion and `kardinal explain` shows why.

## Adding to your CI pipeline

Add a step to your CI pipeline that creates a Bundle after building and pushing your image.

**GitHub Actions example:**

```yaml
- name: Create Bundle
  run: |
    curl -X POST https://kardinal.example.com/api/v1/bundles \
      -H "Authorization: Bearer ${{ secrets.KARDINAL_TOKEN }}" \
      -d '{
        "pipeline": "nginx-demo",
        "artifacts": {
          "images": [{"reference": "ghcr.io/${{ github.repository }}:${{ github.sha }}", "digest": "${{ steps.build.outputs.digest }}"}]
        },
        "provenance": {
          "commitSHA": "${{ github.sha }}",
          "ciRunURL": "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
          "author": "${{ github.actor }}"
        }
      }'
```

Or use the GitHub Action:

```yaml
- uses: kardinal-dev/create-bundle-action@v1
  with:
    pipeline: nginx-demo
    image: ghcr.io/${{ github.repository }}:${{ github.sha }}
    digest: ${{ steps.build.outputs.digest }}
    token: ${{ secrets.KARDINAL_TOKEN }}
```

## Next steps

- [Core Concepts](concepts.md): deeper dive into Bundles, Pipelines, PolicyGates, and health adapters
- [Multi-Cluster Fleet Example](../examples/multi-cluster-fleet/): parallel prod regions with Argo Rollouts canary
- [Design Document](design/design-v2.1.md): full technical design
