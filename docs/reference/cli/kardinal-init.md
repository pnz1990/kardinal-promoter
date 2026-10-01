## kardinal init

Interactive wizard to generate a Pipeline YAML and scaffold the GitOps repo

### Synopsis

kardinal init guides you through creating a Pipeline CRD YAML.

It prompts for application name, namespace, environments, Git repo (an
https:// URL, required), and update strategy (kustomize or helm), then writes
a ready-to-apply pipeline.yaml (or --file).

Use --scaffold-gitops to also create the GitOps repository structure:
  environments/<env>/kustomization.yaml for each environment.
The scaffold is Kustomize-only; it is skipped for the helm strategy.

Use --demo to scaffold with the kardinal-test-app placeholder image.

Example:
  kardinal init
  kardinal init --file deploy/pipeline.yaml
  kardinal init --scaffold-gitops --gitops-dir ./my-gitops
  kardinal init --demo --scaffold-gitops
  kubectl apply -f pipeline.yaml

```
kardinal init [flags]
```

### Options

```
      --demo                Scaffold with kardinal-test-app placeholder image (implies --scaffold-gitops)
      --file string         File to write the Pipeline YAML to (default "pipeline.yaml")
      --gitops-dir string   Directory for the GitOps scaffold (default ".gitops")
  -h, --help                help for init
      --scaffold-gitops     Create GitOps repo structure (environments/<env>/kustomization.yaml)
      --stdout              Print the Pipeline YAML to stdout instead of writing a file (a scaffold still writes its files)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

