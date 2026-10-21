// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestPolicyNamespacesChanged (#2286): a Pipeline update whose
// status.policyNamespaces changed wakes its Bundles (a version waiting for a
// namespace to agree); any other status update does not.
func TestPolicyNamespacesChanged(t *testing.T) {
	pipeline := func(st *kardinalv1alpha1.PipelinePolicyNamespaces, phase string) *kardinalv1alpha1.Pipeline {
		return &kardinalv1alpha1.Pipeline{Status: kardinalv1alpha1.PipelineStatus{PolicyNamespaces: st, Phase: phase}}
	}
	refused := &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 2, NotShared: []string{"team-b"}}
	agreed := &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 2}
	for name, tc := range map[string]struct {
		old, new *kardinalv1alpha1.Pipeline
		want     bool
	}{
		"the namespace agreed":       {pipeline(refused, "Ready"), pipeline(agreed, "Ready"), true},
		"first checked":              {pipeline(nil, "Ready"), pipeline(agreed, "Ready"), true},
		"another generation checked": {pipeline(agreed, "Ready"), pipeline(&kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 3}, "Ready"), true},
		"another status field":       {pipeline(refused, "Ready"), pipeline(refused, "Promoting"), false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, policyNamespacesChanged.Update(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.new}))
		})
	}
}

// refusedPipeline is a Pipeline whose spec.policyNamespaces names team-b,
// refused for its generation.
func refusedPipeline() *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a", Generation: 2},
		Spec: kardinalv1alpha1.PipelineSpec{PolicyNamespaces: []string{"team-b"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}},
		Status: kardinalv1alpha1.PipelineStatus{PolicyNamespaces: &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 2, NotShared: []string{"team-b"}}}}
}

// TestGateChange_RefusedNamespaceNotRecorded (#2286): a gate change under a
// version in flight records no change for a refused namespace's templates,
// which the version keeps as its Graph had them and which are not read
// (they are not among the current gates): left in, they would be recorded as
// deleted.
// Covers GATE-POLNS-07.
func TestGateChange_RefusedNamespaceNotRecorded(t *testing.T) {
	p := refusedPipeline()
	b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "team-a"},
		Spec: kardinalv1alpha1.BundleSpec{Pipeline: "app"}}
	own := kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "own", Namespace: "team-a", Generation: 1,
		Labels: map[string]string{"kardinal.io/applies-to": "prod"}}, Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "true"}}
	stamps := map[string]graph.TemplateStamp{
		"team-a/own":  {Digest: graph.TemplateDigest(own), Generation: 1},
		"team-b/kept": {Digest: "sha256:kept", Generation: 4},
	}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	_, recorded := gateChangeEntry(b, p, stamps, []kardinalv1alpha1.PolicyGate{own}, now)
	assert.True(t, recorded, "unfiltered, the kept template reads as deleted")
	_, recorded = gateChangeEntry(b, p, withoutNamespaces(stamps, []string{"team-b"}), []kardinalv1alpha1.PolicyGate{own}, now)
	assert.False(t, recorded, "nothing changed")
}

// TestNoteKeptGates (#2286): a version in flight re-rendered while its
// Pipeline names a refused namespace says, in a Warning Event, that it
// keeps that namespace's gates; a new version or a Pipeline with none
// refused says nothing.
// Covers GATE-POLNS-07.
func TestNoteKeptGates(t *testing.T) {
	for name, tc := range map[string]struct {
		graphRef string
		pipeline *kardinalv1alpha1.Pipeline
		want     bool
	}{
		"in flight, refused": {"app-app-v1", refusedPipeline(), true},
		"a new version":      {"", refusedPipeline(), false},
		"nothing refused":    {"app-app-v1", &kardinalv1alpha1.Pipeline{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			rec := events.NewFakeRecorder(5)
			r := &Reconciler{Recorder: rec}
			b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "team-a"},
				Status: kardinalv1alpha1.BundleStatus{GraphRef: tc.graphRef}}
			r.noteKeptGates(b, tc.pipeline)
			got := ""
			if len(rec.Events) > 0 {
				got = <-rec.Events
			}
			assert.Equal(t, tc.want, got != "", got)
			if tc.want {
				assert.True(t, strings.Contains(got, "keeps the gates of team-b it started with"), got)
			}
		})
	}
}
