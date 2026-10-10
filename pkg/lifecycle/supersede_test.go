// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func TestSupersedingSiblings(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	bundle := func(name, typ, phase string, at time.Time) v1alpha1.Bundle {
		return v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(at)},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app", Type: typ},
			Status:     v1alpha1.BundleStatus{Phase: phase},
		}
	}
	b1 := bundle("b1", "image", "Promoting", t0)
	rejectedB2 := bundle("b2", "image", "Promoting", t0.Add(time.Second))
	rejectedB2.Spec.Rejected = &v1alpha1.BundleRejection{Reason: "bad build"}
	otherPipeline := bundle("b2", "image", "Promoting", t0.Add(time.Second))
	otherPipeline.Spec.Pipeline = "other"
	held := &v1alpha1.Pipeline{Spec: v1alpha1.PipelineSpec{Holds: []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "b1"}}}}
	tests := []struct {
		name          string
		pipeline      *v1alpha1.Pipeline
		sibling       v1alpha1.Bundle
		countVerified bool
		want          bool
	}{
		{name: "newer, same type, in flight", sibling: bundle("b2", "image", "Promoting", t0.Add(time.Second)), want: true},
		{name: "newer, Available", sibling: bundle("b2", "image", "Available", t0.Add(time.Second)), want: true},
		{name: "older", sibling: bundle("b0", "image", "Promoting", t0.Add(-time.Second))},
		{name: "another type", sibling: bundle("b2", "config", "Promoting", t0.Add(time.Second))},
		{name: "a mixed one over an image one", sibling: bundle("b2", "mixed", "Promoting", t0.Add(time.Second))},
		{name: "another pipeline", sibling: otherPipeline},
		{name: "rejected", sibling: rejectedB2},
		{name: "Failed", sibling: bundle("b2", "image", "Failed", t0.Add(time.Second))},
		{name: "Superseded", sibling: bundle("b2", "image", "Superseded", t0.Add(time.Second))},
		{name: "Verified without countVerified", sibling: bundle("b2", "image", "Verified", t0.Add(time.Second))},
		{name: "Verified with countVerified", sibling: bundle("b2", "image", "Verified", t0.Add(time.Second)),
			countVerified: true, want: true},
		{name: "b is held", pipeline: held, sibling: bundle("b2", "image", "Promoting", t0.Add(time.Second))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			siblings := []v1alpha1.Bundle{b1, tt.sibling}
			rejected := lifecycle.RejectedArtifactsOf(siblings, "app")
			got := lifecycle.SupersedingSiblings(tt.pipeline, &b1, siblings, rejected, tt.countVerified, "")
			assert.Equal(t, tt.want, len(got) == 1, "%v", got)
		})
	}
}

// TestSupersedingSiblings_FleetTargetRollback (D1 with #1603): the rollback
// of one fleet target supersedes the fleet Bundle in that target only, not
// as a whole; a rollback of the same target supersedes an older one.
func TestSupersedingSiblings_FleetTargetRollback(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	p := &v1alpha1.Pipeline{Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod", Fleet: &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}},
	}}}
	bundle := func(name string, at time.Time, target string) v1alpha1.Bundle {
		b := v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(at)},
			Spec:       v1alpha1.BundleSpec{Pipeline: "app", Type: "image"},
			Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
		}
		if target != "" {
			b.Labels = map[string]string{lifecycle.LabelRollback: "true"}
			b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: target}
		}
		return b
	}
	fleet := bundle("fleet", t0, "")
	rbEU := bundle("rb-eu", t0.Add(time.Minute), "prod-eu")
	rbEU2 := bundle("rb-eu-2", t0.Add(2*time.Minute), "prod-eu")
	n := func(b v1alpha1.Bundle, sibling v1alpha1.Bundle, env string) int {
		return len(lifecycle.SupersedingSiblings(p, &b, []v1alpha1.Bundle{b, sibling}, nil, true, env))
	}
	assert.Equal(t, 0, n(fleet, rbEU, ""), "the fleet Bundle is not superseded as a whole")
	assert.Equal(t, 1, n(fleet, rbEU, "prod-eu"), "it is superseded in the target rolled back")
	assert.Equal(t, 0, n(fleet, rbEU, "prod-us"), "not in another target")
	assert.Equal(t, 1, n(rbEU, rbEU2, ""), "a rollback of the same target supersedes an older one")
	assert.Equal(t, "prod-eu", lifecycle.TargetRollback(p, &rbEU))
	assert.Equal(t, "", lifecycle.TargetRollback(nil, &rbEU))
}
