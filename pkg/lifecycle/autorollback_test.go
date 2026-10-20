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

// TestFindRollback: FindRollback finds the controller's automatic rollback
// of the failing Bundle in the environment by its name: one of the names the
// controller creates it at (the alarm or policy source, plain or the
// environment's own), stamped, of the pipeline by spec.pipeline, and for the
// environment.
//
// QA-2 on #2184: a stamped rollback of an earlier failure whose
// kardinal.io/rollback-from (or labels) someone relinked to the failing
// Bundle is not found: the markers are anyone's to patch, the name is not.
//
// Covers RB-AUTO-FORGED-10.
func TestFindRollback(t *testing.T) {
	rb := func(name, from, env string) *v1alpha1.Bundle {
		return stampAuto(&v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default",
				Labels:      map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"},
				Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: from},
			},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: env}},
		})
	}
	ctx := context.Background()
	find := func(c client.Reader, env, from string) string {
		t.Helper()
		got, err := lifecycle.FindRollback(ctx, c, "default", "app", env, from)
		require.NoError(t, err)
		return got
	}
	for _, name := range []string{
		lifecycle.AutoRollbackName("app-v2", "alarm"), lifecycle.AutoRollbackName("app-v2", "alarm-uat"),
		lifecycle.AutoRollbackName("app-v2", "policy"), lifecycle.AutoRollbackName("app-v2", "policy-uat"),
	} {
		assert.Equal(t, name, find(newClient(t, rb(name, "app-v2", "uat")), "uat", "app-v2"), "found at the controller's name")
	}
	c := newClient(t, rb(lifecycle.AutoRollbackName("app-v2", "alarm"), "app-v2", "uat"))
	assert.Empty(t, find(c, "prod", "app-v2"), "a rollback in another environment does not count")

	t.Run("relinked", func(t *testing.T) {
		// app-v1's real, finished rollback in prod, patched to name app-v3.
		relinked := rb(lifecycle.AutoRollbackName("app-v1", "alarm"), "app-v3", "prod")
		assert.Empty(t, find(newClient(t, relinked), "prod", "app-v3"), "an earlier failure's rollback is not app-v3's")
		elsewhere := rb("totally-unrelated-name", "app-v3", "prod")
		assert.Empty(t, find(newClient(t, elsewhere), "prod", "app-v3"), "not at a name the controller creates")
	})
	t.Run("the name decides, not the markers", func(t *testing.T) {
		own := rb(lifecycle.AutoRollbackName("app-v3", "alarm"), "app-v1", "prod")
		own.Labels = nil // someone stripped the labels and relinked the annotation
		assert.Equal(t, own.Name, find(newClient(t, own), "prod", "app-v3"), "app-v3's own is still found")
		other := rb(lifecycle.AutoRollbackName("app-v3", "alarm"), "app-v3", "prod")
		other.Spec.Pipeline = "other" // the label still says app
		assert.Empty(t, find(newClient(t, other), "prod", "app-v3"), "spec.pipeline, not the label")
		forged := rb(lifecycle.AutoRollbackName("app-v3", "alarm"), "app-v3", "prod")
		forged.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "mallory"}
		forged.Annotations[lifecycle.AnnotationCreatedBy] = "mallory"
		assert.Empty(t, find(newClient(t, forged), "prod", "app-v3"), "at the name but not the controller's")
	})
	// #2225: a rollback created since v0.13.0 records the Bundle it rolls
	// back from in the immutable spec.provenance.rollbackFrom, and is bound
	// on it; one created by v0.12.x has no such field and the name binds it.
	t.Run("bound by spec.provenance.rollbackFrom", func(t *testing.T) {
		name := lifecycle.AutoRollbackName("app-v3", "alarm")
		recorded := func(rollbackFrom, annotation string) *v1alpha1.Bundle {
			b := rb(name, annotation, "prod")
			b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "app-v1", RollbackFrom: rollbackFrom}
			return b
		}
		assert.Equal(t, name, find(newClient(t, recorded("app-v3", "app-v3")), "prod", "app-v3"))
		assert.Equal(t, name, find(newClient(t, recorded("app-v3", "app-v9")), "prod", "app-v3"),
			"the annotation relinked: the field still binds it")
		assert.Empty(t, find(newClient(t, recorded("app-v8", "app-v3")), "prod", "app-v3"),
			"at app-v3's name but recorded for another Bundle (a shared hashed name): not app-v3's")
		assert.Equal(t, name, find(newClient(t, rb(name, "app-v3", "prod")), "prod", "app-v3"),
			"a v0.12.x rollback without the field: the name binds it")
	})
}

// TestAutoRollbackNameIn_BoundByProvenance (#2225): at the plain name, the
// controller's rollback recorded for another failing Bundle (as when the
// hashed form of a long name is shared) is not taken for this one's: the
// rollback takes the environment's own name; one recorded for this Bundle,
// or a v0.12.x one without the field, is its own.
//
// Covers RB-AUTO-NAME-04.
func TestAutoRollbackNameIn_BoundByProvenance(t *testing.T) {
	ctx := context.Background()
	at := func(rollbackFrom string) *v1alpha1.Bundle {
		return stampAuto(&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2-rollback-alarm", Namespace: "default",
			Labels:      map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"},
			Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: "app-v2"}},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
				Provenance: &v1alpha1.BundleProvenance{RollbackFrom: rollbackFrom}}})
	}
	for _, tc := range []struct {
		rollbackFrom, want string
		taken              bool
	}{
		{rollbackFrom: "app-v2", want: "app-v2-rollback-alarm"},
		{rollbackFrom: "", want: "app-v2-rollback-alarm"},
		{rollbackFrom: "app-v7", want: "app-v2-rollback-alarm-prod", taken: true},
	} {
		t.Run("recorded "+tc.rollbackFrom, func(t *testing.T) {
			c := newClient(t, at(tc.rollbackFrom))
			got, err := lifecycle.AutoRollbackNameIn(ctx, c, "default", "app", "app-v2", "alarm", "prod")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			taken, err := lifecycle.RollbackTaken(ctx, c, "default", "app-v2-rollback-alarm", "app", "prod", "app-v2")
			require.NoError(t, err)
			assert.Equal(t, tc.taken, taken)
		})
	}
}

// TestRollbackFromOf (#2225): the Bundle a rollback rolls back from is its
// immutable spec.provenance.rollbackFrom, or, for a v0.12.x rollback without
// it, its kardinal.io/rollback-from annotation.
func TestRollbackFromOf(t *testing.T) {
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: "relinked"}}}
	assert.Equal(t, "relinked", lifecycle.RollbackFromOf(b), "v0.12.x: the annotation")
	b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackFrom: "app-v2"}
	assert.Equal(t, "app-v2", lifecycle.RollbackFromOf(b), "the field, not the annotation")
	assert.Empty(t, lifecycle.RollbackFromOf(&v1alpha1.Bundle{}))
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

