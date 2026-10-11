# Quickstart

In about 10 minutes, on a local cluster, you ship a version of
[`kardinal-test-app`](https://github.com/pnz1990/kardinal-test-app) through test, uat and prod,
watch it stop at an approval gate before prod, and approve it.

You need:

- Docker, and a local cluster tool: [kind](https://kind.sigs.k8s.io/), k3d, minikube, Docker
  Desktop or Rancher Desktop
- `kubectl` and `helm`
- the GitHub CLI `gh`, signed in (`gh auth login`)
- a GitHub token that can write contents and pull requests on your fork of the demo repository.
  `kardinal quickstart` reads it from `GITHUB_TOKEN`, or else from `gh auth token`

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
writes to, with one directory per environment. Your fork receives its commits and pull requests.

## 3. Run the quickstart

```bash
kardinal quickstart --repo github.com/<your-user>/kardinal-demo
```

It runs only when the current kubectl context is a kind, k3d, minikube, Docker Desktop or Rancher
Desktop cluster, and refuses any other. It installs kro, Argo CD and kardinal, stores your token
in one Secret, and creates the `demo` Pipeline in the `default` namespace: test, uat and prod,
with an approval gate on prod that you may approve. Then it ships the first version.
<!-- C7: the exact list of what it installs and creates, and its flags -->

```text
<C7: quickstart output, ending with the UI, watch and next-step lines>
```

The first version goes to test and uat on its own, then waits at prod's approval gate. Follow
it with:

```bash
kardinal status demo -w
```

## 4. Open the UI

```bash
kardinal dashboard
```

It port-forwards to the controller and opens the UI in your browser. The UI opens on the fleet. Open `demo`: the map shows test, uat and prod, and the version on its
way through them. Select prod to see why it is waiting.

## 5. Ship a version

```bash
kardinal ship demo --image ghcr.io/pnz1990/kardinal-test-app:<C7: tag> --wait
```

Quickstart's output ends with this command, with a real tag of `kardinal-test-app`.
`ship` creates a version and, with `--wait`, follows it, printing each environment's new state.
The new version supersedes the first one, which has not reached prod.

## 6. Watch it stop at the gate

Test and uat are verified within a minute or two. Prod has an approval gate, so the version
stops there and `ship` ends:

```text
<C7: ship --wait output ending at the gate>
```

```bash
echo $?
# 3
```

Exit code 3 means the version is held until a person acts: here, an approval. A CI job can act
on the code; [Wait for the outcome in CI](ci-integration.md#wait-for-the-outcome-in-ci) lists
them all.

Ask why it is waiting:

```bash
kardinal why demo prod
```

```text
<C7/#1736: why output for the demo's approval gate>
```

The last line is the command that gets past the gate. In the UI, prod's details show the same
gate under "Why it's waiting".
<!-- C7: if quickstart installs with ui.auth.tokenReview=true, the UI can record the approval:
     mention its Approve button here. Without it the UI refuses and points to kardinal approve. -->

## 7. Approve

```bash
kardinal approve demo prod
```

```text
<#1736: the confirmation prompt and its answer>
```

The gate passes. Prod's changes go in without a pull request (`approval: auto`), so the version
goes live in prod within a minute or two.

```bash
kardinal status demo
```

```text
<C7: status with prod Verified>
```

## Clean up

```bash
kind delete cluster --name kardinal
```

Your fork keeps the commits and pull requests; delete it on GitHub when you are done.

## Next steps

- Review prod's changes as pull requests: set `approval: pr-review` on prod with
  `kubectl edit pipeline demo`, and ship again. Prod then opens a pull request in your fork, with
  the version's provenance and every gate's result in its body, and goes live when you merge it
  ([PR evidence](pr-evidence.md)).
- [Your first pipeline](first-pipeline.md): set it up against your own GitOps repository
- [Concepts](concepts.md): versions, pipelines, environments and gates
- [Require approvals](approvals.md): who may approve, how many, and from the CLI or the UI
- [Add to CI](ci-integration.md#wait-for-the-outcome-in-ci): ship from your build and act on the exit code
