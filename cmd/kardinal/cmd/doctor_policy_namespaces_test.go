// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
		"a later dev build warns":  {estate, "v0.15.0-dev", true, doctorWarn, "v0.14.0 refuses them"},
		// A release candidate is its release; a git-describe version is at
		// least its base (QA-5 on #2365).
		"a v0.13 release candidate checks":  {estate, "v0.13.0-rc.1", true, doctorWarn, "v0.14.0 refuses them: team-b/web"},
		"a v0.14 release candidate refuses": {estate, "v0.14.0-rc.1", true, doctorFail, "refused, their new versions wait"},
		"commits after v0.13 check":         {estate, "v0.13.0-5-gabc1234", true, doctorWarn, "v0.14.0 refuses them"},
		"commits after v0.14 refuse":        {estate, "v0.14.0-2-g1234abc", true, doctorFail, "refused, their new versions wait"},
		"commits after v0.12 do not check":  {estate, "v0.12.3-7-gabc1234", true, doctorWarn, "does not check spec.policyNamespaces consent"},
		"a v0.13 pseudo-version is unknown": {estate, "v0.13.1-0.20261020120000-abcdefabcdef", true, doctorWarn, "v0.14.0 refuses them"},
		"none affected": {clean, "v0.13.0", true,
			doctorPass, "every spec.policyNamespaces entry agreed (2 Pipelines, all namespaces, as the controller last checked)"},
		"a controller before v0.13 does not check": {estate, "v0.12.3", true, doctorWarn,
			"controller v0.12.3 does not check spec.policyNamespaces consent"},
		"only the current namespace": {estate, "v0.13.0", false, doctorWarn, "team-b/web (team-a) (namespace team-b)"},
		"a clean namespace only is not a PASS": {clean, "v0.13.0", false, doctorWarn,
			"checked namespace team-b only: every spec.policyNamespaces entry agreed (0 Pipelines, namespace team-b"},
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
			r := checkPolicyNamespaces(context.Background(), b.Build(), "team-b", tc.controller, "")
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

// TestDoctor_PolicyNamespacesUpgradeInProgress (#2383): right after an
// upgrade the new controller Pod is Ready before it holds the Lease, and
// only the leader records its version, so doctor can read an old leader's
// version. While the controller Deployment rolls out a later release or a
// build that is not a release, that is an upgrade in progress (INFO, run
// doctor again), not a controller that does not check; with the Deployment
// on the leader's release or an earlier one, or not known, the WARN stays.
// A leader from v0.13 is checked as before whatever the Deployment runs.
//
// Covers GATE-POLNS-04.
func TestDoctor_PolicyNamespacesUpgradeInProgress(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		leader, deployed string
		icon, detail     string
	}{
		"a later release rolls out": {"v0.12.0", "v0.13.0", doctorInfo,
			"upgrade in progress: the leader still runs v0.12.0, the Deployment runs v0.13.0"},
		"a tag without v":           {"v0.12.0", "0.13.0", doctorInfo, "the Deployment runs 0.13.0"},
		"a later release candidate": {"v0.12.3", "v0.13.0-rc.1", doctorInfo, "the Deployment runs v0.13.0-rc.1"},
		"a build that is not a release": {"v0.12.0", "e2e-kind", doctorInfo,
			"upgrade in progress: the leader still runs v0.12.0, the Deployment runs e2e-kind"},
		"the leader's own release":     {"v0.12.0", "v0.12.0", doctorWarn, "controller v0.12.0 does not check spec.policyNamespaces consent"},
		"the same release, no v":       {"v0.12.0", "0.12.0", doctorWarn, "does not check"},
		"an earlier release":           {"v0.12.3", "v0.12.1", doctorWarn, "controller v0.12.3 does not check"},
		"the Deployment not known":     {"v0.12.0", "", doctorWarn, "controller v0.12.0 does not check"},
		"a v0.13 leader is checked":    {"v0.13.0", "v0.14.0", doctorPass, "every spec.policyNamespaces entry agreed"},
		"an unknown leader is checked": {"unknown", "v0.13.0", doctorPass, "every spec.policyNamespaces entry agreed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fake.NewClientBuilder().WithScheme(rootScheme).Build()
			r := checkPolicyNamespaces(context.Background(), c, "team-b", tc.leader, tc.deployed)
			assert.Equal(t, tc.icon, r.icon, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			assert.Equal(t, tc.icon == doctorInfo, r.noted)
			assert.Equal(t, tc.icon == doctorWarn, r.warned)
			assert.False(t, r.failed)
			if tc.icon == doctorInfo {
				assert.Equal(t, "The new controller takes over a few seconds after its Pod is Ready. Run kardinal doctor again "+
					"once kardinal version no longer shows Controller: "+tc.leader, r.hint)
			}
		})
	}
}

// TestDoctor_DeployedVersion: what the controller Deployment runs is its
// controller container's image tag, or, for an image by digest, its
// app.kubernetes.io/version label; nothing when there is no Deployment.
func TestDoctor_DeployedVersion(t *testing.T) {
	t.Parallel()
	dep := func(labels map[string]string, containers ...corev1.Container) *appsv1.Deployment {
		l := map[string]string{"app.kubernetes.io/name": "kardinal-promoter"}
		for k, v := range labels {
			l[k] = v
		}
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter", Namespace: "kardinal-system", Labels: l},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}}}}
	}
	for name, tc := range map[string]struct {
		objs []sigs_client.Object
		want string
	}{
		"a tag":           {[]sigs_client.Object{dep(nil, corev1.Container{Name: "controller", Image: "ghcr.io/pnz1990/kardinal-promoter/controller:v0.13.0"})}, "v0.13.0"},
		"a registry port": {[]sigs_client.Object{dep(nil, corev1.Container{Name: "controller", Image: "registry:5000/kp/controller:e2e-kind"})}, "e2e-kind"},
		"the controller container": {[]sigs_client.Object{dep(nil, corev1.Container{Name: "sidecar", Image: "proxy:v9"},
			corev1.Container{Name: "controller", Image: "kp/controller:v0.13.1"})}, "v0.13.1"},
		"by digest, the chart's appVersion": {[]sigs_client.Object{dep(map[string]string{"app.kubernetes.io/version": "v0.13.0"},
			corev1.Container{Name: "controller", Image: "kp/controller@sha256:abc"})}, "v0.13.0"},
		"by digest without the label": {[]sigs_client.Object{dep(nil, corev1.Container{Name: "controller", Image: "kp/controller@sha256:abc"})}, ""},
		"no tag":                      {[]sigs_client.Object{dep(nil, corev1.Container{Name: "controller", Image: "registry:5000/kp/controller"})}, ""},
		"no Deployment":               {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs...).Build()
			assert.Equal(t, tc.want, deployedVersion(context.Background(), c, "kardinal-system"))
		})
	}
}

// TestDoctor_SummaryCountsToRunAgain: a check that cannot tell yet is
// counted apart, neither passed nor a warning, and does not fail doctor.
func TestDoctor_SummaryCountsToRunAgain(t *testing.T) {
	var out bytes.Buffer
	err := printDoctorResults(&out, []doctorResult{
		{icon: doctorPass, label: "Controller reachable", detail: "kardinal-promoter v0.12.0 in kardinal-system"},
		{icon: doctorInfo, label: "Policy namespaces shared", detail: "upgrade in progress", hint: "Run kardinal doctor again", noted: true},
	})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "INFO  Policy namespaces shared          upgrade in progress\n"+strings.Repeat(" ", 40)+"Run kardinal doctor again\n")
	assert.True(t, strings.HasSuffix(out.String(), "\n1 check(s) passed, 1 to run again\n"), out.String())
}
