## kardinal override

Force-pass a PolicyGate with a mandatory audit record

### Synopsis

Override a PolicyGate for a specific pipeline stage.

The override is time-limited and creates a mandatory audit record in
PolicyGate.spec.overrides[]. The gate passes immediately without evaluating
the CEL expression until the override expires.

Expired overrides stay in spec.overrides[] as an audit record for as long
as the Bundle exists. Deleting a Bundle deletes its gate instances and their
overrides, and the Pipeline's historyLimit cleanup deletes old finished
Bundles (50 by default). Use --expires-in to control the override window
(default: 1h).

--gate takes the gate template name (for example no-weekend-deploys). The
override is recorded on the instances of that gate that the Pipeline's
in-progress Bundles have for --stage (every stage when --stage is not set),
so run it while the Bundle waits on the gate. Instances of Verified, Failed,
Superseded and Rejected Bundles are left alone, because no promotion waits on them; if
a Failed Bundle resumes, run the override again. The name of one gate
instance, as kubectl get policygates shows it, is also accepted; that instance
alone gets the override.

Example:
  kardinal override my-app --stage prod --gate no-weekend-deploys \
    --reason "P0 hotfix — incident #4521"

```
kardinal override <pipeline> --stage <environment> --gate <gate-name> --reason <text> [--expires-in <duration>] [flags]
```

### Options

```
      --expires-in string   How long the override is active (Go duration, e.g. 1h, 4h, 30m) (default "1h")
      --gate string         PolicyGate name to override
  -h, --help                help for override
      --reason string       Mandatory justification for the override (audit record)
      --stage string        Environment (stage) name the override applies to
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

