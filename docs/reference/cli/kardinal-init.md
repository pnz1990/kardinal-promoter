## kardinal init

Interactive wizard to generate a Pipeline YAML and scaffold the GitOps repo

### Synopsis

kardinal init guides you through creating a Pipeline CRD YAML.

It prompts for application name, namespace, environments, Git repo, and
update strategy, then writes a ready-to-apply pipeline.yaml.

Use --scaffold-gitops to also create the GitOps repository branch structure:
  environments/<env>/kustomization.yaml for each environment.

Use --demo to scaffold with the kardinal-test-app placeholder image.

Example:
  kardinal init
  kardinal init --scaffold-gitops --gitops-dir ./my-gitops
  kardinal init --demo --scaffold-gitops
  kubectl apply -f pipeline.yaml

```
kardinal init [flags]
```

### Options

```
      --demo                Scaffold with kardinal-test-app placeholder image (implies --scaffold-gitops)
      --gitops-dir string   Directory for the GitOps scaffold (default: .gitops) (default ".gitops")
  -h, --help                help for init
  -o, --output string       Output file (default: pipeline.yaml)
      --scaffold-gitops     Create GitOps repo structure (environments/<env>/kustomization.yaml)
      --stdout              Print to stdout instead of writing a file
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

