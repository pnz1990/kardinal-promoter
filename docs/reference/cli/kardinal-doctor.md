## kardinal doctor

Run pre-flight checks to verify the cluster is correctly configured

### Synopsis

Check that kardinal can work in this cluster: the controller answers, the
CRDs are installed, kro and its Graph CRD run, and the controller has an SCM
token. --pipeline also checks one Pipeline. Run it first when something is
wrong.

PASS  Controller reachable      kardinal-version ConfigMap in the controller namespace
  PASS  CRDs installed            every kardinal.io/v1alpha1 resource served
  PASS  kro running               kro controller pod in kro-system
  PASS  kro Graph CRD installed   kro.run/v1alpha1 graphs registered
  PASS  GitHub token              GITHUB_TOKEN set on the controller Deployment

Each check is PASS, WARN or FAIL (on a terminal a green ✓, an ochre ! or a
garnet ✕).

The token check is named after the controller's --scm-provider (GitHub token,
GitLab token, Forgejo token, Gitea token, Bitbucket token or Azure DevOps
token), and is "SCM token" when doctor finds no controller Deployment.

Use --controller-namespace when kardinal-promoter is installed in a namespace
other than kardinal-system. --pipeline checks a Pipeline in the current
namespace (-n, else the kubeconfig context's namespace).

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
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

