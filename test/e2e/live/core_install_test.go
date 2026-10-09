//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// kroNamespace is where hack/install-kro.sh installs kro.
const kroNamespace = "kro-system"

// crdGVR is the CustomResourceDefinition resource.
var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// The kardinal CRDs: the four the docs call the core (docs/installation.md)
// and the eight secondary ones.
var (
	primaryKinds   = []string{"Pipeline", "Bundle", "PolicyGate", "PromotionStep"}
	secondaryKinds = []string{"Subscription", "NotificationHook", "MetricCheck", "ChangeWindow",
		"ScheduleClock", "RollbackPolicy", "AuditEvent", "PRStatus", "HookRun", "Approval", "ImageVerification"}
)

// TestCore_KroInstalled checks the kro hack/install-kro.sh installs
// (docs/installation.md, Install kro). The kro Deployment in kro-system runs
// the version the script pins (KRO_VERSION) with the GraphKind feature gate,
// and its pod is Ready. The CRDs graphs.kro.run,
// graphrevisions.internal.kro.run and resourcegraphdefinitions.kro.run are
// Established and serve v1alpha1. kro runs in rbac aggregation mode: its
// kro:controller ClusterRole aggregates the kardinal chart's ClusterRole, so
// it holds every rule of it. A Bundle's Graph is then reconciled by that kro:
// it is Accepted (Compiled), applied as the namespace's kardinal-graph
// ServiceAccount, carries kro's finalizer, lists the PromotionStep it created
// among its managed resources and becomes Ready.
//
// Covers INST-KRO-01.
func TestCore_KroInstalled(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ver := kroPin(t)

	dep, err := e.Kube.AppsV1().Deployments(kroNamespace).Get(ctx, "kro", metav1.GetOptions{})
	require.NoError(t, err, "the kro Deployment in %s", kroNamespace)
	assert.Equal(t, "v"+ver, dep.Labels["app.kubernetes.io/version"], "kro version label")
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	c := dep.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "registry.k8s.io/kro/kro:v"+ver, c.Image, "kro image")
	assert.Contains(t, featureGates(c.Args), "GraphKind=true", "kro args %v", c.Args)
	assert.True(t, deploymentAvailable(dep), "the kro Deployment is Available: %+v", dep.Status.Conditions)
	pod := e.RunningPod(t, kroNamespace, "app.kubernetes.io/name=kro,app.kubernetes.io/instance=kro")
	p, err := e.Kube.CoreV1().Pods(kroNamespace).Get(ctx, pod, metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, podReady(p), "the kro pod %s is Ready", pod)

	for _, name := range []string{"graphs.kro.run", "graphrevisions.internal.kro.run", "resourcegraphdefinitions.kro.run"} {
		crd := installedCRD(t, e, name)
		assert.True(t, crdCondition(crd, apiextensionsv1.Established), "%s is Established", name)
		assert.Equal(t, []string{"v1alpha1"}, servedVersions(crd), "%s served versions", name)
	}

	aggregated, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, "kro:controller", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, aggregated.AggregationRule, "kro:controller aggregates (rbac.mode=aggregation)")
	sel := metav1.LabelSelector{MatchLabels: map[string]string{"rbac.kro.run/aggregate-to-controller": "true"}}
	assert.Contains(t, aggregated.AggregationRule.ClusterRoleSelectors, sel)
	watch, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, "kardinal-promoter-kro-watch", metav1.GetOptions{})
	require.NoError(t, err, "the kardinal chart's ClusterRole for kro")
	assert.Equal(t, "true", watch.Labels["rbac.kro.run/aggregate-to-controller"])
	for _, r := range watch.Rules {
		assert.True(t, hasRule(aggregated.Rules, r), "kro:controller holds %v %v %v", r.APIGroups, r.Resources, r.Verbs)
	}

	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	name := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, name, "the Bundle's status.graphRef")
	var g *unstructured.Unstructured
	framework.Eventually(t, 2*time.Minute, "the Graph to be Ready", func(ctx context.Context) (bool, string) {
		var err error
		g, err = e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		st, reason, msg := graphCondition(g, "Ready")
		return st == "True", fmt.Sprintf("Ready=%s %s: %s", st, reason, msg)
	})
	assert.Equal(t, bundle, g.GetLabels()["kardinal.io/bundle"])
	st, reason, _ := graphCondition(g, "Accepted")
	assert.Equal(t, "True Compiled", st+" "+reason, "Accepted condition")
	st, _, msg := graphCondition(g, "ResourcesConverged")
	assert.Equal(t, "True", st, "ResourcesConverged: %s", msg)
	sa, _, _ := unstructured.NestedString(g.Object, "status", "appliedServiceAccount")
	assert.Equal(t, "system:serviceaccount:"+a.ns+":kardinal-graph", sa, "status.appliedServiceAccount")
	assert.Contains(t, g.GetFinalizers(), "kro.run/graph-finalizer")
	managed, _, _ := unstructured.NestedSlice(g.Object, "status", "managedResources")
	var kinds []string
	stepManaged := false
	for _, m := range managed {
		mr, _ := m.(map[string]interface{})
		kinds = append(kinds, fmt.Sprint(mr["kind"]))
		if mr["kind"] == "PromotionStep" && mr["name"] == ps.Name && mr["uid"] == string(ps.UID) {
			stepManaged = true
		}
	}
	assert.True(t, stepManaged, "managedResources lists the step %s; kinds %v", ps.Name, kinds)
}

// TestCore_KroTuned checks the kro tuning hack/install-kro.sh applies
// (docs/installation.md, Install kro; ledger gap G9): the kro Deployment runs
// 8 Graph workers instead of kro's 1, with client QPS 300 and burst 500, so
// one large promotion does not hold up every other Graph, and the memory
// limit and request the script sets (G15).
//
// Covers INST-KRO-02, INST-KRO-03.
func TestCore_KroTuned(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	dep, err := e.Kube.AppsV1().Deployments(kroNamespace).Get(context.Background(), "kro", metav1.GetOptions{})
	require.NoError(t, err, "the kro Deployment in %s", kroNamespace)
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	env := map[string]string{}
	for _, v := range dep.Spec.Template.Spec.Containers[0].Env {
		env[v.Name] = v.Value
	}
	assert.Equal(t, "8", env["KRO_GRAPH_CONCURRENT_RECONCILES"], "Graph workers (--graph-concurrent-reconciles)")
	assert.Equal(t, "300", env["KRO_CLIENT_QPS"], "client QPS")
	assert.Equal(t, "500", env["KRO_CLIENT_BURST"], "client burst")
	mem := dep.Spec.Template.Spec.Containers[0].Resources
	assert.Equal(t, "2Gi", mem.Limits.Memory().String(), "memory limit (KRO_MEMORY_LIMIT)")
	assert.Equal(t, "768Mi", mem.Requests.Memory().String(), "memory request (KRO_MEMORY_REQUEST)")
	assert.True(t, deploymentAvailable(dep), "the kro Deployment is Available: %+v", dep.Status.Conditions)
}

// kroPin is KRO_VERSION's default in hack/install-kro.sh.
func kroPin(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../hack/install-kro.sh")
	require.NoError(t, err)
	m := regexp.MustCompile(`KRO_VERSION="\$\{KRO_VERSION:-([^}]+)\}"`).FindSubmatch(raw)
	require.NotNil(t, m, "KRO_VERSION in hack/install-kro.sh")
	return string(m[1])
}

// featureGates lists the gates of every --feature-gates flag in args.
func featureGates(args []string) []string {
	var out []string
	for i, a := range args {
		v, ok := strings.CutPrefix(a, "--feature-gates=")
		if !ok && a == "--feature-gates" && i+1 < len(args) {
			v, ok = args[i+1], true
		}
		if ok {
			out = append(out, strings.Split(v, ",")...)
		}
	}
	return out
}

// deploymentAvailable reports the Deployment's Available condition.
func deploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podReady reports the pod's Ready condition.
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// hasRule reports whether one of rules grants every verb of want on every
// resource of want.
func hasRule(rules []rbacv1.PolicyRule, want rbacv1.PolicyRule) bool {
	for _, group := range want.APIGroups {
		for _, res := range want.Resources {
			for _, verb := range want.Verbs {
				granted := false
				for _, r := range rules {
					if slices.Contains(r.APIGroups, group) && slices.Contains(r.Resources, res) &&
						(slices.Contains(r.Verbs, verb) || slices.Contains(r.Verbs, "*")) {
						granted = true
						break
					}
				}
				if !granted {
					return false
				}
			}
		}
	}
	return true
}

// graphCondition returns the status, reason and message of the Graph's
// condition typ.
func graphCondition(g *unstructured.Unstructured, typ string) (status, reason, message string) {
	conds, _, _ := unstructured.NestedSlice(g.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]interface{})
		if m["type"] == typ {
			return fmt.Sprint(m["status"]), fmt.Sprint(m["reason"]), fmt.Sprint(m["message"])
		}
	}
	return "", "", ""
}

// toCRD decodes an unstructured CRD through JSON, so that a manifest read from
// YAML and an object read from the API server compare equal.
func toCRD(t *testing.T, obj map[string]interface{}) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := json.Marshal(obj)
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, json.Unmarshal(raw, &crd))
	return &crd
}

// indentJSON renders v as indented JSON, so a mismatch shows as a line diff.
func indentJSON(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	return string(raw)
}

// installedCRD reads the CRD name from the API server.
func installedCRD(t *testing.T, e *framework.Env, name string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	u, err := e.Dynamic.Resource(crdGVR).Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "CRD %s", name)
	return toCRD(t, u.Object)
}

func crdCondition(crd *apiextensionsv1.CustomResourceDefinition, typ apiextensionsv1.CustomResourceDefinitionConditionType) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == typ {
			return c.Status == apiextensionsv1.ConditionTrue
		}
	}
	return false
}

func servedVersions(crd *apiextensionsv1.CustomResourceDefinition) []string {
	var out []string
	for _, v := range crd.Spec.Versions {
		if v.Served {
			out = append(out, v.Name)
		}
	}
	return out
}

// crdFiles reads config/crd/bases, keyed by kind.
func crdFiles(t *testing.T) map[string]*unstructured.Unstructured {
	t.Helper()
	files, err := filepath.Glob("../../../config/crd/bases/*.yaml")
	require.NoError(t, err)
	out := map[string]*unstructured.Unstructured{}
	for _, f := range files {
		for _, obj := range framework.Manifests(t, f) {
			require.Equal(t, "CustomResourceDefinition", obj.GetKind(), f)
			kind, _, _ := unstructured.NestedString(obj.Object, "spec", "names", "kind")
			out[kind] = obj
		}
	}
	return out
}

// TestCore_CRDsInstalled checks the kardinal CRDs in config/crd/bases
// (docs/installation.md: the chart's crds/ and the 13 CRDs an upgrade
// applies). The directory holds exactly the 13 kardinal.io CRDs. Each of the
// four core ones (Pipeline, Bundle, PolicyGate, PromotionStep) is accepted by
// a server-side apply, and the installed CRD is the file: Established, its
// names accepted, the same group, names, scope and versions (schema, printer
// columns, subresources), served and stored as v1alpha1. Discovery lists the
// resource with its kind, short names and verbs, and kubectl get renders the
// file's printer columns.
//
// Covers INST-CRD-01.
func TestCore_CRDsInstalled(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	files := crdFiles(t)
	var kinds []string
	for k := range files {
		kinds = append(kinds, k)
	}
	want := append(append([]string(nil), primaryKinds...), secondaryKinds...)
	sort.Strings(want)
	sort.Strings(kinds)
	assert.Equal(t, want, kinds, "the CRDs in config/crd/bases")
	checkCRDs(t, e, files, primaryKinds)
}

// TestCore_SecondaryCRDsServed checks the ten secondary kardinal CRDs
// (Subscription, NotificationHook, MetricCheck, ChangeWindow, ScheduleClock,
// RollbackPolicy, AuditEvent, PRStatus, HookRun, Approval, ImageVerification) the same way: config/crd/bases has
// each, a server-side apply accepts it, the installed CRD is the file, and the
// API serves the resource (discovery, kubectl get with the printer columns, a
// list).
//
// Covers INST-CRD-02.
func TestCore_SecondaryCRDsServed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	checkCRDs(t, e, crdFiles(t), secondaryKinds)
}

// checkCRDs checks that each CRD of kinds in files is installed as the file
// says and served.
func checkCRDs(t *testing.T, e *framework.Env, files map[string]*unstructured.Unstructured, kinds []string) {
	t.Helper()
	ctx := context.Background()
	ns := e.Namespace(t)
	served, err := e.Kube.Discovery().ServerResourcesForGroupVersion(v1alpha1.GroupVersion.String())
	require.NoError(t, err, "discovery of %s", v1alpha1.GroupVersion)
	resources := map[string]metav1.APIResource{}
	for _, r := range served.APIResources {
		resources[r.Name] = r
	}
	for _, kind := range kinds {
		obj, ok := files[kind]
		if !assert.True(t, ok, "config/crd/bases has the %s CRD", kind) {
			continue
		}
		_, err := e.DryRunApply(ctx, obj.DeepCopy())
		assert.NoError(t, err, "server-side apply of the %s CRD", kind)

		file := toCRD(t, obj.Object)
		got := installedCRD(t, e, obj.GetName())
		assert.True(t, crdCondition(got, apiextensionsv1.Established), "%s: Established", kind)
		assert.True(t, crdCondition(got, apiextensionsv1.NamesAccepted), "%s: NamesAccepted", kind)
		assert.Equal(t, "kardinal.io", got.Spec.Group, "%s: group", kind)
		assert.Equal(t, file.Spec.Names, got.Spec.Names, "%s: names", kind)
		assert.Equal(t, file.Spec.Names, got.Status.AcceptedNames, "%s: accepted names", kind)
		assert.Equal(t, file.Spec.Scope, got.Spec.Scope, "%s: scope", kind)
		assert.Equal(t, indentJSON(t, file.Spec.Versions), indentJSON(t, got.Spec.Versions),
			"%s: versions (schema, columns, subresources); a stale CRD means the cluster needs the chart's crds/", kind)
		assert.Equal(t, []string{"v1alpha1"}, servedVersions(got), "%s: served", kind)
		assert.Equal(t, []string{"v1alpha1"}, got.Status.StoredVersions, "%s: stored", kind)

		plural := got.Spec.Names.Plural
		r, ok := resources[plural]
		if assert.True(t, ok, "discovery lists %s", plural) {
			assert.Equal(t, kind, r.Kind, "%s: discovery kind", plural)
			assert.Equal(t, got.Spec.Scope == apiextensionsv1.NamespaceScoped, r.Namespaced, "%s: namespaced", plural)
			assert.ElementsMatch(t, got.Spec.Names.ShortNames, r.ShortNames, "%s: short names", plural)
			assert.Subset(t, []string(r.Verbs), []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				"%s: verbs", plural)
		}
		if got.Spec.Versions[0].Subresources != nil && got.Spec.Versions[0].Subresources.Status != nil {
			_, ok := resources[plural+"/status"]
			assert.True(t, ok, "discovery lists %s/status", plural)
		}

		gvr := v1alpha1.GroupVersion.WithResource(plural)
		if got.Spec.Scope == apiextensionsv1.ClusterScoped {
			_, err := e.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{})
			assert.NoError(t, err, "list %s", plural)
			continue
		}
		_, err = e.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		assert.NoError(t, err, "list %s in %s", plural, ns)
		table := e.GetTable(t, "kardinal.io", "v1alpha1", plural, ns)
		wantCols := []string{"Name"}
		for _, c := range got.Spec.Versions[0].AdditionalPrinterColumns {
			if c.Priority == 0 {
				wantCols = append(wantCols, c.Name)
			}
		}
		assert.Subset(t, table.Columns, wantCols, "kubectl get %s columns", plural)
	}
}

// TestCore_QuickstartManifestsAccepted applies every manifest of
// examples/quickstart unchanged, as the README's kubectl apply would, with a
// server-side dry run: the Pipeline (pipeline.yaml), the platform-policies
// Namespace and the two PolicyGates (policy-gates.yaml), the Bundle
// (bundle.yaml) and the Argo CD ApplicationSet (argocd-applications.yaml).
// The API server accepts each, and every field the manifest sets is in the
// object it would store: the schema prunes none of them.
//
// Covers INST-QUICKSTART-01.
func TestCore_QuickstartManifestsAccepted(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	e.EnsureNamespace(t, framework.PolicyNamespace)
	ctx := context.Background()
	var applied []string
	for _, f := range []string{"pipeline.yaml", "policy-gates.yaml", "bundle.yaml", "argocd-applications.yaml"} {
		for _, obj := range framework.Manifests(t, "../../../examples/quickstart/"+f) {
			id := fmt.Sprintf("%s %s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())
			applied = append(applied, id)
			got, err := e.DryRunApply(ctx, obj.DeepCopy())
			if !assert.NoError(t, err, "%s: apply %s", f, id) {
				continue
			}
			for _, diff := range missingFields("", obj.Object, got.Object) {
				t.Errorf("%s: %s: %s", f, id, diff)
			}
		}
	}
	assert.ElementsMatch(t, []string{
		"Pipeline default/kardinal-test-app",
		"Namespace /platform-policies",
		"PolicyGate platform-policies/no-weekend-deploys",
		"PolicyGate platform-policies/require-uat-soak",
		"Bundle default/kardinal-test-app-sha-9349a3f",
		"ApplicationSet argocd/kardinal-test-app",
	}, applied, "the quickstart manifests")
}

// missingFields lists the fields of want that got lacks or holds with another
// value. Leaves compare as text, since YAML and JSON numbers decode to
// different Go types.
func missingFields(path string, want, got interface{}) []string {
	switch w := want.(type) {
	case map[string]interface{}:
		g, ok := got.(map[string]interface{})
		if !ok {
			return []string{fmt.Sprintf("%s: want an object, got %v", path, got)}
		}
		var out []string
		for k, v := range w {
			gv, ok := g[k]
			if !ok {
				out = append(out, fmt.Sprintf("%s.%s: dropped", path, k))
				continue
			}
			out = append(out, missingFields(path+"."+k, v, gv)...)
		}
		return out
	case []interface{}:
		g, ok := got.([]interface{})
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: want %d items, got %v", path, len(w), got)}
		}
		var out []string
		for i := range w {
			out = append(out, missingFields(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
		}
		return out
	default:
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return []string{fmt.Sprintf("%s: want %v, got %v", path, want, got)}
		}
		return nil
	}
}
