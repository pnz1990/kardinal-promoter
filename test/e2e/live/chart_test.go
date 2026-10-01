//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The chart suite installs the chart from the checkout, one release per test,
// with the values each row claims, and checks the controller runs with them.
//
// A release without controller.watchNamespace (cluster mode) reconciles
// every namespace, so it would act on the Pipelines of every other test.
// Tests that install one hold clusterWide exclusively; tests whose releases
// watch only their own namespace share it.
var clusterWide sync.RWMutex

// namespaceScoped marks a test whose releases all watch only their own
// namespace. Call it right after t.Parallel, before creating namespaces, so
// the lock is released after every cleanup.
func namespaceScoped(t *testing.T) {
	clusterWide.RLock()
	t.Cleanup(clusterWide.RUnlock)
}

// clusterScoped marks a test that installs a cluster-mode release: it runs
// alone. Call it like namespaceScoped.
func clusterScoped(t *testing.T) {
	clusterWide.Lock()
	t.Cleanup(clusterWide.Unlock)
}

// releaseName is a short release name unique to namespace ns.
func releaseName(ns string) string { return "kp-" + ns[len(ns)-8:] }

// nsValues are values for a release that watches only ns (namespace mode),
// with over merged in. The release must be installed into ns.
func nsValues(ns string, over framework.Values) framework.Values {
	return framework.MergeValues(framework.Values{"controller": framework.Values{"watchNamespace": ns}}, over)
}

// resourcePipeline is a.pipeline with resource health on each env's
// Deployment in the app namespace. A namespace-mode controller caches only
// its watch namespace, so it cannot read Argo CD Applications in argocd.
func (a *app) resourcePipeline(approval map[string]string) *v1alpha1.Pipeline {
	p := a.pipeline(approval)
	for i := range p.Spec.Environments {
		env := p.Spec.Environments[i].Name
		p.Spec.Environments[i].Health = v1alpha1.HealthConfig{
			Type:    "resource",
			Timeout: "3m",
			Resource: &v1alpha1.ResourceRef{
				Kind: "Deployment", Name: fixtures.Workload(env), Namespace: a.ns,
			},
		}
	}
	return p
}

// promote creates a Bundle for tag and waits until it is Verified in every
// env of a, merging the promotion PR of each env in prEnvs. It returns the
// Bundle name.
func promote(t *testing.T, a *app, tag string, prEnvs map[string]bool, args ...string) string {
	t.Helper()
	e := a.e
	image := fixtures.Image + ":" + tag
	bundle := e.CreateBundle(t, a.ns, pipelineName, append([]string{"--image", image}, args...)...)
	for _, env := range a.envs {
		if prEnvs[env] {
			e.WaitStepState(t, a.ns, pipelineName, bundle, env, "WaitingForMerge", promoteTimeout)
			pr := e.WaitPR(t, a.repo, time.Minute, env+" promotion PR for "+tag, func(pr gitserver.PR) bool {
				return pr.State == "open" && strings.Contains(pr.Body, tag)
			})
			require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
		}
		e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		e.WaitDeploymentImage(t, a.ns, fixtures.Workload(env), image, syncTimeout)
	}
	return bundle
}

// checkAccess asserts, with SubjectAccessReviews, that user may make every
// request in allowed and none in denied.
func checkAccess(t *testing.T, e *framework.Env, user string, allowed, denied []framework.Access) {
	t.Helper()
	for _, a := range allowed {
		assert.True(t, e.Can(t, user, a), "%s may %s", user, a)
	}
	for _, a := range denied {
		assert.False(t, e.Can(t, user, a), "%s may not %s", user, a)
	}
}

// logLevels returns the "level" of every JSON log line in logs, counted.
func logLevels(logs string) map[string]int {
	out := map[string]int{}
	for _, line := range strings.Split(logs, "\n") {
		var l struct {
			Level string `json:"level"`
		}
		if json.Unmarshal([]byte(line), &l) == nil && l.Level != "" {
			out[l.Level]++
		}
	}
	return out
}

// podWaiting returns the controller container's waiting reason in the
// release's Pods ("" when none waits).
func podWaiting(ctx context.Context, e *framework.Env, ns string, r *framework.Release) (string, string) {
	var pods corev1.PodList
	if err := e.Client.List(ctx, &pods, client.InNamespace(ns),
		client.MatchingLabelsSelector{Selector: r.Selector()}); err != nil {
		return "", err.Error()
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == "controller" && cs.State.Waiting != nil {
				return cs.State.Waiting.Reason, cs.State.Waiting.Message
			}
		}
	}
	return "", fmt.Sprintf("%d pods, none waiting", len(pods.Items))
}

// runningPod waits for one Running, ready controller Pod of r and returns it.
func runningPod(t *testing.T, r *framework.Release) corev1.Pod {
	t.Helper()
	r.WaitRolledOut(t, 3*time.Minute)
	for _, p := range r.Pods(t) {
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning {
			return p
		}
	}
	t.Fatalf("release %s has no running Pod", r.Name)
	return corev1.Pod{}
}

// TestChart_DefaultInstall installs the chart the way the installation guide
// does (release kardinal-promoter in kardinal-system, cluster mode) and runs
// a gated promotion through it. The Graph identity the controller provisions
// lets kro create each node kind, and every grant the controller needs is
// there while the ones it does not need are absent.
//
// Covers CHART-DEFAULT-01, GRAPH-RBAC-01, CHART-RBAC-01.
func TestChart_DefaultInstall(t *testing.T) {
	t.Parallel()
	clusterScoped(t)
	e := framework.New(t)
	ctx := context.Background()

	// The version ConfigMap is the controller's own; a leftover from an
	// earlier release would hide whether this one writes it.
	version := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: framework.ControllerNamespace, Name: "kardinal-version"}}
	if err := e.Client.Delete(ctx, version); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete leftover kardinal-version: %v", err)
	}

	a := newArgoApp(t, e, "test", "prod")
	r := e.InstallChart(t, "kardinal-promoter", framework.ControllerNamespace, framework.Values{"logLevel": "info"})
	r.CleansUp(a.ns)
	require.Equal(t, "kardinal-promoter", r.Fullname)

	d := r.Deployment(t)
	assert.Equal(t, int32(1), d.Status.ReadyReplicas, "the controller Deployment is Ready")
	assert.Equal(t, r.Values["image"].(framework.Values)["repository"].(string)+":"+
		r.Values["image"].(framework.Values)["tag"].(string), d.Spec.Template.Spec.Containers[0].Image)
	framework.Eventually(t, time.Minute, "controller writes the kardinal-version ConfigMap", func(ctx context.Context) (bool, string) {
		var cm corev1.ConfigMap
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(version), &cm); err != nil {
			return false, err.Error()
		}
		return len(cm.Data) > 0, fmt.Sprint(cm.Data)
	})
	out := e.MustKardinal(t, "", "version")
	assert.Contains(t, out, "Controller: ")
	assert.NotContains(t, out, "Controller: unknown", "kardinal version reads the running controller's version")

	// A team gate on prod, both ways: an author it refuses, then one it allows.
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "author-check", Namespace: a.ns, Labels: map[string]string{
			"kardinal.io/scope": "team", "kardinal.io/applies-to": "prod", "kardinal.io/type": "gate",
		}},
		Spec: v1alpha1.PolicyGateSpec{
			Expression:      `bundle.provenance.author != "blocked-author"`,
			Message:         "blocked-author may not deploy to prod",
			RecheckInterval: "10s",
		},
	}
	require.NoError(t, e.Client.Create(ctx, gate))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	blocked := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--author", "blocked-author")
	e.WaitStepState(t, a.ns, pipelineName, blocked, "test", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V2, syncTimeout)
	gateInstance := func(ctx context.Context, bundle string) (*v1alpha1.PolicyGate, string) {
		var list v1alpha1.PolicyGateList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns), client.MatchingLabels{
			"kardinal.io/bundle": bundle, "kardinal.io/gate-template": gate.Name,
		}); err != nil {
			return nil, err.Error()
		}
		if len(list.Items) != 1 {
			return nil, fmt.Sprintf("%d gate instances", len(list.Items))
		}
		return &list.Items[0], ""
	}
	framework.Eventually(t, time.Minute, "the gate refuses blocked-author", func(ctx context.Context) (bool, string) {
		g, why := gateInstance(ctx, blocked)
		if g == nil {
			return false, why
		}
		return g.Status.LastEvaluatedAt != nil && !g.Status.Ready, fmt.Sprintf("ready=%v reason=%q", g.Status.Ready, g.Status.Reason)
	})
	framework.Consistently(t, 20*time.Second, "the refused Bundle does not reach prod", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, a.ns, pipelineName, blocked, "prod")
		if err != nil || ok {
			return false, fmt.Sprintf("prod step exists (%v): %+v", err, ps)
		}
		prs, err := e.Git.PullRequests(ctx, a.repo)
		if err != nil || len(prs) != 0 {
			return false, fmt.Sprintf("PRs: %v %v", prs, err)
		}
		return true, ""
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	allowed := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3, "--author", "e2e-bot")
	e.WaitStepState(t, a.ns, pipelineName, allowed, "test", "Verified", promoteTimeout)
	framework.Eventually(t, time.Minute, "the gate passes e2e-bot", func(ctx context.Context) (bool, string) {
		g, why := gateInstance(ctx, allowed)
		if g == nil {
			return false, why
		}
		return g.Status.Ready, fmt.Sprintf("ready=%v reason=%q", g.Status.Ready, g.Status.Reason)
	})
	e.WaitStepState(t, a.ns, pipelineName, allowed, "prod", "WaitingForMerge", promoteTimeout)

	// kro applied the Graph as the Graph identity: every node kind exists.
	graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kardinal.io/pipeline=" + pipelineName + ",kardinal.io/bundle=" + allowed,
	})
	require.NoError(t, err)
	require.Len(t, graphs.Items, 1, "one Graph per Bundle")
	sa, _, _ := unstructuredString(graphs.Items[0].Object, "spec", "serviceAccountName")
	assert.Equal(t, "kardinal-graph", sa, "kro applies the Graph as the Graph ServiceAccount")
	children := func(list client.ObjectList) int {
		require.NoError(t, e.Client.List(ctx, list, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": allowed}))
		switch l := list.(type) {
		case *v1alpha1.PromotionStepList:
			return len(l.Items)
		case *v1alpha1.PolicyGateList:
			return len(l.Items)
		case *v1alpha1.PRStatusList:
			return len(l.Items)
		}
		return -1
	}
	assert.Equal(t, 2, children(&v1alpha1.PromotionStepList{}), "kro created a PromotionStep per environment")
	assert.Equal(t, 1, children(&v1alpha1.PolicyGateList{}), "kro created the gate instance")
	assert.Equal(t, 2, children(&v1alpha1.PRStatusList{}), "kro created a PRStatus per environment")

	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.Contains(pr.Body, fixtures.V3)
	})
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, allowed, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V3, syncTimeout)

	// The Graph identity: a ServiceAccount and the applier binding in the
	// Graph namespace, the reader binding where the health refs point.
	var graphSA corev1.ServiceAccount
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "kardinal-graph"}, &graphSA))
	applier, err := e.Kube.RbacV1().RoleBindings(a.ns).Get(ctx, "kardinal-promoter-graph-applier", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "kardinal-promoter", applier.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kardinal-promoter-graph-applier"}, applier.RoleRef)
	reader, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, "kardinal-promoter-graph-reader-"+a.ns, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "kardinal-promoter-graph-reader", reader.RoleRef.Name)

	other := framework.ControllerNamespace
	controller := framework.ServiceAccountUser(framework.ControllerNamespace, r.Fullname)
	checkAccess(t, e, controller, []framework.Access{
		{Verb: "list", Group: "kardinal.io", Resource: "pipelines"},
		{Verb: "update", Group: "kardinal.io", Resource: "bundles", Subresource: "status", Namespace: a.ns},
		{Verb: "create", Group: "kro.run", Resource: "graphs", Namespace: a.ns},
		{Verb: "get", Resource: "secrets", Namespace: a.ns, Name: framework.GitSecretName},
		{Verb: "create", Resource: "events", Namespace: a.ns},
		{Verb: "list", Group: "apps", Resource: "deployments"},
		{Verb: "get", Group: "argoproj.io", Resource: "applications", Namespace: framework.ArgoCDNamespace},
		{Verb: "create", Group: "coordination.k8s.io", Resource: "leases", Namespace: framework.ControllerNamespace},
		{Verb: "create", Resource: "serviceaccounts", Namespace: a.ns},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: a.ns},
		{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "kardinal-promoter-graph-applier"},
	}, []framework.Access{
		{Verb: "list", Resource: "secrets"},
		{Verb: "watch", Resource: "secrets", Namespace: a.ns},
		{Verb: "create", Group: "apps", Resource: "deployments", Namespace: a.ns},
		{Verb: "patch", Group: "apps", Resource: "deployments", Namespace: a.ns},
		{Verb: "patch", Group: "argoproj.io", Resource: "applications", Namespace: framework.ArgoCDNamespace},
		{Verb: "create", Group: "batch", Resource: "jobs", Namespace: a.ns},
		{Verb: "list", Resource: "configmaps", Namespace: a.ns},
		{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "cluster-admin"},
		{Verb: "escalate", Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"},
		{Verb: "delete", Resource: "namespaces", Name: a.ns},
	})
	graphUser := framework.ServiceAccountUser(a.ns, "kardinal-graph")
	checkAccess(t, e, graphUser, []framework.Access{
		{Verb: "create", Group: "kardinal.io", Resource: "promotionsteps", Namespace: a.ns},
		{Verb: "create", Group: "kardinal.io", Resource: "policygates", Namespace: a.ns},
		{Verb: "create", Group: "kardinal.io", Resource: "prstatuses", Namespace: a.ns},
		{Verb: "get", Group: "kardinal.io", Resource: "bundles", Namespace: a.ns},
		{Verb: "get", Group: "argoproj.io", Resource: "applications", Namespace: framework.ArgoCDNamespace},
	}, []framework.Access{
		{Verb: "create", Group: "kardinal.io", Resource: "promotionsteps", Namespace: other},
		{Verb: "update", Group: "kardinal.io", Resource: "bundles", Namespace: a.ns},
		{Verb: "get", Resource: "secrets", Namespace: a.ns},
		{Verb: "patch", Group: "apps", Resource: "deployments", Namespace: a.ns},
	})
	checkAccess(t, e, framework.ServiceAccountUser("kro-system", "kro"), []framework.Access{
		{Verb: "watch", Group: "kardinal.io", Resource: "promotionsteps"},
		{Verb: "watch", Group: "kardinal.io", Resource: "policygates"},
		{Verb: "watch", Group: "kardinal.io", Resource: "prstatuses"},
	}, nil)

	logs := r.AllLogs(t)
	assert.Contains(t, logs, "starting kardinal-controller")
	for _, line := range strings.Split(logs, "\n") {
		assert.NotContains(t, strings.ToLower(line), "forbidden", "the controller was refused an API call")
	}
}

