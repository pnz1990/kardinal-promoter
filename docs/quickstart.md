# Quickstart

In about 10 minutes, on a kind cluster, you ship a version of
[`kardinal-test-app`](https://github.com/pnz1990/kardinal-test-app) through test, uat and prod,
see it stop at an approval gate before prod, and approve it. Then you ship the next version and
watch it move on the UI's map.

A **version** is one build of your app that kardinal promotes, here an image tag. A **gate** is a
rule that holds a version before an environment until it passes, here your approval.

This page uses a demo repository on a local cluster. To set kardinal up on your own GitOps
repository instead, follow [Your first pipeline](first-pipeline.md): choose one of the two.

You need:

- Docker and [kind](https://kind.sigs.k8s.io/)
- `kubectl` and `helm`
- a GitHub account, and the GitHub CLI `gh`, logged in (`gh auth login`)

The quickstart uses the token of `gh auth login`, or `GITHUB_TOKEN` when it is set. The token
needs **contents** and **pull requests** read and write on your fork of the demo repository; a
fine-grained token can be given access to the fork only once the fork exists, after step 2.

The commands here use kind. The quickstart also runs on k3d, minikube, Docker Desktop and Rancher
Desktop: create and delete the cluster your own way.

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

It sets up the demo in the current kubectl context, then ships a first version and follows it.
This is its output in a log or a pipe; on a terminal the `-` marks are ✓ and each state has its
symbol:

```text
Setting up the kardinal demo in kind-kardinal
  - kro 0.10.0-rc.0: installed
  - Argo CD v3.5.3: installed
  - Secret kardinal-system/kardinal-scm-token: created from $GITHUB_TOKEN (the controller's token for PRs)
  - kardinal-promoter 0.10.1: installed
  - Secret default/github-token: created from $GITHUB_TOKEN
  - Argo CD ApplicationSet argocd/kardinal-test-app (https://github.com/<you>/kardinal-demo): applied
  - PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): applied
  - Pipeline default/demo: applied
  - First version: shipped as demo-zhxk2
Following version sha-9349a3f (demo-zhxk2) through test, uat and prod (Ctrl-C stops following, the promotion goes on)
  test: Checking health
  test: Live
  uat: Promoting
  uat: Checking health
  uat: Live
  prod: Held by gate prod-approval
Version sha-9349a3f (demo-zhxk2) stopped: prod waits for approval at gate prod-approval (kardinal approve demo prod)

Next:
  Approve prod:    kardinal approve demo prod
  Watch:           kardinal status demo -w
  Open the UI:     kardinal dashboard
  Ship the next:   kardinal ship demo --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0 --wait
```

What it set up:

- **kro** (the Kubernetes engine kardinal runs each promotion on), **Argo CD and kardinal**, each
  installed once: running the quickstart again keeps what is
  there.
- **Your token** in two Secrets: the controller's, for pull requests, and the `demo` Pipeline's,
  for git.
- **Argo CD Applications** for test, uat and prod, from your fork.
- **The `demo` Pipeline** in the `default` namespace: test, uat and prod. Prod promotes without
  a pull request, behind the `prod-approval` gate, which you may approve.

The environment state lines (`test: Live`) go to stderr, the rest to stdout.

??? note "Which clusters it runs on"
    It runs only when the kubectl context is a local cluster (kind, k3d, minikube, Docker Desktop
    or Rancher Desktop, with its API server on this machine). With any other context it stops
    before it sends anything, and prints `kind create cluster --name kardinal` to create one. It
    never changes your kubeconfig; `--context` picks another local context.

The first version, `sha-9349a3f`, went to test and uat on its own and now waits at prod's gate.

!!! tip "If it stops somewhere else"
    If the quickstart stops before prod's gate, for example because the token cannot write to
    your fork, `kardinal why demo` says what each environment waits for and the command that
    moves it on, and `kardinal doctor` checks the cluster and the controller.

## 4. Ask why it is waiting

```bash
kardinal why demo prod
```

```text
prod is waiting for your approval.
  Version  sha-9349a3f (demo-zhxk2)
  Gate     prod-approval · 0 of 1 approvals
  Next     kardinal approve demo prod
```

**Next** is the command that gets past the gate.

## 5. Approve

```bash
kardinal approve demo prod
```

```text
Version: sha-9349a3f (demo-zhxk2)
Approve sha-9349a3f (demo-zhxk2) for prod? [y/N] y
Recorded: kubernetes-admin approves demo-zhxk2 for prod (Approval demo-zhxk2-prod-…)
```

The gate passes, and the version goes live in prod within a minute or two, with no pull request:

```bash
kardinal status demo
```

```text
Pipeline: demo   Namespace: default
sha-9349a3f live in test, uat, prod

ENVIRONMENT  RUNNING                   INCOMING  STATE  SINCE  PR
test         sha-9349a3f (demo-zhxk2)  -         Live   35s    -
uat          sha-9349a3f (demo-zhxk2)  -         Live   15s    -
prod         sha-9349a3f (demo-zhxk2)  -         Live   1s     -
```

## 6. Open the UI

```bash
kardinal dashboard
```

```text
kardinal UI: http://127.0.0.1:8082/ui/
Forwarding to the controller in kardinal-system; Ctrl-C stops.
Opening in browser...
```

It port-forwards to the controller and opens the UI in your browser (`--no-open` only prints the
address). The forward keeps the terminal busy: leave it running, and use a second terminal for
the next step. Open `demo`: the map shows test, uat and prod, each running `sha-9349a3f`. Keep it open
for the next step.

## 7. Ship the next version and watch it

In a second terminal, ship the next version, `main` (the quickstart's **Ship the next** line):

```bash
kardinal ship demo --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0 --wait
```

`--wait` follows it:

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
`kardinal approve demo prod` to copy. The quickstart's UI does not authenticate you, so it cannot
record an approval for you: approve in the CLI.

`ship` ended with exit code 3, because the gate needs a person:

```bash
echo $?
# 3
```

A CI job can act on the code: [`kardinal ship`](reference/cli/kardinal-ship.md)
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
- [Add to CI](ci-integration.md): ship from your build and act on the exit code
