## kardinal policy simulate

Simulate PolicyGate evaluation for a hypothetical promotion context

### Synopsis

Simulate PolicyGate evaluation.

Selects the PolicyGates the controller attaches to the environment (the
pipeline's namespace plus the policy namespaces, matched by the
kardinal.io/applies-to label) and evaluates each one with the controller's
PolicyGate reconciler, against a Bundle that has promoted through every
upstream environment. Metrics, change windows and promotion history are read
from the cluster; nothing is written to it.

--time is UTC: a weekday and an hour ("Saturday 3pm", "tue 10:00",
"15 Friday") or an RFC 3339 timestamp. The weekday is its next occurrence
(today counts). Without --time the current time is used.

--soak-minutes is the soak time of every upstream environment
(upstream.<env>.soakMinutes and bundle.upstreamSoakMinutes).

A blocked gate shows the next hour, within 7 days, at which it would pass with
the same inputs. Gates that do not depend on time show no window.

Example:
  kardinal policy simulate --pipeline nginx-demo --env prod --time "Saturday 3pm"
  # RESULT: BLOCKED
  # Blocked by: no-weekend-deploys

```
kardinal policy simulate [flags]
```

### Options

```
      --env string                  Environment name (required)
  -h, --help                        help for simulate
      --pipeline string             Pipeline name (required)
      --policy-namespaces strings   Namespaces the controller reads org PolicyGates from (its --policy-namespaces flag); a Pipeline's spec.policyNamespaces takes precedence (default [platform-policies])
      --soak-minutes int            Simulated soak time of each upstream environment, in minutes
      --time string                 Simulated UTC time (e.g. "Saturday 3pm", "Tuesday 10:00", RFC 3339)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates

