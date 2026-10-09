//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

var (
	analysisTemplateGVR        = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysistemplates"}
	clusterAnalysisTemplateGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "clusteranalysistemplates"}
	analysisRunGVR             = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}
)

// jobMetric is an Argo Rollouts metric with the Job provider: the podinfo
// image runs script with sh; the measurement succeeds when it exits 0.
func jobMetric(name, script string) map[string]interface{} {
	return map[string]interface{}{
		"name": name,
		"provider": map[string]interface{}{"job": map[string]interface{}{"spec": map[string]interface{}{
			"backoffLimit": int64(0),
			"template": map[string]interface{}{"spec": map[string]interface{}{
				"restartPolicy": "Never",
				"containers": []interface{}{map[string]interface{}{
					"name": "check", "image": fixtures.Image + ":" + fixtures.V1,
					"command": []interface{}{"sh", "-c", script},
				}},
			}},
		}}},
	}
}

// createAnalysisTemplate creates an AnalysisTemplate in ns (ns "" makes a
// ClusterAnalysisTemplate, deleted when the test ends) declaring args.
func createAnalysisTemplate(t *testing.T, e *framework.Env, ns, name string, args []string, metrics ...map[string]interface{}) {
	t.Helper()
	var decl []interface{}
	for _, a := range args {
		decl = append(decl, map[string]interface{}{"name": a})
	}
	var ms []interface{}
	for _, m := range metrics {
		ms = append(ms, m)
	}
	kind, gvr := "AnalysisTemplate", analysisTemplateGVR
	if ns == "" {
		kind, gvr = "ClusterAnalysisTemplate", clusterAnalysisTemplateGVR
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": kind,
		"metadata": map[string]interface{}{"name": name},
		"spec":     map[string]interface{}{"args": decl, "metrics": ms},
	}}
	ctx := context.Background()
	if ns == "" {
		_, err := e.Dynamic.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = e.Dynamic.Resource(gvr).Delete(context.Background(), name, metav1.DeleteOptions{}) })
		return
	}
	_, err := e.Dynamic.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	require.NoError(t, err)
}

// analysisRuns lists the AnalysisRuns of bundle in env.
func analysisRuns(ctx context.Context, e *framework.Env, ns, bundle, env string) ([]unstructured.Unstructured, error) {
	list, err := e.Dynamic.Resource(analysisRunGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kardinal.io/bundle=" + bundle + ",kardinal.io/environment=" + env})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// runArgs returns an AnalysisRun's args with a value, by name.
func runArgs(r unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	args, _, _ := unstructured.NestedSlice(r.Object, "spec", "args")
	for _, a := range args {
		m, _ := a.(map[string]interface{})
		name, _ := m["name"].(string)
		if v, ok := m["value"].(string); ok {
			out[name] = v
		}
	}
	return out
}

// TestRollouts_AnalysisVerifiesEnvironment: prod's verification runs an
// AnalysisTemplate (a Job that fetches /version from prod's Service and
// checks it is the Bundle's tag, passed as the built-in arg tag) and a
// ClusterAnalysisTemplate (checks the built-in arg environment), after the
// health check passed. The step is Verifying while they run and Verified,
// with reason VerificationSucceeded, once both are Successful; test, without
// verification, gets no AnalysisRun.
//
// Covers ANALYSIS-01, ANALYSIS-ARGS-01, ANALYSIS-CLUSTER-01.
func TestRollouts_AnalysisVerifiesEnvironment(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	states := e.RecordStepStates(t, a.ns)
	createAnalysisTemplate(t, e, a.ns, "version-check", []string{"service", "tag"},
		jobMetric("version", `for i in 1 2 3 4 5 6 7 8 9 10; do wget -qO- http://{{args.service}}:9898/version | grep -q '{{args.tag}}' && exit 0; sleep 3; done; exit 1`))
	cluster := "kardinal-e2e-env-" + a.ns[len(a.ns)-8:]
	createAnalysisTemplate(t, e, "", cluster, []string{"environment"}, jobMetric("env", `test "{{args.environment}}" = prod`))

	p := a.pipeline(nil)
	p.Spec.Environments[1].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "version-check"}, {Name: cluster, Kind: "ClusterAnalysisTemplate"}},
		Args:              []v1alpha1.AnalysisArg{{Name: "service", Value: fixtures.Workload("prod")}},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verifying", promoteTimeout)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	c := meta.FindStatusCondition(ps.Status.Conditions, "Verified")
	require.NotNil(t, c)
	assert.Equal(t, "VerificationSucceeded", c.Reason)
	require.Len(t, ps.Spec.Analyses, 2)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	a.running(t, "prod", imageV2, "prod after its analysis")
	checkStates(t, "prod", states.States(bundle, "prod"), "Promoting", "HealthChecking", "Verifying", "Verified")

	runs, err := analysisRuns(ctx, e, a.ns, bundle, "prod")
	require.NoError(t, err)
	require.Len(t, runs, 2)
	byTemplate := map[string]unstructured.Unstructured{}
	for _, r := range runs {
		phase, _, _ := unstructured.NestedString(r.Object, "status", "phase")
		assert.Equal(t, "Successful", phase, r.GetName())
		byTemplate[r.GetLabels()["kardinal.io/analysis-template"]] = r
	}
	assert.Equal(t, map[string]string{"service": fixtures.Workload("prod"), "tag": fixtures.V2},
		runArgs(byTemplate["version-check"]), "Pipeline arg and the built-in tag")
	assert.Equal(t, map[string]string{"environment": "prod"}, runArgs(byTemplate[cluster]), "built-in environment")
	testRuns, err := analysisRuns(ctx, e, a.ns, bundle, "test")
	require.NoError(t, err)
	assert.Empty(t, testRuns, "test has no verification")
}

