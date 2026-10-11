## kardinal status

Show controller health or per-pipeline in-flight promotion details

### Synopsis

Without a pipeline: the controller version and how many pipelines and
bundles there are. With one: per environment, the version running there, a
newer one on its way in, its state in words and since when, its PR, and the
gates holding it back. Run it first when a promotion is stuck; -w refreshes
it and -o json prints it for scripts.

When called without arguments: displays the controller version (the
kardinal-version ConfigMap in --controller-namespace), the pipeline count with
any Degraded pipelines, and the bundle count (active = Available or Promoting).

When called with a pipeline name: first the Pipeline's own problems, an
ERROR when it is not Ready (such as GitCredentialMissing, with the command
that creates the git Secret) and a WARNING when its git Secret is not
labelled kardinal.io/referenceable=true; then the Pipeline's summary sentence
("1.5.0 live in test; waiting for PR merge in prod (#42)"), then one row per environment
(a fleet environment expands to its targets) from the Pipeline's
status.environments, which the controller writes:

  RUNNING   the Bundle whose change runs there, with its version
  INCOMING  a newer Bundle on its way in, with its version
  STATE     where the newest Bundle there stands, in words ("Live",
            "Waiting for PR merge", "Held by gate no-weekend-deploys",
            "Held by rollback")
  SINCE     how long it has been in that state
  PR        the pull request it waits for, while it waits for its merge

REGION, after ENVIRONMENT, shows where each environment runs when one of
them says: its region (spec regions) or the cluster its health check reads
(the health.kubeconfigSecretRef Secret). -o wide adds ACTIVE STEP, the step
the newest Bundle there is on (git-clone, open-pr, health-check, ...).

Then the gates holding a version back, with their CEL expression cut to 40
characters, their message and when each was last checked; and a WARNING: line
for a Rejected Bundle whose change still runs somewhere, with the rollback
that removes it. A pipeline nothing was shipped to yet says so, with the
command that ships the first version.

With a controller older than status.environments, the CLI computes the same
rows itself, with the controller's code; SINCE is then known only for live
environments. -o json and -o yaml print the same data with the API's state
values (Verified, WaitingForMerge) and "source": "controller" or "cli".
kardinal explain shows the steps and every gate of the current Bundle.

```
kardinal status [<pipeline>] [flags]
```

### Examples

```
  kardinal status
  kardinal status my-app -w
  kardinal status my-app -o json | jq -r '.environments[] | .name + "=" + .version'
```

### Options

```
      --controller-namespace string   Namespace kardinal-promoter is installed in (default "kardinal-system")
  -h, --help                          help for status
  -w, --watch                         Refresh every 2 seconds (Ctrl-C to quit)
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