// TestAutoRollbackNameIn (#2082, #2090): the first environment's rollback
// takes the plain name; another environment of the same Bundle gets its
// own; the same one again its own again. A Bundle at a name that is not the
// rollback is never taken, each with one fact wrong: no controller stamp
// (every marker forged, #2187), another spec.pipeline, no intent; the
// controller's own with its labels stripped or rollback-from relinked
// still is (#2223). When both names hold one, the result is
// ErrConflict, naming the Bundle in the way.
//
// Covers RB-AUTO-NAME-01.
func TestAutoRollbackNameIn(t *testing.T) {
	ctx := context.Background()
	rollback := func(name, env string) *v1alpha1.Bundle {
		return stampAuto(&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			Labels:      map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"},
			Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: "app-v2"}},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: env}}})
	}
	nameIn := func(c client.Reader, env string) (string, error) {
		return lifecycle.AutoRollbackNameIn(ctx, c, "default", "app", "app-v2", "alarm", env)
	}
	got, err := nameIn(newClient(t), "prod-eu")
	require.NoError(t, err)
	assert.Equal(t, "app-v2-rollback-alarm", got, "nothing yet: the plain name")

	c := newClient(t, rollback("app-v2-rollback-alarm", "prod-eu"))
	got, err = nameIn(c, "prod-eu")
	require.NoError(t, err)
	assert.Equal(t, "app-v2-rollback-alarm", got, "its own again")
	got, err = nameIn(c, "prod-us")
	require.NoError(t, err)
	assert.Equal(t, "app-v2-rollback-alarm-prod-us", got, "another environment's is taken")

	taken, err := lifecycle.RollbackTaken(ctx, c, "default", "app-v2-rollback-alarm", "app", "prod-us", "app-v2")
	require.NoError(t, err)
	assert.True(t, taken, "prod-eu's is not prod-us's")
	taken, err = lifecycle.RollbackTaken(ctx, c, "default", "app-v2-rollback-alarm", "app", "prod-eu", "app-v2")
	require.NoError(t, err)
	assert.False(t, taken, "prod-eu's own")

	// #2090: not the rollback, so never taken over. Each has one fact wrong.
	squats := map[string]func(b *v1alpha1.Bundle){
		"every marker, no controller stamp": func(b *v1alpha1.Bundle) {
			b.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "mallory"}
			b.Annotations[lifecycle.AnnotationCreatedBy] = "mallory"
		},
		"another pipeline (spec.pipeline)": func(b *v1alpha1.Bundle) { b.Spec.Pipeline = "other" },
		"no intent":                        func(b *v1alpha1.Bundle) { b.Spec.Intent = nil },
	}
	for name, wrong := range squats {
		t.Run(name, func(t *testing.T) {
			squat := rollback("app-v2-rollback-alarm", "prod-eu")
			wrong(squat)
			got, err := nameIn(newClient(t, squat), "prod-eu")
			require.NoError(t, err)
			assert.Equal(t, "app-v2-rollback-alarm-prod-eu", got, "the plain name's Bundle is not adopted")
			taken, err := lifecycle.RollbackTaken(ctx, newClient(t, squat), "default", "app-v2-rollback-alarm", "app", "prod-eu", "app-v2")
			require.NoError(t, err)
			assert.True(t, taken, "met at the name after a create: not adopted either")
			_, err = nameIn(newClient(t, squat, rollback("app-v2-rollback-alarm-prod-eu", "prod-us")), "prod-eu")
			require.ErrorIs(t, err, lifecycle.ErrConflict)
			assert.Contains(t, err.Error(), "bundle app-v2-rollback-alarm-prod-eu is at the automatic rollback's name and is not a rollback of app-v2 in prod-eu")
			found, err := lifecycle.FindRollback(ctx, newClient(t, squat), "default", "app", "prod-eu", "app-v2")
			require.NoError(t, err)
			if name != "no intent" { // the controller's own without an intent rolls the whole pipeline back
				assert.Empty(t, found, "the lookup of an existing rollback does not find it")
			}
		})
	}

	// #2223: the name, the stamp and spec.pipeline decide, not the markers
	// anyone who may patch Bundles can rewrite: the controller's own rollback
	// with its labels stripped or its kardinal.io/rollback-from relinked is
	// still its own, so no second one is created.
	relinked := map[string]func(b *v1alpha1.Bundle){
		"labels stripped":        func(b *v1alpha1.Bundle) { b.Labels = nil },
		"rollback-from relinked": func(b *v1alpha1.Bundle) { b.Annotations[lifecycle.AnnotationRollbackFrom] = "app-v9" },
	}
	for name, patch := range relinked {
		t.Run(name, func(t *testing.T) {
			own := rollback("app-v2-rollback-alarm", "prod-eu")
			patch(own)
			got, err := nameIn(newClient(t, own), "prod-eu")
			require.NoError(t, err)
			assert.Equal(t, "app-v2-rollback-alarm", got, "its own")
			taken, err := lifecycle.RollbackTaken(ctx, newClient(t, own), "default", "app-v2-rollback-alarm", "app", "prod-eu", "app-v2")
			require.NoError(t, err)
			assert.False(t, taken)
		})
	}
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
	taken, err := lifecycle.RollbackTaken(context.Background(), newClient(t, forged), "default", "app-v2-rollback-alarm", "app", "prod", "app-v2")
	require.NoError(t, err)
	assert.True(t, taken, "not the controller's: never adopted")
}
