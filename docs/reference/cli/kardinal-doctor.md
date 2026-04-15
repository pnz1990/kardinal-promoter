## kardinal doctor

Run pre-flight checks to verify the cluster is correctly configured

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

