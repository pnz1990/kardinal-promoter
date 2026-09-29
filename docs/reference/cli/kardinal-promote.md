## kardinal promote

Promote the Bundle verified upstream into an environment

### Synopsis

Promote the newest Bundle that is Verified in every upstream environment
into the given environment.

Creates a Bundle that copies that Bundle's images, config ref and provenance,
with intent.targetEnvironment set to the environment. PolicyGates and approval
mode apply as configured. The command refuses, and creates nothing, when no
Bundle is Verified upstream yet, when that Bundle is already Verified or being
promoted in the environment, or when a newer Bundle is still promoting (the new
Bundle would supersede it). The first environment of a pipeline has nothing
upstream; use kardinal create bundle for it.

```
kardinal promote <pipeline> --env <environment> [flags]
```

### Options

```
  -e, --env string   Target environment name (required)
  -h, --help         help for promote
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

