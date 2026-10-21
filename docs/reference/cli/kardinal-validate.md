## kardinal validate

Validate kardinal YAML before applying to the cluster

### Synopsis

Check kardinal.io YAML offline: every object against its CRD's schema (built
into the CLI); Pipelines and PolicyGates also for their environments, the
dependency graph, unimplemented fields and gate expressions. It exits 1
with the errors; kubectl apply --dry-run=server also runs the CRDs' CEL
rules.

Validate kardinal.io YAML without connecting to the cluster. The file may
hold several documents; each kardinal.io object is checked. Other objects (a
Namespace, another API group's Pipeline) are skipped with a note.

Checks:
  - Every kardinal.io document, against the OpenAPI schema of its CRD, which
    the CLI embeds (the CRDs of its own version, config/crd/bases): no field
    the CRD does not define (kubectl apply's strict field validation refuses
    it), and the schema's types, enums, patterns, formats, bounds and
    required fields, each with its path and the API server's message. As
    the API server does on create, a required field with a default may be
    left out (MetricCheck spec.provider) and a status is ignored (kubectl
    get -o yaml output validates). A kind kardinal.io does not have is an
    error.
  - Pipeline: at least one environment, every environment named, spec.git.url
    set, the environment dependencies form a valid graph (no cycles, no
    unknown dependsOn), and no reserved field that is not implemented is set
    (autoRollback, a health.resource.kind other than Deployment). The
    controller reports the same fields as Ready=False/NotImplemented on the
    Pipeline. With metadata.namespace set,
    a git.secretRef in another namespace is an error too (the controller
    reports it as Ready=False/ValidationFailed). With
    --allowed-repositories (the controller's scm.allowedRepositories), a
    Pipeline must point spec.git.url at one of them unless it never needs
    the controller's SCM token: a git.secretRef and no pr-review
    environment (the controller reports Ready=False/RepositoryNotAllowed);
    with --scm-provider bitbucket-datacenter a URL is matched as KEY/slug,
    as the controller does.
  - PolicyGate: spec.expression set and compiles with the controller's
    PolicyGate CEL environment, and a name of at most 63 characters (the API
    server rejects a longer one); spec.when is a warning (it is ignored)

The CRDs' CEL rules (x-kubernetes-validations) are not run offline, beyond
the ones the checks above repeat (the PolicyGate name length, a reserved
environment name, the removed fields); 'kubectl apply --dry-run=server' runs
them all. The schema is the CLI's version's: use the CLI of the controller's
version.

Exit codes:
  0: file is valid
  1: validation failed (actionable errors printed)

```
kardinal validate [flags]
```

### Examples

```
  kardinal validate -f pipeline.yaml
  kardinal validate -f examples/ --allowed-repositories github.com/acme/*
```

### Options

```
      --allowed-repositories strings   The controller's scm.allowedRepositories (comma-separated host/repository globs): report a Pipeline that would need the controller's SCM token for a spec.git.url that is not one of them
  -f, --file string                    Path to Pipeline or PolicyGate YAML file (required)
  -h, --help                           help for validate
      --scm-provider string            The controller's scm.provider, for --allowed-repositories: with bitbucket-datacenter a spec.git.url is matched as KEY/slug however it names the repository (/scm/, browse or ssh path), as the controller does
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

