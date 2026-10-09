// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"fmt"
	"io"
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
// TestEnvColumnOrder_Edges (#1579 QA): the merge uses each Pipeline's real
// edges, not consecutive DAG-order entries. A wave a → {b, c} next to a chain
// a → c → b: the wave does not order b before c, so the columns are a, c, b.
// With consecutive entries the wave's b → c contradicted the chain and the
// first appearance (a, b, c) won.
func TestEnvColumnOrder_Edges(t *testing.T) {
	wave := widePipeline("a", "wave", v1alpha1.EnvironmentSpec{Name: "a"}, v1alpha1.EnvironmentSpec{Name: "b", Wave: 1},
		v1alpha1.EnvironmentSpec{Name: "c", Wave: 1})
	chain := widePipeline("b", "chain", v1alpha1.EnvironmentSpec{Name: "a"}, v1alpha1.EnvironmentSpec{Name: "c", DependsOn: []string{"a"}},
		v1alpha1.EnvironmentSpec{Name: "b", DependsOn: []string{"c"}})
	assert.Equal(t, []string{"a", "c", "b"}, envColumnOrder([]v1alpha1.Pipeline{wave, chain}, pipelineOrders([]v1alpha1.Pipeline{wave, chain})))
}

func TestEnvColumnOrder(t *testing.T) {
	short := widePipeline("a", "mc", v1alpha1.EnvironmentSpec{Name: "test"}, v1alpha1.EnvironmentSpec{Name: "prod"})
	long := widePipeline("b", "app", v1alpha1.EnvironmentSpec{Name: "test"}, v1alpha1.EnvironmentSpec{Name: "uat"},
		v1alpha1.EnvironmentSpec{Name: "prod"})
	assert.Equal(t, []string{"test", "uat", "prod"}, envColumnOrder([]v1alpha1.Pipeline{short, long}, pipelineOrders([]v1alpha1.Pipeline{short, long})))
	assert.Equal(t, []string{"test", "uat", "prod"}, envColumnOrder([]v1alpha1.Pipeline{long, short}, pipelineOrders([]v1alpha1.Pipeline{long, short})))
	// Waves listed out of order: the DAG decides.
	dag := widePipeline("c", "dag", v1alpha1.EnvironmentSpec{Name: "test"},
		v1alpha1.EnvironmentSpec{Name: "prod", Wave: 2}, v1alpha1.EnvironmentSpec{Name: "uat", Wave: 1})
	assert.Equal(t, []string{"test", "uat", "prod"}, envColumnOrder([]v1alpha1.Pipeline{dag}, pipelineOrders([]v1alpha1.Pipeline{dag})))
	// Contradicting orders keep the first appearance.
	ab := widePipeline("d", "ab", v1alpha1.EnvironmentSpec{Name: "a"}, v1alpha1.EnvironmentSpec{Name: "b"})
	ba := widePipeline("d", "ba", v1alpha1.EnvironmentSpec{Name: "b"}, v1alpha1.EnvironmentSpec{Name: "a"})
	assert.ElementsMatch(t, []string{"a", "b"}, envColumnOrder([]v1alpha1.Pipeline{ab, ba}, pipelineOrders([]v1alpha1.Pipeline{ab, ba})))
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
	assert.Equal(t, []string{"NAMESPACE", "PIPELINE", "BUNDLE", "ENVS", "PROGRESS", "FURTHEST", "SUB", "AGE"}, strings.Fields(lines[0]))
	assert.Regexp(t, `fleet-b1\s+150\s+42 Verified, 108 HealthChecking\s+env-041\s`, out, "FURTHEST: the last Verified environment in DAG order")
	assert.Regexp(t, `app\s+-\s+3\s+-\s+-`, out)
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

// TestGetBundles_SameSecond (#1579 QA): Bundles created in the same second
// (creationTimestamp has one-second resolution) are ordered by their
// sub-second kardinal.io/created-at, newest first; with that equal too, by
// name, descending. -o json lists them in the same order.
func TestGetBundles_SameSecond(t *testing.T) {
	sec := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	mk := func(name string, created time.Time, ms int) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			CreationTimestamp: metav1.NewTime(created)},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"}}
		lifecycle.StampCreatedAt(b, created.Add(time.Duration(ms)*time.Millisecond))
		return b
	}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(
		mk("app-zzz", sec, 100),                          // same second, created first
		mk("app-aaa", sec, 900),                          // same second, created last
		mk("app-bbb", sec, 500), mk("app-ccc", sec, 500), // same instant: by name
		mk("app-old", sec.Add(-time.Second), 999), // an earlier second wins over created-at
	).Build()
	var buf bytes.Buffer
	require.NoError(t, getBundlesFn(&buf, c, "default", []string{"app"}, false))
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n")[1:] {
		names = append(names, strings.Fields(l)[0])
	}
	want := []string{"app-aaa", "app-ccc", "app-bbb", "app-zzz", "app-old"}
	assert.Equal(t, want, names)
}

// wideFleet is one Pipeline of 500 environments in two waves of 250, with an
// in-flight Bundle Verified in wave 1 and promoting in wave 2: the #1579 QA
// case, where get pipelines -A took 44 s.
func wideFleet() ([]v1alpha1.Pipeline, []v1alpha1.Bundle, []v1alpha1.PromotionStep) {
	var envs []v1alpha1.EnvironmentSpec
	for i := range 500 {
		envs = append(envs, v1alpha1.EnvironmentSpec{Name: fmt.Sprintf("env-%03d", i), Wave: 1 + i/250})
	}
	p := widePipeline("fleet", "fleet", envs...)
	b := v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "fleet-b1", Namespace: "fleet"},
		Spec: v1alpha1.BundleSpec{Pipeline: "fleet", Type: "image"}, Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	steps := make([]v1alpha1.PromotionStep, 0, 500)
	for i := range 500 {
		state := "Verified"
		if i >= 250 {
			state = "Promoting"
		}
		steps = append(steps, v1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("s%03d", i), Namespace: "fleet"},
			Spec:       v1alpha1.PromotionStepSpec{PipelineName: "fleet", BundleName: "fleet-b1", Environment: fmt.Sprintf("env-%03d", i)},
			Status:     v1alpha1.PromotionStepStatus{State: state},
		})
	}
	return []v1alpha1.Pipeline{p}, []v1alpha1.Bundle{b}, steps
}

// TestPipelineTable_500EnvsUnderOneSecond (#1579 QA): the table of a
// 500-environment Pipeline, summary and every column, renders in under a
// second (it took 44 s while the intent filter ran per environment).
func TestPipelineTable_500EnvsUnderOneSecond(t *testing.T) {
	ps, bs, ss := wideFleet()
	start := time.Now()
	var buf bytes.Buffer
	require.NoError(t, formatPipelines(&buf, ps, bs, ss, nil, true, false))
	require.NoError(t, formatPipelines(&buf, ps, bs, ss, nil, true, true))
	elapsed := time.Since(start)
	assert.Contains(t, buf.String(), "250 Verified, 250 Promoting")
	assert.Less(t, elapsed, time.Second)
	t.Logf("500 environments: %v for the summary and the full table", elapsed)
}

func BenchmarkPipelineTable_500Envs(b *testing.B) {
	ps, bs, ss := wideFleet()
	for b.Loop() {
		_ = formatPipelines(io.Discard, ps, bs, ss, nil, true, false)
	}
}
