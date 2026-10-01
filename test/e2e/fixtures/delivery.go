// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fixtures

import (
	"fmt"
	"strings"
)

// IndefinitePause makes a Rollout's canary stop at its pause step until it is
// promoted (a `pause: {}` step).
const IndefinitePause = ""

// RolloutRepo is KustomizeRepo with an Argo Rollouts canary instead of a
// Deployment: environments/<env>/{kustomization.yaml,rollout.yaml}. The
// Rollout, named Workload(env), has 2 replicas and two canary steps:
// setWeight 50, then a pause of pause (a Go-style duration such as "10s", or
// IndefinitePause). A new revision whose pods never become ready passes its
// 30s progress deadline and Argo Rollouts aborts it: the Rollout goes
// Degraded and keeps serving the stable revision.
func RolloutRepo(app App, pause string) map[string][]byte {
	pauseStep := "{}"
	if pause != IndefinitePause {
		pauseStep = "{duration: " + pause + "}"
	}
	return overlays(app, "rollout.yaml", func(env, image string) string {
		return strings.NewReplacer("NAME", Workload(env), "IMAGE", image, "PAUSE", pauseStep).Replace(`apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: NAME
  labels:
    app.kubernetes.io/name: NAME
spec:
  replicas: 2
  progressDeadlineSeconds: 30
  progressDeadlineAbort: true
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      app.kubernetes.io/name: NAME
  template:
    metadata:
      labels:
        app.kubernetes.io/name: NAME
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
  strategy:
    canary:
      steps:
        - setWeight: 50
        - pause: PAUSE
`)
	})
}

// CanaryRepo is KustomizeRepo with a Flagger Canary on each environment's
// Deployment: environments/<env>/{kustomization.yaml,deployment.yaml,canary.yaml}.
// The Canary, named Workload(env), uses the kubernetes provider (Flagger
// scales the primary and canary Deployments; no mesh, no metrics) and
// analyzes a new revision in 2 iterations 10s apart. A revision whose pods
// never become ready fails the analysis after the 60s progress deadline.
//
// The Deployment sets no replicas: Flagger scales it to zero once it copied
// it to <name>-primary, and Argo CD must not scale it back.
func CanaryRepo(app App) map[string][]byte {
	files := overlays(app, "deployment.yaml", func(env, image string) string {
		return strings.Replace(deployment(Workload(env), image), "  replicas: 1\n", "", 1)
	})
	for _, env := range app.Envs {
		files[Path(env)+"/kustomization.yaml"] = []byte(strings.Replace(string(files[Path(env)+"/kustomization.yaml"]),
			"  - deployment.yaml\n", "  - deployment.yaml\n  - canary.yaml\n", 1))
		files[Path(env)+"/canary.yaml"] = []byte(strings.ReplaceAll(`apiVersion: flagger.app/v1beta1
kind: Canary
metadata:
  name: NAME
spec:
  provider: kubernetes
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: NAME
  progressDeadlineSeconds: 60
  service:
    port: 9898
    targetPort: 9898
  analysis:
    interval: 10s
    threshold: 2
    iterations: 2
`, "NAME", Workload(env)))
	}
	return files
}

// PrimaryWorkload is the Deployment Flagger promotes env's Canary into.
func PrimaryWorkload(env string) string { return Workload(env) + "-primary" }

// overlays is one kustomize overlay per environment holding a single
// workload manifest named file, which workload renders for the env and image.
func overlays(app App, file string, workload func(env, image string) string) map[string][]byte {
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
  - %s
images:
  - name: %s
    newTag: %s
`, app.Namespace, file, Image, tag))
		files[Path(env)+"/"+file] = []byte(workload(env, Image+":"+tag))
	}
	return files
}
