// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPipelineListServesStatus: GET /api/v1/ui/pipelines serves the
// Pipeline's status.environments and status.summary (P1) as they are, and
// its conditions (the Ready reason, such as GitCredentialMissing, and the
// SecretReferenceable warning) for the UI's badges.
//
// Covers PIPE-ENVSTATUS-02.
func TestPipelineListServesStatus(t *testing.T) {
	since := metav1.NewTime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}
	p.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}
	p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
		{Name: "test", Bundle: "web-b2", Version: "1.5.0", State: "Verified", Since: &since},
		{Name: "prod", Bundle: "web-b1", Version: "1.4.0", IncomingBundle: "web-b2", IncomingVersion: "1.5.0",
			State: "Pending", BlockedBy: []string{"no-weekend-deploys"}, Since: &since},
	}
	p.Status.Summary = "1.5.0 live in test; held by gate no-weekend-deploys in prod"
	p.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "GitCredentialMissing", Message: "create it: kubectl create secret ..."},
		{Type: "SecretReferenceable", Status: metav1.ConditionFalse, Reason: "SecretNotReferenceable", Message: "label it"},
	}
	got := pipelineListResponse([]v1alpha1.Pipeline{p}, nil, nil, nil, time.Now(), nil)
	require.Len(t, got, 1)
	assert.Equal(t, p.Status.Environments, got[0].Environments)
	assert.Equal(t, p.Status.Summary, got[0].Summary)
	require.Len(t, got[0].Conditions, 2)
	reasons := map[string]string{}
	for _, c := range got[0].Conditions {
		reasons[c.Type] = c.Status + "/" + c.Reason
	}
	assert.Equal(t, "False/GitCredentialMissing", reasons["Ready"])
	assert.Equal(t, "False/SecretNotReferenceable", reasons["SecretReferenceable"])

	empty := pipelineListResponse([]v1alpha1.Pipeline{{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "team"}}},
		nil, nil, nil, time.Now(), nil)
	require.Len(t, empty, 1)
	assert.Nil(t, empty[0].Environments, "a Pipeline the reconciler has not written yet")
	assert.Empty(t, empty[0].Summary)
}
