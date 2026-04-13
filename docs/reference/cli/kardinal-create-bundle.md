## kardinal create bundle

Create a Bundle to trigger promotion through a Pipeline

### Synopsis

Create a Bundle to trigger promotion through a Pipeline.

The pipeline name is a required positional argument.
Specify one or more container images with --image.

```
kardinal create bundle <pipeline> [flags]
```

### Options

```
  -h, --help                help for bundle
      --image stringArray   Container image reference (can be specified multiple times)
      --type string         Bundle type: image, config, or mixed (default "image")
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "/Users/rrroizma/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal create](kardinal_create.md)	 - Create kardinal resources

