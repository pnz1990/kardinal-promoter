// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

const ns = "default"

var t0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.Pipeline{}).
		Build()
}

func pipeline(name string, envs ...string) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: "uid-" + types.UID(name)}}
	for _, e := range envs {
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e})
	}
	return p
}

// bundle builds a Verified image Bundle created at t0+minute with one image tag.
func bundle(name, pipe, tag string, minute int) *v1alpha1.Bundle {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			CreationTimestamp: metav1.NewTime(t0.Add(time.Duration(minute) * time.Minute)),
		},
		Spec: v1alpha1.BundleSpec{
			Type:       "image",
			Pipeline:   pipe,
			Provenance: &v1alpha1.BundleProvenance{CommitSHA: "sha-" + tag, Author: "ci"},
		},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	}
	if tag != "" {
		b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: tag}}
	}
	return b
}

// step builds a PromotionStep for bundle/env created at t0+minute. A Verified
// step gets a Verified condition at the same time.
func step(bundle, pipe, env, state string, minute int) *v1alpha1.PromotionStep {
	at := metav1.NewTime(t0.Add(time.Duration(minute) * time.Minute))
	s := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name: bundle + "-" + env, Namespace: ns, CreationTimestamp: at,
			Labels: map[string]string{
				"kardinal.io/pipeline": pipe, "kardinal.io/bundle": bundle, "kardinal.io/environment": env,
			},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: pipe, BundleName: bundle, Environment: env},
		Status: v1alpha1.PromotionStepStatus{State: state},
	}
	if state == "Verified" {
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: at}}
	}
	return s
}

func phase(b *v1alpha1.Bundle, p string) *v1alpha1.Bundle {
	b.Status.Phase = p
	return b
}

func named(s *v1alpha1.PromotionStep, name string) *v1alpha1.PromotionStep {
	s.Name = name
	return s
}

func TestCompareCreation(t *testing.T) {
	sameSecond := t0.Add(5 * time.Second)
	withAnn := func(name string, ts time.Time, ann string) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(ts)}}
		if ann != "" {
			b.Annotations = map[string]string{lifecycle.AnnotationCreatedAt: ann}
		}
		return b
	}
	tests := []struct {
		name string
		a, b *v1alpha1.Bundle
		want int
	}{
		{name: "earlier second wins over name",
			a: withAnn("zzz", t0, ""), b: withAnn("aaa", sameSecond, ""), want: -1},
		{name: "same second: created-at decides, not the name",
			a:    withAnn("app-9999", sameSecond, sameSecond.Add(100*time.Millisecond).Format(time.RFC3339Nano)),
			b:    withAnn("app-0001", sameSecond, sameSecond.Add(900*time.Millisecond).Format(time.RFC3339Nano)),
			want: -1},
		{name: "same second without annotations falls back to the name",
			a: withAnn("a", sameSecond, ""), b: withAnn("b", sameSecond, ""), want: -1},
		{name: "unparseable annotation falls back to the name",
			a: withAnn("b", sameSecond, "garbage"), b: withAnn("a", sameSecond, sameSecond.Format(time.RFC3339Nano)), want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lifecycle.CompareCreation(tc.a, tc.b))
			assert.Equal(t, -tc.want, lifecycle.CompareCreation(tc.b, tc.a))
		})
	}
}

func TestStampCreatedAt(t *testing.T) {
	b := &v1alpha1.Bundle{}
	lifecycle.StampCreatedAt(b, time.Time{})
	assert.Empty(t, b.Annotations, "zero time stamps nothing")

	now := time.Date(2026, 9, 1, 10, 0, 0, 123456789, time.UTC)
	lifecycle.StampCreatedAt(b, now)
	assert.Equal(t, "2026-09-01T10:00:00.123456789Z", b.Annotations[lifecycle.AnnotationCreatedAt])

	lifecycle.StampCreatedAt(b, now.Add(time.Hour))
	assert.Equal(t, "2026-09-01T10:00:00.123456789Z", b.Annotations[lifecycle.AnnotationCreatedAt], "an existing stamp is kept")
}
