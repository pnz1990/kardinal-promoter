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

* [kardinal approve](kardinal_approve.md)	 - Approve a Bundle for promotion, bypassing upstream gate requirements
* [kardinal audit](kardinal_audit.md)	 - Audit log commands — view and summarize promotion events
* [kardinal completion](kardinal_completion.md)	 - Generate shell completion scripts
* [kardinal create](kardinal_create.md)	 - Create kardinal resources
* [kardinal dashboard](kardinal_dashboard.md)	 - Open the kardinal UI dashboard in a browser (Kargo parity)
* [kardinal delete](kardinal_delete.md)	 - Delete kardinal resources
* [kardinal diff](kardinal_diff.md)	 - Show artifact differences between two Bundles
* [kardinal doctor](kardinal_doctor.md)	 - Run pre-flight checks to verify the cluster is correctly configured
* [kardinal explain](kardinal_explain.md)	 - Explain the current state of a promotion pipeline
* [kardinal get](kardinal_get.md)	 - Display one or more kardinal resources
* [kardinal history](kardinal_history.md)	 - Show Bundle promotion history for a pipeline
* [kardinal init](kardinal_init.md)	 - Interactive wizard to generate a Pipeline YAML and scaffold the GitOps repo
* [kardinal logs](kardinal_logs.md)	 - Show promotion step execution logs for a pipeline (Kargo parity)
* [kardinal metrics](kardinal_metrics.md)	 - Show promotion metrics (DORA-style) for a pipeline
* [kardinal override](kardinal_override.md)	 - Force-pass a PolicyGate with a mandatory audit record (K-09)
* [kardinal pause](kardinal_pause.md)	 - Pause a pipeline, preventing new promotions from starting
* [kardinal policy](kardinal_policy.md)	 - Manage and evaluate promotion policy gates
* [kardinal promote](kardinal_promote.md)	 - Trigger promotion of a pipeline to a specific environment
* [kardinal refresh](kardinal_refresh.md)	 - Force re-reconciliation of a Pipeline (Kargo parity)
* [kardinal resume](kardinal_resume.md)	 - Resume a paused pipeline
* [kardinal rollback](kardinal_rollback.md)	 - Roll back a pipeline environment to a previous Bundle
* [kardinal status](kardinal_status.md)	 - Show controller health or per-pipeline in-flight promotion details
* [kardinal validate](kardinal_validate.md)	 - Validate Pipeline and PolicyGate YAML before applying to the cluster
* [kardinal version](kardinal_version.md)	 - Print the CLI, controller, and graph versions

