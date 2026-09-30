// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fixtures

import (
	"fmt"
	"strings"
)

// This file holds the repos and workloads the flow suites (graph, bundle,
// pipeline, step, examples) need beyond KustomizeRepo.

// V2Digest is the manifest-list digest of Image:V2, for Bundles that pin a
// digest.
const V2Digest = "sha256:f9537f729129d339aaef049b76ab2cd4ff06a424f99e5f6a5923c10621018fb1"

// Pause is a second image for multi-image Bundles: a sidecar that runs
// anywhere and pulls fast. PauseV1 is what repos start at.
const (
	Pause   = "registry.k8s.io/pause"
	PauseV1 = "3.9"
	PauseV2 = "3.10"
)

// Deployment is the podinfo Deployment manifest KustomizeRepo writes.
func Deployment(name, image string) string { return deployment(name, image) }

// Service is the podinfo Service manifest KustomizeRepo writes.
func Service(name string) string { return service(name) }

// Kustomization is an overlay over deployment.yaml and service.yaml in
// namespace that pins Image to tag, plus any extra images entries (YAML list
// items, indented by two spaces).
func Kustomization(namespace, tag string, extra ...string) string {
	return fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: %s
resources:
  - deployment.yaml
  - service.yaml
images:
  - name: %s
    newTag: %s
%s`, namespace, Image, tag, strings.Join(extra, ""))
}

// KustomizeRepoFor is KustomizeRepo with each environment's workload named
// name(env) in namespace namespace(env): the layout the examples assume,
// where resource health looks for Deployment <pipeline> in namespace <env>.
func KustomizeRepoFor(envs []string, name, namespace func(env string) string) map[string][]byte {
	files := map[string][]byte{}
	for _, env := range envs {
		files[Path(env)+"/kustomization.yaml"] = []byte(Kustomization(namespace(env), V1))
		files[Path(env)+"/deployment.yaml"] = []byte(deployment(name(env), Image+":"+V1))
		files[Path(env)+"/service.yaml"] = []byte(service(name(env)))
	}
	return files
}

// SidecarRepo is KustomizeRepo where every Deployment also runs a Pause
// sidecar at PauseV1, pinned by a second images entry.
func SidecarRepo(app App) map[string][]byte {
	files := KustomizeRepo(app)
	for _, env := range app.Envs {
		files[Path(env)+"/kustomization.yaml"] = []byte(Kustomization(app.Namespace, V1,
			fmt.Sprintf("  - name: %s\n    newTag: %s\n", Pause, PauseV1)))
		// deployment ends with the podinfo container, so the sidecar is the
		// next item of the containers list.
		files[Path(env)+"/deployment.yaml"] = []byte(deployment(Workload(env), Image+":"+V1) + fmt.Sprintf(`        - name: sidecar
          image: %s:%s
          resources:
            requests:
              cpu: 1m
              memory: 4Mi
`, Pause, PauseV1))
	}
	return files
}

// Helm chart layout of HelmRepo. The Pipeline's update.helm points at
// HelmValuesFile and HelmImagePath; the Argo CD Application renders the chart
// with HelmValuesFile.
const (
	HelmValuesFile = "values-e2e.yaml"
	HelmImagePath  = ".app.image.tag"
)

// HelmRepo is a repo with one Helm chart per environment:
// environments/<env>/{Chart.yaml,values.yaml,values-e2e.yaml,templates/}.
// values-e2e.yaml pins app.image.tag to app.Tag (default V1); values.yaml
// holds an unrelated image.tag that must never change.
func HelmRepo(app App) map[string][]byte {
	tag := app.Tag
	if tag == "" {
		tag = V1
	}
	files := map[string][]byte{}
	for _, env := range app.Envs {
		dir := Path(env)
		files[dir+"/Chart.yaml"] = []byte(fmt.Sprintf("apiVersion: v2\nname: %s\nversion: 0.1.0\n", Workload(env)))
		files[dir+"/values.yaml"] = []byte("# Chart defaults. kardinal writes " + HelmValuesFile + ".\nimage:\n  tag: untouched\n")
		files[dir+"/"+HelmValuesFile] = []byte(fmt.Sprintf("app:\n  image:\n    repository: %s\n    tag: %q\n", Image, tag))
		files[dir+"/templates/deployment.yaml"] = []byte(deployment(Workload(env),
			"{{ .Values.app.image.repository }}:{{ .Values.app.image.tag }}"))
		files[dir+"/templates/service.yaml"] = []byte(service(Workload(env)))
	}
	return files
}

// SlowGitServer is a podinfo Deployment and Service named name that answer
// every request after 30 seconds. A Pipeline whose git.url points at it gets
// a git server that hangs, which drives step timeouts. It has no readiness
// probe: a probe would time out too, and the Service would have no endpoints.
func SlowGitServer(name string) (deploymentYAML, serviceYAML string) {
	d := strings.NewReplacer("NAME", name, "IMAGE", Image+":"+V1).Replace(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: NAME
  labels:
    app.kubernetes.io/name: NAME
spec:
  replicas: 1
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
          command: ["./podinfo", "--port=9898", "--random-delay=true", "--random-delay-unit=s",
            "--random-delay-min=30", "--random-delay-max=31"]
          ports:
            - name: http
              containerPort: 9898
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
`)
	return d, service(name)
}
