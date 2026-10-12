// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// failedBundle is a Failed Bundle of pipeline "app" with conds.
func failedBundle(name, tag string, created time.Time, conds ...metav1.Condition) kardinalv1alpha1.Bundle {
	b := envBundle(name, tag, "Failed", created)
	b.Status.Conditions = conds
	return b
}

// TestFailedVersion: status.failedVersion names the newest Bundle when it is
// Failed, with the reason and message of its InvalidSpec condition, else of
// its Failed condition, else its Ready message; and the environment whose
// step failed. A newer Bundle clears it. The summary says a version that
// failed before any step.
func TestFailedVersion(t *testing.T) {
	failedAt := metav1.NewTime(envT0.Add(time.Hour))
	ready := metav1.Condition{Type: "Ready", Status: "False", Reason: "Failed", LastTransitionTime: failedAt,
		Message: "promotion failed: kro rejected the Graph: compile node \"GateMirror\": ..."}
	rejected := metav1.Condition{Type: "Failed", Status: "True", Reason: "GraphRejected",
		Message: "kro rejected the Graph: compile node \"GateMirror\": ..."}
	invalid := metav1.Condition{Type: "InvalidSpec", Status: "True", Reason: "GraphBuildFailed",
		Message: "build: gate x: bad — fix the Pipeline, Bundle or PolicyGate it names; a change to the Pipeline or its PolicyGates retries this Bundle"}
	live := envBundle("b1", "1.4.0", "Verified", envT0)
	stepFailed := failedBundle("b2", "1.5.0", envT0.Add(time.Minute), ready,
		metav1.Condition{Type: "Failed", Status: "True", Reason: "StepFailed", Message: "environment uat: health check timeout"})
	stepFailed.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified"}, {Name: "uat", Phase: "Failed"}}
	envs := []kardinalv1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "b1", Version: "1.4.0", State: "Verified"}}
	tests := []struct {
		name    string
		bundles []kardinalv1alpha1.Bundle
		want    *kardinalv1alpha1.PipelineFailedVersion
		summary string
	}{
		{name: "no Bundle", want: nil, summary: "1.4.0 live in test"},
		{name: "the newest is Verified", bundles: []kardinalv1alpha1.Bundle{live}, summary: "1.4.0 live in test"},
		{name: "kro rejected the Graph",
			bundles: []kardinalv1alpha1.Bundle{live, failedBundle("b2", "1.5.0", envT0.Add(time.Minute), ready, rejected)},
			want: &kardinalv1alpha1.PipelineFailedVersion{Bundle: "b2", Version: "1.5.0", Reason: "GraphRejected",
				Message: rejected.Message, Since: &failedAt},
			summary: "1.4.0 live in test; 1.5.0 (b2) failed before it started (GraphRejected)"},
		{name: "InvalidSpec wins over Failed",
			bundles: []kardinalv1alpha1.Bundle{failedBundle("b2", "1.5.0", envT0, ready, rejected, invalid)},
			want: &kardinalv1alpha1.PipelineFailedVersion{Bundle: "b2", Version: "1.5.0", Reason: "GraphBuildFailed",
				Message: invalid.Message, Since: &failedAt},
			summary: "1.4.0 live in test; 1.5.0 (b2) failed before it started (GraphBuildFailed)"},
		{name: "only the Ready message",
			bundles: []kardinalv1alpha1.Bundle{failedBundle("b2", "1.5.0", envT0, ready)},
			want: &kardinalv1alpha1.PipelineFailedVersion{Bundle: "b2", Version: "1.5.0", Reason: "Failed",
				Message: "kro rejected the Graph: compile node \"GateMirror\": ...", Since: &failedAt},
			summary: "1.4.0 live in test; 1.5.0 (b2) failed before it started (Failed)"},
		{name: "a step failed in an environment",
			bundles: []kardinalv1alpha1.Bundle{live, stepFailed},
			want: &kardinalv1alpha1.PipelineFailedVersion{Bundle: "b2", Version: "1.5.0", Reason: "StepFailed",
				Message: "environment uat: health check timeout", Environment: "uat", Since: &failedAt},
			summary: "1.4.0 live in test; 1.5.0 (b2) failed in uat (StepFailed)"},
		{name: "a newer Bundle clears it",
			bundles: []kardinalv1alpha1.Bundle{failedBundle("b2", "1.5.0", envT0, ready, rejected), envBundle("b3", "1.6.0", "Promoting", envT0.Add(time.Hour))},
			summary: "1.4.0 live in test"},
		{name: "another pipeline's Bundle is not read",
			bundles: func() []kardinalv1alpha1.Bundle {
				b := failedBundle("x", "9.9", envT0.Add(2*time.Hour), ready, rejected)
				b.Spec.Pipeline = "other"
				return []kardinalv1alpha1.Bundle{live, b}
			}(),
			summary: "1.4.0 live in test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pipeline.FailedVersion("app", tt.bundles)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.summary, pipeline.WithFailure("1.4.0 live in test", got, envs))
		})
	}
	// A version that failed in an environment the projection shows is in the
	// summary already.
	f := pipeline.FailedVersion("app", []kardinalv1alpha1.Bundle{live, stepFailed})
	shown := append(slices.Clone(envs), kardinalv1alpha1.PipelineEnvironmentStatus{Name: "uat", IncomingBundle: "b2", State: "Failed"})
	assert.Equal(t, "1.4.0 live in test; failed in uat", pipeline.WithFailure("1.4.0 live in test; failed in uat", f, shown))
}

// kroGraphRejected is the Ready message the walkthrough's GateMirror failure
// left on its Bundle: kro's CEL error with the expression echoed and a caret
// line under it.
const kroGraphRejected = `promotion failed: kro rejected the Graph: compile node "GateMirror": variable at "spec.live.gates" ` +
	`("(PolicyGates + ApprovalGates).filter(...)"): ERROR: <input>:1:14: found no matching overload for '_+_' applied to ` +
	`'(list(__type_PolicyGates.@idx), list(__type_ApprovalGates.@idx))'` + "\n" +
	` | (PolicyGates + ApprovalGates).filter(g, g.metadata.?labels[?"kardinal.io/environment"].orValue("") == Mirror.environment)` + "\n" +
	` | .............^`

// TestFailedVersionMessageIsBounded: status.failedVersion.message keeps the
// first line of the condition message, without kro's echo of the
// expression and its caret line, and at most 300 characters, cut with "…".
func TestFailedVersionMessageIsBounded(t *testing.T) {
	cases := []struct {
		name, msg, want string
	}{
		{name: "kro's GraphRejected", msg: kroGraphRejected,
			want: `kro rejected the Graph: compile node "GateMirror": variable at "spec.live.gates" ("(PolicyGates + ApprovalGates).filter(...)"): ` +
				`ERROR: <input>:1:14: found no matching overload for '_+_' applied to '(list(__type_PolicyGates.@idx), list(__type_ApprovalGates.@idx))'`},
		{name: "long", msg: "promotion failed: " + strings.Repeat("ab ", 200),
			want: strings.TrimSpace(strings.Repeat("ab ", 200)[:299]) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := failedBundle("b2", "1.5.0", envT0, metav1.Condition{Type: "Ready", Status: "False", Reason: "Failed", Message: tc.msg})
			f := pipeline.FailedVersion("app", []kardinalv1alpha1.Bundle{b})
			assert.Equal(t, tc.want, f.Message)
			assert.LessOrEqual(t, len([]rune(f.Message)), 300)
			assert.NotContains(t, f.Message, "\n")
		})
	}
}
