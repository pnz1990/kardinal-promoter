// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func TestDirectUpstreams(t *testing.T) {
	sequential := []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}
	fanIn := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod-eu", DependsOn: []string{"test"}},
		{Name: "prod-us", DependsOn: []string{"test"}},
		{Name: "global", DependsOn: []string{"prod-eu", "prod-us"}},
	}

	tests := []struct {
		name    string
		envs    []kardinalv1alpha1.EnvironmentSpec
		intent  *kardinalv1alpha1.BundleIntent
		env     string
		want    []string
		wantErr bool
	}{
		{name: "sequential prod depends on uat only", envs: sequential, env: "prod", want: []string{"uat"}},
		{name: "root has no upstream", envs: sequential, env: "test", want: nil},
		{name: "fan-in returns both parents", envs: fanIn, env: "global", want: []string{"prod-eu", "prod-us"}},
		{name: "sibling is not an upstream", envs: fanIn, env: "prod-us", want: []string{"test"}},
		{
			name:   "skipped env is bridged",
			envs:   sequential,
			intent: &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}},
			env:    "prod",
			want:   []string{"test"},
		},
		{
			name:    "env beyond target is an error",
			envs:    sequential,
			intent:  &kardinalv1alpha1.BundleIntent{TargetEnvironment: "uat"},
			env:     "prod",
			wantErr: true,
		},
		{name: "unknown env is an error", envs: sequential, env: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: tt.envs},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "app", Intent: tt.intent},
			}
			got, err := DirectUpstreams(p, b, tt.env)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

// E2E-R18: an environment counts as reached only when every direct upstream
// has the bundle's step Verified, the same rule the Graph gates the
// environment's PromotionStep on.
func TestUpstreamsVerified(t *testing.T) {
	sequential := []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}}
	fanIn := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod-eu", DependsOn: []string{"test"}},
		{Name: "prod-us", DependsOn: []string{"test"}},
		{Name: "global", DependsOn: []string{"prod-eu", "prod-us"}},
	}
	regional := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test", Regions: []string{"us", "eu"}},
		{Name: "prod"},
	}
	step := func(bundle, env, state string) kardinalv1alpha1.PromotionStep {
		return kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: bundle + "-" + env, Namespace: "default"},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	tests := []struct {
		name   string
		envs   []kardinalv1alpha1.EnvironmentSpec
		intent *kardinalv1alpha1.BundleIntent
		steps  []kardinalv1alpha1.PromotionStep
		env    string
		want   bool
	}{
		{name: "root is always reached", envs: sequential, env: "test", want: true},
		{
			name:  "upstream still health checking",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "HealthChecking")},
			env:   "prod",
			want:  false,
		},
		{
			name:  "upstream has no step yet",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified")},
			env:   "prod",
			want:  false,
		},
		{
			name:  "upstream Verified",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified"), step("app-v1", "uat", "Verified")},
			env:   "prod",
			want:  true,
		},
		{
			name:  "another bundle's Verified step does not count",
			envs:  sequential,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v0", "uat", "Verified")},
			env:   "prod",
			want:  false,
		},
		{
			name:   "skipped env is bridged to its upstream",
			envs:   sequential,
			intent: &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"uat"}},
			steps:  []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified")},
			env:    "prod",
			want:   true,
		},
		{
			name:  "fan-in needs every parent Verified",
			envs:  fanIn,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "prod-eu", "Verified"), step("app-v1", "prod-us", "Promoting")},
			env:   "global",
			want:  false,
		},
		{
			name:  "multi-region upstream needs every region",
			envs:  regional,
			steps: []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified")},
			env:   "prod",
			want:  false,
		},
		{
			name:   "env beyond target is not reached",
			envs:   sequential,
			intent: &kardinalv1alpha1.BundleIntent{TargetEnvironment: "uat"},
			steps:  []kardinalv1alpha1.PromotionStep{step("app-v1", "test", "Verified"), step("app-v1", "uat", "Verified")},
			env:    "prod",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: tt.envs},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "app", Intent: tt.intent},
			}
			assert.Equal(t, tt.want, UpstreamsVerified(p, b, tt.env, tt.steps))
		})
	}
}

