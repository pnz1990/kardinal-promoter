## kardinal quickstart

Install kardinal on a local cluster and promote a first version

### Synopsis

Set up a demo on a local cluster (kind, k3d, minikube, Docker or Rancher
Desktop; any other context is refused first): kro, Argo CD, kardinal, the demo's
git repository (--local: on a git server in the cluster, no account needed;
--repo: your GitHub fork), a Pipeline gated on prod, and a first version,
followed until prod waits for your approval. A re-run changes nothing.

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

With --local instead of --repo, the demo's repository is on a git server in
the cluster, and no GitHub account or token is needed. First it checks, and
stops with nothing changed: helm is 3.13 or later (it labels its releases),
the namespace kardinal-git is its own or absent, the cluster has a default
StorageClass, and a kardinal-promoter already installed is one for the local
git server. Then it marks the install: an install ID, the label
kardinal.io/quickstart-id of every namespace it creates (kardinal-git,
kro-system, argocd, kardinal-system, kardinal-test-app-<env>; one that
exists is left unmarked), of the ApplicationSet and its Applications, and of
the Helm releases of kro and kardinal-promoter it installs (helm --labels),
recorded in the ConfigMap
kardinal-git/kardinal-quickstart. Before kardinal it installs, in the
namespace kardinal-git:
  a single-pod Forgejo (the image hack/e2e/versions.env pins, by digest),
    with a 1Gi volume of the default StorageClass (it stops before it creates
    anything without one), behind a ClusterIP Service: reachable only inside
    the cluster, with no port published on this machine
  its user kardinal (a random password nobody keeps) and the repository
    kardinal/kardinal-demo, seeded with kardinal-demo's environments by
    kubectl exec of a script that holds no secret
Then the controller's Secret kardinal-system/kardinal-scm-token and the git
Secret default/git-token each get a token of that user (scopes
write:repository and write:issue), read from kubectl exec's output and
written straight into the Secret: never printed, passed as an argument or
written to a file. kardinal-promoter is installed with scm.provider=forgejo,
the server as scm.apiURL and scm.allowedRepositories limited to the demo
repository; one already installed for another git server is refused. The
ApplicationSet and the Pipeline use the repository's in-cluster URL.
kubectl -n kardinal-git port-forward svc/kardinal-git 3000 shows it in a
browser at http://127.0.0.1:3000 (the forward listens on 127.0.0.1).

Then: kardinal approve demo prod ships it to prod; kardinal dashboard opens
the UI; the Next lines show the command that ships the next version. To get
PR evidence for prod, set its approval to pr-review in the Pipeline.

kardinal quickstart --delete, behind the same local-context check, deletes
the demo and nothing else: the demo's versions first (it waits up to 2m for
the controller to close their open PRs, and stops there if they are not
gone), then the Pipeline demo, the gate prod-approval and the ApplicationSet
(Argo CD deletes its Applications), each only when quickstart applied it
(field manager kardinal-quickstart), and the Secret github-token only when
quickstart created it (label kardinal.io/quickstart) and no other Pipeline
uses it. Without a Pipeline demo, only versions with that label go. Do
not add the label by hand: --delete deletes what carries it. It keeps kro, Argo CD, kardinal-promoter with its
token Secret, and the kardinal-test-app-<env> namespaces, and prints the
command that deletes those. It asks first on a terminal (--yes does not).
A re-run deletes nothing.

kardinal quickstart --local --delete first prints everything it will remove
and keep, then asks; without a terminal it goes ahead only with --yes. It
removes only what carries the install ID of the record in kardinal-git (the
namespace's ID and the record's must match). It deletes the demo the same
way, then the namespaces kardinal-test-app-<env> it created (once Argo CD
has deleted their Applications) and each of kardinal-promoter, kro (their
Helm release carries the ID) and Argo CD (its namespace does) with its CRDs
and the namespace it created for it: helm uninstall, Argo CD's release
manifests. A part stays, CRDs and all, while any object of its API groups
(kardinal.io; argoproj.io; kro.run and internal.kro.run), in any namespace,
is not the demo's: deleting a CRD deletes every object of its kind, and the
reason is printed. Then it deletes the namespace kardinal-git with the git
server, its repository and the record.

```
kardinal quickstart --local | --repo <your fork of kardinal-demo> [flags]
```

### Examples

```
  kind create cluster --name kardinal
  kardinal quickstart --local
  kardinal approve demo prod
  kardinal quickstart --local --delete

  # With your GitHub fork of kardinal-demo instead (GITHUB_TOKEN or gh auth login):
  kardinal quickstart --repo https://github.com/<you>/kardinal-demo
  kardinal quickstart --delete
```

### Options

```
      --chart string           The kardinal-promoter Helm chart, an OCI reference or a directory (default: the published chart, ghcr.io/pnz1990/charts)
      --chart-version string   Chart version (default: this CLI's version; the newest for a development build)
      --delete                 Delete the demo quickstart applied (its versions, Pipeline, gate, ApplicationSet and git Secret); keep kro, Argo CD and kardinal
  -h, --help                   help for quickstart
      --image string           The first version to ship (default "ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0")
      --local                  Keep the demo's git repository on a git server in the cluster (Forgejo, reachable only inside it), seeded with kardinal-demo: no GitHub account, fork or token; with --delete, also remove the server and what quickstart --local installed
      --no-argocd              Do not install Argo CD (the cluster has one)
      --no-wait                Do not follow the first version
      --repo string            Your GitHub fork of github.com/pnz1990/kardinal-demo (the token needs contents and pull requests read and write on it); another git host needs --set scm.provider
      --set stringArray        A Helm value for the kardinal-promoter chart (key=value; repeatable)
      --timeout duration       How long to follow the first version (default 15m)
      --yes                    Do not ask for confirmation on a terminal
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: budget, create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

