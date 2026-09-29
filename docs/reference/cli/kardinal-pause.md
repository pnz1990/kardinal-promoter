## kardinal pause

Pause a pipeline: no new promotion steps start, in-flight ones hold at the next safe point

### Synopsis

Pause a pipeline.

Sets spec.paused on the Pipeline and creates the freeze PolicyGate
freeze-<pipeline>. While the pipeline is paused, no PromotionStep leaves
Pending and a step that is still preparing its change (clone, update,
commit, open PR) holds before its next step. A step that is waiting for a
PR merge or running its health check finishes, so a merged change is never
left unverified. Resume with: kardinal resume <pipeline>.

```
kardinal pause <pipeline> [flags]
```

### Options

```
  -h, --help   help for pause
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

