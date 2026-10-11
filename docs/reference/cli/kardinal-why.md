## kardinal why

Say why an environment waits, and what moves it on

### Synopsis

Say, for <env> of <pipeline> (or each environment), in a sentence what it
waits for, then the version, the gate or PR, and the command that moves it
on. -o wide prints kardinal explain's table instead; -o json and yaml print
explain's entries. -w refreshes every 3 seconds.

why answers, per environment, in one sentence what it waits for, then the
details and the command that moves it on:

  prod is waiting for your approval.
    Version  1.5.0 (my-app-x7k2p)
    Gate     cab · 0 of 1 approvals
    Next     kardinal approve my-app prod

The cases, in the order it checks them: the pipeline is paused (Next:
kardinal resume); the environment is held (on what, by whom, why; Next:
kardinal unhold); a gate holds it (an approval: how many of how many; any
other gate: its message, Rule its expression, Now its last evaluation,
Opens when it opens and how often it is rechecked; Next: kardinal approve or
kardinal override); its PR waits to be merged (PR and its URL); the version
failed (Why the step's message; Next: kardinal logs, or roll back); it is
promoting; it waits for an upstream environment; it has no version yet; or
nothing is waiting ("prod runs 1.5.0 and nothing is waiting.").

-o wide prints kardinal explain's table (every step and gate of the current
version, with kinds and expressions), with the commands that get past a
gate on stderr; -o json and yaml print explain's entries.

```
kardinal why <pipeline> [<env>] [flags]
```

### Examples

```
  kardinal why my-app prod
  kardinal why my-app
  kardinal why my-app prod -o wide
```

### Options

```
      --color        With -o wide: force ANSI color output (auto-detected when TTY)
  -e, --env string   Only this environment (or give it after the pipeline)
  -h, --help         help for why
  -w, --watch        Refresh every 3 seconds
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