// unstructuredString reads a string field of an unstructured object.
func unstructuredString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	var cur interface{} = obj
	for _, f := range fields {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur, ok = m[f]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

// TestChart_Image checks image.repository, image.tag and image.pullPolicy
// reach the controller Pod: the checkout's image runs from the node, Never
// refuses a tag the node lacks, a repository nobody serves fails to pull, and
// an empty tag means the chart's appVersion.
//
// Covers CHART-IMAGE-01.
func TestChart_Image(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)

	ns := e.Namespace(t)
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, nil))
	pod := runningPod(t, r)
	c := pod.Spec.Containers[0]
	img := r.Values["image"].(framework.Values)
	assert.Equal(t, img["repository"].(string)+":"+img["tag"].(string), c.Image)
	assert.Equal(t, corev1.PullNever, c.ImagePullPolicy)

	missing := e.Namespace(t)
	rm, out, err := e.TryInstallChart(t, releaseName(missing), missing, nsValues(missing, framework.Values{
		"image": framework.Values{"tag": "e2e-not-on-node"},
	}), false)
	require.NoError(t, err, out)
	framework.Eventually(t, 2*time.Minute, "Never does not pull a tag the node lacks", func(ctx context.Context) (bool, string) {
		reason, msg := podWaiting(ctx, e, missing, rm)
		return reason == "ErrImageNeverPull", reason + ": " + msg
	})

	unserved := e.Namespace(t)
	ru, out, err := e.TryInstallChart(t, releaseName(unserved), unserved, nsValues(unserved, framework.Values{
		"image": framework.Values{"repository": "registry.invalid/kardinal/controller", "tag": "", "pullPolicy": "IfNotPresent"},
	}), false)
	require.NoError(t, err, out)
	d := ru.Deployment(t)
	assert.Equal(t, "registry.invalid/kardinal/controller:"+chartAppVersion(t, e), d.Spec.Template.Spec.Containers[0].Image,
		"an empty tag is the chart's appVersion")
	assert.Equal(t, corev1.PullIfNotPresent, d.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	framework.Eventually(t, 3*time.Minute, "the node tries to pull the repository", func(ctx context.Context) (bool, string) {
		reason, msg := podWaiting(ctx, e, unserved, ru)
		return (reason == "ErrImagePull" || reason == "ImagePullBackOff") && strings.Contains(msg, "registry.invalid"), reason + ": " + msg
	})
}

// chartAppVersion is the appVersion of the chart under test.
func chartAppVersion(t *testing.T, e *framework.Env) string {
	t.Helper()
	out, err := e.Helm(t, "show", "chart", chartPath(t))
	require.NoError(t, err, out)
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "appVersion:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	t.Fatalf("no appVersion in helm show chart:\n%s", out)
	return ""
}

