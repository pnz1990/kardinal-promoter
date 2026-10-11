## kardinal quickstart

Install kardinal on a local cluster and promote a first version

### Synopsis

Set up a demo on a local cluster (kind, k3d, minikube, Docker or Rancher
Desktop; any other context is refused before anything is written): kro, Argo
CD, kardinal, the git Secret, the Argo CD Applications of your kardinal-demo
fork, a Pipeline with an approval gate on prod, and a first version, which it
follows until prod waits for your approval. A re-run changes nothing.

quickstart refuses, before it sends anything to a cluster, a kubeconfig
context whose name is not kind-*, k3d-*, minikube, docker-desktop or
rancher-desktop, or whose API server is not on this machine (loopback,
0.0.0.0, Docker Desktop's kubernetes.docker.internal; for minikube also its
drivers' networks 192.168.49.0/24, 192.168.39.0/24 and 192.168.59.0/24). It
also refuses to run while a variable is set that points helm, kubectl or
client-go elsewhere (HELM_KUBE*, such as HELM_KUBECONTEXT, and
KUBERNETES_MASTER), and refuses a context whose cluster has a proxy-url, or
whose server an environment proxy (HTTPS_PROXY, HTTP_PROXY) applies to: the
proxy decides where requests go. Go never proxies localhost and 127.0.0.1;
for 0.0.0.0, host.docker.internal or a minikube address, add the host to
NO_PROXY. There is no flag to override it, on purpose. Use
--context to pick a local context. helm and kubectl run on a kubeconfig file
with only that context, without KUBECONFIG or those variables, and kubectl
with KUBERC=off. Ctrl-C or SIGTERM stops the step that runs and removes
that file. What it
cannot see: a loopback port that tunnels to a remote cluster (kubectl
port-forward, ssh -L) looks local; do not point a local context at one.

On a local cluster it installs, each only when it is not there yet:
  kro (the version hack/install-kro.sh pins), with the Graph controller
  Argo CD (its release manifests, polling git every 10s instead of 2-3m;
    --no-argocd when the cluster has one, which keeps its own settings)
  kardinal-promoter (the chart of this CLI's version, in kardinal-system;
    --chart, --chart-version and --set for another)
Then it applies, in default (so no command needs -n):
  the git Secret github-token, from $GITHUB_TOKEN, else gh auth token: the
    token needs contents and pull requests read and write on --repo
  an Argo CD ApplicationSet kardinal-test-app in argocd: the Applications
    kardinal-test-app-<env> sync environments/<env> of --repo
  a PolicyGate prod-approval: prod waits for your approval (you, the user
    the cluster says you are; excludeAuthor is off)
  the Pipeline demo: test, uat and prod, all auto, prod behind the gate,
    health checked with the Applications
It ships the first version (--image) unless demo has one, and follows it
until prod waits for your approval (--no-wait does not follow). It does not
change your kubeconfig. A re-run changes nothing.

Then: kardinal approve demo prod ships it to prod; kardinal dashboard opens
the UI; the Next lines show the command that ships the next version. To get
PR evidence for prod, set its approval to pr-review in the Pipeline.

kardinal quickstart --delete, behind the same local-context check, deletes
the demo and nothing else: the demo's versions first (it waits up to 2m for
the controller to close their open PRs, and stops there if they are not
gone), then the Pipeline demo, the gate prod-approval and the ApplicationSet
(Argo CD deletes its Applications), each only when quickstart applied it
(field manager kardinal-quickstart), and the Secret github-token unless
another Pipeline uses it. It keeps kro, Argo CD, kardinal-promoter with its
token Secret, and the kardinal-test-app-<env> namespaces, and prints the
command that deletes those. It asks first on a terminal (--yes does not).
A re-run deletes nothing.

```
kardinal quickstart --repo <your fork of kardinal-demo> [flags]
```

### Examples

```
  kind create cluster --name kardinal
  kardinal quickstart --repo https://github.com/<you>/kardinal-demo
  kardinal approve demo prod
  kardinal quickstart --delete
```

### Options

```
      --chart string           The kardinal-promoter Helm chart, an OCI reference or a directory (default: the published chart, ghcr.io/pnz1990/charts)
      --chart-version string   Chart version (default: this CLI's version; the newest for a development build)
      --delete                 Delete the demo quickstart applied (its versions, Pipeline, gate, ApplicationSet and git Secret); keep kro, Argo CD and kardinal
  -h, --help                   help for quickstart
      --image string           The first version to ship (default "ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0")
      --no-argocd              Do not install Argo CD (the cluster has one)
      --no-wait                Do not follow the first version
      --repo string            Your fork of github.com/pnz1990/kardinal-demo (the token needs contents and pull requests read and write on it)
      --set stringArray        A Helm value for the kardinal-promoter chart (key=value; repeatable)
      --timeout duration       How long to follow the first version (default 15m)
      --yes                    Do not ask for confirmation on a terminal
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