func TestGateHolds(t *testing.T) {
	p := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod"},
		}},
	}
	const gateName = "app-v1-prod-soak"
	step := func(env, state string, gates ...string) kardinalv1alpha1.PromotionStep {
		return kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: "app-v1-" + env, Namespace: "default"},
			Spec: kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: env,
				RequiredGates: gates},
			Status: kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	testVerified := step("test", "Verified")
	tests := []struct {
		name  string
		phase string
		when  string
		ready bool
		steps []kardinalv1alpha1.PromotionStep
		want  bool
	}{
		{name: "env not reached", phase: "Promoting", steps: []kardinalv1alpha1.PromotionStep{step("test", "Promoting")}},
		{name: "env reached, no step yet", phase: "Promoting", steps: []kardinalv1alpha1.PromotionStep{testVerified}, want: true},
		{name: "ready gate", phase: "Promoting", ready: true, steps: []kardinalv1alpha1.PromotionStep{testVerified}},
		{name: "failed bundle", phase: "Failed", steps: []kardinalv1alpha1.PromotionStep{testVerified}},
		{
			name: "post-deploy gate, Pending step", phase: "Promoting",
			steps: []kardinalv1alpha1.PromotionStep{testVerified, step("prod", "Pending", gateName)},
		},
		{
			name: "pre-deploy gate, Pending step", phase: "Promoting", when: "pre-deploy",
			steps: []kardinalv1alpha1.PromotionStep{testVerified, step("prod", "Pending", gateName)}, want: true,
		},
		{
			name: "pre-deploy gate, step with no state", phase: "Promoting", when: "pre-deploy",
			steps: []kardinalv1alpha1.PromotionStep{testVerified, step("prod", "", gateName)}, want: true,
		},
		{
			name: "pre-deploy gate, step started", phase: "Promoting", when: "pre-deploy",
			steps: []kardinalv1alpha1.PromotionStep{testVerified, step("prod", "Promoting", gateName)},
		},
		{
			name: "pre-deploy gate the step does not require", phase: "Promoting", when: "pre-deploy",
			steps: []kardinalv1alpha1.PromotionStep{testVerified, step("prod", "Pending")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "app"},
				Status:     kardinalv1alpha1.BundleStatus{Phase: tt.phase},
			}
			g := &kardinalv1alpha1.PolicyGate{
				ObjectMeta: metav1.ObjectMeta{Name: gateName, Namespace: "default", Labels: map[string]string{
					"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod",
				}},
				Spec:   kardinalv1alpha1.PolicyGateSpec{Expression: "false", When: tt.when},
				Status: kardinalv1alpha1.PolicyGateStatus{Ready: tt.ready},
			}
			assert.Equal(t, tt.want, GateHolds(p, b, g, tt.steps))
		})
	}
}

// GateState is the state the UI API and kardinal explain share (E2E-R21):
// only a gate that holds its bundle is Block; a gate the bundle has not
// reached is Waiting, and a Superseded bundle's gates are Superseded.
func TestGateState(t *testing.T) {
	p := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "prod"},
		}},
	}
	step := func(env, state string) kardinalv1alpha1.PromotionStep {
		return kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: "app-v1-" + env, Namespace: "default"},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: env},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	reached := []kardinalv1alpha1.PromotionStep{step("test", "Verified")}
	notReached := []kardinalv1alpha1.PromotionStep{step("test", "HealthChecking")}
	evaluated := metav1.Now()
	tests := []struct {
		name      string
		phase     string // bundle phase; "gone" for no Bundle
		noPipe    bool
		template  bool // a gate template, no kardinal.io/bundle label
		ready     bool
		evaluated bool
		steps     []kardinalv1alpha1.PromotionStep
		want      string
	}{
		{name: "ready", phase: "Promoting", ready: true, evaluated: true, steps: reached, want: GateStatePass},
		{name: "holding, evaluated", phase: "Promoting", evaluated: true, steps: reached, want: GateStateBlock},
		{name: "holding, not evaluated yet", phase: "Promoting", steps: reached, want: GateStateBlock},
		{name: "not reached, evaluated", phase: "Promoting", evaluated: true, steps: notReached, want: GateStateWaiting},
		{name: "not reached, not evaluated yet", phase: "Promoting", steps: notReached, want: GateStatePending},
		{name: "Failed bundle", phase: "Failed", evaluated: true, steps: reached, want: GateStateWaiting},
		{name: "Superseded bundle", phase: "Superseded", evaluated: true, steps: reached, want: GateStateSuperseded},
		{name: "Superseded bundle, not evaluated yet", phase: "Superseded", steps: notReached, want: GateStateSuperseded},
		{name: "Superseded bundle, ready", phase: "Superseded", ready: true, steps: reached, want: GateStatePass},
		{name: "Pipeline gone", phase: "Promoting", noPipe: true, evaluated: true, steps: reached, want: GateStateWaiting},
		{name: "Bundle gone", phase: "gone", evaluated: true, steps: reached, want: GateStateWaiting},
		{name: "template, evaluated", phase: "gone", template: true, evaluated: true, want: GateStateWaiting},
		{name: "template, not evaluated", phase: "gone", template: true, want: GateStatePending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b *kardinalv1alpha1.Bundle
			if tt.phase != "gone" {
				b = &kardinalv1alpha1.Bundle{
					ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
					Spec:       kardinalv1alpha1.BundleSpec{Pipeline: "app"},
					Status:     kardinalv1alpha1.BundleStatus{Phase: tt.phase},
				}
			}
			pipe := p
			if tt.noPipe {
				pipe = nil
			}
			labels := map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod"}
			if tt.template {
				labels = map[string]string{"kardinal.io/applies-to": "prod"}
			}
			g := &kardinalv1alpha1.PolicyGate{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod-soak", Namespace: "default", Labels: labels},
				Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "false"},
				Status:     kardinalv1alpha1.PolicyGateStatus{Ready: tt.ready},
			}
			if tt.evaluated {
				g.Status.LastEvaluatedAt = &evaluated
			}
			assert.Equal(t, tt.want, GateState(pipe, b, g, tt.steps))
		})
	}
}
