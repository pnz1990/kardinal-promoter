## kardinal rollback

Roll back a pipeline environment to a previous Bundle

### Synopsis

Roll back a pipeline environment to a previous Bundle.

Without --to, the target is the most recent Bundle, other than the one deployed
now, that was Verified in the environment and deploys different artifacts. A
Bundle that an earlier rollback in the environment rolled back from is skipped.
With --to, the named Bundle must belong to the pipeline, carry images or a
config commit, differ from what is deployed now, and have been Verified in the
environment.

Creates a new Bundle that copies the target's images and config ref, sets
spec.provenance.rollbackOf to the target and intent.targetEnvironment to the
environment. An image the deployed Bundle changed and the target does not name
gets the version from the newest earlier Bundle Verified in the environment;
when there is none, the rollback is refused and names the image. The rollback
goes through the same PolicyGates and PR flow as any Bundle, and through every
environment upstream of the target first.

Config and mixed Bundles get back their config commit the same way. Without
--to, a mixed Bundle goes back to the newest earlier images and config commit,
whichever Bundles deployed them. An image or config Bundle goes back to the
newest earlier images or config commit, also when a mixed Bundle deployed them,
and only those: the rest stays as deployed. --to a Bundle whose type cannot
deploy what the deployed Bundle changed is refused. See docs/rollback.md.

```
kardinal rollback <pipeline> [flags]
```

### Options

```
      --env string   Target environment to roll back (required)
  -h, --help         help for rollback
      --to string    Specific Bundle name to roll back to
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

