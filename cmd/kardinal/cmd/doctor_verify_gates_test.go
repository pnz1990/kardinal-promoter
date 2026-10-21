// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestDoctor_VerifyGates (#2411): doctor names the verify gate templates
// that list only agents, which from v0.13.0 count no person's verdict (one
// allowlist). Against an older or unknown controller, which still counts
// every person's verdict on them, it warns, naming the gates on which a
// person's verdict counted for a kept version, with what to do; from v0.13.0
// (and with a dev build) it passes and names them. A gate that lists people
// or groups, or nobody, is not named; neither is a gate instance. A user who
// may not list PolicyGates cluster-wide gets the current namespace's, and a
// WARN where the cluster's would pass.
//
// Covers CLI-DOCTOR-04.
func TestDoctor_VerifyGates(t *testing.T) {
	t.Parallel()
	gate := func(ns, name string, p *v1alpha1.GateVerifyPolicy) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: v1alpha1.PolicyGateSpec{Expression: "true", Verify: p}}
	}
	instance := func(ns, name, tmpl, tmplNS string, verdicts ...v1alpha1.GateVerdictStatus) *v1alpha1.PolicyGate {
		g := gate(ns, name, &v1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa"}})
		g.Labels = map[string]string{"kardinal.io/gate-template": tmpl, "kardinal.io/gate-template-namespace": tmplNS, "kardinal.io/bundle": "b1"}
		g.Status.Verdicts = verdicts
		return g
	}
	estate := func() []sigs_client.Object {
		return []sigs_client.Object{
			gate("platform", "qa-only", &v1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa"}}),
			gate("team-b", "explore", &v1alpha1.GateVerifyPolicy{Check: "explore", AllowedAgents: []string{"qa", "fixer"}}),
			gate("team-b", "with-lead", &v1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa"}, AllowedUsers: []string{"lead"}}),
			gate("team-b", "with-group", &v1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa"}, AllowedGroups: []string{"qa-team"}}),
			gate("team-b", "anyone", &v1alpha1.GateVerifyPolicy{Check: "qa"}),
			gate("team-b", "plain", nil),
			// A person's verdict counted on two of qa-only's versions, an
			// agent's on explore's.
			instance("team-b", "qa-only-platform-prod--b1", "qa-only", "platform",
				v1alpha1.GateVerdictStatus{Name: "v1", Verifier: "alice@example.com", Counted: true}),
			instance("team-c", "qa-only-platform-prod--b2", "qa-only", "platform",
				v1alpha1.GateVerdictStatus{Name: "v2", Verifier: "agent qa for bob", Counted: true},
				v1alpha1.GateVerdictStatus{Name: "v3", Verifier: "carol@example.com", Counted: true}),
			instance("team-b", "explore-team-b-prod--b1", "explore", "team-b",
				v1alpha1.GateVerdictStatus{Name: "v4", Verifier: "agent qa for bob", Counted: true},
				v1alpha1.GateVerdictStatus{Name: "v5", Verifier: "dave@example.com", Counted: false}),
		}
	}
	clean := func() []sigs_client.Object {
		return []sigs_client.Object{gate("team-b", "anyone", &v1alpha1.GateVerifyPolicy{Check: "qa"})}
	}
	for name, tc := range map[string]struct {
		objs       func() []sigs_client.Object
		controller string
		listAll    bool
		icon       string
		detail     string
	}{
		"v0.12 warns before the upgrade": {estate, "v0.12.3", true, doctorWarn,
			"v0.13.0 counts only their agents: platform/qa-only (a person's verdict counted on 2 gate instances), team-b/explore (all namespaces)"},
		"an unknown controller warns": {estate, "unknown", true, doctorWarn, "v0.13.0 counts only their agents: platform/qa-only"},
		"v0.13 names them":            {estate, "v0.13.0", true, doctorPass, "count no person's verdict: platform/qa-only, team-b/explore (all namespaces)"},
		"a v0.13 release candidate":   {estate, "v0.13.0-rc.1", true, doctorPass, "count no person's verdict"},
		"a dev build is main's":       {estate, "v0.14.0-dev", true, doctorPass, "count no person's verdict"},
		"none lists only agents":      {clean, "v0.12.3", true, doctorPass, "no verify gate lists only agents (all namespaces)"},
		"only the current namespace":  {estate, "v0.12.3", false, doctorWarn, "team-b/explore (namespace team-b)"},
		"a namespace's pass is partial": {estate, "v0.13.0", false, doctorWarn,
			"checked namespace team-b only: count no person's verdict: team-b/explore (namespace team-b)"},
		"a clean namespace only is not a PASS": {clean, "v0.13.0", false, doctorWarn,
			"checked namespace team-b only: no verify gate lists only agents (namespace team-b)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs()...)
			if !tc.listAll {
				b = b.WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c sigs_client.WithWatch,
					list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
					o := &sigs_client.ListOptions{}
					o.ApplyOptions(opts)
					if o.Namespace == "" {
						return errors.New(`policygates.kardinal.io is forbidden: cannot list resource "policygates" at the cluster scope`)
					}
					return c.List(ctx, list, opts...)
				}})
			}
			r := checkVerifyGates(context.Background(), b.Build(), "team-b", tc.controller)
			assert.Equal(t, "Verify gates of agents only", r.label)
			assert.Equal(t, tc.icon, r.icon, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			assert.False(t, r.failed, "never a FAIL: the gates count their agents")
			assert.Equal(t, tc.icon == doctorWarn, r.warned)
			if tc.icon == doctorWarn {
				assert.NotEmpty(t, r.hint)
			}
			if strings.Contains(tc.detail, "counts only their agents") {
				assert.Contains(t, r.hint, "verify.allowedUsers")
			}
		})
	}
}
