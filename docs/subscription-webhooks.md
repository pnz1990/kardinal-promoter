# Subscription webhooks

A [Subscription](subscription.md) polls its source every `interval`. A registry or SCM
webhook makes it poll at once: push an image, a chart or a commit, and the Bundle is
created within seconds instead of at the next poll. With webhooks in place, a long
interval (`1h`) is enough as a fallback, which also keeps registry rate limits away.

The receiver is on the controller's webhook port (`:8083`, the port of `/webhook/scm`
and the Bundle API):

```
POST /webhook/subscriptions/<namespace>/<subscription>/<provider>[/<token>]
```

`<provider>` is one of `dockerhub`, `ghcr` (or `github`), `harbor`, `quay`,
`artifactory` and `generic`. Expose the port the way you expose `/webhook/scm`, for
example with an Ingress for `/webhook/`.

## What a delivery does

An authenticated delivery that announces a push sets the Subscription's
`kardinal.io/refresh` annotation to the current time and gets `202 Accepted`. The
Subscription reconciler sees the annotation, polls the source with the Subscription's
filters and credentials, creates a Bundle when there is a new artifact, and records the
request it answered in `status.lastRefreshRequest`. The receiver itself never reads the
source and never creates a Bundle, so a forged or replayed payload can at most cause a
poll, and the payload's tag or digest is never trusted.

| Answer | When |
|---|---|
| `202 {"status":"refresh requested"}` | The annotation was set |
| `202 {"status":"refresh already pending"}` | A refresh is already pending (the annotation is newer than `status.lastRefreshRequest`): duplicate and redelivered events are not written again |
| `200 {"status":"ignored: <event>"}` | Authenticated, but not a push: a GitHub `ping`, a Harbor delete, an Artifactory `deleted` |
| `400` | The payload is not the provider's format |
| `401 {"status":"unauthorized"}` | Any authentication failure, a Subscription that does not exist, or one without `spec.webhook`: the answer does not reveal which Subscriptions exist |
| `404` | The path is not `/webhook/subscriptions/<namespace>/<name>/<provider>[/<token>]` |
| `413` | The payload is over 1 MB |
| `429` | More than 10 requests a second (bursts of 20) from one source address, or 5 a second (bursts of 10) for one Subscription. Behind an Ingress every sender has the Ingress's address, so the per-Subscription limit is what separates them |

The reconciler polls at most once every 10 seconds per Subscription, however many
deliveries arrive.

## Enabling it on a Subscription

Each Subscription opts in with `spec.webhook.secretRef`, a Secret in its namespace whose
key `token` (at least 16 characters) authenticates the deliveries. Like every Secret a
Subscription reads, it must be labelled `kardinal.io/referenceable: "true"`:

```bash
kubectl create secret generic my-app-webhook -n default --from-literal=token="$(openssl rand -hex 24)"
kubectl label secret my-app-webhook -n default kardinal.io/referenceable=true
```

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-image
  namespace: default
spec:
  type: image
  pipeline: my-app-pipeline
  image:
    registry: docker.io/myorg/my-app
    semverConstraint: ">=1.0.0"
    interval: 1h                 # a fallback; the webhook triggers the polls
  webhook:
    secretRef:
      name: my-app-webhook
```

The token is read on every delivery: rotate it by updating the Secret and the sender.

## Setting up each sender

In the URLs below, `$KARDINAL` is the controller's webhook address
(`https://kardinal.example.com`) and `$TOKEN` the Secret's `token`.

| Sender | Where | URL | Authentication |
|---|---|---|---|
| Docker Hub | Repository, Webhooks | `$KARDINAL/webhook/subscriptions/default/my-app-image/dockerhub/$TOKEN` | The token in the URL (Docker Hub cannot sign) |
| Quay | Repository settings, Notifications, "Push to Repository", Webhook POST | `$KARDINAL/webhook/subscriptions/default/my-app-image/quay/$TOKEN` | The token in the URL |
| Harbor | Project, Webhooks, event "Artifact pushed" (or "Chart uploaded"), HTTP | `$KARDINAL/webhook/subscriptions/default/my-app-image/harbor` | Auth Header: `$TOKEN` (sent as the `Authorization` header) |
| GHCR | Repository or organization Settings, Webhooks, event "Packages" (or "Registry packages"; "Pushes" for a git Subscription), content type JSON | `$KARDINAL/webhook/subscriptions/default/my-app-image/ghcr` | Secret: `$TOKEN` (GitHub signs `X-Hub-Signature-256`; a token in the URL is not accepted) |
| Artifactory | Administration, Webhooks, Docker or Artifact domain, event "pushed" or "deployed" | `$KARDINAL/webhook/subscriptions/default/my-app-image/artifactory` | Secret token: `$TOKEN` (`X-JFrog-Event-Auth`, plain or with "use secret for payload signing") |
| Anything else (CI, a script, GitLab, Gitea) | | `$KARDINAL/webhook/subscriptions/default/my-app-image/generic/$TOKEN` | The token in the URL, or no token in the URL and `X-Kardinal-Signature-256: sha256=<hex HMAC-SHA256 of the body with $TOKEN>` |

A token in the URL is as secret as the token itself: use HTTPS (`tls.enabled` or an
Ingress that terminates TLS), and prefer a signing sender where you have one.

```bash
# generic, signed
body='{"image":"myorg/my-app","tag":"v1.4.0"}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$TOKEN" | cut -d' ' -f2)
curl -X POST -H "X-Kardinal-Signature-256: sha256=$sig" -d "$body" \
  "$KARDINAL/webhook/subscriptions/default/my-app-image/generic"
```

The generic provider accepts any body. The others check that the payload is the
provider's push event: Docker Hub `push_data`, Quay `updated_tags`, Harbor `type`
`PUSH_ARTIFACT`, `pushImage` or `UPLOAD_CHART`, Artifactory `event_type` `pushed` or
`deployed`, GitHub `X-GitHub-Event` `package`, `registry_package` or `push`.

## Troubleshooting

- A refusal for a Subscription that does not exist, or has no `spec.webhook`, reads a
  placeholder Secret and checks the token against a random key, so its 401 takes about as
  long as a wrong token's. A sender that can measure latency precisely may still tell the
  two apart (the Secret read of an existing and a missing Secret differ slightly); the
  Subscription names are not secret in most clusters, but do not put anything sensitive in
  them.
- Every refusal is logged by the controller with the reason (`subscription webhook
  refused`), never with the token: a missing `spec.webhook`, a missing Secret, a token
  under 16 characters, or no valid token or signature.
- `kubectl get subscription my-app-image -o jsonpath='{.metadata.annotations.kardinal\.io/refresh} {.status.lastRefreshRequest}'`
  shows the last request and the last one a poll answered. Equal values mean the poll ran;
  check `status.phase` and `status.message` for its result.
- A delivery that the reconciler answered without a Bundle means the source had no new
  artifact that passes the filters: the receiver does not choose the tag, the poll does.
