# Security Guide

This guide covers RBAC configuration, GitHub token scopes, and security best practices for kardinal-promoter.

---

## Controller RBAC

The kardinal-promoter controller requires a `ClusterRole` and `ClusterRoleBinding`. The Helm chart creates these automatically, but here is the full manifest for reference or manual installation:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kardinal-promoter-controller
rules:
  # kardinal CRDs
  - apiGroups: ["kardinal.io"]
    resources:
      - pipelines
      - bundles
      - promotionsteps
      - policygates
      - prstatuses
      - rollbackpolicies
      - metricchecks
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["kardinal.io"]
    resources:
      - pipelines/status
      - bundles/status
      - promotionsteps/status
      - policygates/status
      - prstatuses/status
      - rollbackpolicies/status
      - metricchecks/status
    verbs: ["get", "update", "patch"]
  - apiGroups: ["kardinal.io"]
    resources:
      - pipelines/finalizers
      - bundles/finalizers
      - promotionsteps/finalizers
    verbs: ["update"]

  # krocodile Graph CRDs
  - apiGroups: ["experimental.kro.run"]
    resources: ["graphs"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Read workload status (health checks)
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]

  # GitHub token secret
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]

  # Events
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]

  # Leader election + version ConfigMap
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "create", "update", "patch"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kardinal-promoter-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kardinal-promoter-controller
subjects:
  - kind: ServiceAccount
    name: kardinal-promoter-controller
    namespace: kardinal-system
```

---

## GitHub Token Scopes

kardinal-promoter uses a GitHub Personal Access Token (PAT) to:

1. Open pull requests (one per environment promotion)
2. Read PR status (merged, closed, open)
3. Post comments on PRs (soak time, gate results, rollback evidence)

### Minimum required scopes (classic PAT)

| Scope | Why |
|---|---|
| `repo` | Read/write access to repositories (open PRs, push branches) |

No admin scopes are required. The token does **not** need:
- `admin:org`
- `admin:repo_hook`
- `delete_repo`
- `workflow`

### Fine-grained PAT (recommended)

GitHub fine-grained PATs give per-repository permissions:

| Permission | Level |
|---|---|
| `Contents` | Read and write (push branches) |
| `Pull requests` | Read and write (open PRs, post comments) |
| `Metadata` | Read (required by GitHub for all fine-grained PATs) |

### Token rotation

Store the token in a Kubernetes Secret and update it without restarting the controller:

```bash
kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=ghp_new_token \
  --dry-run=client -o yaml | kubectl apply -f -
```

The controller reads the secret on every SCM operation — no restart required.

### Using OIDC instead of a PAT

If your cluster supports GitHub Actions OIDC tokens (e.g., EKS with GitHub OIDC provider), you can configure the controller to exchange a short-lived OIDC token for a GitHub App installation token. This avoids long-lived PATs entirely.

This requires:
1. A GitHub App with `Pull requests: Read and write` and `Contents: Read and write`
2. The app installed on the target repositories
3. Set `github.auth.type: github-app` in the Helm values (see `values.yaml` for fields)

---

## Namespace Isolation

### Controller namespace

The controller runs in `kardinal-system` by default. It watches CRDs across all namespaces but writes only to the namespaces where Pipelines are deployed.

### Policy gate scoping

PolicyGates are namespace-scoped:

- **Org-level gates** (`namespace: platform-policies`): mandatory for all pipelines targeting the matching environment. Teams cannot override them.
- **Team-level gates** (team namespace): additive — injected alongside org gates. Teams add restrictions, not bypasses.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: platform-policies
  labels:
    kardinal.io/policy-namespace: "true"
```

Configure the org policy namespace via the controller flag `--policy-namespaces platform-policies`.

### Multi-tenant isolation

For multi-tenant clusters:

1. Run one Pipeline per team namespace
2. Apply a `NetworkPolicy` to restrict `kardinal-system` pod egress to the Kubernetes API server only
3. Use separate GitHub App installations per team (when using OIDC)

---

## Secret Management

### Recommended: External Secrets Operator

Use [External Secrets Operator](https://external-secrets.io/) to sync tokens from Vault, AWS Secrets Manager, or GCP Secret Manager:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: github-token
  namespace: kardinal-system
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: vault-backend
  target:
    name: github-token
  data:
    - secretKey: token
      remoteRef:
        key: secret/kardinal/github-token
        property: value
```

---

## Pod Security

The Helm chart sets secure defaults for the controller pod:

```yaml
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  capabilities:
    drop: ["ALL"]
```

These defaults comply with the Kubernetes `restricted` pod security standard.

---

## Audit Logging

Every promotion action is recorded as a Kubernetes Event:

```bash
kubectl get events -n kardinal-system --field-selector reason=PromotionStarted
kubectl get events -n kardinal-system --field-selector reason=PolicyGateBlocked
kubectl get events -n kardinal-system --field-selector reason=BundleVerified
```

For long-term audit retention, forward Events to your SIEM using a log aggregator (Fluentd, Vector, etc.).

---

## NetworkPolicy

By default, no NetworkPolicy is applied. In environments with a NetworkPolicy-capable CNI (Calico, Cilium, etc.), enable the built-in policy to restrict the controller's network access:

```bash
helm upgrade kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --set networkPolicy.enabled=true
```

The policy allows:
- **Ingress**: kubelet health probes (port 8081) and Prometheus scraping (port 8080)
- **Egress**: Kubernetes API server (`:443`, `:6443`), DNS (`:53`), HTTPS for SCM providers and go-git operations (`:443`), and traffic to `kro-system` for krocodile communication

Disable with `--set networkPolicy.enabled=false` if your CNI does not support NetworkPolicy.

---

## Admission Validation

The `ValidatingAdmissionPolicy` (Kubernetes 1.28+, enabled by default) validates `PolicyGate` resources at admission time:

1. `spec.expression` must not be empty
2. `spec.recheckInterval`, if set, must match Go duration format (e.g. `5m`, `30s`, `1h`)

Full CEL syntax validation (catching invalid CEL expressions) requires a validating webhook — see issue #317.

Disable for clusters without VAP support:
```bash
helm upgrade kardinal ... --set validatingAdmissionPolicy.enabled=false
```

---

## Further Reading

- [Installation](../installation.md) — Helm values reference
- [Monitoring](monitoring.md) — Prometheus metrics
- [FAQ](../faq.md#what-permissions-does-the-controller-need) — RBAC questions
