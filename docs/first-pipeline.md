# Your first pipeline

Set up kardinal-promoter against your own GitOps repository, from an empty cluster to a version
promoted to prod by your CI. For a demo against a sample repository, see the
[Quickstart](quickstart.md).


## What you will build

```station-map
title: The pipeline you will build
caption: CI pushes an image and creates a Bundle. Test and uat promote it on their own; the no-weekend-deploys gate guards prod, and prod waits for you to merge its pull request.
lead: {into: test, label: CI, note: kardinal-test-app:sha-abc1234}
stations:
  test: {state: verified, notes: [auto-promote], status: Verified}
  uat: {state: verified, notes: [auto-promote], status: Verified}
  prod: {state: waiting, notes: [PR review required], status: Waiting for PR merge}
edges:
  - test -> uat
  - {from: uat, to: prod, gates: [no-weekend-deploys]}
thread: [test, uat, prod]
```

- Test and uat promote automatically when the upstream environment is verified.
- Prod requires a human to review and merge a PR.
- The PR includes promotion evidence: what image is being deployed, who built it, and what upstream verification looked like.

!!! info "Test application"
    This guide uses [`pnz1990/kardinal-test-app`](https://github.com/pnz1990/kardinal-test-app) and the [`pnz1990/kardinal-demo`](https://github.com/pnz1990/kardinal-demo) GitOps repository. These are the reference applications used in kardinal's own CI validation.

## Prerequisites

- A Kubernetes 1.35 or later cluster (kind, Docker Desktop, EKS, GKE, or any distribution)
- [kardinal-promoter installed](#install-kardinal-promoter), and the `kardinal` CLI ([Install the CLI](installation.md#3-install-the-cli))
- [Argo CD installed](https://argo-cd.readthedocs.io/en/stable/getting_started/) (or Flux; this guide uses Argo CD)
- A GitHub account with a personal access token (PAT) that has repo write access
- A GitOps repository with Kustomize overlays (see [Set Up Your GitOps Repo](#set-up-your-gitops-repo))

## Install kardinal-promoter

Follow [Install](installation.md): install kro, create the SCM token Secret, install the chart and
the CLI.

Verify the installation:

```bash
kubectl get pods -n kardinal-system
# NAME                                              READY   STATUS    RESTARTS   AGE
# kardinal-promoter-7f8d9c5b6d-x2k9q               1/1     Running   0          30s

kubectl get pods -n kro-system
# NAME                              READY   STATUS    RESTARTS   AGE
# kro-7d4b8f9f5-xk2pq               1/1     Running   0          30s

kardinal version
# CLI:        v0.12.0
# Controller: v0.12.0
# Graph:      kro v0.10.0-rc.0
```

## Set up your GitOps repo

This guide uses [`pnz1990/kardinal-demo`](https://github.com/pnz1990/kardinal-demo) as the GitOps target repository. It already has one directory per environment on `main`. Fork it: kardinal pushes to this repository, so the token needs write access to it.

The repository structure:

```
environments/
  test/
    kustomization.yaml      # pins the image tag kardinal updates
    deployment.yaml         # the kardinal-test-app Deployment, in namespace default
  uat/
    kustomization.yaml
    deployment.yaml
  prod/
    kustomization.yaml
    deployment.yaml
```

## Create Argo CD Applications

Create an Argo CD ApplicationSet to manage the three environments:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: kardinal-test-app
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
      name: kardinal-test-app-{{env}}
    spec:
      project: default
      source:
        repoURL: https://github.com/pnz1990/kardinal-demo   # your fork
        targetRevision: main
        path: environments/{{env}}
        # The kardinal-demo overlays set namespace "default"; move each
        # environment into its own namespace so they do not collide.
        kustomize:
          namespace: kardinal-test-app-{{env}}
      destination:
        server: https://kubernetes.default.svc
        namespace: kardinal-test-app-{{env}}
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
          - CreateNamespace=true
EOF
```

Argo CD creates the three Applications a moment later. Wait for them, so that `kardinal init` finds
them:

```bash
kubectl wait --for=create -n argocd --timeout=2m application/kardinal-test-app-test \
  application/kardinal-test-app-uat application/kardinal-test-app-prod
```

## Create the Pipeline

First, create a Secret with your GitHub token:

```bash
kubectl create secret generic github-token \
  --namespace default \
  --from-literal=token=<your-github-pat>
# A Secret a Pipeline names must be labelled referenceable (docs/guides/security.md).
kubectl label secret github-token --namespace default kardinal.io/referenceable=true
```

Write the Pipeline and apply it with `kardinal init`. It finds the Argo CD Applications above
(`kardinal-test-app-<env>` in `argocd`) and checks health with them; everything else is a default:
each environment follows the one before it, at `environments/<name>` on `main`, updated with
kustomize, and prod waits for its PR to be merged:

```bash
# --repo is your fork of pnz1990/kardinal-demo
kardinal init --name kardinal-test-app --repo https://github.com/pnz1990/kardinal-demo \
  --envs test,uat,prod -n default --apply
# Found Argo CD Application argocd/kardinal-test-app-test: test checks health with it
# Found Argo CD Application argocd/kardinal-test-app-uat: uat checks health with it
# Found Argo CD Application argocd/kardinal-test-app-prod: prod checks health with it
# Pipeline YAML written to pipeline.yaml
# Pipeline default/kardinal-test-app created
```

Running it again with the same flags changes nothing (`unchanged`). Add `--create-secret` to
create the git Secret of the step above from `$GITHUB_TOKEN` (or `gh auth token`). Or apply the
same Pipeline by hand, which also gives prod a longer health timeout. It is
`examples/quickstart/pipeline.yaml` (`kubectl apply -f examples/quickstart/pipeline.yaml` from a checkout) with the Application names written out:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: kardinal-test-app
  namespace: default
spec:
  git:
    url: https://github.com/pnz1990/kardinal-demo   # your fork
    secretRef:
      name: github-token
  environments:
    - name: test
      health:
        type: argocd
        argocd:
          name: kardinal-test-app-test
    - name: uat
      health:
        type: argocd
        argocd:
          name: kardinal-test-app-uat
    - name: prod
      approval: pr-review
      health:
        type: argocd
        argocd:
          name: kardinal-test-app-prod
        timeout: 15m
EOF
```

Verify the Pipeline was created:

```bash
kardinal get pipelines
# PIPELINE            VERSION   TEST   UAT   PROD   SUB   AGE
# kardinal-test-app   -         -      -     -      0     10s
```

!!! tip "Troubleshooting: Pipeline not appearing"
    If the pipeline doesn't appear, check that the controller is running:
    ```bash
    kubectl get pods -n kardinal-system
    kubectl logs -n kardinal-system deployment/kardinal-promoter | tail -20
    ```

## Create your first version

In a real setup, your CI pipeline creates a version after building and pushing images.
For this guide, promote the `kardinal-test-app` image `sha-9349a3f` with its digest pinned.
The kardinal-demo overlays already run `sha-9349a3f`, so a version with only that tag changes nothing and opens no PR; the digest adds a `digest:` line to each overlay.

```bash
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0"

# Create the Bundle
kardinal create bundle kardinal-test-app --image $TEST_IMAGE
```

Or equivalently with kubectl:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: kardinal-test-app-sha-9349a3f
  namespace: default
  labels:
    kardinal.io/pipeline: kardinal-test-app
spec:
  type: image
  pipeline: kardinal-test-app
  images:
    - repository: ghcr.io/pnz1990/kardinal-test-app
      tag: sha-9349a3f
      digest: sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0
  provenance:
    commitSHA: "9349a3f"
    author: "quickstart"
EOF
```

## Watch the promotion

The promotion starts immediately. kardinal-promoter generates a Graph and begins promoting through environments.

```bash
# Watch the pipeline status
kardinal get pipelines
# PIPELINE            VERSION                                 TEST   UAT               PROD          SUB   AGE
# kardinal-test-app   sha-9349a3f (kardinal-test-app-x7k2p)   Live   Checking health   Not reached   0     2m

# See the steps of the version being promoted
kardinal get steps kardinal-test-app
# ENVIRONMENT   STEP           STATE         DURATION   MESSAGE
# test          git-clone      Done          812ms      -
#               ...
# uat           git-clone      Done          790ms      -
#               ...
#               health-check   In progress   -          -

# Check why prod hasn't started: prod gets a PromotionStep only after uat is
# Verified (and, with gates, after every gate passes)
kardinal explain kardinal-test-app --env prod
# No promotion for "prod" in pipeline "kardinal-test-app" yet
```

!!! tip "Troubleshooting: Test stuck in Checking health"
    If the test environment stays in Checking health (`HealthChecking`) for more than a few minutes:
    ```bash
    kubectl get deployment kardinal-test-app -n kardinal-test-app-test
    kubectl describe application kardinal-test-app-test -n argocd
    ```
    Check that ArgoCD has synced the environment and the deployment is healthy.

Once uat is verified, the prod PromotionStep is created. Since prod uses `approval: pr-review`, kardinal-promoter opens a PR:

```bash
kardinal get steps kardinal-test-app
# ENVIRONMENT   STEP             STATE         DURATION   MESSAGE
# ...
# prod          git-clone        Done          790ms      -
#               ...
#               wait-for-merge   In progress   -          -
```

Go to your fork of kardinal-demo. You will see a PR titled:

> **[kardinal] Promote kardinal-test-app-x7k2p to prod**

(`kardinal create bundle` names the Bundle `<pipeline>-<random suffix>`.)

The PR body contains:
- The artifact being promoted (image reference, digest)
- Build provenance (commit SHA, CI run link, author)
- Upstream verification status (test and uat verified timestamps)
- Policy gate compliance (if any gates are configured)

**Merge the PR.** kardinal-promoter checks the PR every 30 seconds and sees the merge. With an SCM webhook set up, it sees it at once. Argo CD then syncs the prod Application, and the health adapter verifies it.

```bash
kardinal get pipelines
# PIPELINE            VERSION                                 TEST   UAT    PROD   SUB   AGE
# kardinal-test-app   sha-9349a3f (kardinal-test-app-x7k2p)   Live   Live   Live   0     8m
```

The promotion is complete.

## Adding policy gates (optional)

To add a no-weekend-deploys gate to prod, create a PolicyGate in the platform-policies namespace:

```bash
kubectl create namespace platform-policies 2>/dev/null || true  # it may exist already

cat <<EOF | kubectl apply -f -
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend-deploys
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
spec:
  expression: "!schedule.isWeekend"
  message: "Production deployments are blocked on weekends"
  recheckInterval: 5m
EOF
```

The next version promoted to prod has this gate in its Graph. Ship it, `main`:

```bash
kardinal create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0
```

On a weekend the gate blocks it before prod, and `kardinal explain` shows the gate with the
controller's latest evaluation, the version it belongs to, and the version still deployed in prod:

```bash
kardinal explain kardinal-test-app --env prod
# ENVIRONMENT   VERSION                          TYPE         NAME                 STATE      EXPRESSION            REASON
# prod          main (kardinal-test-app-wcfzk)   PolicyGate   no-weekend-deploys   Blocking   !schedule.isWeekend   Production deployments are blocked on weekends (!schedule.isWeekend = false; version main)
#
# prod   deployed: sha-9349a3f (kardinal-test-app-x7k2p)
```

On a weekday the gate's STATE is `Passing`, and prod starts promoting `main`. To see what a
weekend would do on any day, simulate it:

```bash
kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Saturday 3pm"
# RESULT: BLOCKED
# Blocked by: no-weekend-deploys
```

## Adding to your CI pipeline

Add a step to your CI pipeline that creates a Bundle after building and pushing your image.

The Bundle API (`POST /api/v1/bundles`) listens on port 8083 of the `kardinal-promoter` Service and is off until you set `bundleAPI.tokenSecretRef.name` (or `bundleAPI.tokenReview: true`). Expose it to CI yourself (for example with an Ingress); `kardinal.example.com` below stands for that address. See [CI integration](ci-integration.md#webhook-token).

**GitHub Actions example:**

```yaml
- name: Create Bundle
  run: |
    curl -X POST https://kardinal.example.com/api/v1/bundles \
      -H "Authorization: Bearer ${{ secrets.KARDINAL_TOKEN }}" \
      -d '{
        "pipeline": "kardinal-test-app",
        "type": "image",
        "images": [{"repository": "ghcr.io/${{ github.repository }}", "tag": "sha-${{ github.sha }}", "digest": "${{ steps.build.outputs.digest }}"}],
        "provenance": {
          "commitSHA": "${{ github.sha }}",
          "ciRunURL": "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
          "author": "${{ github.actor }}"
        }
      }'
```
