// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestSoakOf: a soak gate's progress and when it opens, from the Bundle's
// status.environments[].healthCheckedAt of the environments it reads.
//
// Covers CLI-WHY-01.
func TestSoakOf(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *metav1.Time { m := metav1.NewTime(now.Add(-d)); return &m }
	fanIn := policyPipeline("demo", "test", "eu", "us", "prod")
	fanIn.Spec.Environments[1].DependsOn = []string{"test"}
	fanIn.Spec.Environments[2].DependsOn = []string{"test"}
	fanIn.Spec.Environments[3].DependsOn = []string{"eu", "us"}
	tests := []struct {
		name     string
		p        *v1alpha1.Pipeline
		env      string
		expr     string
		approval bool
		envs     []v1alpha1.EnvironmentStatus
		carried  []v1alpha1.UpstreamEvidence
		ok       bool
		now      string
		opens    string
	}{
		{name: "the walkthrough's soak gate", p: policyPipeline("demo", "test", "uat", "prod"), env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 3",
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(24 * time.Second)}},
			ok:   true, now: "soak 0.4 of 3 min in uat", opens: "in about 2m36s"},
		{name: "parenthesised, strict", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "(bundle.upstreamSoakMinutes > 1)",
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(time.Minute)}},
			ok:   true, now: "soak 1.0 of 2 min in uat", opens: "in about 1m0s"},
		{name: "soaked long enough: the next check opens it", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 3",
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(time.Hour)}},
			ok:   true, now: "soak 60.0 of 3 min in uat", opens: "at its next check"},
		{name: "a named environment", p: policyPipeline("demo", "test", "uat", "prod"), env: "prod",
			expr: "upstream.test.soakMinutes >= 10",
			envs: []v1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified", HealthCheckedAt: ago(5 * time.Minute)}},
			ok:   true, now: "soak 5.0 of 10 min in test", opens: "in about 5m0s"},
		{name: "fan-in reads the least soaked upstream", p: fanIn, env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 30",
			envs: []v1alpha1.EnvironmentStatus{{Name: "eu", Phase: "Verified", HealthCheckedAt: ago(20 * time.Minute)},
				{Name: "us", Phase: "Verified", HealthCheckedAt: ago(10 * time.Minute)}},
			ok: true, now: "soak 10.0 of 30 min in eu, us", opens: "in about 20m0s"},
		{name: "an upstream not live yet", p: fanIn, env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 30",
			envs: []v1alpha1.EnvironmentStatus{{Name: "eu", Phase: "Verified", HealthCheckedAt: ago(20 * time.Minute)},
				{Name: "us", Phase: "HealthChecking"}},
			ok: true, now: "eu, us not live yet, needs 30 min of soak", opens: "30 min after eu and us are live"},
		{name: "part of a larger expression: no time it opens", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: `bundle.upstreamSoakMinutes >= 3 && !schedule.isWeekend`,
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(time.Minute)}},
			ok:   true, now: "soak 1.0 of 3 min in uat", opens: ""},
		{name: "a fraction of a minute: whole minutes open it", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 3.5",
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(time.Minute)}},
			ok:   true, now: "soak 1.0 of 4 min in uat", opens: "in about 3m0s"},
		{name: "strict with a fraction", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "bundle.upstreamSoakMinutes > 2.5",
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(time.Minute)}},
			ok:   true, now: "soak 1.0 of 3 min in uat", opens: "in about 2m0s"},
		{name: "a threshold past a year is not read (no Duration overflow)", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 9223372036854775807"},
		{name: "an exponent is not a number of minutes it reads", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "bundle.upstreamSoakMinutes >= 3e2"},
		{name: "negated: it does not open by soaking", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "!(bundle.upstreamSoakMinutes >= 3)"},
		{name: "negated without parentheses", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: "true && ! ( ( bundle.upstreamSoakMinutes > 3 ) )"},
		{name: "a != elsewhere is not a negation", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: `bundle.provenance.author != "bot" && bundle.upstreamSoakMinutes >= 3`,
			envs: []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: ago(time.Minute)}},
			ok:   true, now: "soak 1.0 of 3 min in uat", opens: ""},
		{name: "a string that only mentions it", p: policyPipeline("demo", "uat", "prod"), env: "prod",
			expr: `bundle.labels.note != "bundle.upstreamSoakMinutes >= 3"`},
		{name: "two soak comparisons", p: policyPipeline("demo", "test", "uat", "prod"), env: "prod",
			expr: "upstream.test.soakMinutes >= 3 && upstream.uat.soakMinutes >= 3"},
		{name: "an approval gate", p: policyPipeline("demo", "uat", "prod"), env: "prod", approval: true,
			expr: "bundle.upstreamSoakMinutes >= 3"},
		{name: "a root environment has no upstream", p: policyPipeline("demo", "test"), env: "test",
			expr: "bundle.upstreamSoakMinutes >= 3"},
		{name: "not a soak gate", p: policyPipeline("demo", "uat", "prod"), env: "prod", expr: "!schedule.isWeekend"},
		{name: "a targetOnly version soaks in the verification it carries (#1311)", p: policyPipeline("demo", "test", "uat", "prod"), env: "prod",
			expr:    "bundle.upstreamSoakMinutes >= 30",
			envs:    []v1alpha1.EnvironmentStatus{{Name: "prod", Phase: "Promoting"}},
			carried: []v1alpha1.UpstreamEvidence{{Environment: "test", Bundle: "demo-a", VerifiedAt: *ago(2 * time.Hour)}, {Environment: "uat", Bundle: "demo-a", VerifiedAt: *ago(10 * time.Minute)}},
			ok:      true, now: "soak 10.0 of 30 min in uat", opens: "in about 20m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := explainGateInstance("demo", "demo-b", tt.env, "soak", tt.expr, false, true, "")
			if tt.approval {
				g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			}
			b := explainBundle("demo-b", "Promoting", now.Add(-time.Hour))
			b.Status.Environments = tt.envs
			if tt.carried != nil {
				b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: tt.env, TargetOnly: true}
				b.Status.UpstreamEvidence = tt.carried
			}
			s, ok := soakOf(tt.p, b, g, now)
			assert.Equal(t, tt.ok, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.now, s.now())
			assert.Equal(t, tt.opens, s.opens())
		})
	}
	assert.NotPanics(t, func() { _, _ = soakOf(nil, nil, nil, now) })
}