// TestRollouts_AnalysisFailureFailsEnvironment: an AnalysisRun that fails
// applies onHealthFailure (none: the step Failed, with the AnalysisRun's
// message) after the change landed; the next environment is never promoted
// and the Bundle Failed.
//
// Covers ANALYSIS-FAIL-01.
func TestRollouts_AnalysisFailureFailsEnvironment(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	createAnalysisTemplate(t, e, a.ns, "always-fails", nil, jobMetric("errors", `echo "error rate 7%"; exit 1`))
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "always-fails"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "analysis always-fails (AnalysisRun ")
	assert.Contains(t, ps.Status.Message, "failed")
	a.running(t, "test", imageV2, "the change landed before the analysis")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	framework.Consistently(t, 20*time.Second, "prod is not promoted", func(ctx context.Context) (bool, string) {
		_, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil {
			return false, err.Error()
		}
		return !ok, "prod has a PromotionStep"
	})
}

// TestRollouts_AnalysisMissingTemplateFailsBundle: a verification that names
// a template that does not exist fails the Bundle before any environment
// is promoted (InvalidSpec, GraphBuildFailed, naming the template).
//
// Covers ANALYSIS-MISSING-01.
func TestRollouts_AnalysisMissingTemplateFailsBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "nope"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundle(t, a.ns, bundle, time.Minute, "Failed with InvalidSpec",
		failedWith("GraphBuildFailed", `AnalysisTemplate "nope" not found`))
	_, ok, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	assert.False(t, ok, "no environment was promoted")
	a.fileHas(t, "prod", fixtures.V1, "prod in git")
}

// TestGraph_AnalysisFailsClosedWithoutRollouts: on a cluster without the
// Argo Rollouts CRDs (the core suite has none), a Pipeline with
// verification fails its Bundle instead of promoting without the analysis.
//
// Covers ANALYSIS-CLOSED-01.
func TestGraph_AnalysisFailsClosedWithoutRollouts(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	_, err := e.Kube.Discovery().ServerResourcesForGroupVersion("argoproj.io/v1alpha1")
	require.NoError(t, err, "Argo CD serves argoproj.io/v1alpha1")
	res, _ := e.Kube.Discovery().ServerResourcesForGroupVersion("argoproj.io/v1alpha1")
	for _, r := range res.APIResources {
		require.NotEqual(t, "analysisruns", r.Name, "this test needs a cluster without Argo Rollouts")
	}
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "smoke"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	b := e.WaitBundle(t, a.ns, bundle, time.Minute, "Failed with InvalidSpec",
		failedWith("GraphBuildFailed", "AnalysisRun is not served"))
	assert.True(t, strings.Contains(fmt.Sprint(b.Status.Conditions), "install Argo Rollouts"))
	_, ok, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	assert.False(t, ok, "no environment was promoted")
	a.fileHas(t, "prod", fixtures.V1, "prod in git")
}

