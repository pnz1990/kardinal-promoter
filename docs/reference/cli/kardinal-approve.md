## kardinal approve

Approve a Bundle for promotion, bypassing upstream gate requirements

### Synopsis

Approve a Bundle for promotion to a specific environment.

Approval is expressed by patching the Bundle with the label
kardinal.io/approved=true (and optionally kardinal.io/approved-for=<env>).

This is useful for hotfix deployments that must skip the normal
upstream soak / gate requirements.

Example:
  kardinal approve nginx-demo-v1-29-0 --env prod

```
kardinal approve <bundle> [flags]
```

### Options

```
      --env string   Target environment to approve for (optional)
  -h, --help         help for approve
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "/Users/rrroizma/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