// chartPath is the chart directory under test.
func chartPath(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(framework.EnvChart)
	require.NotEmpty(t, dir, "%s is not set; run hack/e2e/up.sh chart", framework.EnvChart)
	return dir
}

// TestChart_TerminationGrace checks terminationGracePeriodSeconds reaches
// the controller Pod, including 0.
//
// Covers CHART-GRACE-01.
func TestChart_TerminationGrace(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ns := e.Namespace(t)

	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{"terminationGracePeriodSeconds": 30}))
	pod := runningPod(t, r)
	require.NotNil(t, pod.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, int64(30), *pod.Spec.TerminationGracePeriodSeconds, "the controller Pod's grace period")

	r.Upgrade(t, nsValues(ns, framework.Values{"terminationGracePeriodSeconds": 0}))
	pod = runningPod(t, r)
	require.NotNil(t, pod.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, int64(0), *pod.Spec.TerminationGracePeriodSeconds, "0 means no grace period, not the default")
}

// TestChart_LogLevel checks logLevel sets what the controller logs: debug
// lines only at debug, info lines at info but not at warn.
//
// Covers CHART-LOG-01.
func TestChart_LogLevel(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ns := e.Namespace(t)
	// A 5s clock writes a debug line every 5s ("scheduleclock tick").
	values := func(level string) framework.Values {
		return nsValues(ns, framework.Values{"logLevel": level, "scheduleClock": framework.Values{"interval": "5s"}})
	}

	r := e.InstallChart(t, releaseName(ns), ns, values("debug"))
	pod := runningPod(t, r)
	framework.Eventually(t, time.Minute, "debug lines at logLevel debug", func(context.Context) (bool, string) {
		levels := logLevels(r.Logs(t, pod.Name, false))
		return levels["debug"] > 0 && levels["info"] > 0, fmt.Sprint(levels)
	})

	r.Upgrade(t, values("info"))
	pod = runningPod(t, r)
	framework.Eventually(t, time.Minute, "info lines at logLevel info", func(context.Context) (bool, string) {
		levels := logLevels(r.Logs(t, pod.Name, false))
		return levels["info"] > 0, fmt.Sprint(levels)
	})
	framework.Consistently(t, 20*time.Second, "no debug lines at logLevel info", func(context.Context) (bool, string) {
		levels := logLevels(r.Logs(t, pod.Name, false))
		return levels["debug"] == 0, fmt.Sprint(levels)
	})

	r.Upgrade(t, values("warn"))
	pod = runningPod(t, r)
	framework.Consistently(t, 20*time.Second, "no info or debug lines at logLevel warn", func(context.Context) (bool, string) {
		levels := logLevels(r.Logs(t, pod.Name, false))
		return levels["debug"] == 0 && levels["info"] == 0, fmt.Sprint(levels)
	})
	d := r.Deployment(t)
	assert.Contains(t, d.Spec.Template.Spec.Containers[0].Args, "--zap-log-level=error",
		"controller-runtime has no warn level; the chart maps warn to error")
}

// TestChart_Schema checks the values schema rejects removed keys with an
// error that names them, before anything is applied.
//
// Covers CHART-SCHEMA-01.
func TestChart_Schema(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ns := e.Namespace(t)

	for key, values := range map[string]framework.Values{
		"shard":     {"controller": framework.Values{"shard": "shard-a"}},
		"krocodile": {"krocodile": framework.Values{"enabled": true}},
	} {
		_, out, err := e.TryInstallChart(t, releaseName(ns), ns, nsValues(ns, values), false)
		require.Error(t, err, "helm install with %s", key)
		assert.Contains(t, out, "values don't meet the specifications of the schema")
		assert.Contains(t, out, key, "the error names the removed key")
		_, serr := e.Helm(t, "status", releaseName(ns), "-n", ns)
		assert.Error(t, serr, "no release is created")
	}
	var deps appsv1.DeploymentList
	require.NoError(t, e.Client.List(context.Background(), &deps, client.InNamespace(ns)))
	assert.Empty(t, deps.Items, "nothing is applied")
}

// TestChart_ScheduleClock checks scheduleClock creates the kardinal-clock
// ScheduleClock the controller ticks at scheduleClock.interval, and that
// disabling it removes it.
//
// Covers CHART-CLOCK-01.
func TestChart_ScheduleClock(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ns := e.Namespace(t)
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{"scheduleClock": framework.Values{"interval": "10s"}}))

	key := types.NamespacedName{Namespace: ns, Name: "kardinal-clock"}
	var ticks []time.Time
	framework.Eventually(t, 90*time.Second, "kardinal-clock ticks three times", func(ctx context.Context) (bool, string) {
		var clock v1alpha1.ScheduleClock
		if err := e.Client.Get(ctx, key, &clock); err != nil {
			return false, err.Error()
		}
		if clock.Spec.Interval != "10s" {
			return false, "interval " + clock.Spec.Interval
		}
		tick, err := time.Parse(time.RFC3339, clock.Status.Tick)
		if err != nil {
			return false, "tick " + clock.Status.Tick
		}
		if len(ticks) == 0 || !tick.Equal(ticks[len(ticks)-1]) {
			ticks = append(ticks, tick)
		}
		return len(ticks) >= 4, fmt.Sprint(ticks)
	})
	// The first tick seen can be any age; the later ones are one interval apart.
	for i := 2; i < len(ticks); i++ {
		gap := ticks[i].Sub(ticks[i-1])
		assert.True(t, gap >= 9*time.Second && gap <= 13*time.Second, "tick %d came %s after the one before, want about 10s", i, gap)
	}

	r.Upgrade(t, nsValues(ns, framework.Values{"scheduleClock": framework.Values{"enabled": false}}))
	err := e.Client.Get(context.Background(), key, &v1alpha1.ScheduleClock{})
	assert.True(t, apierrors.IsNotFound(err), "scheduleClock.enabled=false removes kardinal-clock: %v", err)
}

// TestChart_DeprecatedValues checks the two deprecated values do nothing:
// setting them renders the same manifest, no ValidatingAdmissionPolicy is
// ever created, and the controller gets no Job permissions.
//
// Covers CHART-INTEGJOBS-01, CHART-VAP-01.
func TestChart_DeprecatedValues(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()

	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{
		"validatingAdmissionPolicy": framework.Values{"enabled": true},
		"rbac":                      framework.Values{"integrationTestJobs": false},
	}))
	before := r.Deployment(t).Generation
	r.Upgrade(t, nsValues(ns, framework.Values{
		"validatingAdmissionPolicy": framework.Values{"enabled": false},
		"rbac":                      framework.Values{"integrationTestJobs": true},
	}))
	assert.Equal(t, r.Manifest(t, 1), r.Manifest(t, 2), "the values change nothing the chart renders")
	assert.Equal(t, before, r.Deployment(t).Generation, "the controller is not restarted")
	runningPod(t, r)

	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + r.Name}
	vaps, err := e.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().List(ctx, sel)
	require.NoError(t, err)
	assert.Empty(t, vaps.Items, "validatingAdmissionPolicy.enabled creates no policy")
	bindings, err := e.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().List(ctx, sel)
	require.NoError(t, err)
	assert.Empty(t, bindings.Items)

	controller := framework.ServiceAccountUser(ns, r.Fullname)
	checkAccess(t, e, controller, nil, []framework.Access{
		{Verb: "create", Group: "batch", Resource: "jobs", Namespace: ns},
		{Verb: "get", Group: "batch", Resource: "jobs", Namespace: ns},
		{Verb: "delete", Group: "batch", Resource: "jobs", Namespace: ns},
	})
}

// secret creates Secret name in ns with data key=value.
func secret(t *testing.T, e *framework.Env, ns, name, key, value string) {
	t.Helper()
	require.NoError(t, e.Client.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data:       map[string][]byte{key: []byte(value)},
	}))
}

// serviceURL is the in-cluster URL of the release's Service on port.
func serviceURL(r *framework.Release, port int, path string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", r.Fullname, r.Namespace, port, path)
}

// leaseHolder is the Pod name holding the controller's leader Lease in ns.
func leaseHolder(ctx context.Context, e *framework.Env, ns string) (string, string) {
	l, err := e.Kube.CoordinationV1().Leases(ns).Get(ctx, "kardinal-promoter-leader", metav1.GetOptions{})
	if err != nil {
		return "", err.Error()
	}
	if l.Spec.HolderIdentity == nil {
		return "", "no holder"
	}
	pod, _, _ := strings.Cut(*l.Spec.HolderIdentity, "_")
	return pod, *l.Spec.HolderIdentity
}

// TestChart_HighAvailability runs two replicas with leader election, the
// PodDisruptionBudget and the topology spread: one replica leads and
// reconciles, the other takes over when the leader goes, and the budget
// refuses an eviction that would leave no replica.
//
// Covers CHART-HA-01.
func TestChart_HighAvailability(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	r := e.InstallChart(t, releaseName(a.ns), a.ns, nsValues(a.ns, framework.Values{"replicaCount": 2}))
	r.WaitRolledOut(t, 3*time.Minute)
	a.apply(t, a.resourcePipeline(nil))

	pods := r.Pods(t)
	require.Len(t, pods, 2)
	tsc := pods[0].Spec.TopologySpreadConstraints
	require.Len(t, tsc, 1, "topologySpread spreads the replicas")
	assert.Equal(t, "topology.kubernetes.io/zone", tsc[0].TopologyKey)
	assert.Equal(t, corev1.ScheduleAnyway, tsc[0].WhenUnsatisfiable)

	var leader string
	framework.Eventually(t, time.Minute, "one replica holds the leader Lease", func(ctx context.Context) (bool, string) {
		holder, why := leaseHolder(ctx, e, a.ns)
		leader = holder
		return holder == pods[0].Name || holder == pods[1].Name, why
	})
	follower := pods[0].Name
	if follower == leader {
		follower = pods[1].Name
	}
	framework.Eventually(t, time.Minute, "the leader starts its controllers", func(context.Context) (bool, string) {
		return strings.Contains(r.Logs(t, leader, false), "Starting workers"), "no Starting workers yet"
	})
	promote(t, a, fixtures.V2, nil)
	assert.NotContains(t, r.Logs(t, follower, false), "Starting workers", "the follower does not reconcile")

	require.NoError(t, e.Client.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: leader}}))
	// The follower or the leader's replacement takes over, whichever wins the
	// Lease first.
	var next string
	framework.Eventually(t, 2*time.Minute, "another replica takes the Lease", func(ctx context.Context) (bool, string) {
		holder, why := leaseHolder(ctx, e, a.ns)
		next = holder
		return holder != "" && holder != leader, why
	})
	framework.Eventually(t, time.Minute, "the new leader starts its controllers", func(context.Context) (bool, string) {
		return strings.Contains(r.Logs(t, next, false), "Starting workers"), "no Starting workers yet"
	})
	promote(t, a, fixtures.V3, nil)

	r.WaitRolledOut(t, 3*time.Minute)
	pdb, err := e.Kube.PolicyV1().PodDisruptionBudgets(a.ns).Get(ctx, r.Fullname, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, pdb.Spec.MinAvailable)
	assert.Equal(t, 1, pdb.Spec.MinAvailable.IntValue())
	framework.Eventually(t, time.Minute, "the budget allows one disruption", func(ctx context.Context) (bool, string) {
		p, err := e.Kube.PolicyV1().PodDisruptionBudgets(a.ns).Get(ctx, r.Fullname, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return p.Status.DisruptionsAllowed == 1 && p.Status.CurrentHealthy == 2, fmt.Sprintf("%+v", p.Status)
	})
	pods = r.Pods(t)
	require.Len(t, pods, 2)
	evict := func(name string) error {
		return e.Kube.CoreV1().Pods(a.ns).EvictV1(ctx, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: name}})
	}
	require.NoError(t, evict(pods[0].Name), "the budget allows evicting one of two replicas")
	err = evict(pods[1].Name)
	assert.True(t, apierrors.IsTooManyRequests(err), "the budget refuses evicting the last replica: %v", err)
}

// TestChart_Ports moves the metrics and health listeners
// (metricsBindAddress, healthProbeBindAddress) and every Service port: the
// probes still pass and each Service port reaches its listener.
//
// Covers CHART-PORTS-01.
func TestChart_Ports(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ns := e.Namespace(t)
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{
		"metricsBindAddress":     ":9100",
		"healthProbeBindAddress": ":9101",
		"service":                framework.Values{"metricsPort": 9090, "healthPort": 9091, "uiPort": 9092, "webhookPort": 9093},
	}))
	pod := runningPod(t, r)
	ports := map[string]int32{}
	for _, p := range pod.Spec.Containers[0].Ports {
		ports[p.Name] = p.ContainerPort
	}
	assert.Equal(t, map[string]int32{"metrics": 9100, "health": 9101, "ui": 9092, "webhook": 9093}, ports,
		"container ports are where the controller listens")

	probe := e.Probe(t, ns, "probe", nil)
	res := probe.Curl(t, 5*time.Second, serviceURL(r, 9090, "/metrics"))
	assert.Equal(t, 200, res.Code)
	assert.Contains(t, res.Body, "controller_runtime_reconcile_total", "metrics port serves the controller's metrics")
	res = probe.Curl(t, 5*time.Second, serviceURL(r, 9091, "/readyz"))
	assert.Equal(t, 200, res.Code, "health port serves the probes")
	res = probe.Curl(t, 5*time.Second, serviceURL(r, 9092, "/ui/"))
	assert.Equal(t, 200, res.Code, "ui port serves the UI")
	assert.Contains(t, res.Body, "<html")
	res = probe.Curl(t, 5*time.Second, "-X", "POST", "-d", "{}", serviceURL(r, 9093, "/webhook/scm"))
	assert.Equal(t, 401, res.Code)
	assert.Contains(t, res.Body, "webhook secret not configured", "webhook port serves the webhook")
}

// TestChart_WatchNamespace runs two namespace-scoped installs side by side,
// as the security guide's two-team example does: each promotes its own
// namespace through a Role, neither may touch the other's, and a namespace
// with no install is left alone.
//
// Covers CHART-WATCHNS-01.
func TestChart_WatchNamespace(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()

	teamA := newArgoApp(t, e, "test")
	teamB := newArgoApp(t, e, "test")
	ra := e.InstallChart(t, releaseName(teamA.ns), teamA.ns, nsValues(teamA.ns, nil))
	rb := e.InstallChart(t, releaseName(teamB.ns), teamB.ns, nsValues(teamB.ns, nil))
	for _, x := range []struct {
		a *app
		r *framework.Release
	}{{teamA, ra}, {teamB, rb}} {
		x.a.apply(t, x.a.resourcePipeline(nil))
		args := x.r.Deployment(t).Spec.Template.Spec.Containers[0].Args
		assert.Contains(t, args, "--watch-namespace="+x.a.ns)

		role, err := e.Kube.RbacV1().Roles(x.a.ns).Get(ctx, x.r.Fullname+"-manager-role", metav1.GetOptions{})
		require.NoError(t, err, "a Role in the watch namespace")
		assert.NotEmpty(t, role.Rules)
		binding, err := e.Kube.RbacV1().RoleBindings(x.a.ns).Get(ctx, x.r.Fullname+"-manager-rolebinding", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "Role", binding.RoleRef.Kind)
		_, err = e.Kube.RbacV1().ClusterRoles().Get(ctx, x.r.Fullname+"-manager-role", metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "no cluster-wide manager ClusterRole: %v", err)
		cs, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, x.r.Fullname+"-cluster-scoped", metav1.GetOptions{})
		require.NoError(t, err)
		for _, rule := range cs.Rules {
			for _, res := range rule.Resources {
				assert.Contains(t, []string{"changewindows", "changewindows/status"}, res,
					"the ClusterRole holds only cluster-scoped kinds")
			}
		}
	}

	none := e.Namespace(t)
	stray := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: none}, Spec: teamA.resourcePipeline(nil).Spec}
	require.NoError(t, e.Client.Create(ctx, stray))
	strayBundle := e.CreateBundle(t, none, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)

	promote(t, teamA, fixtures.V2, nil)
	promote(t, teamB, fixtures.V3, nil)

	controllerA := framework.ServiceAccountUser(teamA.ns, ra.Fullname)
	checkAccess(t, e, controllerA, []framework.Access{
		{Verb: "list", Group: "kardinal.io", Resource: "pipelines", Namespace: teamA.ns},
		{Verb: "create", Group: "kro.run", Resource: "graphs", Namespace: teamA.ns},
	}, []framework.Access{
		{Verb: "list", Group: "kardinal.io", Resource: "pipelines"},
		{Verb: "get", Group: "kardinal.io", Resource: "pipelines", Namespace: teamB.ns},
		{Verb: "update", Group: "kardinal.io", Resource: "bundles", Subresource: "status", Namespace: teamB.ns},
		{Verb: "create", Group: "kro.run", Resource: "graphs", Namespace: teamB.ns},
		{Verb: "get", Resource: "secrets", Namespace: teamB.ns},
		{Verb: "list", Group: "kardinal.io", Resource: "pipelines", Namespace: none},
	})

	framework.Consistently(t, 15*time.Second, "no install reconciles a namespace it does not watch", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: none, Name: strayBundle}, &b); err != nil {
			return false, err.Error()
		}
		graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(none).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		return b.Status.Phase == "" && len(graphs.Items) == 0, fmt.Sprintf("phase %q, %d Graphs", b.Status.Phase, len(graphs.Items))
	})
}

// TestChart_PolicyNamespaces checks controller.policyNamespaces: org gates
// in a listed namespace hold every Pipeline (both ways), org gates elsewhere
// are not applied, and a namespace-mode install refuses an entry outside its
// watch namespace.
//
// Covers CHART-POLNS-01.
func TestChart_PolicyNamespaces(t *testing.T) {
	t.Parallel()
	clusterScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	listed, unlisted := e.Namespace(t), e.Namespace(t)

	r := e.InstallChart(t, releaseName(a.ns), a.ns, framework.Values{
		"controller": framework.Values{"policyNamespaces": []string{listed}},
	})
	assert.Contains(t, r.Deployment(t).Spec.Template.Spec.Containers[0].Args, "--policy-namespaces="+listed)

	orgGate := func(ns, name, expr string) {
		require.NoError(t, e.Client.Create(ctx, &v1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
				"kardinal.io/scope": "org", "kardinal.io/applies-to": "prod", "kardinal.io/type": "gate",
			}},
			Spec: v1alpha1.PolicyGateSpec{Expression: expr, Message: name, RecheckInterval: "10s"},
		}))
	}
	orgGate(listed, "org-author", `bundle.provenance.author != "blocked-author"`)
	orgGate(unlisted, "org-unlisted", "false")
	a.apply(t, a.pipeline(nil))

	instances := func(ctx context.Context, bundle, gate string) ([]v1alpha1.PolicyGate, error) {
		var list v1alpha1.PolicyGateList
		err := e.Client.List(ctx, &list, client.InNamespace(a.ns), client.MatchingLabels{
			"kardinal.io/bundle": bundle, "kardinal.io/gate-template": gate,
		})
		return list.Items, err
	}
	blocked := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--author", "blocked-author")
	e.WaitStepState(t, a.ns, pipelineName, blocked, "test", "Verified", promoteTimeout)
	framework.Eventually(t, time.Minute, "the listed namespace's org gate holds prod", func(ctx context.Context) (bool, string) {
		gates, err := instances(ctx, blocked, "org-author")
		if err != nil || len(gates) != 1 {
			return false, fmt.Sprintf("%d instances %v", len(gates), err)
		}
		return gates[0].Status.LastEvaluatedAt != nil && !gates[0].Status.Ready, gates[0].Status.Reason
	})
	framework.Consistently(t, 15*time.Second, "prod stays held", func(ctx context.Context) (bool, string) {
		_, ok, err := e.Step(ctx, a.ns, pipelineName, blocked, "prod")
		return err == nil && !ok, fmt.Sprintf("prod step exists=%v err=%v", ok, err)
	})

	allowed := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3, "--author", "e2e-bot")
	e.WaitStepState(t, a.ns, pipelineName, allowed, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V3, syncTimeout)
	gates, err := instances(ctx, allowed, "org-author")
	require.NoError(t, err)
	require.Len(t, gates, 1)
	assert.True(t, gates[0].Status.Ready, "the org gate passes e2e-bot")
	for _, b := range []string{blocked, allowed} {
		gates, err := instances(ctx, b, "org-unlisted")
		require.NoError(t, err)
		assert.Empty(t, gates, "an org gate outside --policy-namespaces is not applied")
	}

	nsMode := e.Namespace(t)
	_, out, err := e.TryInstallChart(t, releaseName(nsMode), nsMode, nsValues(nsMode, framework.Values{
		"controller": framework.Values{"policyNamespaces": []string{listed}},
	}), false)
	require.Error(t, err)
	assert.Contains(t, out, fmt.Sprintf("controller.policyNamespaces entry %q is outside controller.watchNamespace (%s)", listed, nsMode))
}

// TestChart_SCMCredentials checks the chart's SCM settings: the token the
// promotion PR is opened with comes from github.secretRef or, with
// github.token, from the chart's own Secret, through the scm.provider API
// at scm.apiURL. Conflicting token settings fail the install.
//
// Covers CHART-SCM-01.
func TestChart_SCMCredentials(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()

	adminToken := os.Getenv(gitserver.EnvToken)
	require.NotEmpty(t, adminToken)
	cases := []struct {
		name   string
		values framework.Values
		author string
	}{
		{"github.secretRef", nil, "kardinal-bot"},
		{"github.token", framework.Values{"github": framework.Values{"token": adminToken, "secretRef": framework.Values{"name": ""}}}, "kardinal-admin"},
	}
	for _, c := range cases {
		a := newArgoApp(t, e, "prod")
		r := e.InstallChart(t, releaseName(a.ns), a.ns, nsValues(a.ns, c.values))
		args := r.Deployment(t).Spec.Template.Spec.Containers[0].Args
		assert.Contains(t, args, "--scm-provider="+os.Getenv(framework.EnvSCMProvider))
		assert.Contains(t, args, "--scm-api-url="+os.Getenv(framework.EnvSCMAPI))
		if c.values != nil {
			var s corev1.Secret
			require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: r.Fullname + "-github-token"}, &s))
			assert.True(t, string(s.Data["token"]) == adminToken, "github.token is stored in <fullname>-github-token")
		}

		a.apply(t, a.resourcePipeline(map[string]string{"prod": "pr-review"}))
		bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
		e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
		pr := e.WaitPR(t, a.repo, time.Minute, "prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })
		assert.Equal(t, c.author, e.PRAuthor(t, a.repo, pr.Number), "%s: the PR is opened with that token", c.name)
		require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
		e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	}

	ns := e.Namespace(t)
	_, out, err := e.TryInstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{
		"github": framework.Values{"token": "not-a-token"},
	}), false)
	require.Error(t, err)
	assert.Contains(t, out, "set github.token or github.secretRef.name, not both")
	_, out, err = e.TryInstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{
		"github": framework.Values{"secretRef": framework.Values{"namespace": framework.ControllerNamespace}},
	}), false)
	require.Error(t, err)
	assert.Contains(t, out, "github.secretRef.namespace ("+framework.ControllerNamespace+") must be empty or the release namespace")
}

// TestChart_WebhookSecret checks webhook.secretRef: the git server's signed
// merge event marks the PR merged, a wrong signature is refused, and without
// the value the webhook refuses everything.
//
// Covers CHART-WEBHOOK-01.
func TestChart_WebhookSecret(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	hookSecret := randomHex(t, 16)
	secret(t, e, a.ns, "scm-webhook", "secret", hookSecret)
	withSecret := nsValues(a.ns, framework.Values{"webhook": framework.Values{"secretRef": framework.Values{"name": "scm-webhook"}}})
	r := e.InstallChart(t, releaseName(a.ns), a.ns, withSecret)
	pod := runningPod(t, r)
	logs := r.FollowLogs(t, pod.Name)

	hook := serviceURL(r, 8083, "/webhook/scm")
	require.NoError(t, e.Git.AddWebhook(ctx, a.repo, hook, hookSecret))
	a.apply(t, a.resourcePipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	// The controller also polls PRs, so either path may mark the PRStatus
	// merged first; the webhook's own log line shows it accepted the event.
	framework.Eventually(t, time.Minute, "the signed merge event is accepted", func(context.Context) (bool, string) {
		for _, l := range logs.Lines() {
			if strings.Contains(l.Text, "webhook received") && strings.Contains(l.Text, `"merged":true`) &&
				strings.Contains(l.Text, fmt.Sprintf(`"pr":%d`, pr.Number)) {
				return true, ""
			}
		}
		return false, "no accepted merge event logged yet"
	})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	probe := e.Probe(t, a.ns, "probe", nil)
	bad := probe.Curl(t, 5*time.Second, "-X", "POST", "-H", "Content-Type: application/json",
		"-H", "X-Gitea-Event: pull_request", "-H", "X-Gitea-Signature: "+randomHex(t, 32), "-d", `{"action":"closed"}`, hook)
	assert.Equal(t, 401, bad.Code)
	assert.Contains(t, bad.Body, "unauthorized")
	framework.Eventually(t, 30*time.Second, "the refusal is logged", func(context.Context) (bool, string) {
		_, ok := logs.Find("webhook signature invalid or parse error")
		return ok, "no refusal log yet"
	})

	r.Upgrade(t, nsValues(a.ns, nil))
	runningPod(t, r)
	res := probe.Curl(t, 5*time.Second, "-X", "POST", "-d", "{}", hook)
	assert.Equal(t, 401, res.Code)
	assert.Contains(t, res.Body, "webhook secret not configured", "without webhook.secretRef every event is refused")
}

// bundleRequest posts a Bundle API request from probe and returns the result.
func bundleRequest(t *testing.T, probe *framework.Probe, url, token string, body map[string]interface{}) framework.CurlResult {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	args := []string{"-X", "POST", "-H", "Content-Type: application/json", "-d", string(data)}
	if token != "" {
		args = append(args, "-H", "Authorization: Bearer "+token)
	}
	return probe.Curl(t, 5*time.Second, append(args, url)...)
}

// TestChart_BundleAPI checks bundleAPI.tokenSecretRef mounts POST
// /api/v1/bundles: with the token a CI job creates a Bundle that promotes;
// without it, or for another namespace or Pipeline, the API refuses; and
// without the value the route does not exist.
//
// Covers CHART-BUNDLEAPI-01.
func TestChart_BundleAPI(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	token := randomHex(t, 16)
	secret(t, e, a.ns, "bundle-api", "token", token)
	r := e.InstallChart(t, releaseName(a.ns), a.ns, nsValues(a.ns, framework.Values{
		"bundleAPI": framework.Values{"tokenSecretRef": framework.Values{"name": "bundle-api"}},
	}))
	runningPod(t, r)
	a.apply(t, a.resourcePipeline(nil))
	probe := e.Probe(t, a.ns, "ci", nil)
	url := serviceURL(r, 8083, "/api/v1/bundles")
	body := func(over map[string]interface{}) map[string]interface{} {
		b := map[string]interface{}{
			"pipeline": pipelineName, "type": "image",
			"images":     []map[string]string{{"repository": fixtures.Image, "tag": fixtures.V2}},
			"provenance": map[string]string{"commitSHA": "abc1234", "author": "ci-bot", "ciRunURL": "https://ci.example/run/7"},
		}
		for k, v := range over {
			b[k] = v
		}
		return b
	}

	res := bundleRequest(t, probe, url, token, body(nil))
	require.Equal(t, 201, res.Code, res.Body)
	var created struct{ Name, Namespace string }
	require.NoError(t, json.Unmarshal([]byte(res.Body), &created))
	assert.Equal(t, a.ns, created.Namespace, "the default namespace is the watch namespace")
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: created.Name}, &b))
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, "ci-bot", b.Spec.Provenance.Author)
	e.WaitStepState(t, a.ns, pipelineName, created.Name, "test", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V2, syncTimeout)

	res = bundleRequest(t, probe, url, randomHex(t, 16), body(nil))
	assert.Equal(t, 401, res.Code, "a wrong token is refused")
	res = bundleRequest(t, probe, url, "", body(nil))
	assert.Equal(t, 401, res.Code, "no token is refused")
	res = bundleRequest(t, probe, url, token, body(map[string]interface{}{"namespace": framework.ControllerNamespace}))
	assert.Equal(t, 403, res.Code)
	assert.Contains(t, res.Body, fmt.Sprintf("namespace %q is not watched by this controller", framework.ControllerNamespace))
	res = bundleRequest(t, probe, url, token, body(map[string]interface{}{"pipeline": "nope"}))
	assert.Equal(t, 404, res.Code)
	assert.Contains(t, res.Body, fmt.Sprintf("pipeline %s/nope not found", a.ns))

	r.Upgrade(t, nsValues(a.ns, nil))
	runningPod(t, r)
	res = bundleRequest(t, probe, url, token, body(nil))
	assert.Equal(t, 404, res.Code, "without bundleAPI.tokenSecretRef the route is not mounted")
}