// TestRollouts_AnalysisTerminatedWhenStepFails: with two analyses, one that
// fails at once and one that would measure for minutes, the failure fails
// the step and the other run is terminated (spec.terminate), so Argo
// Rollouts stops measuring (regression, QA #1502).
//
// Covers ANALYSIS-TERMINATE-01.
func TestRollouts_AnalysisTerminatedWhenStepFails(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	createAnalysisTemplate(t, e, a.ns, "fails", nil, jobMetric("errors", `exit 1`))
	slow := jobMetric("slow", `sleep 5`)
	slow["interval"] = "10s"
	slow["count"] = int64(60)
	createAnalysisTemplate(t, e, a.ns, "slow", nil, slow)
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "fails"}, {Name: "slow"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", promoteTimeout)
	framework.Eventually(t, 2*time.Minute, "the slow run to be terminated", func(ctx context.Context) (bool, string) {
		runs, err := analysisRuns(ctx, e, a.ns, bundle, "prod")
		if err != nil {
			return false, err.Error()
		}
		for _, r := range runs {
			if r.GetLabels()["kardinal.io/analysis-template"] != "slow" {
				continue
			}
			term, _, _ := unstructured.NestedBool(r.Object, "spec", "terminate")
			phase, _, _ := unstructured.NestedString(r.Object, "status", "phase")
			return term && phase != "Running" && phase != "Pending" && phase != "",
				fmt.Sprintf("terminate=%v phase=%q", term, phase)
		}
		return false, "no run of slow"
	})
	_ = ctx
}

// TestRollouts_AnalysisInvalidArgFailsClosed: a Bundle tag outside the OCI
// tag grammar is never passed into an AnalysisRun: the API server refuses
// the Bundle (when the Bundle CRD has the tag pattern) or the Bundle fails
// before any environment (regression, QA #1502).
//
// Covers ANALYSIS-INJECT-01.
func TestRollouts_AnalysisInvalidArgFailsClosed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	createAnalysisTemplate(t, e, a.ns, "version-check", []string{"tag"}, jobMetric("v", `echo {{args.tag}}`))
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "version-check"}}}
	a.apply(t, p)
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "podinfo-inject", Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: `6.14.1"; exit 0; #`}}},
	}
	if err := e.Client.Create(ctx, b); err != nil {
		assert.Contains(t, err.Error(), "spec.images[0].tag", "refused at admission")
		return
	}
	e.WaitBundle(t, a.ns, b.Name, time.Minute, "Failed with InvalidSpec", failedWith("GraphBuildFailed", "arg tag"))
	runs, err := analysisRuns(ctx, e, a.ns, b.Name, "prod")
	require.NoError(t, err)
	assert.Empty(t, runs)
	a.fileHas(t, "prod", fixtures.V1, "prod in git")
}

