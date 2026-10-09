## kardinal

kardinal manages promotion pipelines on Kubernetes

### Synopsis

kardinal is the CLI for kardinal-promoter.
It communicates with the Kubernetes API server to read and write CRDs.

### Options

```
      --context string      Kubeconfig context override
  -h, --help                help for kardinal
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal audit](kardinal-audit.md)	 - Audit log commands — view and summarize promotion events
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
* [kardinal release-hold](kardinal-release-hold.md)	 - Release the hold of a rollback on an environment
* [kardinal resume](kardinal-resume.md)	 - Resume a paused pipeline
* [kardinal rollback](kardinal-rollback.md)	 - Roll back a pipeline environment to a previous Bundle
* [kardinal status](kardinal-status.md)	 - Show controller health or per-pipeline in-flight promotion details
* [kardinal validate](kardinal-validate.md)	 - Validate Pipeline and PolicyGate YAML before applying to the cluster
* [kardinal version](kardinal-version.md)	 - Print the CLI, controller, and kro (Graph) versions

