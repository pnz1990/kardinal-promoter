## kardinal doctor

Run pre-flight checks to verify the cluster is correctly configured

### Synopsis

Run pre-flight checks for kardinal-promoter:

  ✅ Controller reachable      version ConfigMap found
  ✅ CRDs installed            kardinal.io resource groups registered
  ✅ krocodile running         graph-controller pod in kro-system
  ✅ krocodile CRDs installed  experimental.kro.run groups registered
  ✅ GitHub token              github-token secret present

Use 'kardinal doctor' as the first troubleshooting step.

```
kardinal doctor [flags]
```

### Options

```
  -h, --help              help for doctor
      --pipeline string   Also check health of this Pipeline (optional)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

