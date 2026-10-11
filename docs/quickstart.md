# Quickstart

In about 10 minutes, on a local cluster, you ship a version of
[`kardinal-test-app`](https://github.com/pnz1990/kardinal-test-app) through test, uat and prod,
see it stop at an approval gate before prod, and approve it. Then you ship the next version and
watch it move on the UI's map.

You need:

- Docker, and a local cluster tool: [kind](https://kind.sigs.k8s.io/), k3d, minikube, Docker
  Desktop or Rancher Desktop
- `kubectl` and `helm`
- a GitHub token that can write contents and pull requests on your fork of the demo repository:
  `GITHUB_TOKEN`, or the GitHub CLI signed in (`gh auth login`)

## 1. Install the CLI

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
curl -Lo kardinal "https://github.com/pnz1990/kardinal-promoter/releases/latest/download/kardinal-${OS}-${ARCH}"
chmod +x kardinal && sudo mv kardinal /usr/local/bin/
```

## 2. Create a cluster and fork the demo repository

```bash
kind create cluster --name kardinal
gh repo fork pnz1990/kardinal-demo --clone=false
```

[`kardinal-demo`](https://github.com/pnz1990/kardinal-demo) is the GitOps repository kardinal
writes to, with one directory per environment. Your fork receives its commits.

## 3. Run the quickstart

```bash
kardinal quickstart --repo https://github.com/<you>/kardinal-demo
```

It sets up the demo in the current kubectl context, then ships a first version and follows it:

```text
Setting up the kardinal demo in kind-kardinal
  ✓ kro 0.10.0-rc.0: installed
  ✓ Argo CD v3.5.3: installed
  ✓ Secret kardinal-system/kardinal-scm-token: created from $GITHUB_TOKEN (the controller's token for PRs)
  ✓ kardinal-promoter 0.10.1: installed
  ✓ Secret default/github-token: created from $GITHUB_TOKEN
  ✓ Argo CD ApplicationSet argocd/kardinal-test-app (https://github.com/<you>/kardinal-demo): applied
  ✓ PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): applied
  ✓ Pipeline default/demo: applied
  ✓ First version: shipped as demo-7xk2p
Following version sha-9349a3f (demo-7xk2p) through test, uat and prod (Ctrl-C stops following, the promotion goes on)
  test: Checking health
  test: Live
  uat: Promoting
  uat: Checking health
  uat: Live
  prod: Held by gate prod-approval
Version sha-9349a3f (demo-7xk2p) stopped: prod waits for approval at gate prod-approval (kardinal approve demo prod)

Next:
  Approve prod:    kardinal approve demo prod
  Watch:           kardinal status demo -w
  Open the UI:     kardinal dashboard
  Ship the next:   kardinal ship demo --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355f… --wait
```

What it set up:

- **kro, Argo CD and kardinal**, each installed once: running the quickstart again keeps what is
  there.
- **Your token** in two Secrets: the controller's, for pull requests, and the `demo` Pipeline's,
  for git.
- **Argo CD Applications** for test, uat and prod, from your fork.
- **The `demo` Pipeline** in the `default` namespace: test, uat and prod. Prod promotes without
  a pull request, behind the `prod-approval` gate, which you may approve.

It runs only when the current kubectl context is a kind, k3d, minikube, Docker Desktop or Rancher
Desktop cluster. With any other context it changes nothing and prints the command that switches
to a local one.

The first version, `sha-9349a3f`, went to test and uat on its own and now waits at prod's gate.

## 4. Ask why it is waiting

```bash
kardinal why demo prod
```

```text
prod is waiting for your approval.
  Version  sha-9349a3f (demo-7xk2p)
  Gate     prod-approval · 0 of 1 approvals
  Next     kardinal approve demo prod
```

**Next** is the command that gets past the gate.

## 5. Approve

```bash
kardinal approve demo prod
```

```text
Version: sha-9349a3f (demo-7xk2p)
Approve sha-9349a3f (demo-7xk2p) for prod? [y/N] y
Recorded: kubernetes-admin approves demo-7xk2p for prod (Approval demo-7xk2p-prod-…)
```

The gate passes, and the version goes live in prod within a minute or two, with no pull request:

```bash
kardinal status demo
```

```text
Pipeline: demo   Namespace: default
sha-9349a3f live in test, uat, prod

ENVIRONMENT  RUNNING                   INCOMING  STATE  SINCE  PR
test         demo-7xk2p (sha-9349a3f)  -         Live   35s    -
uat          demo-7xk2p (sha-9349a3f)  -         Live   15s    -
prod         demo-7xk2p (sha-9349a3f)  -         Live   1s     -
```

## 6. Open the UI

```bash
kardinal dashboard
# kardinal UI: http://localhost:8082/ui/
```

It port-forwards to the controller and opens the UI in your browser (`--no-open` only prints the
address). Open `demo`: the map shows test, uat and prod, each running `sha-9349a3f`. Keep it open
for the next step.

## 7. Ship the next version and watch it

In another terminal, run the **Ship the next** line from the quickstart's output. It ships
version `main`, and `--wait` follows it:

```text
Shipped main as demo-q8m4t; following it (Ctrl-C stops following, the promotion goes on)
  test: Promoting
  test: Checking health
  test: Live
  uat: Promoting
  uat: Checking health
  uat: Live
  prod: Held by gate prod-approval
Version main (demo-q8m4t) stopped: prod waits for approval at gate prod-approval (kardinal approve demo prod)
```

In the UI, the version moves along the map as the lines print: through test and uat, then
stopped at the gate before prod. Select prod: its details say why it is waiting, with
`kardinal approve demo prod` to copy.

`ship` ended with exit code 3, because the gate needs a person:

```bash
echo $?
# 3
```

A CI job can act on the code: [Wait for the outcome in CI](ci-integration.md#wait-for-the-outcome-in-ci)
lists them all.

Approve it while you watch the map:

```bash
kardinal approve demo prod
```

The prompt names `main (demo-q8m4t)`. Within a minute or two prod runs `main`, and the map shows
it live in every environment.

## Clean up

```bash
kind delete cluster --name kardinal
```

Your fork keeps the commits; delete it on GitHub when you are done.

## Next steps

- Review prod's changes as pull requests: set `approval: pr-review` on prod with
  `kubectl edit pipeline demo`, and ship again. Prod then opens a pull request in your fork, with
  the version's provenance and every gate's result in its body, and goes live when you merge it
  ([PR evidence](pr-evidence.md)).
- [Your first pipeline](first-pipeline.md): set it up against your own GitOps repository
- [Concepts](concepts.md): versions, pipelines, environments and gates
- [Require approvals](approvals.md): who may approve, how many, and from the CLI or the UI
- [Add to CI](ci-integration.md#wait-for-the-outcome-in-ci): ship from your build and act on the exit code
