## kardinal policy list

List PolicyGates

### Synopsis

List PolicyGate templates: every one, or those the controller attaches to
<pipeline>'s environments. CEL is the controller's syntax check (valid,
invalid, or - not checked yet); LAST-EVALUATED is the newest evaluation of the
template's per-Bundle instances.

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
kardinal policy list [<pipeline>] [flags]
```

### Examples

```
  kardinal policy list
  kardinal policy list my-app
```

### Options

```
  -h, --help                        help for list
      --pipeline string             Show only the gates attached to this pipeline (or give it as the argument)
      --policy-namespaces strings   Namespaces the controller reads org PolicyGates from (its --policy-namespaces flag); a Pipeline's spec.policyNamespaces and its own namespace are read as well (default [platform-policies])
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates

