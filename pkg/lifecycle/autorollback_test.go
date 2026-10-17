// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestAutoRollbackName_FitsBundleLabel: automatic rollback Bundle names are
// deterministic and valid kardinal.io/bundle label values, however long the
// failing Bundle's name is.
func TestAutoRollbackName_FitsBundleLabel(t *testing.T) {
	long := strings.Repeat("a", 60)
	tests := []struct {
		name   string
		bundle string
		want   string
	}{
		{name: "short name is readable", bundle: "app-v2", want: "app-v2-rollback-alarm"},
		{name: "long name is shortened", bundle: long},
		{name: "long name ending in a dash is shortened", bundle: strings.Repeat("a", 40) + "-" + strings.Repeat("b", 30)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lifecycle.AutoRollbackName(tc.bundle, "alarm")
			if tc.want != "" {
				assert.Equal(t, tc.want, got)
			}
			assert.LessOrEqual(t, len(got), 63)
			assert.Empty(t, validation.IsValidLabelValue(got))
			assert.Empty(t, validation.IsDNS1123Subdomain(got))
			assert.Equal(t, got, lifecycle.AutoRollbackName(tc.bundle, "alarm"), "deterministic")
			assert.NotEqual(t, got, lifecycle.AutoRollbackName(tc.bundle, "policy"), "distinct per source")
		})
	}
	assert.NotEqual(t, lifecycle.AutoRollbackName(long+"x", "alarm"), lifecycle.AutoRollbackName(long+"y", "alarm"),
		"shortened names stay distinct")
}

// TestFindRollback: FindRollback matches on the rolled-back Bundle and the environment.
func TestFindRollback(t *testing.T) {
	rb := func(name, from, env string) client.Object {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default",
				Labels: map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"},
				Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: from,
					lifecycle.AnnotationCreatedBy: lifecycle.ControllerCreator},
			},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: env},
				Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: lifecycle.ControllerCreator, Via: v1alpha1.ViaAutoRollback}},
		}
	}
	c := newClient(t, rb("rb-uat", "app-v2", "uat"), rb("rb-other", "app-v1", "prod"))
	ctx := context.Background()

	got, err := lifecycle.FindRollback(ctx, c, "default", "app", "uat", "app-v2")
	require.NoError(t, err)
	assert.Equal(t, "rb-uat", got)

	got, err = lifecycle.FindRollback(ctx, c, "default", "app", "prod", "app-v2")
	require.NoError(t, err)
	assert.Empty(t, got, "a rollback in another environment does not count")
}

// TestAutoRollbackName_LongEnvironment (#2082 QA): the rollback of a Bundle
// in an environment of any length up to 63 characters is a valid object
// name and label value, deterministic, never the Bundle's other rollback
// names, and different for different Bundles with long environment names.
func TestAutoRollbackName_LongEnvironment(t *testing.T) {
	for _, n := range []int{1, 37, 38, 47, 48, 62, 63} {
		env := strings.Repeat("e", n-1) + "z"
		for _, bundle := range []string{"app-1-4-0", strings.Repeat("b", 50) + "-1-4-0"} {
			got := lifecycle.AutoRollbackName(bundle, "alarm-"+env)
			assert.LessOrEqual(t, len(got), 63, "%d: %s", n, got)
			assert.Empty(t, validation.IsValidLabelValue(got), "%d: %s", n, got)
			assert.Empty(t, validation.IsDNS1123Subdomain(got), "%d: %s", n, got)
			assert.Equal(t, got, lifecycle.AutoRollbackName(bundle, "alarm-"+env), "deterministic")
			assert.NotEqual(t, lifecycle.AutoRollbackName(bundle, "alarm"), got, "%d: not the first environment's", n)
			assert.NotEqual(t, lifecycle.AutoRollbackName(bundle, "alarm-"+env+"x"), got, "%d: not another environment's", n)
			assert.NotEqual(t, lifecycle.AutoRollbackName(bundle+"x", "alarm-"+env), got, "%d: not another Bundle's", n)
		}
	}
}

// TestAutoRollbackNameIn (#2082): the first environment's rollback takes the
// plain name; another environment of the same Bundle, or the same one again,
// gets its own or its own again.
func TestAutoRollbackNameIn(t *testing.T) {
	ctx := context.Background()
	rb := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2-rollback-alarm", Namespace: "default",
		Annotations: map[string]string{lifecycle.AnnotationCreatedBy: lifecycle.ControllerCreator}},
		Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: "prod-eu"},
			Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: lifecycle.ControllerCreator, Via: v1alpha1.ViaAutoRollback}}}
	empty := newClient(t)
	got, err := lifecycle.AutoRollbackNameIn(ctx, empty, "default", "app-v2", "alarm", "prod-eu")
	require.NoError(t, err)
	assert.Equal(t, "app-v2-rollback-alarm", got, "nothing yet: the plain name")

	c := newClient(t, rb)
	got, err = lifecycle.AutoRollbackNameIn(ctx, c, "default", "app-v2", "alarm", "prod-eu")
	require.NoError(t, err)
	assert.Equal(t, "app-v2-rollback-alarm", got, "its own again")
	got, err = lifecycle.AutoRollbackNameIn(ctx, c, "default", "app-v2", "alarm", "prod-us")
	require.NoError(t, err)
	assert.Equal(t, "app-v2-rollback-alarm-prod-us", got, "another environment's is taken")

	taken, err := lifecycle.RollbackTaken(ctx, c, "default", "app-v2-rollback-alarm", "prod-us")
	require.NoError(t, err)
	assert.True(t, taken, "prod-eu's is not prod-us's")
	taken, err = lifecycle.RollbackTaken(ctx, c, "default", "app-v2-rollback-alarm", "prod-eu")
	require.NoError(t, err)
	assert.False(t, taken, "prod-eu's own")
}

// TestAutoRollbackName_Readable: a shortened name still reads as a rollback
// of its Bundle (the Bundle's start and "-rollback-"), and no two of many
// Bundle and environment names of every length share one.
func TestAutoRollbackName_Readable(t *testing.T) {
	seen := map[string]string{}
	for _, b := range []int{1, 9, 20, 40, 43, 44, 52, 60, 63} {
		bundle := strings.Repeat("b", b-1) + "1"
		for e := 1; e <= 63; e += 2 {
			env := strings.Repeat("e", e-1) + "z"
			for _, source := range []string{"alarm", "policy", "alarm-" + env, "policy-" + env} {
				full := bundle + "-rollback-" + source
				got := lifecycle.AutoRollbackName(bundle, source)
				assert.LessOrEqual(t, len(got), 63, full)
				assert.Empty(t, validation.IsValidLabelValue(got), full)
				assert.Empty(t, validation.IsDNS1123Subdomain(got), full)
				if len(full) <= 63 {
					assert.Equal(t, full, got, "a name that fits is unchanged")
				} else {
					assert.Contains(t, got, "-rollback-", "%s still says rollback", got)
					assert.True(t, strings.HasPrefix(got, bundle[:min(len(bundle), 30)]), "%s starts with its Bundle", got)
				}
				if other, ok := seen[got]; ok {
					assert.Equal(t, other, full, "%s and %s share %s", other, full, got)
				}
				seen[got] = full
			}
		}
	}
	t.Logf("%d names, none shared", len(seen))
}

// QA-3 on #2187: every clause of IsAutoRollback counts. A human stamp with
// the controller's creator (a person whose username is kardinal-controller),
// the controller's stamp with another creator, the controller's stamp via
// another path (subscription), or none, are not the automatic rollback.
// IsControllerRollback also counts the controller's rollback for a UI or
// API caller, never a client's own.
//
// Covers RB-AUTO-FORGED-04.
func TestIsAutoRollback(t *testing.T) {
	b := func(kind, via, createdBy string) *v1alpha1.Bundle {
		out := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{lifecycle.LabelRollback: "true"},
			Annotations: map[string]string{lifecycle.AnnotationCreatedBy: createdBy}}}
		if kind != "" {
			out.Spec.Actor = &v1alpha1.Actor{Kind: kind, Principal: createdBy, Via: via}
		}
		return out
	}
	const ctl = lifecycle.ControllerCreator
	for _, tc := range []struct {
		name             string
		b                *v1alpha1.Bundle
		auto, controller bool
	}{
		{"the controller's automatic rollback", b(v1alpha1.ActorController, v1alpha1.ViaAutoRollback, ctl), true, true},
		{"a human named kardinal-controller", b(v1alpha1.ActorHuman, "", ctl), false, false},
		{"a human stamp via auto-rollback", b(v1alpha1.ActorHuman, v1alpha1.ViaAutoRollback, ctl), false, false},
		{"the controller's stamp, another creator", b(v1alpha1.ActorController, v1alpha1.ViaAutoRollback, "mallory"), false, false},
		{"the controller via subscription", b(v1alpha1.ActorController, v1alpha1.ViaSubscription, ctl), false, false},
		{"no stamp", b("", "", ctl), false, false},
		{"the controller's rollback for a UI caller", b(v1alpha1.ActorHuman, v1alpha1.ViaUI, "alice"), false, true},
		{"the controller's rollback for an API caller", b(v1alpha1.ActorHuman, v1alpha1.ViaAPI, "alice"), false, true},
		{"a client's own rollback", b(v1alpha1.ActorHuman, "", "alice"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.auto, lifecycle.IsAutoRollback(tc.b))
			assert.Equal(t, tc.controller, lifecycle.IsControllerRollback(tc.b))
		})
	}
}

// QA-3 on #2187: a Bundle that someone created at the rollback's name after
// it was found free (the create then meets AlreadyExists) is taken, so the
// controller never adopts it.
func TestRollbackTaken_ForgedAtTheName(t *testing.T) {
	forged := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2-rollback-alarm", Namespace: "default",
		Labels:      map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"},
		Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: "app-v2", lifecycle.AnnotationCreatedBy: "mallory"}},
		Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
			Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "mallory"}}}
	taken, err := lifecycle.RollbackTaken(context.Background(), newClient(t, forged), "default", "app-v2-rollback-alarm", "prod")
	require.NoError(t, err)
	assert.True(t, taken, "not the controller's: never adopted")
}
