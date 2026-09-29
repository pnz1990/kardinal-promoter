## kardinal doctor

Run pre-flight checks to verify the cluster is correctly configured

### Synopsis

Run pre-flight checks for kardinal-promoter:

  ✅ Controller reachable      kardinal-version ConfigMap in the controller namespace
  ✅ CRDs installed            every kardinal.io/v1alpha1 resource served
  ✅ kro running               kro controller pod in kro-system
  ✅ kro Graph CRD installed   kro.run/v1alpha1 graphs registered
  ✅ GitHub token              GITHUB_TOKEN set on the controller Deployment

Use --controller-namespace when kardinal-promoter is installed in a namespace
other than kardinal-system. --pipeline checks a Pipeline in the current
namespace (-n, else the kubeconfig context's namespace).

Use 'kardinal doctor' as the first troubleshooting step.

```
kardinal doctor [flags]
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
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

