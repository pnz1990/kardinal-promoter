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

#### Current limitation

kardinal does not have a Project CRD. All Pipelines and Bundles installed in a shared
namespace use the same `ClusterRole`. This means:

- A platform team running kardinal for 20 application teams **cannot** grant Team A
  write access to their Pipeline without also granting read access to Team B's Pipelines
  and Bundles.
- Kubernetes RBAC cannot distinguish between resources of the same kind in the same namespace
  without label selectors — and kardinal's `ClusterRole` grants access to all kardinal CRDs.

#### Recommended workaround: one install per team namespace

Until a Project CRD or namespace-scoped RBAC isolation is added, the only safe
multi-tenant configuration is **one kardinal installation per application namespace**.

This uses the `controller.watchNamespace` Helm value (added in v0.6.0) to limit each
controller to a single namespace, with a `Role`/`RoleBinding` scoped to that namespace
instead of a cluster-wide `ClusterRole`.

**Example: two teams, two installs**

```bash
# Team A — installs kardinal watching only the "team-a" namespace
helm install kardinal-team-a oci://ghcr.io/pnz1990/kardinal-promoter/chart/kardinal-promoter \
  --namespace team-a \
  --create-namespace \
  --set controller.watchNamespace=team-a \
  --set controller.github.token.secretName=github-token

# Team B — separate install watching only the "team-b" namespace
helm install kardinal-team-b oci://ghcr.io/pnz1990/kardinal-promoter/chart/kardinal-promoter \
  --namespace team-b \
  --create-namespace \
  --set controller.watchNamespace=team-b \
  --set controller.github.token.secretName=github-token
```

Each install creates a `Role` and `RoleBinding` scoped to its watch namespace. Team A
cannot see or modify Team B's Pipelines, Bundles, or PolicyGates.

**Cost**: one controller replica per team namespace. For 20 teams, this means 20
controller pods. Each controller is lightweight (~50 MB RAM), but the operational
overhead of managing multiple Helm releases is real. Use a tool like ArgoCD's
ApplicationSet or Flux's HelmRelease to manage the installs at scale.

**When this is not appropriate**: if you have a central platform team that needs
read access across all team namespaces for observability (e.g. `kardinal get pipelines
--all-namespaces`), the per-namespace model will not provide that. In this case,
consider running a read-only cluster-scoped installation alongside the namespace-scoped
installs, using RBAC to restrict writes.

#### Additional isolation steps

1. Apply a `NetworkPolicy` to restrict each `kardinal-*` namespace pod egress to the Kubernetes API server only
2. Use separate GitHub App installations per team (when using OIDC) so token blast radius is bounded
3. Use separate GitHub token `Secrets` in each team namespace — never share a token across namespace installs

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

kardinal writes an immutable `AuditEvent` CRD record at every significant promotion
lifecycle transition. AuditEvents are append-only — the spec is set at creation and
never mutated. Kubernetes RBAC can be used to prevent deletion, satisfying SOC 2,
ISO 27001, and FedRAMP audit trail requirements.

### Events written automatically

| Action | Trigger |
|---|---|
| `PromotionStarted` | Bundle begins promoting through an environment |
| `PromotionSucceeded` | Health check passed; PromotionStep reached Verified |
| `PromotionFailed` | PromotionStep reached Failed state |
| `PromotionSuperseded` | A newer Bundle superseded an in-flight promotion |
| `GateEvaluated` | PolicyGate changed readiness state (blocked or unblocked) |
| `RollbackStarted` | `onHealthFailure: rollback` triggered a rollback Bundle |

### Fields on every event

| Field | Description |
|---|---|
| `spec.timestamp` | RFC 3339 time when the event occurred |
| `spec.pipelineName` | Name of the Pipeline |
| `spec.bundleName` | Name of the Bundle being promoted |
| `spec.environment` | Environment name (e.g. `prod`) |
| `spec.action` | One of the action values in the table above |
| `spec.actor` | Author from Bundle provenance, or controller service account |
| `spec.outcome` | `Success`, `Failure`, or `Pending` |
| `spec.message` | Human-readable description |
| `spec.bundleImage` | Container image tag, when applicable |

### Querying audit events

```bash
# List all audit events (most recent first)
kardinal get auditevents

# Filter by pipeline
kardinal get auditevents --pipeline my-app

# Filter by environment
kardinal get auditevents --pipeline my-app --env prod

# Raw kubectl (shows all fields)
kubectl get auditevents -n kardinal-system -o wide

# Watch a specific pipeline's events in real-time
kubectl get auditevents -n kardinal-system \
  -l kardinal.io/pipeline=my-app \
  --watch
```

### SIEM integration

Export AuditEvents as structured JSON for forwarding to your SIEM:

```bash
# JSON dump of all events (pipe to your log forwarder)
kubectl get auditevents -n kardinal-system -o json \
  | jq -c '.items[] | {
      ts: .spec.timestamp,
      pipeline: .spec.pipelineName,
      bundle: .spec.bundleName,
      env: .spec.environment,
      action: .spec.action,
      actor: .spec.actor,
      outcome: .spec.outcome,
      message: .spec.message
    }'
```

