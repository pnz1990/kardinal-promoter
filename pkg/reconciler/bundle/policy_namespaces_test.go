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
