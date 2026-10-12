// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

var updateWalkGates = flag.Bool("update-walk-gates", false, "rewrite testdata/walk-gates.golden")

// walkGatesFixture is the v0.11 walkthrough's prod: version sha-9349a3f
// (demo-kzwhs) live in test and uat (uat 24s ago), held at prod by the
// quickstart's approval gate and a team soak gate of 3 minutes.
func walkGatesFixture(now time.Time) []sigs_client.Object {
	p := policyPipeline("demo", "test", "uat", "prod")
	since := metav1.NewTime(now.Add(-time.Minute))
	p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
		{Name: "test", Bundle: "demo-kzwhs", Version: "sha-9349a3f", State: "Verified", Since: &since},
		{Name: "uat", Bundle: "demo-kzwhs", Version: "sha-9349a3f", State: "Verified", Since: &since},
		{Name: "prod", Bundle: "demo-r48zm", Version: "main", IncomingBundle: "demo-kzwhs", IncomingVersion: "sha-9349a3f",
			State: "Pending", BlockedBy: []string{"prod-approval", "uat-soak-before-prod"}, Since: &since},
	}
	b := explainBundle("demo-kzwhs", "Promoting", now.Add(-2*time.Minute))
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "sha-9349a3f"}}
	healthy := metav1.NewTime(now.Add(-24 * time.Second))
	b.Status.Environments = []v1alpha1.EnvironmentStatus{
		{Name: "test", Phase: "Verified", HealthCheckedAt: &healthy},
		{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthy},
	}
	old := explainBundle("demo-r48zm", "Verified", now.Add(-time.Hour))
	old.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/pnz1990/kardinal-test-app", Tag: "main"}}
	approval := explainGateInstance("demo", "demo-kzwhs", "prod", "prod-approval", "true", false, true,
		"prod waits for your approval (waiting for approvals: 0 of 1; version sha-9349a3f)")
	approval.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
	approval.Spec.Message = "prod waits for your approval: kardinal approve demo prod"
	soak := explainGateInstance("demo", "demo-kzwhs", "prod", "uat-soak-before-prod", "bundle.upstreamSoakMinutes >= 3", false, true,
		"bundle.upstreamSoakMinutes >= 3 = false; version sha-9349a3f")
	soak.Spec.RecheckInterval = "30s"
	soak.Status.BundleVersion = "sha-9349a3f"
	p.Status.Summary = pipelinerec.Summarize(p, metav1.Condition{}, p.Status.Environments)
	return []sigs_client.Object{p, b, old, approval, soak,
		explainStep("demo", "demo-kzwhs", "test", "Verified", "", now.Add(-2*time.Minute)),
		explainStep("demo", "demo-kzwhs", "uat", "Verified", "", now.Add(-time.Minute)),
	}
}

// TestWalkGates_Golden (walkthrough v0.11): status's Blocking gates shows an
// approval gate as its tally ("approval 0 of 1"), not its expression
// (true); explain and why show a soak gate's progress and when it opens,
// and offer its override only after waiting.
//
// Covers CLI-WHY-01.
func TestWalkGates_Golden(t *testing.T) {
	keepStyle(t)
	t.Setenv("KUBECONFIG", "/dev/null")
	stubIdentity(t, Identity{Username: "kubernetes-admin"})
	now := time.Now().Truncate(time.Second)
	prevWhy, prevExplain, prevStatus := whyNow, explainNow, statusNow
	clock := func() time.Time { return now }
	whyNow, explainNow, statusNow = clock, clock, clock
	t.Cleanup(func() { whyNow, explainNow, statusNow = prevWhy, prevExplain, prevStatus })
	c := policyClient(t, walkGatesFixture(now)...)

	var golden strings.Builder
	for _, args := range [][]string{{"status", "demo"}, {"explain", "demo", "prod"}, {"why", "demo", "prod"}} {
		out, stderr, err := runOutput(t, c, args...)
		require.NoError(t, err, args)
		golden.WriteString("$ kardinal " + strings.Join(args, " ") + "\n" + volatile.ReplaceAllString(out, "<time>"))
		if stderr != "" {
			golden.WriteString("(stderr)\n" + stderr)
		}
		golden.WriteString("\n")
	}
	got := golden.String()
	assert.Contains(t, got, "approval 0 of 1")
	assert.Contains(t, got, "soak 0.4 of 3 min in uat")
	assert.NotContains(t, got, "Sha-9349a3f")

	path := filepath.Join("testdata", "walk-gates.golden")
	if *updateWalkGates {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test -run TestWalkGates_Golden -update-walk-gates")
	assert.Equal(t, string(want), got)
}
