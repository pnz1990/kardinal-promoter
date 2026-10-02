## kardinal status

Show controller health or per-pipeline in-flight promotion details

### Synopsis

Show the health of the kardinal controller and cluster resource summary.

When called without arguments: displays the controller version (the
kardinal-version ConfigMap in --controller-namespace), the pipeline count with
any Degraded pipelines, and the bundle count (active = Available or Promoting).

When called with a pipeline name: shows in-flight promotion details for that
pipeline — the current bundle per environment (the newest bundle that is not
Superseded and has a PromotionStep there, or a gate instance there and has not
failed; see kardinal explain), its PromotionSteps (one row per environment it
has a step in, ▶ marking a step that is Promoting, WaitingForMerge or
HealthChecking, with the Bundle each row belongs to, the step it is on, and the
last 40 characters of the step's PR URL, open or merged; REGION is - unless the
step was created by a Graph built before multi-region fan-out was removed;
spec.regions is deprecated), the Bundle deployed in every environment (the
one whose change landed there last, as kardinal rollback judges it, with its
image tags or config commit; "none" when no change has landed yet), and the
PolicyGates holding it back (with their CEL expression cut to 40 characters,
current reason and when each was last checked). A gate is listed as blocking only while it holds the bundle back: it
is not ready and either every upstream environment is Verified for that bundle
and the bundle has no PromotionStep in the gate's environment yet, or the
bundle's Pending PromotionStep there waits on it. A gate of an environment the
bundle has not reached yet is not listed. This is the first command to run
when a promotion is stuck.

Examples:
  # Cluster-level summary
  kardinal status

  # Per-pipeline in-flight view
  kardinal status nginx-demo

For detailed gate diagnostics, use 'kardinal explain <pipeline>'.
For step-level log output, use 'kardinal logs <pipeline>'.

```
kardinal status [pipeline] [flags]
```

### Options

```
      --controller-namespace string   Namespace kardinal-promoter is installed in (default "kardinal-system")
  -h, --help                          help for status
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

