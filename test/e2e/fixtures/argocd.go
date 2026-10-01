// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fixtures

import (
	"fmt"
	"strings"
)

// MarkerExists is a hook script condition that is true once the marker
// Service name exists in ns (framework.Env.CreateMarker): its cluster DNS
// name resolves. A script waits for the test with
// "until <MarkerExists>; do sleep 1; done".
func MarkerExists(ns, name string) string {
	return fmt.Sprintf("nslookup %s.%s.svc.cluster.local >/dev/null 2>&1", name, ns)
}

// WithHook adds an Argo CD hook Job named name to env's overlay in files (a
// KustomizeRepo). The Job runs script with sh in the podinfo image, which the
// overlay's images entry retags with every promotion, so a script can tell
// versions apart with ./podinfo --version. phase is PreSync, Sync or
// PostSync; a Job that exits non-zero fails the sync operation.
//
// Argo CD deletes the Job as soon as it succeeds, so the next sync creates a
// new one. With BeforeHookCreation alone, the completed Job stays for the next
// sync to delete and recreate under the same name, and Argo CD v3.5.3 marks
// that sync Succeeded from the old Job's Complete status: in 4 of 4 tries
// with a PostSync hook that fails on V2, the V2 operation was Succeeded and
// the step Verified while the new Job still ran, and once after it failed.
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
