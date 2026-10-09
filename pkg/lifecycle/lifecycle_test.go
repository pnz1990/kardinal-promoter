// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"slices"
	"strings"
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
		{name: "same second: an unstamped Bundle sorts before a stamped one, whatever the names",
			a: withAnn("b", sameSecond, ""), b: withAnn("a", sameSecond, sameSecond.Format(time.RFC3339Nano)), want: -1},
		{name: "unparseable annotation counts as unstamped",
			a: withAnn("b", sameSecond, "garbage"), b: withAnn("a", sameSecond, sameSecond.Format(time.RFC3339Nano)), want: -1},
		{name: "same second and same stamp falls back to the name",
			a:    withAnn("a", sameSecond, sameSecond.Format(time.RFC3339Nano)),
			b:    withAnn("b", sameSecond, sameSecond.Format(time.RFC3339Nano)),
			want: -1},
		{name: "same object", a: withAnn("a", sameSecond, ""), b: withAnn("a", sameSecond, ""), want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lifecycle.CompareCreation(tc.a, tc.b))
			assert.Equal(t, -tc.want, lifecycle.CompareCreation(tc.b, tc.a))
		})
	}
}

// TestCompareCreation_TotalOrderWithMixedStamps is #1316: in one second, x is
// stamped .900, y is not stamped and z is stamped .100. Comparing the stamp
// only when both Bundles had one made z < x < y < z, so the sorted order
// depended on the input order. Every input order must sort to y, z, x.
func TestCompareCreation_TotalOrderWithMixedStamps(t *testing.T) {
	second := t0.Add(5 * time.Second)
	mk := func(name string, stamp time.Duration) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(second)}}
		if stamp > 0 {
			b.Annotations = map[string]string{lifecycle.AnnotationCreatedAt: second.Add(stamp).Format(time.RFC3339Nano)}
		}
		return b
	}
	x, y, z := mk("x", 900*time.Millisecond), mk("y", 0), mk("z", 100*time.Millisecond)
	orders := [][]*v1alpha1.Bundle{
		{x, y, z}, {x, z, y}, {y, x, z}, {y, z, x}, {z, x, y}, {z, y, x},
	}
	for _, in := range orders {
		names := func(bs []*v1alpha1.Bundle) []string {
			out := make([]string, 0, len(bs))
			for _, b := range bs {
				out = append(out, b.Name)
			}
			return out
		}
		t.Run(strings.Join(names(in), ""), func(t *testing.T) {
			got := slices.Clone(in)
			slices.SortFunc(got, lifecycle.CompareCreation)
			assert.Equal(t, []string{"y", "z", "x"}, names(got))
		})
	}
}

// TestCurrentBundle pins the pipeline-level current Bundle rule shared by the
// UI API (activeBundleName) and kardinal get pipelines.
func TestCurrentBundle(t *testing.T) {
	b := func(name string, minute int, p string) v1alpha1.Bundle {
		return *phase(bundle(name, "app", "", minute), p)
	}
	tests := []struct {
		name    string
		bundles []v1alpha1.Bundle
		want    string
	}{
		{name: "no bundles", want: ""},
		{name: "newest wins whatever its phase",
			bundles: []v1alpha1.Bundle{b("b1", 1, "Verified"), b("b2", 2, "Failed")}, want: "b2"},
		{name: "a new bundle with no phase yet is current",
			bundles: []v1alpha1.Bundle{b("b2", 2, ""), b("b1", 1, "Verified")}, want: "b2"},
		{name: "a newer Superseded bundle is skipped",
			bundles: []v1alpha1.Bundle{b("b1", 1, "Verified"), b("b2", 2, "Superseded")}, want: "b1"},
		{name: "every bundle Superseded: the newest",
			bundles: []v1alpha1.Bundle{b("b2", 2, "Superseded"), b("b1", 1, "Superseded")}, want: "b2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lifecycle.CurrentBundle(tc.bundles)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Name)
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

// TestCompareAuditEvents orders by spec.timestamp, then kardinal.io/created-at
// (records without it first), then name.
func TestCompareAuditEvents(t *testing.T) {
	sec := metav1.NewTime(t0)
	ae := func(name string, ts metav1.Time, createdAt string) *v1alpha1.AuditEvent {
		a := &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AuditEventSpec{Timestamp: ts}}
		if createdAt != "" {
			a.Annotations = map[string]string{lifecycle.AnnotationCreatedAt: createdAt}
		}
		return a
	}
	later := metav1.NewTime(t0.Add(time.Second))
	tests := []struct {
		name string
		a, b *v1alpha1.AuditEvent
		want int
	}{
		{"timestamp decides", ae("z", sec, "2099-01-01T00:00:00Z"), ae("a", later, ""), -1},
		{"created-at within a second", ae("z-success", sec, t0.Add(100*time.Millisecond).Format(time.RFC3339Nano)),
			ae("a-failure", sec, t0.Add(200*time.Millisecond).Format(time.RFC3339Nano)), -1},
		{"no annotation first", ae("z", sec, ""), ae("a", sec, t0.Format(time.RFC3339Nano)), -1},
		{"name last", ae("a", sec, ""), ae("b", sec, ""), -1},
		{"same", ae("a", sec, ""), ae("a", sec, ""), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, lifecycle.CompareAuditEvents(tt.a, tt.b))
			assert.Equal(t, -tt.want, lifecycle.CompareAuditEvents(tt.b, tt.a))
		})
	}
}
