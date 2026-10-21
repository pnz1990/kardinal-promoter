// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// TestBundleReconciler_PolicyNamespacesWait (#2286): a new version whose
// Pipeline names a namespace that has not agreed to share its PolicyGates
// stays Available (it does not fail) with Ready=False
// PolicyNamespaceNotShared naming it, one Warning Event over reconciles, and
// a retry within a minute; not yet checked, it waits the same way with no
// Event; once the namespace agrees, the version is translated and promotes.
// Covers GATE-POLNS-05.
func TestBundleReconciler_PolicyNamespacesWait(t *testing.T) {
	for name, tc := range map[string]struct {
		cause   *translator.PolicyNamespacesError
		message string
		events  int
	}{
		"not shared":      {&translator.PolicyNamespacesError{NotShared: []string{"team-b"}}, "spec.policyNamespaces names team-b", 1},
		"not checked yet": {&translator.PolicyNamespacesError{Unchecked: true}, "not checked yet", 0},
	} {
		t.Run(name, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
				Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline, b).WithStatusSubresource(b).Build()
			tr := &mockTranslator{graphName: "nginx-demo-v1-graph", err: fmt.Errorf("translator.Translate: %w", tc.cause)}
			rec := events.NewFakeRecorder(10)
			r := &bundle.Reconciler{Client: c, Translator: tr, Recorder: rec}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "nginx-demo-v1", Namespace: "default"}}
			for range 2 {
				res, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err, "a wait, not an error to back off on")
				assert.Positive(t, res.RequeueAfter)
				assert.LessOrEqual(t, res.RequeueAfter, time.Minute)
			}
			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, "Available", got.Status.Phase, "the version waits, it does not fail")
			ready := findCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, metav1.ConditionFalse, ready.Status)
			assert.Equal(t, "PolicyNamespaceNotShared", ready.Reason)
			assert.Contains(t, ready.Message, tc.message)
			warnings := 0
			for len(rec.Events) > 0 {
				if e := <-rec.Events; strings.Contains(e, "PolicyNamespaceNotShared") {
					warnings++
				}
			}
			assert.Equal(t, tc.events, warnings)

			// The namespace agreed: the version is translated.
			tr.err = nil
			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, "Promoting", got.Status.Phase)
		})
	}
}

// TestBundleReconciler_InFlightWaitsForPolicyNamespacesCheck (#2286): a
// version in flight whose Pipeline changed is not re-rendered while the
// Pipeline reconciler has not checked spec.policyNamespaces for the new
// generation (Translate refuses it, Unchecked): its Graph stays as it is,
// GraphSynced=False PolicyNamespacesUnchecked, no GraphSyncFailed Event, a
// retry within a minute, and the spec change is applied once checked.
// Covers GATE-POLNS-07.
func TestBundleReconciler_InFlightWaitsForPolicyNamespacesCheck(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Promoting", GraphRef: "my-app-my-app-v1",
			PipelineSpecHash: "stale-hash-from-old-spec"},
	}
	c := indexedBuilder(newScheme()).WithObjects(pipeline, b).WithStatusSubresource(&kardinalv1alpha1.Bundle{}).Build()
	tr := &mockTranslator{graphName: "my-app-my-app-v1",
		err: fmt.Errorf("translator.Translate: %w", &translator.PolicyNamespacesError{Unchecked: true})}
	rec := events.NewFakeRecorder(10)
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &mockGraphCheckerV2{exists: true}, Recorder: rec}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"}}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err, "a wait, not an error to back off on")
	assert.True(t, tr.called)
	assert.Positive(t, res.RequeueAfter)
	assert.LessOrEqual(t, res.RequeueAfter, time.Minute)
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "stale-hash-from-old-spec", got.Status.PipelineSpecHash, "the Graph is not updated")
	synced := findCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionFalse, synced.Status)
	assert.Equal(t, "PolicyNamespacesUnchecked", synced.Reason)
	assert.Contains(t, synced.Message, "not checked yet")
	for len(rec.Events) > 0 {
		assert.NotContains(t, <-rec.Events, "GraphSyncFailed")
	}

	tr.err = nil // checked
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.NotEqual(t, "stale-hash-from-old-spec", got.Status.PipelineSpecHash, "the spec change is applied")
	synced = findCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionTrue, synced.Status)
}

