// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
// who may not list Pipelines cluster-wide gets the current namespace's. A
// Pipeline naming spec.policyNamespaces with no condition for its current
// generation is not checked yet (QA-3 on #2397): an INFO to run doctor again
// when nothing is affected, listed beside the affected ones otherwise.
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
	// A Pipeline naming spec.policyNamespaces that this controller has not
	// reconciled yet (#2383, QA-3): no condition, or one of an earlier
	// generation.
	notYet := func(ns, name string, cond *metav1.Condition) *v1alpha1.Pipeline {
		p := pipeline(ns, name, cond)
		p.Generation = 2
		p.Spec.PolicyNamespaces = []string{"team-x"}
		return p
	}
	checked := func() *metav1.Condition {
		c := *shared
		c.ObservedGeneration = 2
		return &c
	}
	unchecked := func() []sigs_client.Object {
		old := *shared
		old.ObservedGeneration = 1
		return append(clean(), notYet("team-f", "fresh", nil), notYet("team-g", "edited", &old), notYet("team-h", "done", checked()))
	}
	mixed := func() []sigs_client.Object { return append(estate(), notYet("team-f", "fresh", nil)) }
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
		"not checked yet is not a PASS": {unchecked, "v0.13.0", true, doctorInfo,
			"not checked yet: team-f/fresh, team-g/edited (all namespaces): the controller has not checked their current spec.policyNamespaces"},
		"not checked yet beside affected ones": {mixed, "v0.13.0", true, doctorWarn,
			"v0.14.0 refuses them: team-b/web (team-a); team-c/api (team-a, team-d) (all namespaces); not checked yet: team-f/fresh"},
		"not checked yet beside refused ones": {mixed, "v0.14.0", true, doctorFail, "(all namespaces); not checked yet: team-f/fresh"},
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
			r := checkPolicyNamespaces(context.Background(), b.Build(), "team-b", tc.controller, controllerRollout{})
			assert.Equal(t, "Policy namespaces shared", r.label)
			assert.Equal(t, tc.icon, r.icon, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			assert.Equal(t, tc.icon == doctorFail, r.failed)
			assert.Equal(t, tc.icon == doctorWarn, r.warned)
			assert.Equal(t, tc.icon == doctorInfo, r.noted)
			if tc.icon != doctorPass {
				assert.NotEmpty(t, r.hint)
			}
			if tc.icon == doctorInfo {
				assert.Equal(t, "Run kardinal doctor again in a minute: the controller checks a Pipeline's spec.policyNamespaces "+
					"when it reconciles it", r.hint)
			}
		})
	}
}

// TestDoctor_PolicyNamespacesUpgradeInProgress (#2383): right after an
// upgrade the new controller Pod is Ready before it holds the Lease, and
// only the leader records its version, so doctor can read an old leader's
// version. While the controller Deployment is rolling out (or finished
// within rolloutGrace) a later release or a build that is not a release,
// that is an upgrade in progress (INFO, run doctor again), not a controller
// that does not check. Otherwise the WARN stays, naming what the Deployment
// runs when that differs (QA-5 on #2397): a tag that is not a release with no
// rollout (a mirror's latest) or a later release rolled out long ago is a
// WARN, not an INFO forever. A leader from v0.13 is checked as before.
//
// Covers GATE-POLNS-04.
func TestDoctor_PolicyNamespacesUpgradeInProgress(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		leader, deployed string
		rolling          bool
		icon, detail     string
	}{
		"a later release rolls out": {"v0.12.0", "v0.13.0", true, doctorInfo,
			"upgrade in progress: the leader still runs v0.12.0, the Deployment runs v0.13.0"},
		"a tag without v":           {"v0.12.0", "0.13.0", true, doctorInfo, "the Deployment runs 0.13.0"},
		"a later release candidate": {"v0.12.3", "v0.13.0-rc.1", true, doctorInfo, "the Deployment runs v0.13.0-rc.1"},
		"a build that is not a release rolls out": {"v0.12.0", "e2e-kind", true, doctorInfo,
			"upgrade in progress: the leader still runs v0.12.0, the Deployment runs e2e-kind"},
		"a tag that is not a release, no rollout": {"v0.12.3", "latest", false, doctorWarn,
			"controller v0.12.3 does not check spec.policyNamespaces consent (the Deployment runs latest)"},
		"the same tag mid-rollout": {"v0.12.3", "latest", true, doctorInfo,
			"upgrade in progress: the leader still runs v0.12.3, the Deployment runs latest"},
		"a later release rolled out long ago": {"v0.12.0", "v0.13.0", false, doctorWarn,
			"controller v0.12.0 does not check spec.policyNamespaces consent (the Deployment runs v0.13.0)"},
		"a stuck rollout of a later release": {"v0.12.3", "v0.13.0", false, doctorWarn,
			"controller v0.12.3 does not check spec.policyNamespaces consent (the Deployment runs v0.13.0; rollout stuck: ProgressDeadlineExceeded)"},
		"a stuck restart of the leader's release": {"v0.12.3", "v0.12.3", false, doctorWarn,
			"controller v0.12.3 does not check spec.policyNamespaces consent (rollout stuck: ProgressDeadlineExceeded)"},
		"the leader's own release, rolling (a restart)": {"v0.12.0", "v0.12.0", true, doctorWarn,
			"controller v0.12.0 does not check spec.policyNamespaces consent"},
		"the same release, no v":       {"v0.12.0", "0.12.0", true, doctorWarn, "does not check"},
		"an earlier release":           {"v0.12.3", "v0.12.1", true, doctorWarn, "controller v0.12.3 does not check"},
		"the Deployment not known":     {"v0.12.0", "", true, doctorWarn, "controller v0.12.0 does not check"},
		"a v0.13 leader is checked":    {"v0.13.0", "v0.14.0", true, doctorPass, "every spec.policyNamespaces entry agreed"},
		"an unknown leader is checked": {"unknown", "v0.13.0", true, doctorPass, "every spec.policyNamespaces entry agreed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fake.NewClientBuilder().WithScheme(rootScheme).Build()
			dep := controllerRollout{version: tc.deployed, rolling: tc.rolling}
			if strings.Contains(name, "stuck") {
				dep.stuck = "ProgressDeadlineExceeded"
			}
			r := checkPolicyNamespaces(context.Background(), c, "team-b", tc.leader, dep)
			assert.Equal(t, tc.icon, r.icon, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			assert.Equal(t, tc.icon == doctorInfo, r.noted)
			assert.Equal(t, tc.icon == doctorWarn, r.warned)
			assert.False(t, r.failed)
			if tc.icon == doctorWarn && (tc.deployed == "" || releaseVersion(tc.deployed) == releaseVersion(tc.leader)) {
				assert.NotContains(t, r.detail, "the Deployment runs", "only a Deployment that differs is named")
			}
			if !strings.Contains(name, "stuck") {
				assert.NotContains(t, r.detail, "rollout stuck")
			}
			if tc.icon == doctorInfo {
				assert.Equal(t, "The new controller takes over a few seconds after its Pod is Ready. Run kardinal doctor again "+
					"once kardinal version no longer shows Controller: "+tc.leader, r.hint)
			}
		})
	}
}

// rolloutDeployment is a settled controller Deployment of image at now:
// its generation observed, one replica updated and available, Progressing
// last updated an hour before.
func rolloutDeployment(image string, now time.Time) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter", Namespace: "kardinal-system", Generation: 4,
		Labels: map[string]string{"app.kubernetes.io/name": "kardinal-promoter"}},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "controller", Image: image}}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 4, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
			AvailableReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing,
				Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", LastUpdateTime: metav1.NewTime(now.Add(-time.Hour))}}}}
}

// stuckRollout is QA-5's first probe: a rollout of v0.13.0 whose new Pod
// crash-loops, Progressing=False ProgressDeadlineExceeded an hour ago, one
// of two replicas updated, one unavailable.
func stuckRollout(now time.Time) *appsv1.Deployment {
	d := rolloutDeployment("ghcr.io/pnz1990/kardinal-promoter/controller:v0.13.0", now)
	*d.Spec.Replicas = 2
	d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas, d.Status.UnavailableReplicas = 2, 1, 1, 1
	d.Status.Conditions[0] = appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
		Reason: "ProgressDeadlineExceeded", LastUpdateTime: metav1.NewTime(now.Add(-time.Hour))}
	return d
}

// pendingReplica is QA-5's second probe: latest, no rollout for 30 days,
// one replica Pending.
func pendingReplica(now time.Time) *appsv1.Deployment {
	d := rolloutDeployment("mirror.example/kardinal-promoter/controller:latest", now)
	*d.Spec.Replicas = 2
	d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.UnavailableReplicas = 2, 2, 1
	d.Status.Conditions[0].LastUpdateTime = metav1.NewTime(now.Add(-30 * 24 * time.Hour))
	return d
}

// TestDoctor_PolicyNamespacesStuckRollout: QA-5's two probes on #2397, from
// the Deployment to the check, against a v0.12.3 leader: a stuck rollout of
// v0.13.0 and a mirror's latest short of a replica are the WARN, naming the
// Deployment's version and, when stuck, the reason, never an INFO forever.
//
// Covers GATE-POLNS-04.
func TestDoctor_PolicyNamespacesStuckRollout(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		dep    *appsv1.Deployment
		detail string
	}{
		"a stuck v0.13.0 rollout": {stuckRollout(now),
			"controller v0.12.3 does not check spec.policyNamespaces consent (the Deployment runs v0.13.0; rollout stuck: ProgressDeadlineExceeded)"},
		"latest with a Pending replica": {pendingReplica(now),
			"controller v0.12.3 does not check spec.policyNamespaces consent (the Deployment runs latest)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.dep).Build()
			r := checkPolicyNamespaces(context.Background(), c, "team-b", "v0.12.3", readControllerRollout(context.Background(), c, "kardinal-system", now))
			assert.Equal(t, doctorWarn, r.icon, r.detail)
			assert.Equal(t, tc.detail, r.detail)
			assert.False(t, r.noted)
		})
	}
}

// TestDoctor_ControllerRollout: what doctor reads of the controller
// Deployment. The version is its controller container's image tag, or, for
// an image by digest, its app.kubernetes.io/version label. It is rolling
// out while its generation is not observed yet, or while its Progressing
// condition is True and was last updated within rolloutGrace (a rollout
// moves it on every step and at completion). A stuck rollout
// (Progressing=False, its reason kept) and a Deployment short of a replica
// long after its last rollout are not (QA-5 on #2397); no Deployment reads
// as nothing.
func TestDoctor_ControllerRollout(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	dep := func(image string, edit ...func(*appsv1.Deployment)) *appsv1.Deployment {
		d := rolloutDeployment(image, now)
		for _, f := range edit {
			f(d)
		}
		return d
	}
	for name, tc := range map[string]struct {
		objs []sigs_client.Object
		want controllerRollout
	}{
		"a tag, settled long ago": {[]sigs_client.Object{dep("ghcr.io/pnz1990/kardinal-promoter/controller:v0.13.0")},
			controllerRollout{version: "v0.13.0"}},
		"a registry port": {[]sigs_client.Object{dep("registry:5000/kp/controller:e2e-kind")}, controllerRollout{version: "e2e-kind"}},
		"the controller container": {[]sigs_client.Object{dep("kp/controller:v0.13.1", func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers = append([]corev1.Container{{Name: "sidecar", Image: "proxy:v9"}}, d.Spec.Template.Spec.Containers...)
		})}, controllerRollout{version: "v0.13.1"}},
		"by digest, the chart's appVersion": {[]sigs_client.Object{dep("kp/controller@sha256:abc", func(d *appsv1.Deployment) {
			d.Labels["app.kubernetes.io/version"] = "v0.13.0"
		})}, controllerRollout{version: "v0.13.0"}},
		"by digest without the label": {[]sigs_client.Object{dep("kp/controller@sha256:abc")}, controllerRollout{}},
		"no tag":                      {[]sigs_client.Object{dep("registry:5000/kp/controller")}, controllerRollout{}},
		"a generation not observed yet": {[]sigs_client.Object{dep("kp/controller:latest", func(d *appsv1.Deployment) {
			d.Generation = 5
		})}, controllerRollout{version: "latest", rolling: true}},
		"mid-rollout, progressed a minute ago": {[]sigs_client.Object{dep("kp/controller:latest", func(d *appsv1.Deployment) {
			*d.Spec.Replicas = 2
			d.Status.Conditions[0].Reason = "ReplicaSetUpdated"
			d.Status.Conditions[0].LastUpdateTime = metav1.NewTime(now.Add(-time.Minute))
		})}, controllerRollout{version: "latest", rolling: true}},
		"finished a minute ago": {[]sigs_client.Object{dep("kp/controller:latest", func(d *appsv1.Deployment) {
			d.Status.Conditions[0].LastUpdateTime = metav1.NewTime(now.Add(-time.Minute))
		})}, controllerRollout{version: "latest", rolling: true}},
		"finished just past the grace": {[]sigs_client.Object{dep("kp/controller:latest", func(d *appsv1.Deployment) {
			d.Status.Conditions[0].LastUpdateTime = metav1.NewTime(now.Add(-rolloutGrace - time.Second))
		})}, controllerRollout{version: "latest"}},
		// QA-5's probes on #2397: neither is rolling, however long it lasts.
		"a stuck v0.13.0 rollout":       {[]sigs_client.Object{stuckRollout(now)}, controllerRollout{version: "v0.13.0", stuck: "ProgressDeadlineExceeded"}},
		"latest with a Pending replica": {[]sigs_client.Object{pendingReplica(now)}, controllerRollout{version: "latest"}},
		"stuck a minute ago": {[]sigs_client.Object{dep("kp/controller:v0.13.0", func(d *appsv1.Deployment) {
			d.Status.Conditions[0] = appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded", LastUpdateTime: metav1.NewTime(now.Add(-time.Minute))}
		})}, controllerRollout{version: "v0.13.0", stuck: "ProgressDeadlineExceeded"}},
		"no Deployment": {nil, controllerRollout{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs...).Build()
			assert.Equal(t, tc.want, readControllerRollout(context.Background(), c, "kardinal-system", now))
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
