## kardinal init

Interactive wizard to generate a Pipeline YAML

### Synopsis

kardinal init guides you through creating a Pipeline CRD YAML.

It prompts for application name, namespace, environments, Git repo, and
update strategy, then writes a ready-to-apply pipeline.yaml.

Example:
  kardinal init
  kubectl apply -f pipeline.yaml

```
kardinal init [flags]
```

### Options

```
  -h, --help            help for init
  -o, --output string   Output file (default: pipeline.yaml)
      --stdout          Print to stdout instead of writing a file
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "/Users/rrroizma/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

