## kardinal

kardinal manages promotion pipelines on Kubernetes

### Synopsis

kardinal is the CLI for kardinal-promoter.
It communicates with the Kubernetes API server to read and write CRDs.

### Options

```
      --context string      Kubeconfig context override
  -h, --help                help for kardinal
      --kubeconfig string   Path to kubeconfig file (default "~/.kube/config")
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml
```

### SEE ALSO

* [kardinal approve](kardinal-approve.md)	 - Approve a Bundle for promotion, bypassing upstream gate requirements
* [kardinal audit](kardinal-audit.md)	 - Audit log commands — view and summarize promotion events
* [kardinal create](kardinal-create.md)	 - Create kardinal resources
* [kardinal dashboard](kardinal-dashboard.md)	 - Open the kardinal UI dashboard in a browser (Kargo parity)
* [kardinal diff](kardinal-diff.md)	 - Show artifact differences between two Bundles
* [kardinal doctor](kardinal-doctor.md)	 - Run pre-flight checks to verify the cluster is correctly configured
* [kardinal explain](kardinal-explain.md)	 - Explain the current state of a promotion pipeline
* [kardinal get](kardinal-get.md)	 - Display one or more kardinal resources
* [kardinal history](kardinal-history.md)	 - Show Bundle promotion history for a pipeline
* [kardinal init](kardinal-init.md)	 - Interactive wizard to generate a Pipeline YAML
* [kardinal logs](kardinal-logs.md)	 - Show promotion step execution logs for a pipeline (Kargo parity)
* [kardinal metrics](kardinal-metrics.md)	 - Show promotion metrics (DORA-style) for a pipeline
* [kardinal override](kardinal-override.md)	 - Force-pass a PolicyGate with a mandatory audit record (K-09)
* [kardinal pause](kardinal-pause.md)	 - Pause a pipeline, preventing new promotions from starting
* [kardinal policy](kardinal-policy.md)	 - Manage and evaluate promotion policy gates
* [kardinal promote](kardinal-promote.md)	 - Trigger promotion of a pipeline to a specific environment
* [kardinal refresh](kardinal-refresh.md)	 - Force re-reconciliation of a Pipeline (Kargo parity)
* [kardinal resume](kardinal-resume.md)	 - Resume a paused pipeline
* [kardinal rollback](kardinal-rollback.md)	 - Roll back a pipeline environment to a previous Bundle
* [kardinal status](kardinal-status.md)	 - Show controller health and cluster resource summary
* [kardinal validate](kardinal-validate.md)	 - Validate Pipeline and PolicyGate YAML before applying to the cluster
* [kardinal version](kardinal-version.md)	 - Print the CLI, controller, and graph versions

