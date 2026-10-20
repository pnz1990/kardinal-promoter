// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestDoctor_PolicyNamespaces (#2286): doctor lists the Pipelines whose
// spec.policyNamespaces names a namespace that has not agreed to share its
// PolicyGates (PolicyNamespacesShared=False, as the Pipeline reconciler
// writes it), with the namespaces; a v0.13 controller only warns of them
// (WARN, naming the release that refuses them), one from v0.14 refuses them
// (FAIL), an unknown version warns; a controller older than v0.13 does not
// check, so finding none there is a WARN; no affected Pipeline passes. A user
// who may not list Pipelines cluster-wide gets the current namespace's.
//
// Covers GATE-POLNS-04.
func TestDoctor_PolicyNamespaces(t *testing.T) {
	t.Parallel()
	pipeline := func(ns, name string, cond *metav1.Condition) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if cond != nil {
			p.Status.Conditions = []metav1.Condition{*cond}
		}
		return p
	}
	notShared := func(list string) *metav1.Condition {
		return &metav1.Condition{Type: "PolicyNamespacesShared", Status: metav1.ConditionFalse, Reason: "NotShared",
			Message: "spec.policyNamespaces names namespaces that have not agreed to share their PolicyGates: " + list +
				". Label each kardinal.io/policy-shareable=true, or list it in the controller's --policy-shareable-namespaces; " +
				"in this release their gates are still read, a later release refuses them"}
	}
	shared := &metav1.Condition{Type: "PolicyNamespacesShared", Status: metav1.ConditionTrue, Reason: "Shared"}
	// Each subtest builds its own objects: the fake client writes their
	// resourceVersion, so parallel subtests must not share them.
	estate := func() []sigs_client.Object {
		return []sigs_client.Object{
			pipeline("team-b", "web", notShared("team-a")),
			pipeline("team-c", "api", notShared("team-a, team-d")),
			pipeline("team-a", "app", nil),
			pipeline("team-e", "ok", shared),
		}
	}
	clean := func() []sigs_client.Object {
		return []sigs_client.Object{pipeline("team-a", "app", nil), pipeline("team-e", "ok", shared)}
	}
	for name, tc := range map[string]struct {
		objs       func() []sigs_client.Object
		controller string
		listAll    bool
		icon       string
		detail     string
	}{
		"v0.13 warns": {estate, "v0.13.0", true, doctorWarn,
			"v0.14.0 refuses them: team-b/web (team-a); team-c/api (team-a, team-d) (all namespaces)"},
		"v0.14 refuses": {estate, "v0.14.0", true, doctorFail,
			"refused, their new versions wait: team-b/web (team-a); team-c/api (team-a, team-d) (all namespaces)"},
		"a v0.14 patch refuses":    {estate, "0.14.2", true, doctorFail, "refused, their new versions wait"},
		"an unknown version warns": {estate, "unknown", true, doctorWarn, "v0.14.0 refuses them"},
		"a dev build warns":        {estate, "v0.14.0-dev", true, doctorWarn, "v0.14.0 refuses them"},
		"none affected": {clean, "v0.13.0", true,
			doctorPass, "every spec.policyNamespaces entry agreed (2 Pipelines, all namespaces)"},
		"a controller before v0.13 does not check": {estate, "v0.12.3", true, doctorWarn,
			"controller v0.12.3 does not check spec.policyNamespaces consent"},
		"only the current namespace": {estate, "v0.13.0", false, doctorWarn, "team-b/web (team-a) (namespace team-b)"},
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
						return errors.New(`pipelines.kardinal.io is forbidden: cannot list resource "pipelines" at the cluster scope`)
					}
					return c.List(ctx, list, opts...)
				}})
			}
			r := checkPolicyNamespaces(context.Background(), b.Build(), "team-b", tc.controller)
			assert.Equal(t, "Policy namespaces shared", r.label)
			assert.Equal(t, tc.icon, r.icon, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			assert.Equal(t, tc.icon == doctorFail, r.failed)
			assert.Equal(t, tc.icon == doctorWarn, r.warned)
			if tc.icon != doctorPass {
				assert.NotEmpty(t, r.hint)
			}
		})
	}
}
