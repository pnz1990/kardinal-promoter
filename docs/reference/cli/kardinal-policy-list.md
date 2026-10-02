## kardinal policy list

List PolicyGates

### Synopsis

List PolicyGate templates.

Without --pipeline, lists every template in every namespace. With --pipeline,
lists the templates the controller attaches to that pipeline's environments.

The CEL column is the controller's syntax check of the expression: valid,
invalid (see kubectl describe), or - when not checked yet.

The controller evaluates the per-Bundle instances the Graph creates from a
template, never the template, so LAST-EVALUATED is the newest evaluation of
the template's instances (with --pipeline, of that pipeline's instances), or
- when none has been evaluated. An instance records its template's name and
namespace (kardinal.io/gate-template and kardinal.io/gate-template-namespace),
so a template in an org policy namespace or in spec.policyNamespaces counts the
instances in every Pipeline's namespace.

```
kardinal policy list [flags]
```

### Options

```
  -h, --help                        help for list
      --pipeline string             Show only the gates attached to this pipeline
      --policy-namespaces strings   Namespaces the controller reads org PolicyGates from (its --policy-namespaces flag); a Pipeline's spec.policyNamespaces and its own namespace are read as well (default [platform-policies])
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

