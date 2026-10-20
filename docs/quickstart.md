# Quickstart

In about 10 minutes, on a kind cluster, you ship a version of
[`kardinal-test-app`](https://github.com/pnz1990/kardinal-test-app) through test, uat and prod,
see it stop at an approval gate before prod, and approve it. Then you ship the next version and
watch it move on the UI's map.

A **version** is one build of your app that kardinal promotes, here an image tag. A **gate** is a
rule that holds a version before an environment until it passes, here your approval.

Everything runs in a local cluster, including the git repository kardinal writes to: you need
no GitHub account, fork or token. To use your own GitHub fork of the demo repository instead, see
[With your GitHub fork](#with-your-github-fork). To set kardinal up on your own GitOps
repository, follow [Your first pipeline](first-pipeline.md).

You need:

- Docker and [kind](https://kind.sigs.k8s.io/)
- `kubectl` and `helm`
- internet access for the cluster, to pull the images of kro, Argo CD, Forgejo, kardinal and the
  test app

The commands here use kind. The quickstart also runs on k3d, minikube, Docker Desktop and Rancher
Desktop: create and delete the cluster your own way.

## 1. Install the CLI

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
curl -Lo kardinal "https://github.com/pnz1990/kardinal-promoter/releases/latest/download/kardinal-${OS}-${ARCH}"
chmod +x kardinal && sudo mv kardinal /usr/local/bin/
```

## 2. Create a cluster

```bash
kind create cluster --name kardinal
```

## 3. Run the quickstart

```bash
kardinal quickstart --local
```

It sets up the demo in the current kubectl context, then ships a first version and follows it.
This is its output in a log or a pipe; on a terminal the `-` marks are ✓ and each state has its
symbol:

```text
Setting up the kardinal demo in kind-kardinal
  - kro 0.10.0-rc.0: installed
  - Argo CD v3.5.3: installed
  - Git server kardinal-git/kardinal-git (http://kardinal-git.kardinal-git.svc.cluster.local:3000/kardinal/kardinal-demo.git): installed, repository kardinal/kardinal-demo seeded
  - Secret kardinal-system/kardinal-scm-token: created (the controller's token on the git server, for PRs)
  - kardinal-promoter 0.12.2: installed
  - Secret default/git-token: created (the Pipeline's token on the git server)
  - Argo CD ApplicationSet argocd/kardinal-test-app (http://kardinal-git.kardinal-git.svc.cluster.local:3000/kardinal/kardinal-demo.git): applied
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
Version sha-9349a3f (demo-zhxk2) stopped: prod waits for approval at gate prod-approval (kardinal --context kind-kardinal approve demo prod)

Next:
  Approve prod:    kardinal --context kind-kardinal approve demo prod
  Watch:           kardinal --context kind-kardinal status demo -w
  Open the UI:     kardinal --context kind-kardinal dashboard
  Ship the next:   kardinal --context kind-kardinal ship demo --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0 --wait
  See the repo:    kubectl --context kind-kardinal -n kardinal-git port-forward svc/kardinal-git 3000, then http://127.0.0.1:3000/kardinal/kardinal-demo
  Remove it all:   kardinal --context kind-kardinal quickstart --local --delete
```

What it set up:

- **kro** (the Kubernetes engine kardinal runs each promotion on), **Argo CD and kardinal**, each
  installed once: running the quickstart again keeps what is
  there.
- **A git server** in the namespace `kardinal-git`: one [Forgejo](https://forgejo.org/) pod whose
  repository `kardinal/kardinal-demo` holds the environments of
  [`kardinal-demo`](https://github.com/pnz1990/kardinal-demo), one directory each. kardinal
  writes its commits there. The server is reachable only inside the cluster: its Service is a
  ClusterIP, with no port published on your machine.
- **Two tokens of the git server** in two Secrets: the controller's, for pull requests, and the
  `demo` Pipeline's, for git. The quickstart reads each from the server and writes it straight
  into its Secret; neither is printed or written to a file. The server's user has a random
  password that nobody keeps.
- **Argo CD Applications** for test, uat and prod, from that repository. The Argo CD it installs
  checks git every 10 seconds, so each environment deploys within seconds of its commit; an Argo
  CD that was already there keeps its own settings.
- **The `demo` Pipeline** in the `default` namespace: test, uat and prod. Prod promotes without
  a pull request, behind the `prod-approval` gate, which you may approve.

The environment state lines (`test: Live`) go to stderr, the rest to stdout. Every command it
prints names the context it set the demo up in (`--context kind-kardinal`, and `--kubeconfig`
when the context came from another kubeconfig than `~/.kube/config`, through `--kubeconfig` or
`KUBECONFIG`), so you can copy it into any shell and it runs against this cluster.

To look at the repository and kardinal's commits in a browser, forward the server's port; the
forward listens on `127.0.0.1` only:

```bash
kubectl -n kardinal-git port-forward svc/kardinal-git 3000
```

Then open `http://127.0.0.1:3000/kardinal/kardinal-demo`. Reading needs no login.

??? note "Which clusters it runs on"
    It runs only when the kubectl context is a local cluster (kind, k3d, minikube, Docker Desktop
    or Rancher Desktop, with its API server on this machine). With any other context it stops
    before it sends anything, and prints `kind create cluster --name kardinal` to create one. It
    also refuses while a proxy would decide where its requests go: a `proxy-url` on the context's
    cluster, `HTTPS_PROXY` (`HTTP_PROXY` for an `http` server) that applies to the API server, or
    `HELM_KUBE*` and `KUBERNETES_MASTER` set. `localhost` and `127.0.0.1` are never proxied; for
    `0.0.0.0` (k3d), `host.docker.internal` or a minikube address, add the host to `NO_PROXY`.
    It never changes your kubeconfig; `--context` picks another local context.

The first version, `sha-9349a3f`, went to test and uat on its own and now waits at prod's gate.

!!! tip "If it stops somewhere else"
    If the quickstart stops before prod's gate, `kardinal why demo` says what each environment
    waits for and the command that moves it on, and `kardinal doctor` checks the cluster and the
    controller. The git server needs a default StorageClass for its volume (kind, k3d, minikube
    and Docker Desktop have one); without one the quickstart stops before it installs it.

## 4. Ask why it is waiting

```bash
kardinal why demo prod
```

```text
prod is waiting for your approval.
  Version  sha-9349a3f (demo-zhxk2), by kubernetes-admin
  Gate     prod-approval · 0 of 1 approvals
  Next     kardinal approve demo prod
```

**Next** is the command that gets past the gate.

<figure class="k-demo" id="demo-why-waiting">
  <picture>
    <source media="(prefers-reduced-motion: reduce) and (prefers-color-scheme: dark)" srcset="../assets/demos/why-waiting-dark.png">
    <source media="(prefers-reduced-motion: reduce)" srcset="../assets/demos/why-waiting.png">
    <source media="(prefers-color-scheme: dark)" srcset="../assets/demos/why-waiting-dark.webp">
    <img src="../assets/demos/why-waiting.webp" alt="The UI: selecting a waiting stage, then its gate, to read why the promotion waits." width="1280" height="800" loading="lazy">
  </picture>
  <figcaption>Why a stage is waiting, in the UI · <code>kardinal why demo prod</code></figcaption>
</figure>

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
      +2s  test: Promoting
      +4s  test: Checking health
      +9s  test: Live
     +11s  uat: Promoting
     +13s  uat: Checking health
     +18s  uat: Live
     +18s  prod: Held by gate prod-approval
Summary after 20s:
  test   Live                         took 9s
  uat    Live                         took 7s
  prod   Held by gate prod-approval   for 2s
Version main (demo-q8m4t) stopped: prod waits for approval at gate prod-approval (kardinal approve demo prod)
```

Each line starts with the time since the version was shipped. The summary gives each
environment's last state, with how long its promotion took or how long it has waited.

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

To keep the cluster, remove what the quickstart created instead:

```bash
kardinal quickstart --local --delete
```

It prints what it will remove and what it keeps, then asks (`--yes` goes ahead without asking,
and is needed when there is no terminal to ask on). It deletes the demo's versions, the `demo`
Pipeline, the `prod-approval` gate, the ApplicationSet, the git Secret and the namespaces it
created for the Applications, and then the git server with its repository. It also uninstalls
each of kardinal, Argo CD and kro that the quickstart installed, with its CRDs and the namespace
it created for it. Only what the quickstart marked with its install ID is removed: what was there
before stays. A part also stays, CRDs and all, while anything other than the demo uses its API
(your own Argo CD Applications, other Pipelines): deleting a CRD deletes every object of its kind.

## With your GitHub fork

The quickstart can use your fork of [`kardinal-demo`](https://github.com/pnz1990/kardinal-demo)
on GitHub instead of the git server, so its commits and prod's pull requests land on GitHub.
Instead of steps 2 and 3:

- You need a GitHub account and the GitHub CLI `gh`, logged in (`gh auth login`). The quickstart
  uses its token, or `GITHUB_TOKEN` when it is set. The token needs **contents** and **pull
  requests** read and write on your fork; a fine-grained token can be given access to the fork
  only once the fork exists.
- quickstart supports forks on github.com. A `--repo` on another git host is refused before
  anything is installed; install kardinal by hand for it ([Installation](installation.md),
  [SCM providers](scm-providers.md)), or pass `--set scm.provider=<provider> --set
  scm.apiURL=<url>` so that the quickstart installs the controller for that host.

```bash
gh repo fork pnz1990/kardinal-demo --clone=false
```

```bash
kardinal quickstart --repo https://github.com/<you>/kardinal-demo
```

It installs the same demo, with your token in the two Secrets and the Applications reading your
fork:

```text
Setting up the kardinal demo in kind-kardinal
  - kro 0.10.0-rc.0: installed
  - Argo CD v3.5.3: installed
  - Secret kardinal-system/kardinal-scm-token: created from $GITHUB_TOKEN (the controller's token for PRs)
  - kardinal-promoter 0.12.2: installed
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
Version sha-9349a3f (demo-zhxk2) stopped: prod waits for approval at gate prod-approval (kardinal --context kind-kardinal approve demo prod)

Next:
  Approve prod:    kardinal --context kind-kardinal approve demo prod
  Watch:           kardinal --context kind-kardinal status demo -w
  Open the UI:     kardinal --context kind-kardinal dashboard
  Ship the next:   kardinal --context kind-kardinal ship demo --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0 --wait
```

Steps 4 to 7 are the same. `kardinal quickstart --delete` deletes the demo's versions, the `demo`
Pipeline, the `prod-approval` gate, the ApplicationSet and the Secret it created, and keeps kro,
Argo CD and kardinal. Your fork keeps the commits; delete it on GitHub when you are done.

## Next steps

- Review prod's changes as pull requests: set `approval: pr-review` on prod with
  `kubectl edit pipeline demo`, and ship again. Prod then opens a pull request in the demo
  repository, with the version's provenance and every gate's result in its body, and goes live
  when you merge it ([PR evidence](pr-evidence.md)).
- [Your first pipeline](first-pipeline.md): set it up against your own GitOps repository
- [Concepts](concepts.md): versions, pipelines, environments and gates
- [Require approvals](approvals.md): who may approve, how many, and from the CLI or the UI
- [Add to CI](ci-integration.md): ship from your build and act on the exit code
