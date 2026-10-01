// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// SuspendCanary sets the Canary's spec.suspend. Flagger skips every analysis
// tick of a suspended Canary, so it keeps its phase after its target changes:
// a test uses that to stretch the moment between the GitOps tool applying a
// new revision and Flagger's next tick noticing it.
func (e *Env) SuspendCanary(t *testing.T, ns, name string, suspend bool) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	if _, err := e.Dynamic.Resource(CanaryGVR).Namespace(ns).Patch(context.Background(), name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("set suspend=%t on Canary %s/%s: %v", suspend, ns, name, err)
	}
}

// CanaryPhaseSetAt is when Flagger set the current phase of the Canary
// ns/name: the lastUpdateTime of its Promoted condition, whose reason is the
// phase. Flagger rewrites status.lastTransitionTime at every tick of a Failed
// Canary, so that field does not date the phase.
func (e *Env) CanaryPhaseSetAt(t *testing.T, ns, name string) time.Time {
	t.Helper()
	u, err := e.Dynamic.Resource(CanaryGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Canary %s/%s: %v", ns, name, err)
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, cond := range conditions {
		m, _ := cond.(map[string]interface{})
		if m["type"] != "Promoted" || m["reason"] != phase {
			continue
		}
		s, _ := m["lastUpdateTime"].(string)
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("Canary %s/%s: Promoted condition lastUpdateTime %q: %v", ns, name, s, err)
		}
		return at
	}
	t.Fatalf("Canary %s/%s has no Promoted condition for phase %q", ns, name, phase)
	return time.Time{}
}
