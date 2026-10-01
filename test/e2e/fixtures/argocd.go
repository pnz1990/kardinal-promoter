// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fixtures

import (
	"fmt"
	"strings"
)

// ChartPath is the podinfo Helm chart's directory in a HelmChartRepo.
const ChartPath = "charts/podinfo"

// HelmChartRepo is a repo with one podinfo Helm chart at ChartPath, for
// Argo CD Applications that set the image through
// spec.source.helm.valuesObject (update.strategy argocd). The chart renders
// one Deployment named name. Its tag is .Values.image.tag (default V1), or
// .Values.podinfo.image.tag when the podinfo key is set, so a test can
// point update.argocd.imageKey at a nested key.
func HelmChartRepo(name string) map[string][]byte {
	deploy := strings.Replace(deployment(name, Image+":TAG"), Image+":TAG",
		Image+`:{{ (.Values.podinfo | default .Values).image.tag }}`, 1)
	return map[string][]byte{
		ChartPath + "/Chart.yaml": []byte(`apiVersion: v2
name: podinfo
version: 0.1.0
`),
		ChartPath + "/values.yaml":               []byte("image:\n  tag: " + V1 + "\n"),
		ChartPath + "/templates/deployment.yaml": []byte(deploy),
	}
}

// WithHook adds an Argo CD hook Job named name to env's overlay in files (a
// KustomizeRepo). The Job runs script with sh in the podinfo image, which the
// overlay's images entry retags with every promotion, so a script can tell
// versions apart with ./podinfo --version. phase is PreSync, Sync or
// PostSync; a Job that exits non-zero fails the sync operation.
//
// Argo CD deletes the Job as soon as it succeeds, so the next sync creates a
// new one. Leaving the completed Job for the next sync to delete and recreate
// under the same name is racy: Argo CD can read the old Job's Complete status
// and mark the operation Succeeded before the new Job runs (seen on Argo CD 3
// in TestHealth_ArgoFailures).
func WithHook(files map[string][]byte, env, name, phase, script string) {
	kust := Path(env) + "/kustomization.yaml"
	files[kust] = []byte(strings.Replace(string(files[kust]), "  - service.yaml\n",
		"  - service.yaml\n  - "+name+".yaml\n", 1))
	files[Path(env)+"/"+name+".yaml"] = []byte(fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  annotations:
    argocd.argoproj.io/hook: %s
    argocd.argoproj.io/hook-delete-policy: HookSucceeded,BeforeHookCreation
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: hook
          image: %s:%s
          command: ["sh", "-c", %q]
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
`, name, phase, Image, V1, script))
}
