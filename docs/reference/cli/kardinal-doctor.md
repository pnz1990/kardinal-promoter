## kardinal doctor

Run pre-flight checks to verify the cluster is correctly configured

### Synopsis

Check that kardinal can work in this cluster: the controller answers, the
CRDs are installed, kro and its Graph CRD run, the controller has an SCM
token, and promotion receipts do not outgrow etcd while retention is off.
--pipeline also checks one Pipeline. Run it first when something is wrong.

PASS  Controller reachable      kardinal-version ConfigMap in the controller namespace
  PASS  CRDs installed            every kardinal.io/v1alpha1 resource served
  PASS  kro running               kro controller pod in kro-system
  PASS  kro Graph CRD installed   kro.run/v1alpha1 graphs registered
  PASS  GitHub token              GITHUB_TOKEN set on the controller Deployment
  PASS  Policy namespaces shared  every spec.policyNamespaces entry agreed

Each check is PASS, WARN or FAIL (on a terminal a green ✓, an ochre ! or a
garnet ✕), or INFO (i) when it cannot tell yet: run doctor again. The
summary counts the INFO checks as "to run again"; they do not fail doctor.

The token check is named after the controller's --scm-provider (GitHub token,
GitLab token, Forgejo token, Gitea token, Bitbucket token or Azure DevOps
token), and is "SCM token" when doctor finds no controller Deployment.

Use --controller-namespace when kardinal-promoter is installed in a namespace
other than kardinal-system. --pipeline checks a Pipeline in the current
namespace (-n, else the kubeconfig context's namespace). For each of its
environments whose health check reads a remote cluster
(health.kubeconfigSecretRef) it also reads the kubeconfig Secret (doctor
needs get on it), and asks that cluster, as the kubeconfig's identity,
whether it may get the object the health check names and, with
health.refresh on, patch it: a missing patch is a WARN, because the
refresh is then refused and promotions wait for the GitOps tool's poll.

The policy namespaces check lists the Pipelines whose spec.policyNamespaces
names a namespace that has not agreed to share its PolicyGates (the
condition PolicyNamespacesShared=False), with those namespaces: a WARN
against a v0.13 controller, which still reads their gates, and a FAIL against
v0.14 and later, which refuse them (their new versions wait); a release
candidate or a build after a release tag counts as that release. Run it
before upgrading to v0.14. Against a controller older than v0.13, which does
not check, it is a WARN saying so: upgrade to v0.13 and run it again. Right
after such an upgrade the new controller's Pod is Ready a few seconds before
it takes over, and only the controller in charge records its version: while
the controller Deployment is rolling out (or finished in the last 5 minutes)
a later release than that version, or a build that is not a release, the
check is an INFO (upgrade in progress) instead. Run it again once kardinal
version shows the new controller. Outside a rollout it is the WARN, naming
what the Deployment runs when that differs; a rollout that stalled
(Progressing=False, such as ProgressDeadlineExceeded) is not in progress,
and the WARN says "rollout stuck" with the reason. A Pipeline that names
spec.policyNamespaces but that the controller has not checked for its
current generation (no PolicyNamespacesShared condition of that generation:
not reconciled yet after an upgrade, whatever its Ready says) is "not
checked yet", an INFO when nothing else is found: run doctor again in a
minute. It
reads the condition as the controller last wrote it (the controller reads
the named Namespaces again every 5 minutes), in every namespace or, when you
may not list Pipelines cluster-wide, in the current one: a clean result
there is a WARN, since the other namespaces were not checked.

Use 'kardinal doctor' as the first troubleshooting step.

```
kardinal doctor [flags]
```

### Examples

```
  kardinal doctor
  kardinal doctor --pipeline my-app -n team-a
```

### Options

```
      --controller-namespace string   Namespace kardinal-promoter is installed in (default "kardinal-system")
  -h, --help                          help for doctor
      --pipeline string               Also check health of this Pipeline (optional)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

