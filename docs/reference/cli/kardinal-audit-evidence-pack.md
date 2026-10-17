## kardinal audit evidence-pack

Write the records of a period, with a manifest of hashes and the control mapping, as one tar.gz

### Synopsis

Write every kardinal record of a period into one gzipped tar for an auditor:
the AuditEvents, the versions created in the period with their Approvals,
gate instances and Verdicts, the policy in force, the control mapping and a
manifest with each file's SHA-256. The checksums are not a signature: sign
the pack with your own identity (docs/compliance.md).

The cluster holds only what retention and each Pipeline's historyLimit kept.
A period that ends before the oldest AuditEvent fails: export continuously
with kardinal audit export.

The period is the versions (Bundles) created in [--since, --until) and the
AuditEvents timestamped in it. A version brings its Approvals, gate instances
and Verdicts. The Pipelines, gate templates, ChangeWindows, AgentIdentities
and admission policies are the ones in force at export.

    manifest.json                format, kardinal version, exporter, period,
                                 filters, each file's records, bytes and
                                 SHA-256, coverage, integrity note
    SHA256SUMS                   sha256sum -c input for every other file
    controls.json                each control, its records and their files
    auditevents.ndjson           as kardinal audit export --format json
    auditevents.ocsf.ndjson      as kardinal audit export --format ocsf
    bundles.ndjson               the versions created in the period
    approvals.ndjson             their Approvals
    policygates.ndjson           their gate instances (overrides, results)
    verdicts.ndjson              their Verdicts
    pipelines.ndjson             the Pipelines
    policygate-templates.ndjson  the gate templates
    changewindows.ndjson         windows and freezes, with exemptions
    agentidentities.ndjson       the agent registry
    admission.yaml               the chart's ValidatingAdmissionPolicies
                                 and bindings

coverage.oldest is the oldest record of each kind the cluster still holds,
coverage.missing the kinds it does not serve, coverage.forbidden the kinds
you may not read: those files are left out, not written empty. The read
needs get and list on these kinds, and list on the admission policies.

The pack is written beside -o and renamed into place when complete. Its
SHA-256 is printed to standard error. Memory is bounded by one List page
(500 objects).

```
kardinal audit evidence-pack --since <time> [--until <time>] [-A | -n <ns>] [--pipeline <name>] [--environment <env>] [-o pack.tar.gz] [flags]
```

### Examples

```
  kardinal audit evidence-pack -A --since 2026-07-01 --until 2026-10-01 -o q3.tar.gz
  kardinal audit evidence-pack -n payments --pipeline checkout --since 30d -o checkout.tar.gz
```

### Options

```
  -A, --all-namespaces       Records of every namespace
      --environment string   Only this environment's AuditEvents, gate instances, Approvals and Verdicts
  -h, --help                 help for evidence-pack
  -o, --output string        Write the pack to this file instead of standard output
      --pipeline string      Only this pipeline's records
      --since string         Start of the period: a date (2026-07-01), an RFC 3339 time, or a duration before now (30d)
      --until string         End of the period (excluded), as --since; default now. Set it for a pack that does not change between exports
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
```

### SEE ALSO

* [kardinal audit](kardinal-audit.md)	 - Audit log commands: view and summarize promotion events

