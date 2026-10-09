// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func widePipeline(ns, name string, envs ...v1alpha1.EnvironmentSpec) v1alpha1.Pipeline {
	return v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: v1alpha1.PipelineSpec{Environments: envs}}
}

// TestEnvColumnOrder (#1579): columns follow each Pipeline's DAG, merged so
// that test, uat, prod stay in that order next to a test → prod Pipeline,
// whichever is listed first.
func TestEnvColumnOrder(t *testing.T) {
	short := widePipeline("a", "mc", v1alpha1.EnvironmentSpec{Name: "test"}, v1alpha1.EnvironmentSpec{Name: "prod"})
	long := widePipeline("b", "app", v1alpha1.EnvironmentSpec{Name: "test"}, v1alpha1.EnvironmentSpec{Name: "uat"},
		v1alpha1.EnvironmentSpec{Name: "prod"})
	assert.Equal(t, []string{"test", "uat", "prod"}, envColumnOrder([]v1alpha1.Pipeline{short, long}))
	assert.Equal(t, []string{"test", "uat", "prod"}, envColumnOrder([]v1alpha1.Pipeline{long, short}))
	// Waves listed out of order: the DAG decides.
	dag := widePipeline("c", "dag", v1alpha1.EnvironmentSpec{Name: "test"},
		v1alpha1.EnvironmentSpec{Name: "prod", Wave: 2}, v1alpha1.EnvironmentSpec{Name: "uat", Wave: 1})
	assert.Equal(t, []string{"test", "uat", "prod"}, envColumnOrder([]v1alpha1.Pipeline{dag}))
	// Contradicting orders keep the first appearance.
	ab := widePipeline("d", "ab", v1alpha1.EnvironmentSpec{Name: "a"}, v1alpha1.EnvironmentSpec{Name: "b"})
	ba := widePipeline("d", "ba", v1alpha1.EnvironmentSpec{Name: "b"}, v1alpha1.EnvironmentSpec{Name: "a"})
	assert.ElementsMatch(t, []string{"a", "b"}, envColumnOrder([]v1alpha1.Pipeline{ab, ba}))
}

// TestPipelineTable_WideSummary (#1579): with more than maxEnvColumns
// environment columns, kardinal get pipelines -A shows one summary row per
// Pipeline (ENVS, PROGRESS) and a note; kardinal get pipelines <name>
// shows every environment of that Pipeline.
func TestPipelineTable_WideSummary(t *testing.T) {
	at := metav1.NewTime(time.Now().Add(-time.Hour))
	var fleetEnvs []v1alpha1.EnvironmentSpec
	var objs []client.Object
	for i := range 150 {
		e := v1alpha1.EnvironmentSpec{Name: fmt.Sprintf("env-%03d", i)}
		if i > 0 {
			e.Wave = 1
		}
		fleetEnvs = append(fleetEnvs, e)
	}
	fleet := widePipeline("acc-fleet", "fleet", fleetEnvs...)
	fleet.CreationTimestamp = at
	app := widePipeline("default", "app", v1alpha1.EnvironmentSpec{Name: "test"}, v1alpha1.EnvironmentSpec{Name: "uat"},
		v1alpha1.EnvironmentSpec{Name: "prod"})
	app.CreationTimestamp = at
	objs = append(objs, &fleet, &app,
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "fleet-b1", Namespace: "acc-fleet", CreationTimestamp: at},
			Spec: v1alpha1.BundleSpec{Pipeline: "fleet", Type: "image"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}})
	for i := range 150 {
		state := "Verified"
		if i >= 42 {
			state = "HealthChecking"
		}
		objs = append(objs, &v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("s%03d", i), Namespace: "acc-fleet"},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "fleet", BundleName: "fleet-b1", Environment: fmt.Sprintf("env-%03d", i)},
			Status:     v1alpha1.PromotionStepStatus{State: state},
		})
	}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(objs...).Build()

	var buf bytes.Buffer
	require.NoError(t, getPipelinesOnce(&buf, c, "", nil, true))
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Equal(t, []string{"NAMESPACE", "PIPELINE", "BUNDLE", "ENVS", "PROGRESS", "SUB", "AGE"}, strings.Fields(lines[0]))
	assert.Contains(t, out, "fleet-b1   150    42 Verified, 108 HealthChecking")
	assert.Regexp(t, `app\s+-\s+3\s+-`, out)
	assert.Contains(t, out, "More than 8 environments: one row per Pipeline. Every environment of one: kardinal get pipelines <name>")
	assert.NotContains(t, out, "ENV-000")

	buf.Reset()
	require.NoError(t, getPipelinesOnce(&buf, c, "acc-fleet", []string{"fleet"}, false))
	head := strings.Fields(strings.SplitN(buf.String(), "\n", 2)[0])
	assert.Equal(t, "ENV-000", head[2], "every environment, in DAG order")
	assert.Equal(t, "ENV-149", head[len(head)-3])
	assert.Len(t, head, 2+150+2)

	// A namespace of few-environment Pipelines keeps a column per environment.
	buf.Reset()
	require.NoError(t, getPipelinesOnce(&buf, c, "default", nil, false))
	assert.Equal(t, []string{"PIPELINE", "BUNDLE", "TEST", "UAT", "PROD", "SUB", "AGE"}, strings.Fields(strings.SplitN(buf.String(), "\n", 2)[0]))
}

// TestGetBundles_NewestFirst (#1579): kardinal get bundles lists the newest
// Bundle first (kardinal.io/created-at), not by name.
func TestGetBundles_NewestFirst(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	mk := func(name string, minute int) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			CreationTimestamp: metav1.NewTime(t0.Add(time.Duration(minute) * time.Minute))},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"}}
		lifecycle.StampCreatedAt(b, b.CreationTimestamp.Time)
		return b
	}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).
		WithObjects(mk("app-nwxbj", 0), mk("app-84x44", 40), mk("app-tp67s", 44), mk("app-dczr7", 45)).Build()
	var buf bytes.Buffer
	require.NoError(t, getBundlesFn(&buf, c, "default", []string{"app"}, false))
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n")[1:] {
		names = append(names, strings.Fields(l)[0])
	}
	assert.Equal(t, []string{"app-dczr7", "app-tp67s", "app-84x44", "app-nwxbj"}, names)
}
