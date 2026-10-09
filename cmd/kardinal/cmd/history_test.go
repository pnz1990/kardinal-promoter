// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestHistory_ListsPromotionSteps verifies that historyFn lists promotion steps for a pipeline.
func TestHistory_ListsPromotionSteps(t *testing.T) {
	s := cliTestScheme(t)
	step1 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-dev",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "dev", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified", PRURL: "https://github.com/org/repo/pull/10"},
	}
	step2 := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v2-dev",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v2", Environment: "dev", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Promoting"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(step1, step2).WithStatusSubresource(step1, step2).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo", "", 20)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "nginx-demo-v1", "should show bundle v1")
	assert.Contains(t, out, "nginx-demo-v2", "should show bundle v2")
	assert.Contains(t, out, "BUNDLE", "should have header")
	assert.Contains(t, out, "ACTION", "should have ACTION column")
	assert.Contains(t, out, "ENV", "should have ENV column")
	assert.Contains(t, out, "#10", "should show PR number")
}

// TestHistory_EnvFilter verifies that env filter works.
func TestHistory_EnvFilter(t *testing.T) {
	s := cliTestScheme(t)
	stepDev := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-dev",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "dev", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	stepProd := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1-prod",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: "nginx-demo-v1", Environment: "prod", StepType: "open-pr"},
		Status: v1alpha1.PromotionStepStatus{State: "Verified"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(stepDev, stepProd).WithStatusSubresource(stepDev, stepProd).Build()

	var buf bytes.Buffer
	err := historyFn(&buf, c, "default", "nginx-demo", "prod", 20)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "prod", "should contain prod env")
	assert.NotContains(t, out, "dev", "should not contain dev when filtered to prod")
}

// TestHistory_RollbackAction (C09b-cli-13): a step of a rollback Bundle shows
// action=rollback. The builder labels PromotionSteps with pipeline, bundle and
// environment only, so the action comes from the Bundle's provenance.
func TestHistory_RollbackAction(t *testing.T) {
	s := cliTestScheme(t)
	stepFor := func(bundle string) *v1alpha1.PromotionStep {
		return &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bundle + "-prod",
				Namespace: "default",
				Labels: map[string]string{
					"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": bundle, "kardinal.io/environment": "prod",
				},
			},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: bundle, Environment: "prod", StepType: "open-pr"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"},
		}
	}
	rollback := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-rollback-x1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo",
			Provenance: &v1alpha1.BundleProvenance{RollbackOf: "nginx-demo-v1"}},
	}
	labelled := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-rollback-x2", Namespace: "default",
			Labels: map[string]string{"kardinal.io/rollback": "true"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	promote := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	objs := []sigs_client.Object{rollback, labelled, promote,
		stepFor("nginx-demo-rollback-x1"), stepFor("nginx-demo-rollback-x2"), stepFor("nginx-demo-v1")}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()

	var buf bytes.Buffer
	require.NoError(t, historyFn(&buf, c, "default", "nginx-demo", "", 20))

	actions := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n")[1:] {
		f := strings.Fields(line)
		actions[f[0]] = f[1]
	}
	assert.Equal(t, map[string]string{
		"nginx-demo-rollback-x1": "rollback",
		"nginx-demo-rollback-x2": "rollback",
		"nginx-demo-v1":          "promote",
	}, actions)
}

// TestHistory_Duration (E2E-13 / C09b-cli-13): DURATION is creation to
// Verified (or to the last completed step), not the step's age.
func TestHistory_Duration(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { t := metav1.NewTime(created.Add(d)); return &t }
	verified := func(d time.Duration) []metav1.Condition {
		return []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: *at(d)}}
	}
	steps := []v1alpha1.StepStatus{
		{Name: "git-clone", CompletedAt: at(10 * time.Second)},
		{Name: "health-check", CompletedAt: at(4 * time.Minute)},
	}
	cases := []struct {
		name   string
		status v1alpha1.PromotionStepStatus
		want   string
	}{
		{"verified condition", v1alpha1.PromotionStepStatus{State: "Verified", Conditions: verified(30 * time.Second), Steps: steps}, "30s"},
		{"verified, steps only", v1alpha1.PromotionStepStatus{State: "Verified", Steps: steps}, "4m"},
		{"failed", v1alpha1.PromotionStepStatus{State: "Failed", Steps: steps[:1]}, "10s"},
		{"verified, no times", v1alpha1.PromotionStepStatus{State: "Verified"}, "--"},
		{"running", v1alpha1.PromotionStepStatus{State: "WaitingForMerge", Steps: steps[:1]}, "..."},
		{"rolling back ended at the alarm", v1alpha1.PromotionStepStatus{State: "RollingBack", Steps: steps}, "4m"},
		{"rolling back, no times", v1alpha1.PromotionStepStatus{State: "RollingBack"}, "--"},
		{"pending", v1alpha1.PromotionStepStatus{}, "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: "s", CreationTimestamp: metav1.NewTime(created)},
				Spec:       v1alpha1.PromotionStepSpec{BundleName: "b", Environment: "prod"},
				Status:     tc.status,
			}
			rows := buildHistoryRows([]v1alpha1.PromotionStep{step}, nil, "", 0)
			require.Len(t, rows, 1)
			assert.Equal(t, tc.want, rows[0].Duration)
		})
	}
}

// TestHistory_Order: rows are newest first, and steps created in the same
// second are ordered by name, whatever order the API returns them in. The
// limit keeps the first rows of that order.
func TestHistory_Order(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	step := func(name string, age time.Duration) v1alpha1.PromotionStep {
		return v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created.Add(-age))},
			Spec:       v1alpha1.PromotionStepSpec{BundleName: name, Environment: "prod"},
		}
	}
	steps := []v1alpha1.PromotionStep{
		step("b-old", time.Minute), step("c-new", 0), step("a-new", 0), step("b-new", 0), step("a-old", time.Minute),
	}
	bundles := func(rows []HistoryRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Bundle)
		}
		return out
	}
	want := []string{"a-new", "b-new", "c-new", "a-old", "b-old"}
	for i := range steps {
		// Every rotation of the input gives the same order.
		rotated := append(append([]v1alpha1.PromotionStep{}, steps[i:]...), steps[:i]...)
		assert.Equal(t, want, bundles(buildHistoryRows(rotated, nil, "", 0)), "input rotated by %d", i)
	}
	assert.Equal(t, want[:2], bundles(buildHistoryRows(steps, nil, "", 2)))
}

// TestHistory_RetiredBundle verifies that history still lists a Bundle whose
// Graph was retired (#1492): its steps are gone and only its
// status.retiredSteps are left.
func TestHistory_RetiredBundle(t *testing.T) {
	s := cliTestScheme(t)
	at := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default", CreationTimestamp: at},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status: v1alpha1.BundleStatus{
			Phase: "Verified",
			Conditions: []metav1.Condition{{Type: "GraphRetired", Status: metav1.ConditionTrue, Reason: "Retired",
				LastTransitionTime: at}},
			RetiredAt: &at,
			RetiredSteps: []v1alpha1.RetiredStep{{Name: "nginx-demo-v1-dev", Environment: "dev", StepType: "open-pr",
				State: "Verified", PRURL: "https://github.com/org/repo/pull/10", CreatedAt: at, VerifiedAt: &at}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(b).WithStatusSubresource(b).Build()

	var buf bytes.Buffer
	require.NoError(t, historyFn(&buf, c, "default", "nginx-demo", "", 20))
	out := buf.String()
	assert.Contains(t, out, "nginx-demo-v1")
	assert.Contains(t, out, "#10")
}
