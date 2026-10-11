# Quickstart

This guide walks you through setting up your first promotion pipeline with kardinal-promoter. By the end, you will have a working pipeline that promotes the `kardinal-test-app` through test, uat, and prod environments using Git pull requests.

## Fast Start — about 10 minutes

Install with `demo.enabled=true` and you get a pre-configured
Pipeline named `demo` in the release namespace. It pushes to the repository in
`demo.git.url`, which defaults to the
[`pnz1990/kardinal-demo`](https://github.com/pnz1990/kardinal-demo) reference repository.
Your token cannot write to that repository, so fork it first and set `demo.git.url` to the fork.

Before you start, the cluster needs:

- kro with the `GraphKind` feature gate (step 1 below).
- Argo CD with one Application per environment: `kardinal-test-app-test`, `kardinal-test-app-uat`
  and `kardinal-test-app-prod` in the `argocd` namespace. The demo Pipeline checks health
  through them. Create them with the ApplicationSet in
  [Create Argo CD Applications](first-pipeline.md#create-argo-cd-applications).

```bash
# 0. Fork the demo GitOps repo (your GITHUB_PAT needs write access to the fork)
gh repo fork pnz1990/kardinal-demo --clone=false
DEMO_REPO=https://github.com/<your-user>/kardinal-demo

# 1. Install kro (from a kardinal-promoter checkout)
bash hack/install-kro.sh

# 2. Store the token in two Secrets: the controller's (never labelled, no
#    Pipeline may use it) and the demo Pipeline's own git Secret, labelled so
#    a Pipeline may name it. Then install with demo mode.
kubectl create namespace kardinal-system
kubectl create secret generic scm-token \
  --namespace kardinal-system \
  --from-literal=token=$GITHUB_PAT
kubectl create secret generic demo-git-token \
  --namespace kardinal-system \
  --from-literal=token=$GITHUB_PAT
kubectl label secret demo-git-token --namespace kardinal-system kardinal.io/referenceable=true

helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.10.0 \
  --namespace kardinal-system \
  --set demo.enabled=true \
  --set demo.git.url=$DEMO_REPO \
  --set scm.secretRef.name=scm-token \
  --set demo.secretRef.name=demo-git-token

# 3. Check the demo Pipeline (it lives in the release namespace)
kardinal get pipelines -n kardinal-system
# PIPELINE   BUNDLE   TEST   UAT   PROD   SUB   AGE
# demo       -        -      -     -      0     10s

# 4. Trigger the first promotion. The kardinal-demo overlays already run
#    sha-9349a3f, so a Bundle with only that tag changes nothing; the digest
#    makes it a real change (see "Create your first Bundle" below).
kardinal create bundle demo -n kardinal-system \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0
```

The demo Pipeline uses your fork of the `kardinal-demo` GitOps repo (it already has the
correct Kustomize layout). Test and uat environments promote automatically; prod opens a PR for review.

!!! note "Estimated time: about 10 minutes on a fresh kind cluster"
    Prerequisites: kind cluster, `helm`, `kubectl`, the `kardinal` CLI
    ([Install the CLI](installation.md#3-install-the-cli)), Argo CD, and a GitHub PAT
    with write access to the repository in `demo.git.url`. Point the ApplicationSet
    `repoURL` in [Create Argo CD Applications](first-pipeline.md#create-argo-cd-applications) at the same fork.

---

## Next steps

- [Your first pipeline](first-pipeline.md): set it up against your own GitOps repository
- [Core Concepts](concepts.md): deeper dive into Bundles, Pipelines, PolicyGates, and health adapters
- [Multi-Cluster Fleet Example](https://github.com/pnz1990/kardinal-promoter/tree/main/examples/multi-cluster-fleet): parallel prod regions with Argo Rollouts canary
- [Architecture](architecture.md): how the controller works