// TestRollouts_AnalysisTemplateEditedMidFlight: a template edited while its
// run measures, picked up by a Pipeline edit, starts a new run (a new name);
// the step waits for the newest run, not the replaced one, and is Verified
// by it (regression, QA #1502).
//
// Covers ANALYSIS-REBUILD-01.
func TestRollouts_AnalysisTemplateEditedMidFlight(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	createAnalysisTemplate(t, e, a.ns, "check", nil, jobMetric("m", `sleep 600`))
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "check"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verifying", promoteTimeout)
	var first string
	framework.Eventually(t, time.Minute, "the first run", func(ctx context.Context) (bool, string) {
		runs, err := analysisRuns(ctx, e, a.ns, bundle, "prod")
		if err != nil || len(runs) == 0 {
			return false, fmt.Sprint(err)
		}
		first = runs[0].GetName()
		return true, ""
	})

	// Edit the template, then the Pipeline so the Graph is re-translated.
	tmpl, err := e.Dynamic.Resource(analysisTemplateGVR).Namespace(a.ns).Get(ctx, "check", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedSlice(tmpl.Object, []interface{}{jobMetric("m", `exit 0`)}, "spec", "metrics"))
	_, err = e.Dynamic.Resource(analysisTemplateGVR).Namespace(a.ns).Update(ctx, tmpl, metav1.UpdateOptions{})
	require.NoError(t, err)
	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.Environments[0].Verification.Args = []v1alpha1.AnalysisArg{{Name: "unused", Value: "x"}}
	require.NoError(t, e.Client.Update(ctx, &live))

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	c := meta.FindStatusCondition(ps.Status.Conditions, "Verified")
	require.NotNil(t, c)
	assert.Equal(t, "VerificationSucceeded", c.Reason)
	runs, err := analysisRuns(ctx, e, a.ns, bundle, "prod")
	require.NoError(t, err)
	var names []string
	for _, r := range runs {
		names = append(names, r.GetName())
	}
	assert.NotContains(t, names, first, "the replaced run is gone")
	assert.NotEmpty(t, names)
}

// TestRollouts_AnalysisForgedRunIgnored: an AnalysisRun created by hand with
// the selector labels and the Bundle's UID, and status Successful, is no
// verdict: the step keeps waiting for the run its Graph rendered
// (regression, QA #1502 round 2).
//
// Covers ANALYSIS-FORGED-01.
func TestRollouts_AnalysisForgedRunIgnored(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	createAnalysisTemplate(t, e, a.ns, "slow", nil, jobMetric("m", `sleep 600`))
	p := a.pipeline(nil)
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "slow"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verifying", promoteTimeout)
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: bundle}, &b))

	forged := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "AnalysisRun",
		"metadata": map[string]interface{}{"name": "forged-" + bundle, "labels": map[string]interface{}{
			"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": bundle, "kardinal.io/environment": "prod",
			"kardinal.io/analysis-template": "slow", "kardinal.io/bundle-uid": string(b.UID)}},
		"spec": map[string]interface{}{"metrics": []interface{}{map[string]interface{}{"name": "m",
			"provider": map[string]interface{}{"job": map[string]interface{}{"spec": map[string]interface{}{
				"template": map[string]interface{}{"spec": map[string]interface{}{"restartPolicy": "Never",
					"containers": []interface{}{map[string]interface{}{"name": "c", "image": fixtures.Image + ":" + fixtures.V1,
						"command": []interface{}{"true"}}}}}}}}}}},
	}}
	// Argo Rollouts' AnalysisRun has no status subresource: whoever may
	// create one sets its status.
	require.NoError(t, unstructured.SetNestedField(forged.Object, "Successful", "status", "phase"))
	_, err := e.Dynamic.Resource(analysisRunGVR).Namespace(a.ns).Create(ctx, forged, metav1.CreateOptions{})
	require.NoError(t, err)

	framework.Consistently(t, 30*time.Second, "the forged Successful run is no verdict", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || !ok {
			return false, fmt.Sprint(err)
		}
		if ps.Spec.Live != nil {
			for _, r := range ps.Spec.Live.Analyses {
				if r.Name == forged.GetName() {
					return false, "the mirror copied the forged run"
				}
			}
		}
		return ps.Status.State == "Verifying", "state " + ps.Status.State
	})
}