With **Fluentd / Vector / Fluent Bit**: configure a Kubernetes input that tails the
`auditevents` resource and forwards to your SIEM sink (Splunk, Datadog, OpenSearch,
etc.). The structured JSON output above is the recommended log format.

### RBAC: preventing deletion

By default the controller's service account creates AuditEvents but cannot delete
them. To prevent all users from deleting audit records, apply:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kardinal-audit-readonly
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["auditevents"]
    verbs: ["get", "list", "watch"]
    # Intentionally no "delete" or "update"
```

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

## UI API Access Control

The embedded UI server runs on port `:8082` and serves two surfaces:

| Path prefix | Content | Default |
|---|---|---|
| `/ui/*` | React app (HTML/JS/CSS) | Open — no sensitive data |
| `/api/v1/ui/*` | Pipeline state, Bundle history, gate details | Open — unprotected by default |

Without authentication, any pod in the cluster that can reach `:8082` can read all pipeline state. Enable Bearer token protection for production deployments.

### Enabling Bearer token authentication

Set the `--ui-auth-token` flag (or `KARDINAL_UI_TOKEN` environment variable):

```bash
# Helm values
helm upgrade kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --set controller.uiAuthToken="$(openssl rand -hex 32)"
```

Or set it directly in the Deployment:

```yaml
env:
  - name: KARDINAL_UI_TOKEN
    valueFrom:
      secretKeyRef:
        name: kardinal-ui-token
        key: token
```

When set, all `/api/v1/ui/*` requests must include:

```
Authorization: Bearer <token>
```

Requests without a valid Bearer token receive `HTTP 401` with a `Www-Authenticate: Bearer realm="kardinal-ui"` header.

The static React assets at `/ui/*` are **not** gated — they contain no sensitive data and must load before the browser can supply a token.

### Accessing the UI securely (before TLS is configured)

Until TLS is configured, the recommended access method is:

```bash
kubectl port-forward svc/kardinal-kardinal-promoter 8082:8082 -n kardinal-system
```

Then access the UI at `http://localhost:8082/ui/`. The browser may display a warning when accessed over plain HTTP (`window.location.protocol != 'https:'`).

> **Production note**: Do not expose port 8082 via a LoadBalancer or Ingress without TLS and auth enabled. Use port-forward for operator access or configure TLS as described below.

---

## TLS Configuration

Both the UI server (`:8082`) and the webhook/bundle-API server (`:8083`) support TLS via the `--tls-cert-file` and `--tls-key-file` flags (environment variables `KARDINAL_TLS_CERT_FILE` / `KARDINAL_TLS_KEY_FILE`).

When both flags are set, both servers switch to `https.ListenAndServeTLS`. When neither is set, both remain on plain HTTP (backwards compatible). Providing only one of the two flags is detected at startup and logs a warning before falling back to plain HTTP.

### Helm: cert-manager integration (recommended)

Use cert-manager to provision a certificate and mount it as a Kubernetes Secret:

```yaml
# 1. Create a Certificate using cert-manager
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: kardinal-tls
  namespace: kardinal-system
spec:
  secretName: kardinal-tls
  duration: 2160h  # 90 days
  renewBefore: 360h
  dnsNames:
    - kardinal-kardinal-promoter.kardinal-system.svc.cluster.local
  issuerRef:
    name: letsencrypt-prod  # your ClusterIssuer
    kind: ClusterIssuer
```

```yaml
# 2. Mount the cert-manager Secret and configure Helm
controller:
  tlsCertFile: /etc/kardinal-tls/tls.crt
  tlsKeyFile: /etc/kardinal-tls/tls.key

# Add to values.yaml:
extraVolumes:
  - name: kardinal-tls
    secret:
      secretName: kardinal-tls
extraVolumeMounts:
  - name: kardinal-tls
    mountPath: /etc/kardinal-tls
    readOnly: true
```

Or set directly at deploy time:

```bash
helm upgrade kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --set controller.tlsCertFile=/etc/kardinal-tls/tls.crt \
  --set controller.tlsKeyFile=/etc/kardinal-tls/tls.key
```

### Self-signed certificates (development only)

Generate a self-signed cert for local testing:

```bash
openssl req -x509 -newkey rsa:4096 -keyout tls.key -out tls.crt \
  -days 365 -nodes -subj '/CN=localhost'

kubectl create secret tls kardinal-tls \
  --cert=tls.crt --key=tls.key \
  -n kardinal-system
```

> **Do not use self-signed certificates in production.** Use cert-manager or a trusted CA.

---

## Further Reading

- [Installation](../installation.md) — Helm values reference
- [Monitoring](monitoring.md) — Prometheus metrics
- [FAQ](../faq.md#what-permissions-does-the-controller-need) — RBAC questions
