//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	deleteReaderBindingAtEnd(t, e, "kardinal-promoter-graph-reader-"+a.ns)
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
// lines only at debug, info lines at info but not at warn, and warn lines
// still at warn.
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

	// At warn the webhook secret is set, so an event with a bad signature
	// makes the controller log a warn line on purpose.
	hookSecret := randomHex(t, 16)
	secret(t, e, ns, "scm-webhook", "secret", hookSecret)
	r.Upgrade(t, framework.MergeValues(values("warn"),
		framework.Values{"webhook": framework.Values{"secretRef": framework.Values{"name": "scm-webhook"}}}))
	pod = runningPod(t, r)
	framework.Consistently(t, 20*time.Second, "no info or debug lines at logLevel warn", func(context.Context) (bool, string) {
		levels := logLevels(r.Logs(t, pod.Name, false))
		return levels["debug"] == 0 && levels["info"] == 0, fmt.Sprint(levels)
	})
	probe := e.Probe(t, ns, "probe", nil)
	res := probe.Curl(t, 5*time.Second, "-X", "POST", "-H", "Content-Type: application/json",
		"-H", "X-Gitea-Event: pull_request", "-H", "X-Gitea-Signature: "+randomHex(t, 32), "-d", `{"action":"closed"}`,
		serviceURL(r, 8083, "/webhook/scm"))
	require.Equal(t, 401, res.Code, brief(res))
	framework.Eventually(t, 30*time.Second, "the refused webhook event is logged at logLevel warn", func(context.Context) (bool, string) {
		for _, line := range strings.Split(r.Logs(t, pod.Name, false), "\n") {
			if strings.Contains(line, `"level":"warn"`) && strings.Contains(line, "webhook signature invalid or parse error") {
				return true, ""
			}
		}
		return false, "no warn line for the refused event"
	})
	levels := logLevels(r.Logs(t, pod.Name, false))
	assert.Zero(t, levels["debug"]+levels["info"], "no info or debug lines at logLevel warn: %v", levels)
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
	framework.Eventually(t, 3*time.Minute, "kardinal-clock ticks four times", func(ctx context.Context) (bool, string) {
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
		return len(ticks) >= 5, fmt.Sprint(ticks)
	})
	// The first tick seen can be any age. The later ones are never closer
	// than the interval; a loaded node can delay one, or a slow poll miss
	// one, but the closest pair is one interval apart, not the 60s default.
	closest := time.Hour
	for i := 2; i < len(ticks); i++ {
		gap := ticks[i].Sub(ticks[i-1])
		assert.GreaterOrEqual(t, gap, 9*time.Second, "tick %d came %s after the one before, sooner than the 10s interval", i, gap)
		closest = min(closest, gap)
	}
	assert.LessOrEqual(t, closest, 15*time.Second, "the closest ticks are %s apart, want about 10s: %v", closest, ticks)

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
	// The names the removed template gave its policies and bindings, in
	// case one is created without the instance label.
	for _, name := range []string{"kardinal-policygate-validation", "kardinal-pipeline-validation", "kardinal-bundle-validation"} {
		_, err := e.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, name, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "ValidatingAdmissionPolicy %s: %v", name, err)
		_, err = e.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(ctx, name, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "ValidatingAdmissionPolicyBinding %s: %v", name, err)
	}

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
// with no install is left alone. The only cluster-wide grants are ChangeWindows
// and get on the install's own Namespace object.
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
				assert.Contains(t, []string{"changewindows", "changewindows/status", "namespaces"}, res,
					"the ClusterRole holds only cluster-scoped kinds")
				if res == "namespaces" {
					assert.Equal(t, []string{"get"}, rule.Verbs, "namespaces: get only")
					assert.Equal(t, []string{x.a.ns}, rule.ResourceNames, "namespaces: only the watched one")
				}
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
		{Verb: "get", Resource: "namespaces", Name: teamA.ns},
	}, []framework.Access{
		{Verb: "get", Resource: "namespaces", Name: teamB.ns},
		{Verb: "list", Resource: "namespaces"},
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

	deleteReaderBindingAtEnd(t, e, framework.ChartFullname(releaseName(a.ns))+"-graph-reader-"+a.ns)
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
		lines := logs.Lines()
		for _, l := range lines {
			if strings.Contains(l.Text, "webhook received") && strings.Contains(l.Text, `"merged":true`) &&
				strings.Contains(l.Text, fmt.Sprintf(`"pr":%d`, pr.Number)) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("no accepted merge event in the %d log lines read", len(lines))
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

// newRepoApp is an app with a GitOps repo and no Argo CD Applications, for
// tests that need a Pipeline the UI lists but promote nothing.
func newRepoApp(t *testing.T, e *framework.Env, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	return &app{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
}

// uiPipelines is the UI API route that lists the watched Pipelines.
const uiPipelines = "/api/v1/ui/pipelines"

// bearer is a curl Authorization header for token.
func bearer(token string) string { return "Authorization: Bearer " + token }

// httpsURL is the in-cluster HTTPS URL of the release's Service on port.
func httpsURL(r *framework.Release, port int, path string) string {
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:%d%s", r.Fullname, r.Namespace, port, path)
}

// brief is res for an assertion message.
func brief(res framework.CurlResult) string {
	return fmt.Sprintf("%d %.200s", res.Code, res.Body+res.Err)
}

// crashLogs waits until the release's controller crash-loops and returns
// the log of its last run.
func crashLogs(t *testing.T, e *framework.Env, r *framework.Release) string {
	t.Helper()
	framework.Eventually(t, 3*time.Minute, "the controller crash-loops", func(ctx context.Context) (bool, string) {
		reason, msg := podWaiting(ctx, e, r.Namespace, r)
		return reason == "CrashLoopBackOff", reason + ": " + msg
	})
	pods := r.Pods(t)
	require.NotEmpty(t, pods)
	return r.Logs(t, pods[0].Name, true)
}

// hostRequest makes an HTTP request from the test process, as a browser on
// the host would (through a port-forward or a NodePort). A "Host" entry in
// header sets the Host header. It uses no proxy and never fails the test.
func hostRequest(t *testing.T, method, target string, header map[string]string) framework.CurlResult {
	t.Helper()
	res := framework.CurlResult{Headers: map[string]string{}}
	logged := make([]string, 0, len(header))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	for k, v := range header {
		if strings.EqualFold(k, "Authorization") {
			logged = append(logged, k+": <redacted>")
		} else {
			logged = append(logged, k+": "+v)
		}
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	// A request that fails says how far it got: a NodePort that sends the
	// connection to a gone Pod never connects, a server that hangs never
	// answers.
	var connected, wrote bool
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn:      func(httptrace.GotConnInfo) { connected = true },
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote = true },
	}))
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	if err != nil {
		res.Err = fmt.Sprintf("%v (connected %t, request sent %t)", err, connected, wrote)
	} else {
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			res.Err = err.Error()
		}
		res.Code, res.Body = resp.StatusCode, string(body)
		for k, v := range resp.Header {
			res.Headers[strings.ToLower(k)] = strings.Join(v, ", ")
		}
	}
	t.Logf("%s %s %v -> %s", method, target, logged, firstLine(brief(res)))
	return res
}

// firstLine is the first line of s.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// nodeInternalIP is the kind node's InternalIP, where NodePorts listen.
func nodeInternalIP(t *testing.T, e *framework.Env) string {
	t.Helper()
	nodes, err := e.Kube.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	for _, n := range nodes.Items {
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				return addr.Address
			}
		}
	}
	t.Fatal("no node has an InternalIP")
	return ""
}

// releaseLabels are the chart's selector labels for r: the labels its
// Service, PodDisruptionBudget and NetworkPolicy select the controller by.
func releaseLabels(r *framework.Release) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "kardinal-promoter", "app.kubernetes.io/instance": r.Name}
}

// notReady gives a probe Pod a readiness probe that never passes, so a probe
// carrying the controller's labels never becomes a Service endpoint.
func notReady(p *corev1.Pod) {
	p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "exit 1"}}},
		PeriodSeconds: 60,
	}
}

// mountSecret mounts Secret name read-only at path in a probe Pod.
func mountSecret(name, path string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: name,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}})
		c := &p.Spec.Containers[0]
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: name, MountPath: path, ReadOnly: true})
	}
}

// bundleGraph is the Graph of bundle in ns.
func bundleGraph(t *testing.T, e *framework.Env, ns, bundle string) *unstructured.Unstructured {
	t.Helper()
	list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{
		LabelSelector: "kardinal.io/bundle=" + bundle,
	})
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "one Graph for Bundle %s", bundle)
	return &list.Items[0]
}

// hasAppRef reports whether Graph g has a ref node on an Argo CD
// Application in ns.
func hasAppRef(g *unstructured.Unstructured, ns string) bool {
	nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
	for _, n := range nodes {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _, _ := unstructured.NestedString(node, "ref", "kind")
		refNS, _, _ := unstructured.NestedString(node, "ref", "metadata", "namespace")
		if kind == "Application" && refNS == ns {
			return true
		}
	}
	return false
}

// deleteReaderBindingAtEnd deletes, when the test ends, the Graph reader
// RoleBinding name in argocd that a cluster-mode release's controller made.
// Nothing deletes it when the release and the Graph namespace go (see
// docs/installation.md), so each run would leave one. Call it before
// installing the release, so it runs after the uninstall.
func deleteReaderBindingAtEnd(t *testing.T, e *framework.Env, name string) {
	t.Helper()
	t.Cleanup(func() {
		if os.Getenv(framework.EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		switch {
		case err == nil:
			t.Logf("reader binding %s/%s was left after uninstall; deleted it", framework.ArgoCDNamespace, name)
		case !apierrors.IsNotFound(err):
			t.Errorf("delete reader binding %s/%s: %v", framework.ArgoCDNamespace, name, err)
		}
	})
}

// kroPodIP is the IP of the running kro controller Pod.
func kroPodIP(t *testing.T, e *framework.Env) string {
	t.Helper()
	pods, err := e.Kube.CoreV1().Pods("kro-system").List(context.Background(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=kro",
	})
	require.NoError(t, err)
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" {
			return p.Status.PodIP
		}
	}
	t.Fatal("no running kro Pod in kro-system")
	return ""
}

// listening maps each server the controller logged "server listening" for
// (webhook, ui) to whether it serves TLS.
func listening(logs string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(logs, "\n") {
		var l struct {
			Server  string `json:"server"`
			TLS     bool   `json:"tls"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &l) == nil && l.Message == "server listening" {
			out[l.Server] = l.TLS
		}
	}
	return out
}

// TestChart_TLSAndExtras serves the UI and the webhook over TLS from a
// cert-manager Secret mounted with controller.extraVolumes and
// extraVolumeMounts, sets the UI token with extraEnv and the CORS origins
// with extraArgs. A client that trusts the CA gets HTTPS answers and the
// token and origin settings apply; plain HTTP and a client that does not
// trust the CA are refused. The chart refuses a cert file without a key file
// (or the reverse), and cert paths with no Secret mounted there, before
// applying anything. When the same flags reach the controller anyway
// (through extraEnv), one TLS flag stops it with an error that says so, and
// paths with nothing mounted stop it with an error naming the file.
//
// Covers CHART-TLS-01, CHART-EXTRA-01, CHART-TLS-02, CHART-TLS-03.
func TestChart_TLSAndExtras(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	a := newRepoApp(t, e, "test")
	ns := a.ns
	fullname := framework.ChartFullname(releaseName(ns))
	e.Certificate(t, ns, "kardinal-tls", fullname+"."+ns+".svc", fullname+"."+ns+".svc.cluster.local")
	token := randomHex(t, 16)
	secret(t, e, ns, "ui-token", "token", token)
	origin := "https://kardinal.example.com"
	tlsFiles := framework.Values{"tlsCertFile": "/etc/kardinal-tls/tls.crt", "tlsKeyFile": "/etc/kardinal-tls/tls.key"}
	extras := framework.Values{
		"extraVolumes": []interface{}{framework.Values{"name": "kardinal-tls", "secret": framework.Values{"secretName": "kardinal-tls"}}},
		"extraVolumeMounts": []interface{}{framework.Values{
			"name": "kardinal-tls", "mountPath": "/etc/kardinal-tls", "readOnly": true,
		}},
		"extraArgs": []interface{}{"--cors-allowed-origins=" + origin},
		"extraEnv": []interface{}{framework.Values{"name": "KARDINAL_UI_TOKEN", "valueFrom": framework.Values{
			"secretKeyRef": framework.Values{"name": "ui-token", "key": "token"},
		}}},
	}
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{"controller": framework.MergeValues(tlsFiles, extras)}))
	a.apply(t, a.resourcePipeline(nil))

	pod := runningPod(t, r)
	c := pod.Spec.Containers[0]
	assert.Contains(t, c.Args, "--cors-allowed-origins="+origin, "extraArgs reach the container")
	env := map[string]corev1.EnvVar{}
	for _, v := range c.Env {
		env[v.Name] = v
	}
	assert.Equal(t, "/etc/kardinal-tls/tls.crt", env["KARDINAL_TLS_CERT_FILE"].Value)
	assert.Equal(t, "/etc/kardinal-tls/tls.key", env["KARDINAL_TLS_KEY_FILE"].Value)
	uiToken := env["KARDINAL_UI_TOKEN"].ValueFrom
	require.True(t, uiToken != nil && uiToken.SecretKeyRef != nil, "extraEnv reaches the container: %+v", env["KARDINAL_UI_TOKEN"])
	assert.Equal(t, "ui-token", uiToken.SecretKeyRef.Name)
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == "kardinal-tls" {
			vol = &pod.Spec.Volumes[i]
		}
	}
	require.True(t, vol != nil && vol.Secret != nil, "extraVolumes reach the Pod")
	assert.Equal(t, "kardinal-tls", vol.Secret.SecretName)
	assert.Contains(t, c.VolumeMounts, corev1.VolumeMount{Name: "kardinal-tls", MountPath: "/etc/kardinal-tls", ReadOnly: true},
		"extraVolumeMounts reach the container")

	framework.Eventually(t, time.Minute, "both servers listen with TLS", func(context.Context) (bool, string) {
		got := listening(r.Logs(t, pod.Name, false))
		return got["webhook"] && got["ui"], fmt.Sprint(got)
	})
	assert.Contains(t, r.Logs(t, pod.Name, false), "UI API authentication enabled (--ui-auth-token set)",
		"the token from extraEnv turns UI auth on")

	trusting := e.Probe(t, ns, "client", nil, mountSecret("kardinal-tls", "/tls"))
	https := func(args ...string) framework.CurlResult {
		return trusting.Curl(t, 5*time.Second, append([]string{"--cacert", "/tls/ca.crt"}, args...)...)
	}
	res := https(httpsURL(r, 8082, "/ui/"))
	assert.Equal(t, 200, res.Code, "the UI over HTTPS: %s", brief(res))
	assert.Contains(t, res.Body, "<html")
	res = https(httpsURL(r, 8083, "/webhook/scm/health"))
	assert.Equal(t, 200, res.Code, "the webhook over HTTPS: %s", brief(res))
	res = trusting.Curl(t, 5*time.Second, serviceURL(r, 8082, "/ui/"))
	assert.Equal(t, 400, res.Code, brief(res))
	assert.Contains(t, res.Body, "Client sent an HTTP request to an HTTPS server")
	res = trusting.Curl(t, 5*time.Second, httpsURL(r, 8082, "/ui/"))
	assert.Equal(t, 0, res.Code, "a client that does not trust the CA gets no answer: %s", brief(res))
	assert.Contains(t, res.Err, "certificate")

	api := httpsURL(r, 8082, uiPipelines)
	res = https(api)
	assert.Equal(t, 401, res.Code, "no token: %s", brief(res))
	res = https("-H", bearer(token), api)
	assert.Equal(t, 200, res.Code, brief(res))
	assert.Contains(t, res.Body, pipelineName)
	res = https("-H", bearer(token), "-H", "Origin: "+origin, api)
	assert.Equal(t, 200, res.Code, brief(res))
	assert.Equal(t, origin, res.Headers["access-control-allow-origin"], "the origin from extraArgs gets CORS headers")
	res = https("-H", bearer(token), "-H", "Origin: https://evil.example", api)
	assert.Equal(t, 403, res.Code, brief(res))
	assert.Contains(t, res.Body, "CORS: origin not allowed")

	// A cert file without a key file: the chart refuses it before applying
	// anything, and the controller refuses the same flags when they reach it
	// another way (extraEnv here).
	partial := e.Namespace(t)
	for _, only := range []string{"tlsCertFile", "tlsKeyFile"} {
		_, out, err := e.TryInstallChart(t, releaseName(partial), partial, nsValues(partial, framework.Values{
			"controller": framework.Values{only: tlsFiles[only]},
		}), false)
		require.Error(t, err, "helm install with only %s: %s", only, out)
		assert.Contains(t, out, "controller.tlsCertFile and controller.tlsKeyFile must be set together (only "+only+" is set)")
	}
	var d appsv1.Deployment
	err := e.Client.Get(context.Background(), types.NamespacedName{Namespace: partial, Name: framework.ChartFullname(releaseName(partial))}, &d)
	assert.True(t, apierrors.IsNotFound(err), "the refused installs apply nothing: %v", err)
	rp, out, err := e.TryInstallChart(t, releaseName(partial), partial, nsValues(partial, framework.Values{
		"controller": framework.Values{"extraEnv": []interface{}{framework.Values{"name": "KARDINAL_TLS_CERT_FILE", "value": tlsFiles["tlsCertFile"]}}},
	}), false)
	require.NoError(t, err, out)
	assert.Contains(t, crashLogs(t, e, rp), "--tls-cert-file and --tls-key-file must be set together")

	// TLS paths with no Secret mounted there: the chart refuses them, and the
	// controller exits when it cannot open the files (set through extraEnv).
	unmounted := e.Namespace(t)
	_, out, err = e.TryInstallChart(t, releaseName(unmounted), unmounted, nsValues(unmounted, framework.Values{"controller": tlsFiles}), false)
	require.Error(t, err, "helm install with TLS paths and no Secret mounted: %s", out)
	assert.Contains(t, out, "controller.tlsCertFile (/etc/kardinal-tls/tls.crt) is not in a mounted Secret")
	err = e.Client.Get(context.Background(), types.NamespacedName{Namespace: unmounted, Name: framework.ChartFullname(releaseName(unmounted))}, &d)
	assert.True(t, apierrors.IsNotFound(err), "the refused install applies nothing: %v", err)
	ru, out, err := e.TryInstallChart(t, releaseName(unmounted), unmounted, nsValues(unmounted, framework.Values{
		"controller": framework.Values{"extraEnv": []interface{}{
			framework.Values{"name": "KARDINAL_TLS_CERT_FILE", "value": tlsFiles["tlsCertFile"]},
			framework.Values{"name": "KARDINAL_TLS_KEY_FILE", "value": tlsFiles["tlsKeyFile"]},
		}},
	}), false)
	require.NoError(t, err, out)
	assert.Contains(t, crashLogs(t, e, ru), "open /etc/kardinal-tls/tls.crt: no such file or directory")
}

// TestChart_UIAuth checks the UI API access values. With
// ui.auth.tokenSecretRef the API wants that bearer token; origins in
// ui.corsAllowedOrigins get CORS headers (the preflight too) and others are
// refused; a host in ui.allowedHosts is one of the UI's own names, so a page
// served there is same-origin. With ui.auth.tokenReview the API takes
// Kubernetes tokens and serves each caller what its own RBAC allows.
//
// Covers CHART-UIAUTH-01.
func TestChart_UIAuth(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newRepoApp(t, e, "test")
	ns := a.ns
	token := randomHex(t, 16)
	secret(t, e, ns, "ui-token", "token", token)
	origin := "https://kardinal.example.com"
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{"ui": framework.Values{
		"corsAllowedOrigins": []string{origin},
		"allowedHosts":       []string{"kardinal.example"},
		"auth":               framework.Values{"tokenSecretRef": framework.Values{"name": "ui-token"}},
	}}))
	a.apply(t, a.resourcePipeline(nil))
	args := runningPod(t, r).Spec.Containers[0].Args
	assert.Contains(t, args, "--cors-allowed-origins="+origin)
	assert.Contains(t, args, fmt.Sprintf("--ui-allowed-hosts=%[1]s,%[1]s.%[2]s,%[1]s.%[2]s.svc,%[1]s.%[2]s.svc.cluster.local,kardinal.example", r.Fullname, ns))
	assert.NotContains(t, args, "--ui-tokenreview-auth=true")

	probe := e.Probe(t, ns, "client", nil)
	api := serviceURL(r, 8082, uiPipelines)
	res := probe.Curl(t, 5*time.Second, api)
	assert.Equal(t, 401, res.Code, "no token: %s", brief(res))
	assert.Equal(t, `Bearer realm="kardinal-ui"`, res.Headers["www-authenticate"])
	res = probe.Curl(t, 5*time.Second, "-H", bearer(randomHex(t, 16)), api)
	assert.Equal(t, 401, res.Code, "a wrong token: %s", brief(res))
	res = probe.Curl(t, 5*time.Second, "-H", bearer(token), api)
	assert.Equal(t, 200, res.Code, brief(res))
	assert.Contains(t, res.Body, pipelineName)
	res = probe.Curl(t, 5*time.Second, serviceURL(r, 8082, "/ui/"))
	assert.Equal(t, 200, res.Code, "the UI's own files need no token: %s", brief(res))

	res = probe.Curl(t, 5*time.Second, "-H", bearer(token), "-H", "Origin: "+origin, api)
	assert.Equal(t, 200, res.Code, brief(res))
	assert.Equal(t, origin, res.Headers["access-control-allow-origin"])
	res = probe.Curl(t, 5*time.Second, "-X", "OPTIONS", "-H", "Origin: "+origin, "-H", "Access-Control-Request-Method: GET", api)
	assert.Equal(t, 200, res.Code, "the preflight needs no token: %s", brief(res))
	assert.Equal(t, origin, res.Headers["access-control-allow-origin"])
	assert.Equal(t, "GET, POST, OPTIONS", res.Headers["access-control-allow-methods"])
	assert.Equal(t, "Authorization, Content-Type", res.Headers["access-control-allow-headers"])
	res = probe.Curl(t, 5*time.Second, "-H", bearer(token), "-H", "Origin: https://evil.example", api)
	assert.Equal(t, 403, res.Code, brief(res))
	assert.Contains(t, res.Body, "CORS: origin not allowed")

	res = probe.Curl(t, 5*time.Second, "-H", bearer(token), "-H", "Host: kardinal.example", "-H", "Origin: http://kardinal.example", api)
	assert.Equal(t, 200, res.Code, "a page on an allowed host is same-origin: %s", brief(res))
	assert.Empty(t, res.Headers["access-control-allow-origin"], "same-origin needs no CORS headers")
	res = probe.Curl(t, 5*time.Second, "-H", bearer(token), "-H", "Host: evil.example", "-H", "Origin: http://evil.example", api)
	assert.Equal(t, 403, res.Code, "a page on another host is not: %s", brief(res))
	assert.Contains(t, res.Body, "CORS: origin not allowed")

	controller := framework.ServiceAccountUser(ns, r.Fullname)
	reviews := []framework.Access{
		{Verb: "create", Group: "authentication.k8s.io", Resource: "tokenreviews"},
		{Verb: "create", Group: "authorization.k8s.io", Resource: "subjectaccessreviews"},
	}
	checkAccess(t, e, controller, nil, reviews)

	r.Upgrade(t, nsValues(ns, framework.Values{"ui": framework.Values{"auth": framework.Values{"tokenReview": true}}}))
	assert.Contains(t, runningPod(t, r).Spec.Containers[0].Args, "--ui-tokenreview-auth=true")
	checkAccess(t, e, controller, reviews, nil)

	for _, name := range []string{"viewer", "nobody"} {
		require.NoError(t, e.Client.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}))
	}
	_, err := e.Kube.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "kardinal-viewer"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"kardinal.io"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "kardinal-viewer"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "kardinal-viewer"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "viewer", Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	viewer := framework.ServiceAccountUser(ns, "viewer")
	framework.Eventually(t, 30*time.Second, "viewer may list Pipelines", func(context.Context) (bool, string) {
		return e.Can(t, viewer, framework.Access{Verb: "list", Group: "kardinal.io", Resource: "pipelines", Namespace: ns}), "not yet"
	})
	saToken := func(name string) string {
		expiry := int64(600)
		tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, name, &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &expiry},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		return tr.Status.Token
	}
	viewerToken, nobodyToken := saToken("viewer"), saToken("nobody")
	framework.Eventually(t, 90*time.Second, "the UI API takes viewer's Kubernetes token", func(context.Context) (bool, string) {
		res := probe.Curl(t, 5*time.Second, "-H", bearer(viewerToken), api)
		return res.Code == 200 && strings.Contains(res.Body, pipelineName), brief(res)
	})
	res = probe.Curl(t, 5*time.Second, "-H", bearer(nobodyToken), api)
	assert.Equal(t, 403, res.Code, "a caller without RBAC: %s", brief(res))
	assert.Contains(t, res.Body, fmt.Sprintf(`forbidden: user "system:serviceaccount:%s:nobody" cannot list pipelines.kardinal.io in namespace %s`, ns, ns))
	for what, tok := range map[string]string{"no token": "", "a token Kubernetes did not issue": randomHex(t, 16), "the old static token": token} {
		args := []string{api}
		if tok != "" {
			args = []string{"-H", bearer(tok), api}
		}
		res = probe.Curl(t, 5*time.Second, args...)
		assert.Equal(t, 401, res.Code, "%s: %s", what, brief(res))
	}
}

// TestChart_UIExpose checks what the installation guide says about exposing
// the UI. With no UI auth mode, kubectl port-forward is served, while
// another pod, a NodePort and a request with proxy headers get a 403 that
// says why, and the Host must be one of the controller's names. With
// ui.auth.tokenSecretRef the NodePort serves token holders, and a page on
// the node address is same-origin only once ui.allowedHosts names it.
//
// Covers CHART-UIEXPOSE-01.
func TestChart_UIExpose(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newRepoApp(t, e, "test")
	ns := a.ns
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, nil))
	a.apply(t, a.resourcePipeline(nil))
	runningPod(t, r)

	local := e.PortForward(t, ns, r.Fullname, 8082)
	res := hostRequest(t, http.MethodGet, local+uiPipelines, nil)
	assert.Equal(t, 200, res.Code, "port-forward is served: %s", brief(res))
	assert.Contains(t, res.Body, pipelineName)
	res = hostRequest(t, http.MethodGet, local+"/ui/", nil)
	assert.Equal(t, 200, res.Code, brief(res))
	res = hostRequest(t, http.MethodGet, local+uiPipelines, map[string]string{"Host": "evil.example"})
	assert.Equal(t, 403, res.Code, brief(res))
	assert.Contains(t, res.Body, "UI API: host not allowed")
	res = hostRequest(t, http.MethodGet, local+uiPipelines, map[string]string{"X-Forwarded-For": "203.0.113.7"})
	assert.Equal(t, 403, res.Code, "a request through a proxy is not local: %s", brief(res))
	assert.Contains(t, res.Body, "only local clients")

	other := e.Probe(t, ns, "other-pod", nil)
	res = other.Curl(t, 5*time.Second, serviceURL(r, 8082, uiPipelines))
	assert.Equal(t, 403, res.Code, "another pod: %s", brief(res))
	assert.Contains(t, res.Body, "only local clients (kubectl port-forward) are served")

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ui-nodeport"},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: releaseLabels(r),
			Ports:    []corev1.ServicePort{{Name: "ui", Port: 8082, TargetPort: intstr.FromString("ui")}},
		},
	}
	require.NoError(t, e.Client.Create(ctx, svc))
	nodeIP := nodeInternalIP(t, e)
	nodeURL := fmt.Sprintf("http://%s:%d", nodeIP, svc.Spec.Ports[0].NodePort)
	framework.Eventually(t, time.Minute, "the NodePort answers", func(context.Context) (bool, string) {
		res = hostRequest(t, http.MethodGet, nodeURL+uiPipelines, nil)
		return res.Code != 0, brief(res)
	})
	assert.Equal(t, 403, res.Code, "a NodePort client: %s", brief(res))
	assert.Contains(t, res.Body, "only local clients")
	res = hostRequest(t, http.MethodGet, nodeURL+"/ui/", nil)
	assert.Equal(t, 200, res.Code, brief(res))

	token := randomHex(t, 16)
	secret(t, e, ns, "ui-token", "token", token)
	withToken := func(hosts ...string) framework.Values {
		ui := framework.Values{"auth": framework.Values{"tokenSecretRef": framework.Values{"name": "ui-token"}}}
		if hosts != nil {
			ui["allowedHosts"] = hosts
		}
		return nsValues(ns, framework.Values{"ui": ui})
	}
	r.Upgrade(t, withToken())
	runningPod(t, r)
	auth := map[string]string{"Authorization": "Bearer " + token}
	framework.Eventually(t, time.Minute, "the NodePort wants the token", func(context.Context) (bool, string) {
		res = hostRequest(t, http.MethodGet, nodeURL+uiPipelines, nil)
		return res.Code == 401, brief(res)
	})
	res = hostRequest(t, http.MethodGet, nodeURL+uiPipelines, auth)
	assert.Equal(t, 200, res.Code, brief(res))
	assert.Contains(t, res.Body, pipelineName)
	page := map[string]string{"Authorization": "Bearer " + token, "Origin": nodeURL}
	res = hostRequest(t, http.MethodGet, nodeURL+uiPipelines, page)
	assert.Equal(t, 403, res.Code, "the node address is not one of the UI's names yet: %s", brief(res))
	assert.Contains(t, res.Body, "CORS: origin not allowed")

	r.Upgrade(t, withToken(nodeIP))
	runningPod(t, r)
	framework.Eventually(t, time.Minute, "a page on an allowed node address is same-origin", func(context.Context) (bool, string) {
		res = hostRequest(t, http.MethodGet, nodeURL+uiPipelines, page)
		return res.Code == 200, brief(res)
	})
	assert.Empty(t, res.Headers["access-control-allow-origin"])
}

// TestChart_RolloutKeepsServing opens a new connection to the UI through a
// NodePort every 100ms while a helm upgrade rolls the controller out. A
// controller Pod marked for deletion keeps serving for the chart's
// shutdownDelaySeconds (a preStop sleep) while kube-proxy, which can lag the
// EndpointSlice by a second or more, stops routing to it, and only then gets
// SIGTERM; so every request is answered. shutdownDelaySeconds 0 removes the
// hook.
//
// Covers INST-SHUTDOWN-04, CHART-SHUTDOWN-DELAY-01.
func TestChart_RolloutKeepsServing(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	values := func(rollout string, extra framework.Values) framework.Values {
		v := framework.Values{"podAnnotations": framework.Values{"e2e.kardinal.io/rollout": rollout}}
		for k, x := range extra {
			v[k] = x
		}
		return nsValues(ns, v)
	}
	r := e.InstallChart(t, releaseName(ns), ns, values("1", nil))
	old := runningPod(t, r)
	assert.Equal(t, []string{"sleep", "5"}, preStopCommand(t, old), "the chart's default shutdown delay")

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ui-nodeport"},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: releaseLabels(r),
			Ports:    []corev1.ServicePort{{Name: "ui", Port: 8082, TargetPort: intstr.FromString("ui")}},
		},
	}
	require.NoError(t, e.Client.Create(ctx, svc))
	page := fmt.Sprintf("http://%s:%d/ui/", nodeInternalIP(t, e), svc.Spec.Ports[0].NodePort)
	framework.Eventually(t, time.Minute, "the NodePort serves the UI", func(context.Context) (bool, string) {
		res := hostRequest(t, http.MethodGet, page, nil)
		return res.Code == 200, brief(res)
	})

	// One request at a time, each on a new connection, so each one goes
	// wherever kube-proxy routes it at that moment.
	var (
		mu     sync.Mutex
		sent   int
		failed []string
	)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			at := time.Now()
			why := ""
			resp, err := c.Get(page)
			if err != nil {
				why = err.Error()
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					why = resp.Status
				}
			}
			mu.Lock()
			sent++
			if why != "" {
				failed = append(failed, at.UTC().Format("15:04:05.000")+" "+why)
			}
			mu.Unlock()
		}
	}()
	r.Upgrade(t, values("2", nil))
	pod := runningPod(t, r)
	time.Sleep(2 * time.Second)
	close(stop)
	<-done
	require.NotEqual(t, old.Name, pod.Name, "the upgrade replaced the controller Pod")
	t.Logf("%d requests during the rollout, %d failed", sent, len(failed))
	assert.GreaterOrEqual(t, sent, 50, "the requests span the rollout")
	assert.Empty(t, failed, "every request during the rollout is answered")

	r.Upgrade(t, values("3", framework.Values{"shutdownDelaySeconds": 0}))
	pod = runningPod(t, r)
	assert.Nil(t, preStopCommand(t, pod), "shutdownDelaySeconds 0 removes the preStop hook")
}

// preStopCommand is the command of the controller container's preStop hook
// in pod, or nil when it has none.
func preStopCommand(t *testing.T, pod corev1.Pod) []string {
	t.Helper()
	for _, c := range pod.Spec.Containers {
		if c.Name != "controller" {
			continue
		}
		if c.Lifecycle == nil || c.Lifecycle.PreStop == nil {
			return nil
		}
		require.NotNil(t, c.Lifecycle.PreStop.Exec, "the preStop hook runs a command")
		return c.Lifecycle.PreStop.Exec.Command
	}
	t.Fatalf("Pod %s has no controller container", pod.Name)
	return nil
}

// TestChart_GraphIdentity checks the graph values. aggregateToKro grants
// kro's controller watch on the kinds kro creates, and without it kro has
// none. serviceAccountName is the identity the controller provisions and
// kro applies each Graph as. readerNamespaces is where that identity may
// read a health ref: a ref elsewhere is dropped from the Graph with a
// warning, and the promotion still checks health. kroNamespace is the
// NetworkPolicy egress rule to kro.
//
// Covers CHART-GRAPH-01.
func TestChart_GraphIdentity(t *testing.T) {
	t.Parallel()
	clusterScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	name := releaseName(a.ns)
	fullname := framework.ChartFullname(name)
	readerBinding := fullname + "-graph-reader-" + a.ns
	deleteReaderBindingAtEnd(t, e, readerBinding)
	kro := framework.ServiceAccountUser("kro-system", "kro")
	watchSteps := framework.Access{Verb: "watch", Group: "kardinal.io", Resource: "promotionsteps"}
	teamGraph := framework.ServiceAccountUser(a.ns, "team-graph")
	readApps := framework.Access{Verb: "get", Group: "argoproj.io", Resource: "applications", Namespace: framework.ArgoCDNamespace}
	values := func(readers []string, over framework.Values) framework.Values {
		return framework.MergeValues(framework.Values{"graph": framework.Values{
			"serviceAccountName": "team-graph", "readerNamespaces": readers, "aggregateToKro": true,
		}}, over)
	}

	r := e.InstallChart(t, name, a.ns, framework.Values{"graph": framework.Values{"aggregateToKro": false}})
	_, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, fullname+"-kro-watch", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "aggregateToKro=false ships no ClusterRole for kro: %v", err)
	framework.Eventually(t, 30*time.Second, "kro may not watch PromotionSteps", func(context.Context) (bool, string) {
		return !e.Can(t, kro, watchSteps), "kro may watch promotionsteps"
	})

	r.Upgrade(t, values([]string{"flux-system"}, nil))
	role, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, fullname+"-kro-watch", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "true", role.Labels["rbac.kro.run/aggregate-to-controller"])
	framework.Eventually(t, time.Minute, "kro may watch PromotionSteps through the aggregated role", func(context.Context) (bool, string) {
		return e.Can(t, kro, watchSteps), "not yet"
	})
	pod := runningPod(t, r)
	args := pod.Spec.Containers[0].Args
	assert.Contains(t, args, "--graph-service-account=team-graph")
	assert.Contains(t, args, "--graph-reader-namespaces=flux-system")
	logs := r.FollowLogs(t, pod.Name)
	a.apply(t, a.pipeline(nil))
	v2 := promote(t, a, fixtures.V2, nil)

	g := bundleGraph(t, e, a.ns, v2)
	sa, _, _ := unstructured.NestedString(g.Object, "spec", "serviceAccountName")
	assert.Equal(t, "team-graph", sa, "kro applies the Graph as graph.serviceAccountName")
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "team-graph"}, &corev1.ServiceAccount{}))
	applier, err := e.Kube.RbacV1().RoleBindings(a.ns).Get(ctx, fullname+"-graph-applier", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, applier.Subjects, 1)
	assert.Equal(t, [3]string{"ServiceAccount", "team-graph", a.ns},
		[3]string{applier.Subjects[0].Kind, applier.Subjects[0].Name, applier.Subjects[0].Namespace})
	_, err = e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, readerBinding, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "argocd is not a reader namespace, so no reader binding there: %v", err)
	line, ok := logs.Find("no health ref node: namespace not in --graph-reader-namespaces")
	assert.True(t, ok, "the dropped health ref is logged")
	assert.Contains(t, line.Text, `"namespace":"argocd"`)
	assert.False(t, hasAppRef(g, framework.ArgoCDNamespace), "the Argo CD health ref is dropped from the Graph")
	assert.False(t, e.Can(t, teamGraph, readApps), "team-graph may not read Applications in argocd")

	r.Upgrade(t, values([]string{framework.ArgoCDNamespace}, nil))
	assert.Contains(t, runningPod(t, r).Spec.Containers[0].Args, "--graph-reader-namespaces="+framework.ArgoCDNamespace)
	v3 := promote(t, a, fixtures.V3, nil)
	reader, err := e.Kube.RbacV1().RoleBindings(framework.ArgoCDNamespace).Get(ctx, readerBinding, metav1.GetOptions{})
	require.NoError(t, err, "a reader binding in argocd")
	assert.Equal(t, fullname+"-graph-reader", reader.RoleRef.Name)
	require.Len(t, reader.Subjects, 1)
	assert.Equal(t, [2]string{"team-graph", a.ns}, [2]string{reader.Subjects[0].Name, reader.Subjects[0].Namespace})
	applier, err = e.Kube.RbacV1().RoleBindings(a.ns).Get(ctx, fullname+"-graph-applier", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, strings.Split(applier.Annotations["kardinal.io/reader-namespaces"], ","), framework.ArgoCDNamespace)
	assert.True(t, hasAppRef(bundleGraph(t, e, a.ns, v3), framework.ArgoCDNamespace), "the Argo CD health ref is in the Graph")
	assert.True(t, e.Can(t, teamGraph, readApps), "team-graph may read Applications in argocd")

	netpol := framework.Values{"networkPolicy": framework.Values{"enabled": true}}
	r.Upgrade(t, values([]string{framework.ArgoCDNamespace}, netpol))
	runningPod(t, r)
	kroMetrics := fmt.Sprintf("http://%s:8078/metrics", kroPodIP(t, e))
	other := e.Probe(t, a.ns, "other", nil)
	res := other.Curl(t, 5*time.Second, kroMetrics)
	require.NotEqual(t, 0, res.Code, "kro answers on its metrics port: %s", brief(res))
	asController := e.Probe(t, a.ns, "as-controller", releaseLabels(r), notReady)
	framework.Eventually(t, time.Minute, "the policy lets the controller reach kro-system", func(context.Context) (bool, string) {
		res := asController.Curl(t, 5*time.Second, kroMetrics)
		return res.Code != 0, brief(res)
	})
	r.Upgrade(t, values([]string{framework.ArgoCDNamespace}, framework.MergeValues(netpol,
		framework.Values{"graph": framework.Values{"kroNamespace": ""}})))
	framework.Eventually(t, time.Minute, `graph.kroNamespace "" drops the egress rule to kro`, func(context.Context) (bool, string) {
		res := asController.Curl(t, 3*time.Second, kroMetrics)
		return res.Code == 0, brief(res)
	})
}

// TestChart_NetworkPolicy checks networkPolicy on a CNI that enforces it.
// Each port admits the peers networkPolicy.ingressFrom lists, or any peer
// when none are listed. The controller may reach DNS, :443 and :6443, and
// the extraEgress destinations, and nothing else: its clone times out, and
// it cannot promote until extraEgress lets it reach the git server.
//
// kindnet enforces a policy for a new Pod only once it has added the Pod's
// IP to its nftables set, about a second after the Pod gets the IP, and it
// never re-checks a connection it let through (ct label 28). The controller
// checks its SCM token as it starts, inside that window. The check has its
// own Transport without keep-alives (pkg/scm/token_validator.go), so no
// connection it opened is left for go-git's clone to reuse. The test does
// not restart the controller: it creates the Bundle once the policy is
// enforced for the Pod's IP, and the clone timing out shows that no
// connection from before the policy outlives it.
//
// Covers CHART-NETPOL-01.
func TestChart_NetworkPolicy(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	ns := a.ns

	scm, err := url.Parse(os.Getenv(framework.EnvSCMAPI))
	require.NoError(t, err)
	host := strings.Split(scm.Hostname(), ".")
	require.True(t, len(host) >= 2 && scm.Port() != "", "%s=%s is http://<service>.<namespace>...:<port>", framework.EnvSCMAPI, scm)
	gitPort, err := strconv.Atoi(scm.Port())
	require.NoError(t, err)
	gitEgress := framework.Values{
		"to": []interface{}{framework.Values{"namespaceSelector": framework.Values{
			"matchLabels": framework.Values{"kubernetes.io/metadata.name": host[1]},
		}}},
		"ports": []interface{}{framework.Values{"port": gitPort, "protocol": "TCP"}},
	}
	values := func(extraEgress ...interface{}) framework.Values {
		if extraEgress == nil {
			extraEgress = []interface{}{}
		}
		return nsValues(ns, framework.Values{"networkPolicy": framework.Values{
			"enabled": true,
			"ingressFrom": framework.Values{"metrics": []interface{}{framework.Values{
				"podSelector": framework.Values{"matchLabels": framework.Values{"role": "scraper"}},
			}}},
			"extraEgress": extraEgress,
		}})
	}
	r := e.InstallChart(t, releaseName(ns), ns, values())
	runningPod(t, r)
	pol, err := e.Kube.NetworkingV1().NetworkPolicies(ns).Get(ctx, r.Fullname+"-controller", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, releaseLabels(r), pol.Spec.PodSelector.MatchLabels, "the policy selects the controller Pod")

	scraper := e.Probe(t, ns, "scraper", map[string]string{"role": "scraper"})
	other := e.Probe(t, ns, "other", nil)
	asController := e.Probe(t, ns, "as-controller", releaseLabels(r), notReady)
	metrics := serviceURL(r, 8080, "/metrics")
	framework.Eventually(t, time.Minute, "ingressFrom.metrics admits the scraper", func(context.Context) (bool, string) {
		res := scraper.Curl(t, 5*time.Second, metrics)
		return res.Code == 200, brief(res)
	})
	framework.Eventually(t, time.Minute, "ingressFrom.metrics refuses other pods", func(context.Context) (bool, string) {
		res := other.Curl(t, 3*time.Second, metrics)
		return res.TimedOut(), brief(res)
	})
	res := other.Curl(t, 5*time.Second, serviceURL(r, 8082, "/ui/"))
	assert.Equal(t, 200, res.Code, "a port with no ingressFrom admits any pod: %s", brief(res))
	res = other.Curl(t, 5*time.Second, serviceURL(r, 8081, "/readyz"))
	assert.Equal(t, 200, res.Code, brief(res))
	res = other.Curl(t, 5*time.Second, "-X", "POST", "-d", "{}", serviceURL(r, 8083, "/webhook/scm"))
	assert.Equal(t, 401, res.Code, brief(res))

	gitURL := strings.TrimSuffix(scm.String(), "/") + "/api/v1/version"
	podinfo := fmt.Sprintf("http://%s.%s.svc.cluster.local:9898/", fixtures.Workload("test"), ns)
	res = other.Curl(t, 5*time.Second, gitURL)
	require.NotEqual(t, 0, res.Code, "the git server answers pods the policy does not select: %s", brief(res))
	res = other.Curl(t, 5*time.Second, podinfo)
	require.Equal(t, 200, res.Code, brief(res))
	framework.Eventually(t, time.Minute, "the controller may not reach the git server", func(context.Context) (bool, string) {
		res := asController.Curl(t, 3*time.Second, gitURL)
		return res.TimedOut(), brief(res)
	})
	res = asController.Curl(t, 5*time.Second, "-k", "https://kubernetes.default.svc.cluster.local/version")
	assert.NotEqual(t, 0, res.Code, "the controller may reach the API server: %s", brief(res))
	res = asController.Curl(t, 3*time.Second, podinfo)
	assert.True(t, res.TimedOut(), "the controller may not reach other pods: %s", brief(res))

	// Promote once kindnet enforces the policy for the Pod's IP (see above).
	since := time.Now()
	t.Cleanup(func() {
		if t.Failed() {
			e.DiagnoseCNI(t, ns, since)
		}
	})
	pod := runningPod(t, r)
	framework.Eventually(t, time.Minute, "the policy is enforced for "+pod.Name+"'s IP", func(context.Context) (bool, string) {
		res := other.Curl(t, 3*time.Second, "http://"+net.JoinHostPort(pod.Status.PodIP, "8080")+"/metrics")
		return res.TimedOut(), brief(res)
	})

	a.apply(t, a.resourcePipeline(nil))
	bundle := e.CreateBundle(t, ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	// The controller's own clone must time out: that, not only the absence
	// of a promotion, shows the policy blocks its git traffic.
	framework.Eventually(t, 2*time.Minute, "without git egress the controller's clone times out", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipelineName, bundle, "test")
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no test step yet"
		}
		require.NotEqual(t, "Verified", ps.Status.State, "without git egress the promotion landed: %s", ps.Status.Message)
		return strings.Contains(ps.Status.Message, "step git-clone") && strings.Contains(ps.Status.Message, "i/o timeout"),
			ps.Status.State + ": " + ps.Status.Message
	})
	framework.Consistently(t, 10*time.Second, "without git egress the promotion does not land", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipelineName, bundle, "test")
		if err != nil {
			return false, err.Error()
		}
		if ok && ps.Status.State == "Verified" {
			return false, "the test step is Verified: " + ps.Status.Message
		}
		return true, ""
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, ns, fixtures.Workload("test")))

	r.Upgrade(t, values(gitEgress))
	runningPod(t, r)
	framework.Eventually(t, time.Minute, "extraEgress lets the controller reach the git server", func(context.Context) (bool, string) {
		res := asController.Curl(t, 3*time.Second, gitURL)
		return res.Code != 0, brief(res)
	})
	e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V2, syncTimeout)
}

// TestChart_Demo installs the chart with demo.enabled. The demo Pipeline is
// what the values describe and promotes a Bundle through test, uat and a
// reviewed prod; demo.secretRef is the Secret it pushes with; disabling
// demo removes it; a missing demo.git.url fails the install with the
// chart's message.
//
// Covers CHART-DEMO-01.
func TestChart_Demo(t *testing.T) {
	t.Parallel()
	clusterScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	envs := []string{"test", "uat", "prod"}
	ns := e.Namespace(t)
	repo := e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))
	require.Equal(t, "main", repo.Branch, "the demo Pipeline promotes branch main")
	// The demo Pipeline's health refs are these Application names in argocd.
	for _, env := range envs {
		e.ArgoApp(t, "kardinal-test-app-"+env, repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, "kardinal-test-app-"+env, syncTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}
	name := releaseName(ns)
	deleteReaderBindingAtEnd(t, e, framework.ChartFullname(name)+"-graph-reader-"+ns)
	demo := func(over framework.Values) framework.Values {
		return framework.Values{"demo": framework.MergeValues(framework.Values{
			"enabled": true, "image": fixtures.Image + ":" + fixtures.V1, "git": framework.Values{"url": repo.CloneURL},
		}, over)}
	}
	r := e.InstallChart(t, name, ns, demo(nil))
	runningPod(t, r)

	key := types.NamespacedName{Namespace: ns, Name: "demo"}
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, key, &p), "demo.enabled creates Pipeline demo")
	assert.Equal(t, "demo", p.Labels["app.kubernetes.io/component"])
	assert.Equal(t, name, p.Labels["app.kubernetes.io/instance"])
	assert.Equal(t, "true", p.Annotations["kardinal.io/demo"])
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, p.Annotations["kardinal.io/demo-image"], "demo.image")
	assert.Contains(t, p.Annotations["kardinal.io/demo-instructions"], "kardinal create bundle demo --image")
	assert.Equal(t, repo.CloneURL, p.Spec.Git.URL, "demo.git.url")
	assert.Equal(t, "main", p.Spec.Git.Branch)
	assert.Equal(t, "directory", p.Spec.Git.Layout)
	require.NotNil(t, p.Spec.Git.SecretRef)
	assert.Equal(t, framework.GitSecretName, p.Spec.Git.SecretRef.Name, "the default is the chart's SCM token Secret")
	approvals := map[string]string{}
	for _, env := range p.Spec.Environments {
		approvals[env.Name] = env.Approval
	}
	assert.Equal(t, map[string]string{"test": "auto", "uat": "auto", "prod": "pr-review"}, approvals)

	v2 := e.CreateBundle(t, ns, "demo", "--image", fixtures.Image+":"+fixtures.V2)
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, ns, "demo", v2, env, "Verified", promoteTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V2, syncTimeout)
	}
	e.WaitStepState(t, ns, "demo", v2, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.Contains(pr.Body, fixtures.V2)
	})
	require.NoError(t, e.Git.MergePR(ctx, repo, pr.Number))
	e.WaitStepState(t, ns, "demo", v2, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2, syncTimeout)

	// demo.secretRef: a Secret whose token the git server refuses stops the
	// push; the real token in the same Secret lets the same Bundle land.
	secret(t, e, ns, "demo-git", "token", randomHex(t, 20))
	r.Upgrade(t, demo(framework.Values{"secretRef": framework.Values{"name": "demo-git"}}))
	require.NoError(t, e.Client.Get(ctx, key, &p))
	require.NotNil(t, p.Spec.Git.SecretRef)
	assert.Equal(t, "demo-git", p.Spec.Git.SecretRef.Name, "demo.secretRef.name")
	runningPod(t, r)
	v3 := e.CreateBundle(t, ns, "demo", "--image", fixtures.Image+":"+fixtures.V3)
	framework.Eventually(t, 2*time.Minute, "the refused token fails the push", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, "demo", v3, "test")
		if err != nil || !ok {
			return false, fmt.Sprintf("no step yet (%v)", err)
		}
		return strings.Contains(ps.Status.Message, "retrying in"), ps.Status.State + ": " + ps.Status.Message
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, ns, fixtures.Workload("test")))
	var real, demoGit corev1.Secret
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: framework.GitSecretName}, &real))
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-git"}, &demoGit))
	demoGit.Data = map[string][]byte{"token": real.Data["token"]}
	require.NoError(t, e.Client.Update(ctx, &demoGit))
	e.WaitStepState(t, ns, "demo", v3, "test", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V3, syncTimeout)

	r.Upgrade(t, framework.Values{})
	framework.Eventually(t, 2*time.Minute, "demo.enabled=false removes the demo Pipeline", func(ctx context.Context) (bool, string) {
		err := e.Client.Get(ctx, key, &v1alpha1.Pipeline{})
		return apierrors.IsNotFound(err), fmt.Sprint(err)
	})

	other := e.Namespace(t)
	_, out, err := e.TryInstallChart(t, releaseName(other), other, nsValues(other, framework.Values{
		"demo": framework.Values{"enabled": true, "git": framework.Values{"url": nil}},
	}), false)
	require.Error(t, err)
	assert.Contains(t, out, "demo.git.url is required (your fork of pnz1990/kardinal-demo)")
	_, out, err = e.TryInstallChart(t, releaseName(other), other, nsValues(other, framework.Values{
		"demo": framework.Values{"enabled": true, "git": framework.Values{"url": ""}},
	}), false)
	require.Error(t, err)
	assert.Contains(t, out, "values don't meet the specifications of the schema")
	assert.Contains(t, out, "url")
}

// logLine is the first line of logs that contains every one of substrs.
func logLine(logs *framework.LogStream, substrs ...string) (framework.StreamLine, bool) {
	for _, l := range logs.Lines() {
		all := true
		for _, s := range substrs {
			all = all && strings.Contains(l.Text, s)
		}
		if all {
			return l, true
		}
	}
	return framework.StreamLine{}, false
}

// TestChart_RestartMidStep stops the controller with SIGTERM (a scale-down,
// as in a rolling update or a node drain) while a pr-review step, its branch
// already pushed, waits on the SCM API to open its PR, then starts it again.
// The Pod keeps the chart's 60s grace period and gets SIGTERM after its 5s
// shutdown delay. With no HTTP request in flight, the shutdown cancels the
// SCM call rather than waiting for it, the workers finish and the controller
// exits within seconds of the SIGTERM. After the restart
// the step runs again and opens one PR with one commit; the base branch is
// untouched until the merge, and the Bundle is Verified.
//
// Covers INST-SHUTDOWN-01, STEP-RESUME-01.
func TestChart_RestartMidStep(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	heads, ok := e.Git.(gitserver.Committer)
	require.True(t, ok, "git server %s does not report branch heads", e.Git.Kind())
	prCommits, ok := e.Git.(gitserver.PRCommitLister)
	require.True(t, ok, "git server %s does not list PR commits", e.Git.Kind())
	a := newArgoApp(t, e, "prod")
	base, err := heads.BranchSHA(ctx, a.repo)
	require.NoError(t, err)

	// An SCM API that never answers: a Pod no ingress may reach, so every
	// call to it hangs until it is cancelled or times out.
	holeLabels := map[string]string{"role": "scm-blackhole"}
	hole := e.Probe(t, a.ns, "scm-blackhole", holeLabels)
	_, err = e.Kube.NetworkingV1().NetworkPolicies(a.ns).Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "scm-blackhole"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: holeLabels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	holeURL := fmt.Sprintf("http://%s:9898", hole.IP)
	caller := e.Probe(t, a.ns, "caller", nil)
	framework.Eventually(t, time.Minute, "connections to the blackhole hang", func(context.Context) (bool, string) {
		res := caller.Curl(t, 3*time.Second, holeURL+"/api/v1/version")
		return res.Code == 0 && strings.Contains(res.Err, "(28)"), brief(res)
	})

	values := func(scm framework.Values) framework.Values {
		if scm == nil {
			return nsValues(a.ns, nil)
		}
		return nsValues(a.ns, framework.Values{"scm": scm})
	}
	r := e.InstallChart(t, releaseName(a.ns), a.ns, values(framework.Values{"apiURL": holeURL}))
	pod := runningPod(t, r)
	require.NotNil(t, pod.Spec.TerminationGracePeriodSeconds)
	assert.EqualValues(t, 60, *pod.Spec.TerminationGracePeriodSeconds, "the chart's default grace period")
	logs := r.FollowLogs(t, pod.Name)

	a.apply(t, a.resourcePipeline(map[string]string{"prod": "pr-review"}))
	image := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", image)
	var opening framework.StreamLine
	framework.Eventually(t, 2*time.Minute, "the step pushes and starts opening its PR", func(context.Context) (bool, string) {
		opening, ok = logLine(logs, `"step":"open-pr"`, `"message":"executing step"`)
		return ok, "open-pr has not started"
	})

	d := r.Deployment(t)
	scaleDown := client.MergeFrom(d.DeepCopy())
	zero := int32(0)
	d.Spec.Replicas = &zero
	scaledDown := time.Now()
	require.NoError(t, e.Client.Patch(ctx, d, scaleDown))
	select {
	case <-logs.Done():
	case <-time.After(90 * time.Second):
		t.Fatalf("the controller Pod %s still runs 90s after the scale-down", pod.Name)
	}
	// The log ends when the kubelet reports the container stopped, which can
	// lag the process's exit by seconds on a busy node (the container
	// runtime handles exit events in turn); the last line the controller
	// wrote dates its exit.
	stopped := time.Since(scaledDown)
	lines := logs.Lines()
	require.NotEmpty(t, lines, "the controller logged")
	sigterm, ok := logLine(logs, "Stopping and waiting for non leader election runnables")
	require.True(t, ok, "the controller logs the SIGTERM")
	exited := lines[len(lines)-1].At.Sub(sigterm.At)
	t.Logf("the controller got SIGTERM %s after the scale-down and wrote its last line %s later; the kubelet reported it stopped %s after the scale-down",
		sigterm.At.Sub(scaledDown).Round(time.Millisecond), exited.Round(time.Millisecond), stopped.Round(time.Millisecond))
	assert.GreaterOrEqual(t, sigterm.At.Sub(scaledDown), 5*time.Second, "the Pod serves for the chart's 5s shutdown delay before SIGTERM")
	assert.True(t, lines[len(lines)-1].At.After(sigterm.At), "the controller's last line comes after the SIGTERM")
	finished, ok := logLine(logs, "Wait completed, proceeding to shutdown the manager")
	assert.True(t, ok, "the manager finishes its shutdown, so the controller exits rather than being killed")

	received, ok := logLine(logs, "Shutdown signal received, waiting for all workers to finish")
	assert.True(t, ok, "the controller logs the SIGTERM and waits for its workers")
	workers, ok := logLine(logs, "All workers finished")
	assert.True(t, ok, "the workers finish before the controller exits")
	assert.True(t, !received.At.Before(sigterm.At) && !workers.At.Before(received.At) && !finished.At.Before(workers.At),
		"the shutdown runs in order after the SIGTERM: signal %s, workers finished %s, manager done %s",
		received.At.Sub(sigterm.At).Round(time.Millisecond), workers.At.Sub(sigterm.At).Round(time.Millisecond), finished.At.Sub(sigterm.At).Round(time.Millisecond))
	cancelled, ok := logLine(logs, `"message":"step failed, will retry"`, "context canceled")
	if assert.True(t, ok, "the shutdown cancels the open-pr call in flight") {
		assert.False(t, cancelled.At.Before(sigterm.At), "the call ends after the SIGTERM")
		assert.Less(t, cancelled.At.Sub(opening.At), 30*time.Second, "the call is cancelled, not timed out")
	}
	assert.Less(t, exited, 10*time.Second, "with no request in flight the controller exits within seconds of the SIGTERM, long before the 60s SIGKILL")

	promotion := fmt.Sprintf("kardinal/%s/prod", bundle)
	kustomization, err := e.Git.ReadFile(ctx, a.repo, promotion, fixtures.Path("prod")+"/kustomization.yaml")
	require.NoError(t, err, "the step pushed %s before the restart", promotion)
	assert.Contains(t, string(kustomization), fixtures.V2)
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs, "no PR is open before the restart")

	r.Upgrade(t, values(nil))
	runningPod(t, r)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	prs, err = e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	require.Len(t, prs, 1, "the restarted step opens one PR")
	pr := prs[0]
	assert.Equal(t, promotion, pr.Head)
	assert.Equal(t, "open", pr.State)
	commits, err := prCommits.PRCommits(ctx, a.repo, pr.Number)
	require.NoError(t, err)
	assert.Len(t, commits, 1, "the PR has one commit: the re-run replaced the first push")
	now, err := heads.BranchSHA(ctx, a.repo)
	require.NoError(t, err)
	assert.Equal(t, base, now, "the base branch is untouched before the merge")

	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), image, syncTimeout)
	prs, err = e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Len(t, prs, 1, "no other PR is opened")
}

// drainScript runs in a probe Pod. It opens three kinds of request to the
// controller Pod at $5 in the background, and logs each connect (nc -v) to
// /tmp/<a|b|c>.err:
//   - a: a webhook event (head $1, body $2) whose body is sent once /tmp/go
//     exists; its response goes to /tmp/a.out;
//   - b: every 2s until /tmp/go exists, a webhook event (head $3) whose body
//     never comes, held open until /tmp/end exists;
//   - c: a metrics scrape (head $4) whose body never comes, held open until
//     /tmp/end exists.
const drainScript = `cd /tmp
( { printf '%s' "$1"; until [ -e go ]; do sleep 1; done; printf '%s' "$2"; } | timeout 80 nc -v "$5" 8083 >a.out 2>a.err ) </dev/null >/dev/null 2>&1 &
( while [ ! -e go ]; do { printf '%s' "$3"; until [ -e end ]; do sleep 1; done; } | timeout 80 nc -v "$5" 8083 >/dev/null 2>>b.err & sleep 2; done; wait ) </dev/null >/dev/null 2>&1 &
( { printf '%s' "$4"; until [ -e end ]; do sleep 1; done; } | timeout 80 nc -v "$5" 8080 >/dev/null 2>c.err ) </dev/null >/dev/null 2>&1 &
`

// TestChart_ShutdownDrain stops the controller with SIGTERM while its git
// push is in flight (the git server holds it in a pre-receive hook) and HTTP
// requests are open: a webhook event whose body comes after the SIGTERM,
// webhook events whose body never comes (a new one every 2s, so one is
// younger than its 30s read timeout allows to end before the drain does),
// and a metrics scrape whose body never comes. The webhook server stops
// accepting connections, answers the event in flight and gives up on the
// others after 20s. Reconciles run until then; then the push is cancelled
// rather than waited for. The manager gives up 30s after the SIGTERM (the
// metrics server would wait a minute for its scrape) and the controller
// exits, before the chart's 60s SIGKILL. After the restart the step pushes
// again and the Bundle is Verified.
//
// Covers INST-SHUTDOWN-02, INST-SHUTDOWN-03.
func TestChart_ShutdownDrain(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	heads, ok := e.Git.(gitserver.Committer)
	require.True(t, ok, "git server %s does not report branch heads", e.Git.Kind())
	a := newArgoApp(t, e, "prod")
	base, err := heads.BranchSHA(ctx, a.repo)
	require.NoError(t, err)

	// With a webhook secret the webhook handler reads each event's body.
	secret(t, e, a.ns, "scm-webhook", "secret", randomHex(t, 16))
	values := nsValues(a.ns, framework.Values{"webhook": framework.Values{"secretRef": framework.Values{"name": "scm-webhook"}}})
	r := e.InstallChart(t, releaseName(a.ns), a.ns, values)
	pod := runningPod(t, r)
	logs := r.FollowLogs(t, pod.Name)
	caller := e.Probe(t, a.ns, "caller", nil)

	stalled, release := e.StallPushes(t, a.repo)
	a.apply(t, a.resourcePipeline(nil))
	image := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", image)
	framework.Eventually(t, 2*time.Minute, "the git server holds the step's push", func(context.Context) (bool, string) {
		return stalled(), "no push is held"
	})

	ip := pod.Status.PodIP
	event := func(length int, closing bool) string {
		head := fmt.Sprintf("POST /webhook/scm HTTP/1.1\r\nHost: %s:8083\r\nContent-Type: application/json\r\n"+
			"X-Gitea-Event: pull_request\r\nX-Gitea-Signature: %s\r\nContent-Length: %d\r\n", ip, randomHex(t, 32), length)
		if closing {
			head += "Connection: close\r\n"
		}
		return head + "\r\n"
	}
	body := `{"action":"closed"}`
	scrape := fmt.Sprintf("POST /metrics HTTP/1.1\r\nHost: %s:8080\r\nContent-Length: 64\r\n\r\n", ip)
	out, err := caller.Exec(t, "sh", "-c", drainScript, "sh", event(len(body), true), body, event(64, false), scrape, ip)
	require.NoError(t, err, out)
	framework.Eventually(t, 30*time.Second, "the requests are connected", func(context.Context) (bool, string) {
		out, _ := caller.Exec(t, "sh", "-c", "cd /tmp && for f in a b c; do grep -q succeeded $f.err 2>/dev/null && echo $f; done")
		return strings.Join(strings.Fields(out), " ") == "a b c", "connected: " + out
	})

	d := r.Deployment(t)
	scaleDown := client.MergeFrom(d.DeepCopy())
	zero := int32(0)
	d.Spec.Replicas = &zero
	sigterm := time.Now()
	require.NoError(t, e.Client.Patch(ctx, d, scaleDown))
	var drain framework.StreamLine
	framework.Eventually(t, 30*time.Second, "the webhook server starts draining", func(context.Context) (bool, string) {
		drain, ok = logLine(logs, `"server":"webhook"`, `"message":"shutting down server"`)
		return ok, "the webhook server has not logged shutting down server"
	})
	out, err = caller.Exec(t, "touch", "/tmp/go")
	require.NoError(t, err, out)
	// The server's listener closes with the drain, so the controller refuses a
	// new connection; the process (still draining) answers with a reset.
	var refused framework.CurlResult
	framework.Eventually(t, 10*time.Second, "the webhook server refuses new connections", func(context.Context) (bool, string) {
		refused = caller.Curl(t, 3*time.Second, fmt.Sprintf("http://%s:8083/webhook/scm", ip))
		return refused.Refused(), fmt.Sprintf("the new connection was not refused: %d %s", refused.Code, refused.Err)
	})
	select {
	case <-logs.Done():
	case <-time.After(90 * time.Second):
		t.Fatalf("the controller Pod %s still runs 90s after the scale-down", pod.Name)
	}
	exited := time.Since(sigterm)
	t.Logf("the controller exited %s after the scale-down", exited.Round(time.Millisecond))
	out, err = caller.Exec(t, "touch", "/tmp/end")
	require.NoError(t, err, out)

	at := func(what string, substrs ...string) time.Time {
		t.Helper()
		l, ok := logLine(logs, substrs...)
		require.True(t, ok, "the controller logs %s", what)
		return l.At
	}
	stop := at("the SIGTERM", "Stopping and waiting for non leader election runnables")
	answered := at("the refused event", `"message":"webhook signature invalid or parse error"`)
	assert.True(t, answered.After(drain.At), "the webhook server handles the event whose body came after the drain began")
	response, _ := caller.Exec(t, "cat", "/tmp/a.out")
	assert.Contains(t, response, "HTTP/1.1 401", "the event in flight gets its response")
	gaveUp := at("the webhook server giving up", "webhook server: shutdown: context deadline exceeded")
	assert.InDelta(t, 20, gaveUp.Sub(drain.At).Seconds(), 3, "the webhook server gives its requests in flight 20s")
	reconcilers := at("the reconcilers stopping", "Stopping and waiting for leader election runnables")
	assert.GreaterOrEqual(t, reconcilers.Sub(drain.At).Seconds(), 18.0, "reconciles run on while the webhook server drains")
	cancelled := at("the git push cancelled", `"message":"step failed, will retry"`, "git-push", "context canceled")
	assert.True(t, cancelled.After(reconcilers.Add(-time.Second)), "the push is cancelled once the reconcilers stop, not before")
	at("the metrics server waiting for its scrape", "Shutting down metrics server with timeout of 1 minute")
	bound := at("the manager giving up", "failed waiting for all runnables to end within grace period of 30s")
	t.Logf("after the drain began: event answered %s, webhook server gave up %s, reconcilers stopped %s, push cancelled %s; manager gave up %s after the SIGTERM",
		answered.Sub(drain.At).Round(time.Millisecond), gaveUp.Sub(drain.At).Round(time.Millisecond),
		reconcilers.Sub(drain.At).Round(time.Millisecond), cancelled.Sub(drain.At).Round(time.Millisecond), bound.Sub(stop).Round(time.Millisecond))
	assert.InDelta(t, 30, bound.Sub(stop).Seconds(), 3, "the manager gives the whole shutdown 30s")
	assert.Less(t, exited, 50*time.Second, "the controller exits on its own, before the chart's 60s SIGKILL")

	release()
	r.Upgrade(t, values)
	runningPod(t, r)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), image, syncTimeout)
	now, err := heads.BranchSHA(ctx, a.repo)
	require.NoError(t, err)
	assert.NotEqual(t, base, now, "the restarted step pushed")
}
