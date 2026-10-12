## kardinal inbox

List what waits for a person: on you first, then on others

### Synopsis

List every environment that waits for a person, in the namespace (every
namespace with -A): an approval, a gate that does not open by itself, a hold,
a paused pipeline, a PR to merge. What you can move on comes first, with the
command that does it; then what waits on someone else, and who.

inbox lists, per environment, what waits for a person, from each Pipeline's
status.environments, the gate instances of the versions that wait, and its
holds:

  approval  an approval gate that has not its quorum (n of m) and nobody
            rejected
  gate      a gate that does not pass and does not open by itself (its
            expression reads nothing that changes with time; see ship)
  hold      a hold (kardinal hold, rollback --hold)
  paused    the pipeline is paused
  pr        its PR waits for a person to merge it (not pr.merge.auto)

"Waiting on you" is what you can move on, with the command in NEXT: an
approval your approval would count for, by the same tally the server keeps
(allowedUsers, allowedGroups, excludeAuthor, one approval per person), when
you may create Approvals; a gate, a hold or a pause when you hold the rights
the API server and the chart's admission policies check (patch policygates
and update policygates/override; update pipelines and pipelines/hold; patch
pipelines and update pipelines/pause), asked with SelfSubjectAccessReviews.
Everything else is "Waiting on someone else", with WHO: the allowed users
and groups, someone other than you when you created the version, or the PR's
reviewers. An approval excludeAuthor cannot enforce (no verified creator, or
a kardinal component created the version) is on no one. A PR is never on
you: the CLI cannot tell who may merge. Gates that open by themselves and
promotions in flight are not listed. The oldest wait comes first.

A command carries -n when its namespace is not the kubeconfig context's. -A
lists every namespace; when you may not list Pipelines cluster-wide it
fails and says to use -n. A namespace whose gates or Bundles it may not
read is noted on stderr.
-o json|yaml prints {onYou, others, skipped}; each item has namespace,
pipeline, environment, bundle, version, kind, waiting, onYou, who, next
(without --reason <why>: needsReason says it needs one) and since.

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
  -A, --all-namespaces   Every namespace you may read
  -h, --help             help for inbox
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