// TestRollouts_AnalysisInCompactGraph runs prod's verification with the
// compact Graph shape (kardinal.io/graph-shape: compact): its AnalysisRun is
// an item of the AnalysisRuns collection, created once the step is
// Verifying, with the Bundle's tag as an arg, and prod is Verified once it is
// Successful. A second Bundle with a failing analysis fails prod, and its
// other run, which would measure for minutes, gets spec.terminate.
//
// Covers ANALYSIS-COMPACT-01.
func TestRollouts_AnalysisInCompactGraph(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	createAnalysisTemplate(t, e, a.ns, "version-check", []string{"service", "tag"},
		jobMetric("version", `for i in 1 2 3 4 5 6 7 8 9 10; do wget -qO- http://{{args.service}}:9898/version | grep -q '{{args.tag}}' && exit 0; sleep 3; done; exit 1`))
	p := a.pipeline(nil)
	p.Annotations = map[string]string{"kardinal.io/graph-shape": "compact"}
	p.Spec.Environments[1].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "version-check"}},
		Args:              []v1alpha1.AnalysisArg{{Name: "service", Value: fixtures.Workload("prod")}},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	c := meta.FindStatusCondition(ps.Status.Conditions, "Verified")
	require.NotNil(t, c)
	assert.Equal(t, "VerificationSucceeded", c.Reason)
	require.NotNil(t, ps.Spec.Live)
	require.Len(t, ps.Spec.Live.Analyses, 1)
	assert.Equal(t, "Successful", ps.Spec.Live.Analyses[0].Phase)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	assert.Equal(t, "compact", bundleGraph(t, e, a.ns, bundle).GetLabels()["kardinal.io/graph-shape"])
	runs, err := analysisRuns(ctx, e, a.ns, bundle, "prod")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "AnalysisRuns", runs[0].GetLabels()["kro.run/node-id"], "made by the AnalysisRuns collection")
	assert.Equal(t, map[string]string{"service": fixtures.Workload("prod"), "tag": fixtures.V2}, runArgs(runs[0]))
	testRuns, err := analysisRuns(ctx, e, a.ns, bundle, "test")
	require.NoError(t, err)
	assert.Empty(t, testRuns, "test has no verification")

	// A failing analysis fails prod and terminates the other run.
	createAnalysisTemplate(t, e, a.ns, "fails", nil, jobMetric("errors", `exit 1`))
	slow := jobMetric("slow", `sleep 5`)
	slow["interval"] = "10s"
	slow["count"] = int64(60)
	createAnalysisTemplate(t, e, a.ns, "slow", nil, slow)
	p = a.pipeline(nil)
	p.Annotations = map[string]string{"kardinal.io/graph-shape": "compact"}
	p.Spec.Environments[1].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "fails"}, {Name: "slow"}}}
	a.apply(t, p)
	second := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitStepState(t, a.ns, pipelineName, second, "prod", "Failed", promoteTimeout)
	framework.Eventually(t, 2*time.Minute, "the slow run to be terminated", func(ctx context.Context) (bool, string) {
		runs, err := analysisRuns(ctx, e, a.ns, second, "prod")
		if err != nil {
			return false, err.Error()
		}
		for _, r := range runs {
			if r.GetLabels()["kardinal.io/analysis-template"] != "slow" {
				continue
			}
			term, _, _ := unstructured.NestedBool(r.Object, "spec", "terminate")
			phase, _, _ := unstructured.NestedString(r.Object, "status", "phase")
			return term && phase != "Running" && phase != "Pending" && phase != "",
				fmt.Sprintf("terminate=%v phase=%q", term, phase)
		}
		return false, "no run of slow"
	})
}

// TestGraph_AnalysisFailsClosedWithoutRolloutsCompact is
// TestGraph_AnalysisFailsClosedWithoutRollouts with the compact Graph shape.
//
// Covers ANALYSIS-CLOSED-01.
func TestGraph_AnalysisFailsClosedWithoutRolloutsCompact(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	res, err := e.Kube.Discovery().ServerResourcesForGroupVersion("argoproj.io/v1alpha1")
	require.NoError(t, err, "Argo CD serves argoproj.io/v1alpha1")
	for _, r := range res.APIResources {
		require.NotEqual(t, "analysisruns", r.Name, "this test needs a cluster without Argo Rollouts")
	}
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Annotations = map[string]string{"kardinal.io/graph-shape": "compact"}
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{
		AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "smoke"}}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundle(t, a.ns, bundle, time.Minute, "Failed with GraphBuildFailed",
		failedWith("GraphBuildFailed", "AnalysisRun is not served"))
	_, ok, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	assert.False(t, ok, "no environment was promoted")
	a.fileHas(t, "prod", fixtures.V1, "prod in git")
}
