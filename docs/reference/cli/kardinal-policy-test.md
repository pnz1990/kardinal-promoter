## kardinal policy test

Validate PolicyGate YAML syntax and dry-run CEL expressions

### Synopsis

Validate every PolicyGate in a YAML file (multiple documents are fine).

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

Example:
  kardinal policy test policy-gates.yaml

```
kardinal policy test <file> [flags]
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

