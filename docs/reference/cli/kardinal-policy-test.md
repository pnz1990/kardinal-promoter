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
gate's kardinal.io/applies-to label.

Results:
  PASS     the gate would allow promotion in that context
  FAIL     the gate would block promotion in that context
  UNKNOWN  the expression needs cluster data the local context lacks
           (metrics, upstream, bundle.pr); use 'kardinal policy simulate'

No cluster access is required. The command exits non-zero only when an
expression does not compile.

```
kardinal policy test <file> [flags]
```

### Examples

```
  kardinal policy test policy-gates.yaml
```

### Options

```
  -h, --help   help for test
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates

