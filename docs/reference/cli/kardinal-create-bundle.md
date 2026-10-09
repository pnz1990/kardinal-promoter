## kardinal create bundle

Create a Bundle to trigger promotion through a Pipeline

### Synopsis

Create a Bundle to trigger promotion through a Pipeline.

The pipeline name is a required positional argument; the Pipeline must exist.
An image or mixed Bundle needs at least one --image. A config or mixed Bundle
needs --config-commit, the commit of the config repository to promote;
--config-repo names that repository and defaults to the Pipeline's git.url.
An image Bundle with --config-repo or --config-commit is refused: it would
deploy only its images and ignore them.

--commit, --author and --ci-run-url set the Bundle's provenance, shown in the
PR body and the UI. kardinal records them as given: they are what the caller
asserts, as with the Bundle API.

The Bundle API (POST /api/v1/bundles) applies the same checks.

Use --dry-run to preview the promotion graph without creating any resources.

```
kardinal create bundle <pipeline> [flags]
```

### Examples

```
  kardinal create bundle my-app --image ghcr.io/org/my-app:sha-abc1234 \
    --commit abc1234 --author "$GITHUB_ACTOR" --ci-run-url "$RUN_URL"
  kardinal create bundle my-app --type config --config-commit 9f8e7d6
```

### Options

```
      --author string          Author or CI actor of the build (provenance)
      --ci-run-url string      Absolute http(s) URL of the CI run that built the Bundle (provenance)
      --commit string          Source commit SHA that produced the Bundle (provenance)
      --config-commit string   Commit SHA of the config repository to promote; required for config and mixed Bundles
      --config-repo string     Git URL of the config repository (default: the Pipeline's git.url)
      --dry-run                Preview the promotion graph without creating any cluster resources
  -h, --help                   help for bundle
      --image stringArray      Container image reference (can be specified multiple times); required for image and mixed Bundles
      --type string            Bundle type: image, config, or mixed (default "image")
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml (json and yaml: get auditevents, bundles, pipelines, steps, subscriptions)
```

### SEE ALSO

* [kardinal create](kardinal-create.md)	 - Create kardinal resources

