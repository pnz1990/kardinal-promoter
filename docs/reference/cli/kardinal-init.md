## kardinal init

Write a Pipeline for your GitOps repository, from flags or a few questions

### Synopsis

Write the smallest Pipeline that promotes your environments, defaults left
out: from flags (--repo makes it non-interactive), else the wizard asks for
what the flags do not say. With a cluster it finds the Argo CD Applications
or Flux Kustomizations to check health with; --create-secret makes the git
Secret, --apply applies the Pipeline.

With --repo, init asks nothing: --name (default my-app), --envs (default
test,uat,prod, in promotion order), --branch (default main), --strategy
(kustomize, the default, or helm), --prod-approval (pr-review, the default,
or auto: the last environment's approval) and --secret (default
github-token) say the rest, and -n the namespace. Without --repo it asks for
each of them that no flag gives, on a terminal or one answer per line from a
pipe.

It writes the smallest Pipeline that says it, as examples/minimal does: a
default (branch main, kustomize, approval auto, the default health check) is
left out (also namespace default, unless -n gives it). The name, namespace and environments must
be DNS-1123 labels, --repo an https or ssh git URL (https://host/owner/repo,
ssh://git@host/owner/repo or git@host:owner/repo.git) with no spaces, query
or fragment, and --branch a git branch name; every value is written as a
YAML scalar and the YAML is decoded back and compared, so no value can add
a field. When a cluster can be reached it looks, for each environment, for
an Argo CD Application <name>-<env> in argocd or a Flux Kustomization
<name>-<env> in flux-system, and sets health type argocd or flux where it
finds one (--no-detect skips this). The Pipeline is checked as kardinal
validate checks it before anything is written. Running init again with the
same answers changes nothing ("unchanged") and succeeds; a file with
another Pipeline is not overwritten without --force.

An http:// --repo is refused unless --insecure-http is given: the controller
authenticates to the repository with the git token, and over http the token
crosses the network in clear text. --insecure-http is for a git server
without TLS, such as a local or in-cluster Forgejo or Gitea; init then warns
on stderr. The host is not considered (localhost in a Pipeline is the
controller's own loopback, and a private name or address is no guarantee),
and the other rules still apply.

--create-secret creates the git Secret (key token) from $GITHUB_TOKEN, else
from gh auth token, labelled kardinal.io/referenceable=true so the Pipeline
may use it; an existing Secret is kept (and labelled). --apply creates the
Pipeline; one of the same name and spec is unchanged, one with another spec
is replaced only with --force.

Use --scaffold-gitops to also create the GitOps repository structure:
  environments/<env>/kustomization.yaml for each environment.
The scaffold is Kustomize-only; it is skipped for the helm strategy.

Use --demo to scaffold with the kardinal-test-app placeholder image.

```
kardinal init [flags]
```

### Examples

```
  kardinal init --name my-app --repo https://github.com/myorg/gitops
  kardinal init --name my-app --repo https://github.com/myorg/gitops --envs dev,prod --create-secret --apply
  kardinal init
  kardinal init --demo --scaffold-gitops && kubectl apply -f pipeline.yaml
```

### Options

```
      --apply                  Apply the Pipeline to the cluster
      --branch string          Base branch of the repository (default main)
      --create-secret          Create the git Secret from $GITHUB_TOKEN, else gh auth token, labelled for the Pipeline to use
      --demo                   Scaffold with kardinal-test-app placeholder image (implies --scaffold-gitops)
      --envs string            Environments in promotion order, comma-separated (default test,uat,prod)
  -f, --file string            File to write the Pipeline YAML to (default "pipeline.yaml")
      --force                  Overwrite the file, and replace a Pipeline of the same name with --apply
      --gitops-dir string      Directory for the GitOps scaffold (default ".gitops")
  -h, --help                   help for init
      --insecure-http          Allow an http:// repository URL, for a git server without TLS; the git token is then sent in clear text
      --name string            Application and Pipeline name (default my-app)
      --no-detect              Do not look in the cluster for Argo CD Applications or Flux Kustomizations
      --prod-approval string   Approval of the last environment: pr-review (default) or auto
      --repo string            GitOps repository https:// or ssh URL; with it, init asks nothing
      --scaffold-gitops        Create GitOps repo structure (environments/<env>/kustomization.yaml)
      --secret string          Name of the Secret holding the git token (default "github-token")
      --stdout                 Print the Pipeline YAML to stdout instead of writing a file (a scaffold still writes its files)
      --strategy string        Update strategy: kustomize (default) or helm
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

