## kardinal policy test

Validate PolicyGate YAML syntax and dry-run CEL expressions

### Synopsis

Check every PolicyGate in a YAML file offline: each expression is compiled and
evaluated as the controller would, now, for a Bundle with no images, metrics or
history. PASS or FAIL per gate, UNKNOWN when it needs cluster data (use policy
simulate). It exits non-zero only when an expression does not compile.

Each expression is compiled with the controller's PolicyGate CEL environment,
then evaluated by the controller's reconciler against a local context: the
current time, a Bundle with no images or provenance, no metrics, no upstream
history and no change windows. The environment is the first entry of the
gate's kardinal.io/applies-to label. The file is -f <file>, as kardinal
validate takes it, or the argument.

With no cluster there is no upstream environment: bundle.upstreamSoakMinutes
is 0, as for a version that just reached the upstream, and when that decides the result (the gate
would pass, or fail, once soaked) a Note says so (upstream.<env>.* has no
entry, so such a gate is UNKNOWN). kardinal policy simulate
<pipeline> <env> --soak-minutes <n> evaluates the gate with a soak.

Results:
  PASS     the gate would allow promotion in that context
  FAIL     the gate would block promotion in that context
  UNKNOWN  the expression needs cluster data the local context lacks
           (metrics, upstream, bundle.pr); use 'kardinal policy simulate'

No cluster access is required. The command exits non-zero only when an
expression does not compile.

```
kardinal policy test -f <file> [flags]
```

### Examples

```
  kardinal policy test -f policy-gates.yaml
  kardinal policy test policy-gates.yaml
```

### Options

```
  -f, --file string   Path to the PolicyGate YAML file (or give it as the argument)
  -h, --help          help for test
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates

