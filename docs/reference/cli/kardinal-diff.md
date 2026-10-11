## kardinal diff

Show what changes between two versions (Bundles)

### Synopsis

Compare the images (by repository) and the provenance of two Bundles. A tag
reads (absent) when a Bundle has no image for the repository and - when it is
pinned by digest only; CHANGED marks the rows that differ.

Images are matched by repository. A tag cell reads "(absent)" when the Bundle
has no image for that repository, and "-" when the image is pinned by digest
only. CHANGED is "*" on rows that differ. Commit and author are listed under
PROVENANCE.

```
kardinal diff <bundle-a> <bundle-b> [flags]
```

### Examples

```
  kardinal diff my-app-sha-abc1234 my-app-sha-def5678
```

### Options

```
  -h, --help   help for diff
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, promote, rollback, ship, status, unhold, why; wide: status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

