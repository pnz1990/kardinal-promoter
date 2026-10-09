## kardinal reject

Reject a Bundle: it is never promoted again, and rollback never picks it

### Synopsis

Reject a Bundle.

Sets spec.rejected on the Bundle with your Kubernetes username (read from the
API server with a SelfSubjectReview, as kubectl auth whoami does) and the
reason. The Bundle turns Rejected, whatever its phase:

  - no new PromotionStep is created for it;
  - its steps that have not delivered the change (Pending, Promoting, or
    WaitingForMerge with the PR still open) fail, and their PRs are closed;
  - a step whose change is live (HealthChecking, or a PR that merged) keeps
    going and is health-checked;
  - kardinal rollback, onHealthFailure=rollback and kardinal promote never
    pick it or any Bundle carrying its images or config commit, and a
    Subscription creates no Bundle for them.

Rejecting is final: spec.rejected cannot be changed or removed. It does not
revert an environment that already runs the Bundle; roll that environment
back with kardinal rollback. See docs/rollback.md.

```
kardinal reject <bundle> [flags]
```

### Options

```
  -h, --help            help for reject
      --reason string   Why the Bundle is rejected (required)
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

