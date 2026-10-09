# REST API

The controller serves two HTTP APIs. Both are described by an OpenAPI 3.1 document,
[`openapi.json`](openapi.json), which the controller also serves at `GET /api/v1/openapi.json`
on both listeners (no credentials needed: it holds no data). It is generated from the
controller's request and response types, so it always matches the version you run:

```bash
kubectl -n kardinal-system port-forward svc/kardinal-promoter 8082:8082 &
curl -s http://localhost:8082/api/v1/openapi.json -o kardinal-openapi.json
# for example: openapi-generator-cli generate -i kardinal-openapi.json -g go -o kardinal-client
```

| API | Listener | Paths | Credentials |
|-----|----------|-------|-------------|
| UI API | `--ui-listen-address`, port `8082` | `/api/v1/ui/*` | The UI auth mode: none (loopback only), the shared UI token, or a Kubernetes token with TokenReview |
| Bundle API | `--webhook-bind-address`, port `8083` | `POST /api/v1/bundles` | The Bundle API token |
| SCM webhooks | port `8083` | `POST /webhook/scm`, `GET /webhook/scm/health` | The webhook secret (signature header), see [SCM providers](../scm-providers.md) |

With controller TLS set (`controller.tlsCertFile`), both listeners use `https://`.

## Endpoints

| Method | Path | Operation |
|--------|------|-----------|
| `GET` | `/api/v1/ui/pipelines` | List Pipelines with their health, active Bundle and DORA metrics |
| `GET` | `/api/v1/ui/pipelines/{pipeline}/bundles` | List the Bundles of a Pipeline, newest first |
| `POST` | `/api/v1/ui/bundles` | Create an image Bundle |
| `GET` | `/api/v1/ui/bundles/{bundle}/graph` | The promotion DAG of a Bundle |
| `GET` | `/api/v1/ui/bundles/{bundle}/steps` | The PromotionSteps of a Bundle, with per-step timings |
| `GET` | `/api/v1/ui/gates` | List PolicyGates |
| `POST` | `/api/v1/ui/gates/{gate}/approve` | Override a PolicyGate for a while |
| `POST` | `/api/v1/ui/gates/{namespace}/{gate}/approve` | Override a PolicyGate in a namespace for a while |
| `POST` | `/api/v1/ui/promote` | Promote the Bundle Verified upstream into an environment |
| `POST` | `/api/v1/ui/approvals` | Approve, reject or revoke a Bundle for an environment's approval gates, as the UI user (TokenReview mode only) |
| `POST` | `/api/v1/ui/rollback` | Roll an environment back |
| `POST` | `/api/v1/ui/pause` | Pause a Pipeline |
| `POST` | `/api/v1/ui/resume` | Resume a Pipeline |
| `POST` | `/api/v1/ui/validate-cel` | Compile a PolicyGate CEL expression |
| `GET` | `/api/v1/ui/steps/{namespace}/{step}/events` | The Kubernetes Events of a PromotionStep |
| `POST` | `/api/v1/bundles` | Create a Bundle from CI (served only when a Bundle API token is set) |
| `GET` | `/webhook/scm/health` | SCM webhook configuration and counters |
| `GET` | `/api/v1/openapi.json` | The OpenAPI description |

Errors are `text/plain` with the HTTP status the OpenAPI document lists for each operation:
`400` invalid request, `401` no valid credentials, `403` refused (with no UI auth mode: a
client that is not local, or a cross-origin request from an origin not allowed; in
TokenReview mode: RBAC denied it), `404` not found (also `POST /api/v1/bundles` when no Bundle
API token is set), `405` wrong method, `409` conflict, `429` rate limited, `503` the
TokenReview or SubjectAccessReview API is unreachable (TokenReview mode fails closed).
Write endpoints record the caller as the requester (`kardinal.io/requested-by` on the Bundles
they create, `createdBy` on gate overrides): the Kubernetes user with TokenReview, else
`kardinal-ui`.

## API tokens for automation: ServiceAccount tokens

For scripts, bots and dashboards, run the UI API in TokenReview mode
(`ui.auth.tokenReview=true`, see
[Option 2: Kubernetes tokens](../guides/security.md#option-2-kubernetes-tokens-tokenreview)) and
give each client its own ServiceAccount. The controller validates the token with a
`TokenReview` and authorizes every object the request touches with a `SubjectAccessReview`
for that ServiceAccount, so the client can do through the API exactly what its RBAC allows,
and you revoke it like any Kubernetes identity.

1. Create a ServiceAccount and bind the permissions it needs (the table in
   [Option 2](../guides/security.md#option-2-kubernetes-tokens-tokenreview) lists them per
   action). A read-only dashboard for one team's namespace needs a Role and RoleBinding
   there:

    ```yaml
    apiVersion: v1
    kind: ServiceAccount
    metadata:
      name: release-dashboard
      namespace: team-a
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata:
      name: kardinal-api-viewer
      namespace: team-a
    rules:
      - apiGroups: ["kardinal.io"]
        resources: ["pipelines", "bundles", "policygates", "promotionsteps"]
        verbs: ["get", "list"]
      - apiGroups: [""]
        resources: ["events"]          # step events
        verbs: ["list"]
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: RoleBinding
    metadata:
      name: release-dashboard-kardinal
      namespace: team-a
    roleRef:
      apiGroup: rbac.authorization.k8s.io
      kind: Role
      name: kardinal-api-viewer
    subjects:
      - kind: ServiceAccount
        name: release-dashboard
        namespace: team-a
    ```

    The list endpoints read all namespaces, so with a namespaced binding they are checked
    against all namespaces and refused, unless the controller runs with
    `--watch-namespace` set to that namespace. For a dashboard over every team, use the same
    rules in a ClusterRole bound with a ClusterRoleBinding.

2. Mint a short-lived token with the TokenRequest API. `kubectl create token` calls it:

    ```bash
    TOKEN=$(kubectl create token release-dashboard -n team-a --duration=1h)
    curl -s -H "Authorization: Bearer $TOKEN" https://kardinal.example.com/api/v1/ui/pipelines
    ```

    Programs call `POST /api/v1/namespaces/team-a/serviceaccounts/release-dashboard/token`
    (client-go: `CoreV1().ServiceAccounts("team-a").CreateToken`) and request a new token
    before `status.expirationTimestamp`. Inside the cluster, mount a projected
    ServiceAccount token instead: the kubelet rotates it.

    ```yaml
    volumes:
      - name: kardinal-token
        projected:
          sources:
            - serviceAccountToken:
                path: token
                expirationSeconds: 3600
    ```

3. Keep the default audience. The controller's TokenReview does not name an audience, so
   the API server accepts tokens for its own audience only: a token minted with
   `--audience` for something else is refused with `401`.

To revoke a client, delete its RoleBinding (effective within the 30-second review cache) or
its ServiceAccount (its tokens stop validating). Prefer TokenRequest tokens to
`kubernetes.io/service-account-token` Secrets, which never expire.

The shared UI token (`ui.auth.tokenSecretRef`) is one secret for every caller with full
access and no per-caller authorization; use it only behind an authenticating proxy.

## CI: the Bundle API token

`POST /api/v1/bundles` takes the Bundle API token (`bundleAPI.tokenSecretRef`,
`--bundle-api-token`), not a Kubernetes token: CI systems outside the cluster can create
Bundles without cluster credentials. See [CI integration](../ci-integration.md#webhook-token).
It is one token shared by every CI caller, limited to 60 requests a minute. A CI job that has
cluster credentials can instead create the Bundle with `kardinal create bundle` or `kubectl`,
authorized by its own RBAC.
