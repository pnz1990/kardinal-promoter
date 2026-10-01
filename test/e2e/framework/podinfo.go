// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FailReadiness makes every podinfo pod of the Deployment ns/name fail its
// readiness probe (podinfo's POST /readyz/disable), so the Deployment loses
// its available replicas while the image stays the same. It is the e2e
// stand-in for a release that turns unhealthy after it rolled out.
func (e *Env) FailReadiness(t *testing.T, ns, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pods, err := e.Kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + name})
	if err != nil {
		t.Fatalf("list pods of %s/%s: %v", ns, name, err)
	}
	disabled := 0
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil {
			continue // an old replica on its way out
		}
		disabled++
		path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:9898/proxy/readyz/disable", ns, p.Name)
		if err := e.Kube.CoreV1().RESTClient().Post().AbsPath(path).Do(ctx).Error(); err != nil {
			t.Fatalf("disable readiness of pod %s/%s: %v", ns, p.Name, err)
		}
	}
	if disabled == 0 {
		t.Fatalf("no running pods for %s/%s", ns, name)
	}
}
