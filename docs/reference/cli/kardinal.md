## kardinal

kardinal manages promotion pipelines on Kubernetes

### Synopsis

kardinal promotes versions of your application through environments, with
gates, pull requests and health checks, on Kubernetes.

Getting started:
  kardinal init                                   write a Pipeline for your repository
  kardinal create bundle my-app --image app:1.4   start a promotion
  kardinal status my-app                          see where each version is, and why

Output follows where it goes. On a terminal, each state reads as a glyph and
its label in the status colours of the UI, from its design tokens
(web/tokens/tokens.json): Live (spruce), Promoting and Checking health
(thread red), Waiting for PR merge and gates not ready yet (dusk blue), Held
by gate (ochre), Failed (on garnet, the only filled state), Replaced by newer
version and Not reached (grey); Paused and Held by rollback are bold.
Without COLORTERM=truecolor and a COLORFGBG that names the background, they
are the nearest of the 16 ANSI colours, which the terminal's theme adapts.
Without a UTF-8 locale the glyphs are ASCII (`* Live`, `~ Waiting for PR merge`).

Piped to a file or another program, or with NO_COLOR (any value) or
TERM=dumb, output is plain words: the labels without glyphs or colour, and
ASCII only, also for what the cluster says (a dash is "-", an arrow "->",
any other non-ASCII character "?"). On Windows, colour needs a console with
virtual-terminal processing; without it output has no colour.

-o json and -o yaml print the API's values (Verified, WaitingForMerge) and the
cluster's text unchanged. Warnings and hints go to stderr, so stdout stays
valid JSON. get prints the objects as the API server lists them. The others
print:

  create bundle, promote, rollback:
    bundle, pipeline, namespace, type, version, url; promote adds
    environment and from (the Bundle copied); rollback adds environment,
    rollbackOf (the Bundle restored), replaces (the one replaced) and, with
    --hold, hold {environment, bundle, reason, expiresAt}
  create bundle --dry-run:
    pipeline, namespace, type, version, nodes, compact,
    environments [{name, gates}] in promotion order
  release-hold:
    pipeline, namespace, environment, bundle (the rollback it was held on),
    createdBy, reason, url
  status:
    controller, pipelines, degraded [namespace/name], bundles, activeBundles
  status <pipeline>:
    pipeline, namespace, phase, paused, summary, source ("controller" or
    "cli"), environments (status.environments: name, bundle, version,
    incomingBundle, incomingVersion, state, since, blockedBy, prURL, held),
    blockingGates [{name, instance, environment, bundle, expression, message,
    reason, lastEvaluatedAt}], warnings
  explain:
    pipeline, namespace, entries [{environment, bundle, type (Step or
    PolicyGate), name, state, expression, reason}], holds [{environment,
    bundle, createdBy, reason, bundleMissing}]
  history:
    a list of {bundle, action (promote or rollback), environment, state,
    prURL, duration, createdAt}

url is the pipeline in the UI: KARDINAL_UI_URL, else the address kardinal
dashboard opens (http://localhost:8082/ui/). For example
`kardinal create bundle my-app --image app:1.4 -o json | jq -r .bundle`
names the new Bundle.

### Options

```
      --context string      Kubeconfig context override
  -h, --help                help for kardinal
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal approve](kardinal-approve.md)	 - Approve (or reject) the version waiting for an environment's approval gates
* [kardinal audit](kardinal-audit.md)	 - Audit log commands: view and summarize promotion events
* [kardinal completion](kardinal-completion.md)	 - Generate shell completion scripts
* [kardinal create](kardinal-create.md)	 - Create kardinal resources
* [kardinal dashboard](kardinal-dashboard.md)	 - Open the kardinal UI dashboard in a browser
* [kardinal delete](kardinal-delete.md)	 - Delete kardinal resources
* [kardinal diff](kardinal-diff.md)	 - Show artifact differences between two Bundles
* [kardinal doctor](kardinal-doctor.md)	 - Run pre-flight checks to verify the cluster is correctly configured
* [kardinal explain](kardinal-explain.md)	 - Explain the current state of a promotion pipeline
* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources
* [kardinal history](kardinal-history.md)	 - Show Bundle promotion history for a pipeline
* [kardinal hold](kardinal-hold.md)	 - Keep an environment on what runs there now
* [kardinal init](kardinal-init.md)	 - Interactive wizard to generate a Pipeline YAML and scaffold the GitOps repo
* [kardinal logs](kardinal-logs.md)	 - Show promotion step execution logs for a pipeline
* [kardinal metrics](kardinal-metrics.md)	 - Show promotion metrics (DORA-style) for a pipeline
* [kardinal override](kardinal-override.md)	 - Force-pass a PolicyGate with a mandatory audit record
* [kardinal pause](kardinal-pause.md)	 - Pause a pipeline: no new promotion steps start, in-flight ones hold at the next safe point
* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates
* [kardinal promote](kardinal-promote.md)	 - Promote the Bundle verified upstream into an environment
* [kardinal refresh](kardinal-refresh.md)	 - Force re-reconciliation of a Pipeline
* [kardinal reject](kardinal-reject.md)	 - Reject a Bundle: it is never promoted again, and rollback never picks it
* [kardinal release-hold](kardinal-release-hold.md)	 - Release the hold of a rollback on an environment
* [kardinal resume](kardinal-resume.md)	 - Resume a paused pipeline
* [kardinal rollback](kardinal-rollback.md)	 - Roll back a pipeline environment to a previous Bundle
* [kardinal ship](kardinal-ship.md)	 - Ship a version: create a Bundle, and with --wait follow it
* [kardinal status](kardinal-status.md)	 - Show controller health or per-pipeline in-flight promotion details
* [kardinal validate](kardinal-validate.md)	 - Validate Pipeline and PolicyGate YAML before applying to the cluster
* [kardinal version](kardinal-version.md)	 - Print the CLI, controller, and kro (Graph) versions

