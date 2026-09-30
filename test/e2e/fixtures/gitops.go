// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package fixtures builds the GitOps repos live e2e tests promote through.
//
// The test app is podinfo, pinned to versions that exist: it serves Prometheus
// metrics and has a readiness probe, so it can drive health, canary analysis
// and MetricCheck tests. A tag that does not exist (BrokenTag) gives a real
// unhealthy rollout: the new pods never pull, so the Deployment never becomes
// Available.
package fixtures

import (
	"fmt"
	"strings"
)

// Image is the repository every fixture deploys.
const Image = "ghcr.io/stefanprodan/podinfo"

// Real podinfo tags, oldest first. Tests promote V2 and V3 over V1.
const (
	V1 = "6.13.0"
	V2 = "6.14.1"
	V3 = "6.15.0"
	// BrokenTag does not exist, so a Deployment updated to it never becomes Available.
	BrokenTag = "0.0.0-e2e-missing"
)

// App describes one podinfo app promoted through Envs.
type App struct {
	// Namespace is where every environment's workload runs (the test namespace).
	Namespace string
	// Envs are the environment names in promotion order.
	Envs []string
	// Tag is the image tag every environment starts at. Default V1.
	Tag string
}

// Workload is the Deployment and Service name of env.
func Workload(env string) string { return "podinfo-" + env }

// Path is env's directory in the repo, the Pipeline's environments[].path.
func Path(env string) string { return "environments/" + env }

// KustomizeRepo is a repo with one kustomize overlay per environment:
// environments/<env>/{kustomization.yaml,deployment.yaml,service.yaml}.
// The kustomization pins Image to app.Tag, which the kustomize update
// strategy rewrites.
func KustomizeRepo(app App) map[string][]byte {
	tag := app.Tag
	if tag == "" {
		tag = V1
	}
	files := map[string][]byte{}
	for _, env := range app.Envs {
		files[Path(env)+"/kustomization.yaml"] = []byte(fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: %s
resources:
  - deployment.yaml
  - service.yaml
images:
  - name: %s
    newTag: %s
`, app.Namespace, Image, tag))
		files[Path(env)+"/deployment.yaml"] = []byte(deployment(Workload(env), Image+":"+tag))
		files[Path(env)+"/service.yaml"] = []byte(service(Workload(env)))
	}
	return files
}

// deployment is podinfo with a fast readiness probe and a short progress
// deadline, so both healthy and broken rollouts settle within a test timeout.
func deployment(name, image string) string {
	return strings.NewReplacer("NAME", name, "IMAGE", image).Replace(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: NAME
  labels:
    app.kubernetes.io/name: NAME
spec:
  replicas: 1
  progressDeadlineSeconds: 60
  selector:
    matchLabels:
      app.kubernetes.io/name: NAME
  template:
    metadata:
      labels:
        app.kubernetes.io/name: NAME
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9898"
    spec:
      containers:
        - name: podinfo
          image: IMAGE
          ports:
            - name: http
              containerPort: 9898
          readinessProbe:
            httpGet:
              path: /readyz
              port: 9898
            periodSeconds: 2
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
`)
}

func service(name string) string {
	return strings.ReplaceAll(`apiVersion: v1
kind: Service
metadata:
  name: NAME
  labels:
    app.kubernetes.io/name: NAME
spec:
  selector:
    app.kubernetes.io/name: NAME
  ports:
    - name: http
      port: 9898
      targetPort: http
`, "NAME", name)
}
