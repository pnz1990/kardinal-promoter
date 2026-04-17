## kardinal status

Show controller health and cluster resource summary

### Synopsis

Show the health of the kardinal controller and a summary of managed resources.

Displays:
  - Controller pod status and version
  - Count of Pipelines and active Bundles
  - Any pipelines currently in a failed/stuck state

For detailed diagnostics, use 'kardinal doctor'.

```
kardinal status [flags]
```

### Options

```
  -h, --help   help for status
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
