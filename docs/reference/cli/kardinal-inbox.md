## kardinal inbox

List what waits for a person: on you first, then on others

### Synopsis

List every environment that waits for a person, in the namespace (every
namespace with -A): an approval, a gate that does not open by itself, a hold,
a paused pipeline, a PR to merge. What you can move on comes first, with the
command that does it; then what waits on someone else, and who.

inbox lists, per environment, what waits for a person, from each Pipeline's
status.environments, its gate instances and holds:

  approval  an approval gate that has not its quorum (n of m)
  gate      a gate that does not pass and does not open by itself (its
            expression reads nothing that changes with time; see ship)
  hold      a hold (kardinal hold, rollback --hold)
  paused    the pipeline is paused
  pr        its PR waits for a person to merge it (not pr.merge.auto)

"Waiting on you" is what you can move on: an approval when you are in its
allowedUsers or allowedGroups (or both are empty) and did not create the
version under excludeAuthor, and you may create approvals; a gate, a hold
or a pause when your Kubernetes rights let you override, unhold or resume
(a SelfSubjectAccessReview). Its NEXT column is the command. Everything else
is "Waiting on someone else", with WHO: the allowed users and groups, or the
PR's reviewers. A PR is never on you: the CLI cannot tell who may merge.
Gates that open by themselves (a soak, a schedule) and promotions in flight
are not listed: nothing is waiting for a person.

-A lists every namespace (with -n in the commands). -o json|yaml prints
{onYou: [...], others: [...]}, each item with namespace, pipeline,
environment, bundle, version, kind, waiting, onYou, who, next and since.

```
kardinal inbox [flags]
```

### Examples

```
  kardinal inbox
  kardinal inbox -A
  kardinal inbox -o json
```

### Options

```
  -A, --all-namespaces   Every namespace
  -h, --help             help for inbox
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

