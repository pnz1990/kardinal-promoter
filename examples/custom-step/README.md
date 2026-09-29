# Custom Step Example — Version Gate

This example runs a custom promotion step server that enforces a version gate:
pre-release image tags (containing `alpha`, `beta`, `rc`, `snapshot`, or `dev`)
are rejected in the `prod` environment.

## Run locally

```bash
go run examples/custom-step/server.go
# Listening on :8080

curl -s -X POST http://localhost:8080/step \
  -H "Content-Type: application/json" \
  -d '{"bundle":{"type":"image","images":[{"repository":"ghcr.io/myorg/app","tag":"v2.0.0-beta"}]},"environment":"prod","inputs":{},"outputs_so_far":{}}' | jq .
# {"result":"fail","message":"version gate: ... is a pre-release tag"}

curl -s -X POST http://localhost:8080/step \
  -H "Content-Type: application/json" \
  -d '{"bundle":{"type":"image","images":[{"repository":"ghcr.io/myorg/app","tag":"v2.0.0"}]},"environment":"prod","inputs":{},"outputs_so_far":{}}' | jq .
# {"result":"pass","message":"version gate: all images are stable releases","outputs":{"gated_at":"..."}}
```

## Deploy to Kubernetes

```bash
# Build and push the image
docker build -t ghcr.io/myorg/custom-step-server:latest examples/custom-step/
docker push ghcr.io/myorg/custom-step-server:latest

# Deploy
kubectl apply -f examples/custom-step/k8s/
```

## Use in a Pipeline

> **Not runnable yet.** A custom step runs only when it is listed in
> `spec.environments[].steps`, and `steps` is not implemented yet. The
> controller rejects a Pipeline that sets it, so the `steps` block in
> `pipeline.yaml` is commented out. Applied as is, the Pipeline promotes with
> the default steps and does not call this server. See
> [docs/custom-steps.md](../../docs/custom-steps.md).

`examples/custom-step/pipeline.yaml` shows where the version gate will go in the prod environment:

```bash
kubectl apply -f examples/custom-step/pipeline.yaml   # default steps only, for now
```