// TestBundleReconciler_UpstreamEvidenceWaitsForPolicyNamespaces (#2286): a
// new targetOnly version decides whether it carries the upstream
// verification only once its Pipeline's spec.policyNamespaces agrees: the
// decision reads the gates, and a refused namespace's are not read, so a
// verify gate there would be missed for good.
// Covers GATE-POLNS-05.
func TestBundleReconciler_UpstreamEvidenceWaitsForPolicyNamespaces(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default", Generation: 1},
		Spec: kardinalv1alpha1.PipelineSpec{PolicyNamespaces: []string{"team-b"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
		Status: kardinalv1alpha1.PipelineStatus{PolicyNamespaces: &kardinalv1alpha1.PipelinePolicyNamespaces{
			ObservedGeneration: 1, NotShared: []string{"team-b"}}},
	}
	verify := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "qa", Namespace: "team-b", Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "verdicts.met",
			Verify: &kardinalv1alpha1.GateVerifyPolicy{Check: "qa", Environment: "test", Required: 1}},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{Pipeline: "my-app", Type: "image",
			Intent: &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod", TargetOnly: true}},
		Status: kardinalv1alpha1.BundleStatus{Phase: "Available"},
	}
	c := indexedBuilder(newScheme()).WithObjects(pipeline, verify, b).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}).Build()
	tr := &mockTranslator{graphName: "my-app-my-app-v1",
		err: fmt.Errorf("translator.Translate: %w", &translator.PolicyNamespacesError{NotShared: []string{"team-b"}})}
	r := &bundle.Reconciler{Client: c, Translator: tr, Recorder: events.NewFakeRecorder(10)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"}}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Nil(t, findCondition(got.Status.Conditions, "UpstreamEvidence"), "not decided while team-b is refused")

	// team-b agrees.
	var p kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "my-app", Namespace: "default"}, &p))
	p.Status.PolicyNamespaces.NotShared = nil
	require.NoError(t, c.Status().Update(context.Background(), &p))
	tr.err = nil
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	ev := findCondition(got.Status.Conditions, "UpstreamEvidence")
	require.NotNil(t, ev)
	assert.Equal(t, "VerifyGate", ev.Reason, "team-b's verify gate is seen: %s", ev.Message)
}

// TestBundleReconciler_GraphGoneWaitsForPolicyNamespaces (#2286, QA-2 on
// #2381): a version in flight whose Graph was deleted while its Pipeline
// names a refused namespace is not built again (Translate refuses it,
// GraphGone): it stays Promoting with GraphSynced=False
// PolicyNamespaceNotShared saying what unblocks it, one Warning Event over
// reconciles, no GraphSyncFailed, and a retry within a minute; once the
// namespace agrees the Graph is built again.
// Covers GATE-POLNS-07.
func TestBundleReconciler_GraphGoneWaitsForPolicyNamespaces(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
	}
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "my-app", Type: "image"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: "Promoting", GraphRef: "my-app-my-app-v1"},
	}
	c := indexedBuilder(newScheme()).WithObjects(pipeline, b).WithStatusSubresource(&kardinalv1alpha1.Bundle{}).Build()
	tr := &mockTranslator{graphName: "my-app-my-app-v1", err: fmt.Errorf("translator.Translate: %w",
		&translator.PolicyNamespacesError{NotShared: []string{"team-b"}, GraphGone: true})}
	rec := events.NewFakeRecorder(10)
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &mockGraphCheckerV2{exists: false}, Recorder: rec}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-v1", Namespace: "default"}}

	for range 2 {
		res, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err, "a wait, not an error to back off on")
		assert.Positive(t, res.RequeueAfter)
		assert.LessOrEqual(t, res.RequeueAfter, time.Minute)
	}
	var got kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "Promoting", got.Status.Phase)
	synced := findCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionFalse, synced.Status)
	assert.Equal(t, "PolicyNamespaceNotShared", synced.Reason)
	assert.Contains(t, synced.Message, "label it kardinal.io/policy-shareable=true")
	warnings := 0
	for len(rec.Events) > 0 {
		e := <-rec.Events
		assert.NotContains(t, e, "GraphSyncFailed")
		if strings.Contains(e, "PolicyNamespaceNotShared") {
			warnings++
		}
	}
	assert.Equal(t, 1, warnings, "one Event over reconciles")

	tr.err = nil // team-b agreed
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, tr.called)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.NotEqual(t, "PolicyNamespaceNotShared", findCondition(got.Status.Conditions, "GraphSynced").Reason)
}
