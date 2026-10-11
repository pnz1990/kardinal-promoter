## kardinal approve

Approve (or reject) the version waiting for an environment's approval gates

### Synopsis

Record your approval (or, with --decision reject, a rejection) of the version
waiting to enter <env> of <pipeline>, in your Kubernetes name; give a Bundle
instead with kardinal approve <bundle> --env <env>. An approval gate passes
once enough allowed people approved and nobody rejected. --revoke deletes
your Approval.

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
decision it replaces yours. Your Approval is found by its labels and
spec.user, not by its name; if the name it would get is taken, the API
server generates one. An Approval belongs to its Bundle and is deleted
with it. See docs/approvals.md.

```
kardinal approve <pipeline> <env> [flags]
```

### Examples

```
  kardinal approve my-app prod --comment "checked the canary"

  # a named Bundle, the older form
  kardinal approve my-app-v1-5 --env prod

  # decline (a gate rejection, not kardinal reject)
  kardinal approve my-app prod --decision reject

  # take your decision back
  kardinal approve my-app prod --revoke
```

### Options

```
      --comment string    Note shown with the decision
      --decision string   approve or reject (default "approve")
  -e, --env string        Environment the approval is for (or give it after the pipeline)
  -h, --help              help for approve
      --revoke            Delete your Approval instead of recording one
      --version string    Approve only if this is the version waiting (the image tag, config commit or chart version)
      --yes               Do not ask for confirmation on a terminal
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

