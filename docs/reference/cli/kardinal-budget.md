## kardinal budget

Show an agent's budgets: what it has used, what waits, and the exemptions

### Synopsis

Show the budgets of agent <agent> (its AgentIdentity spec.budgets) as the controller
counts them in each ledger (status.usage): for each rule and limit, what is used
of it, the rolling window and when room frees; the versions that wait for room,
for the ones you may read; and the exemptions, in effect, expired or ignored.
-o json prints status.usage.

A budget rule limits the agent's versions a day (versionsPerDay), its
promotions at once (maxConcurrent), its fleet targets an hour
(fleetTargetsPerHour), the failures it leaves in place (maxUnavailable) and a
share of one fleet (fleetShare). A step of the agent's version beyond a limit
waits in Pending, never failed, until the ledger grants it; kardinal why says
so. The ledger is written by one controller of each installation; a missing
ledger means that controller does not count this agent yet (budgets off in
that release, or its writer is not running): kardinal doctor says which. A
person lets one version through with kardinal override <pipeline> <env>
--budget --reason <why>.

```
kardinal budget <agent> [flags]
```

### Examples

```
  kardinal budget fixer
  kardinal budget fixer --ledger kardinal-promoter
  kardinal budget fixer -o json
```

### Options

```
  -h, --help            help for budget
      --ledger string   Only this ledger (an installation's; default: every one)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: budget, create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

