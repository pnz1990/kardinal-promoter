## kardinal verify-receipt

Verify a signed promotion receipt: its signature, subject, environment and levels

### Synopsis

Verify a promotion's signed receipt (docs/receipts.md), from the cluster
(<pipeline> <env>, the newest unless --bundle), an image's OCI referrers
(--image with --env) or a file (--file). Check it with --key, or with
--identity and --issuer (both required: no default trusts any identity).
The exit code is 0 only when every check passes.

It checks the signature (and the certificate chain against the trusted
root); the transparency log entry or timestamp when there is one; the
subject; that the summary is PASSED for the environment asked; and each
--require'd level (a leading ! means it must be absent). From the cluster, the receipt's spec.statement must be the signed statement
byte for byte, and the Bundle it names, when it still exists, must have the
UID the receipt records: a PromotionReceipt edited after it was signed fails.
The command never contacts Rekor: it checks the log entry and inclusion proof
embedded in the bundle against the trusted root. --image reads the registry
with your docker credentials (~/.docker/config.json). A KMS receipt is
verified with the KMS key's public key (--key, PEM).

```
kardinal verify-receipt [<pipeline> <env>] [flags]
```

### Examples

```
  kardinal verify-receipt checkout prod --key receipt.pub
  kardinal verify-receipt checkout prod --bundle checkout-1-5-0 \
    --identity https://kubernetes.io/namespaces/kardinal-system/serviceaccounts/kardinal-promoter \
    --issuer https://oidc.eks.us-east-2.amazonaws.com/id/EXAMPLE
  kardinal verify-receipt --image ghcr.io/acme/checkout@sha256:9f2c... --env prod --key receipt.pub
  kardinal verify-receipt --file receipt.sigstore.json --key receipt.pub --require GATES_PASSED,APPROVED,!OVERRIDDEN
```

### Options

```
      --bundle string         The Bundle whose receipt to verify (default: the newest receipt of the environment)
      --env string            The environment the receipt must be for (with --image or --file)
      --file string           Read the receipt from this Sigstore bundle file
  -h, --help                  help for verify-receipt
      --identity string       Certificate identity (subject alternative name) of a keyless receipt
      --image string          Read the receipt from this image's OCI referrers (repository@sha256:...)
      --insecure-registry     Read --image over plain HTTP
      --issuer string         OIDC issuer of a keyless receipt
      --key string            Public key file (PEM) of a key or KMS receipt
      --repository string     Read --image's receipts from this repository (receipts.sinks.oci.repository) instead of the image's
      --require strings       Levels that must be verified, comma-separated (GATES_PASSED, APPROVED, HEALTHY, ...); !LEVEL must be absent
      --trusted-root string   Sigstore trusted_root.json (default: the public-good root, fetched with TUF)
```

### Options inherited from parent commands

```
      --context string      Kubeconfig context override
      --kubeconfig string   Path to kubeconfig file (default: $KUBECONFIG, else ~/.kube/config)
  -n, --namespace string    Kubernetes namespace (default: current context namespace)
  -o, --output string       Output format: table (default), json, yaml, wide (json and yaml: create bundle, explain, get auditevents, get bundles, get pipelines, get steps, get subscriptions, history, hold, inbox, promote, rollback, ship, status, unhold, verify-receipt, why; wide: get subscriptions, status, why)
```

### SEE ALSO

* [kardinal](kardinal.md)	 - kardinal manages promotion pipelines on Kubernetes

