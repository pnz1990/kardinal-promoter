## kardinal approve

Approve (or reject) a Bundle for an environment's approval gates

### Synopsis

Approve a Bundle for an environment.

Creates an Approval object for the Bundle and environment in your name: your
Kubernetes username and groups, read from the API server with a
SelfSubjectReview (as kubectl auth whoami does). The chart's
ValidatingAdmissionPolicy refuses an Approval in anyone else's name.

A PolicyGate with spec.approval on that environment counts the Approvals of
the Bundle: it is ready when its expression is true, at least
approval.required allowed people (approval.allowedUsers or allowedGroups)
approved, and none of them rejected. The gate then lets the environment's
PromotionStep start, for approval: auto environments as for pr-review ones.
Approving before the Bundle reaches the gate is fine.

  --decision reject   records a rejection: it blocks the gate.
  --revoke            deletes your Approval for the Bundle and environment.

Running approve again with the same decision does nothing; with another
decision it replaces yours. An Approval belongs to its Bundle and is deleted
with it. See docs/policy-gates.md (Approval gates).

```
kardinal approve <bundle> --env <environment> [flags]
```

### Options

```
      --comment string    Note shown with the decision
      --decision string   approve or reject (default "approve")
      --env string        Environment the approval is for (required)
  -h, --help              help for approve
      --revoke            Delete your Approval instead of recording one
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

