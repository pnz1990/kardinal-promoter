// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// rejectedLiveObjects: b1 is Verified everywhere; b2 was rejected after its
// change reached test (Verified) and prod (HealthChecking), while its uat
// step was cancelled (Failed).
func rejectedLiveObjects() []sigs_client.Object {
	old := time.Now().Add(-18 * time.Hour)
	recent := time.Now().Add(-17 * time.Minute)
	b2 := explainBundle("b2", "Rejected", recent)
	b2.Spec.Rejected = &v1alpha1.BundleRejection{By: "alice", Reason: "CVE"}
	return []sigs_client.Object{
		policyPipeline("demo", "test", "uat", "prod"),
		explainBundle("b1", "Verified", old), b2,
		explainStep("demo", "b1", "test", "Verified", "b1 test", old),
		explainStep("demo", "b1", "uat", "Verified", "b1 uat", old),
		explainStep("demo", "b1", "prod", "Verified", "b1 prod", old),
		explainStep("demo", "b2", "test", "Verified", "b2 test", recent),
		explainStep("demo", "b2", "uat", "Failed", "b2 uat", recent),
		explainStep("demo", "b2", "prod", "HealthChecking", "b2 prod", recent),
	}
}

const (
	hintTest = "WARNING: bundle b2 is Rejected in test: rejected change is live; roll back (kardinal rollback demo --env test)"
	hintProd = "WARNING: bundle b2 is Rejected in prod: rejected change is live; roll back (kardinal rollback demo --env prod)"
)

// TestCurrentBundleByEnv_RejectedLive (QA #1489): a Rejected Bundle whose
// change is live in an environment (HealthChecking or Verified there) stays
// the current Bundle there; where its step did not go live, the older
// Bundle is current.
//
// Covers BUNDLE-REJECT-07.
func TestCurrentBundleByEnv_RejectedLive(t *testing.T) {
	var bundles []v1alpha1.Bundle
	var steps []v1alpha1.PromotionStep
	for _, o := range rejectedLiveObjects() {
		switch x := o.(type) {
		case *v1alpha1.Bundle:
			bundles = append(bundles, *x)
		case *v1alpha1.PromotionStep:
			steps = append(steps, *x)
		}
	}
	assert.Equal(t, map[string]string{"test": "b2", "uat": "b1", "prod": "b2"},
		currentBundleByEnv(bundles, steps, nil))

	active, hints := currentSteps("demo", bundles, steps)
	var names []string
	for _, s := range active {
		names = append(names, s.Name)
	}
	assert.ElementsMatch(t, []string{"demo-b1-test", "demo-b1-uat", "demo-b1-prod", "demo-b2-test", "demo-b2-prod"}, names,
		"the steps of b1 and the live steps of b2; not b2's cancelled uat step")
	assert.Equal(t, []string{hintProd, hintTest}, hints)
}

// TestStatus_RejectedLive: kardinal status shows the rejected change where it
// is live, with a roll-back hint per environment.
func TestStatus_RejectedLive(t *testing.T) {
	out := runStatusPipeline(t, rejectedLiveObjects()...)
	assert.Regexp(t, `\n  test +- +b2 +Verified`, out)
	assert.Regexp(t, `\n  uat +- +b1 +Verified`, out)
	assert.Regexp(t, `\n▶ prod +- +b2 +HealthChecking`, out)
	assert.Contains(t, out, hintTest)
	assert.Contains(t, out, hintProd)
}

// TestExplain_RejectedLive: kardinal explain shows the live rejected change
// and the hint, for the environment asked about only.
func TestExplain_RejectedLive(t *testing.T) {
	c := policyClient(t, rejectedLiveObjects()...)
	out, err := runExplain(t, c, "demo", "prod", false)
	require.NoError(t, err)
	assert.Contains(t, out, "b2 prod")
	assert.Contains(t, out, hintProd)
	assert.NotContains(t, out, hintTest)

	out, err = runExplain(t, c, "demo", "uat", false)
	require.NoError(t, err)
	assert.Contains(t, out, "b1 uat")
	assert.NotContains(t, out, "WARNING")
}

// TestGetSteps_RejectedLive: kardinal get steps lists the live steps of a
// Rejected Bundle with the hint, not its cancelled ones.
func TestGetSteps_RejectedLive(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, getStepsOnce(&buf, policyClient(t, rejectedLiveObjects()...), "default", "demo"))
	out := buf.String()
	assert.Contains(t, out, "HealthChecking")
	assert.Contains(t, out, hintProd)
	assert.Contains(t, out, hintTest)
}

// TestLogs_RejectedLive: kardinal logs shows the live steps of a Rejected
// Bundle, not its cancelled ones.
func TestLogs_RejectedLive(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, logsFn(&buf, policyClient(t, rejectedLiveObjects()...), "default", "demo", "", ""))
	out := buf.String()
	assert.Contains(t, out, "b2 prod")
	assert.Contains(t, out, "b2 test")
	assert.NotContains(t, out, "b2 uat")
}
