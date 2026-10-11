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
its label in the status colours of the UI: Live (spruce), Promoting and
Checking health (red), Waiting for PR merge and gates not ready yet (blue),
Held by gate (ochre), Failed (white on garnet, the only filled state),
Replaced by newer version and Not reached (grey); Paused and Held by rollback
are in bold. Piped to a file or another program, output is plain words: the
labels without glyphs or colour, and ASCII only. NO_COLOR (any value) or
TERM=dumb keeps glyphs, in ASCII (`* Live`, `~ Waiting for PR merge`, `|| Held by gate`),
without colour. COLORTERM=truecolor gives the UI's exact
colours, for a light background when COLORFGBG says so.

-o json and -o yaml print the API's values (Verified, WaitingForMerge) for
scripts: get, status, explain and history print what they show, and create
bundle, promote and rollback print what they created, so
`kardinal create bundle my-app --image app:1.4 -o json | jq -r .bundle`
names the new Bundle. Warnings and hints go to stderr, so stdout stays valid
JSON.

### Options

```
      --context string      Kubeconfig context override
  -h, --help                help for kardinal
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: create bundle, explain, get, history, promote, release-hold, rollback, status)
```

### SEE ALSO

* [kardinal approve](kardinal-approve.md)	 - Approve (or reject) a Bundle for an environment's approval gates
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
* [kardinal status](kardinal-status.md)	 - Show controller health or per-pipeline in-flight promotion details
* [kardinal validate](kardinal-validate.md)	 - Validate Pipeline and PolicyGate YAML before applying to the cluster
* [kardinal version](kardinal-version.md)	 - Print the CLI, controller, and kro (Graph) versions

