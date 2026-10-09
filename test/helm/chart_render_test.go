// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

// Offline render tests for the Helm chart. Each test runs `helm template`
// and decodes the output into typed Kubernetes objects, so a regression in
// RBAC, ports, flags, values or CRDs fails `go test ./test/helm/...` without
// a cluster.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	czap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"
)

const releaseNS = "kardinal-system"

// renderedDoc is one manifest from `helm template`.
type renderedDoc struct {
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	raw        []byte // JSON
}

func chartPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "chart", "kardinal-promoter")
}

// helmTemplate runs `helm template <release> <chart> --namespace kardinal-system <args>`.
func helmTemplate(t *testing.T, release string, args ...string) (string, error) {
	t.Helper()
	cmdArgs := append([]string{"template", release, chartPath(t), "--namespace", releaseNS}, args...)
	out, err := exec.Command(helmBin(t), cmdArgs...).CombinedOutput()
	return string(out), err
}

// render runs helm template and fails the test when rendering fails.
func render(t *testing.T, release string, args ...string) []renderedDoc {
	t.Helper()
	out, err := helmTemplate(t, release, args...)
	require.NoError(t, err, "helm template %v:\n%s", args, out)
	return parseDocs(t, out)
}

var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

func parseDocs(t *testing.T, out string) []renderedDoc {
	t.Helper()
	var docs []renderedDoc
	for _, part := range docSeparator.Split(out, -1) {
		j, err := yaml.YAMLToJSON([]byte(part))
		require.NoError(t, err, "rendered manifest is not valid YAML:\n%s", part)
		var head struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if len(bytes.TrimSpace(j)) == 0 || string(j) == "null" {
			continue
		}
		require.NoError(t, json.Unmarshal(j, &head))
		if head.Kind == "" {
			continue
		}
		docs = append(docs, renderedDoc{
			APIVersion: head.APIVersion, Kind: head.Kind,
			Name: head.Metadata.Name, Namespace: head.Metadata.Namespace, raw: j,
		})
	}
	return docs
}

func docsOfKind(docs []renderedDoc, kind string) []renderedDoc {
	var out []renderedDoc
	for _, d := range docs {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// decodeStrict decodes a rendered manifest into a typed object and fails on
// any field the type does not have (what the API server's strict field
// validation rejects on `helm install`).
func decodeStrict(t *testing.T, d renderedDoc, into interface{}) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(d.raw))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(into), "%s %s does not match its schema:\n%s", d.Kind, d.Name, d.raw)
}

func controllerContainer(t *testing.T, docs []renderedDoc) corev1.Container {
	t.Helper()
	deps := docsOfKind(docs, "Deployment")
	require.Len(t, deps, 1, "chart must render exactly one Deployment")
	var dep appsv1.Deployment
	decodeStrict(t, deps[0], &dep)
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == "controller" {
			return c
		}
	}
	t.Fatal("Deployment has no controller container")
	return corev1.Container{}
}

// argValues maps --flag to its value for every --flag=value arg.
func argValues(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, a := range c.Args {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		k, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		m[k] = v
	}
	return m
}

func envByName(c corev1.Container) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		m[e.Name] = e
	}
	return m
}

// allFeatures turns on every optional template so a test sees every object.
var allFeatures = []string{
	"--set", "networkPolicy.enabled=true",
	"--set", "demo.enabled=true",
	"--set", "prometheusRule.enabled=true",
	"--set", "grafanaDashboard.enabled=true",
	"--set", "replicaCount=2",
	"--set", "ui.auth.tokenReview=true",
	"--set", "rbac.argocdApplicationsWrite=true",
	// Deprecated no-op (#1278): kept so the schema still accepts it and
	// TestChartRBACLeastPrivilege proves it grants nothing.
	"--set", "rbac.integrationTestJobs=true",
}

// ── C08-api-config-04, -16, -21: no ValidatingAdmissionPolicy ─────────────────

// TestChartRendersNoValidatingAdmissionPolicy: the chart's VAPs denied every
// Pipeline (spec.gitRepo does not exist), denied promote/rollback Bundles and
// valid durations, needed Kubernetes 1.30, and collided across releases.
// Validation lives in the CRD schema (api/v1alpha1/crd_schema_test.go);
// validatingAdmissionPolicy.enabled is kept as a deprecated no-op so existing
// `--set validatingAdmissionPolicy.enabled=false` installs keep working.
func TestChartRendersNoValidatingAdmissionPolicy(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--set", "validatingAdmissionPolicy.enabled=true"},
		{"--set", "validatingAdmissionPolicy.enabled=false"},
	} {
		docs := render(t, "kardinal-promoter", args...)
		for _, d := range docs {
			assert.NotContains(t, d.APIVersion, "admissionregistration.k8s.io",
				"args %v: chart must not render %s %s", args, d.Kind, d.Name)
		}
	}
}

// TestChartClusterScopedNamesUniquePerRelease: two releases (the documented
// one-install-per-team recipe) must not own the same cluster-scoped object.
func TestChartClusterScopedNamesUniquePerRelease(t *testing.T) {
	clusterScoped := map[string]bool{
		"ClusterRole": true, "ClusterRoleBinding": true,
		"ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true,
		"ValidatingWebhookConfiguration": true, "MutatingWebhookConfiguration": true,
	}
	names := func(release string, args ...string) map[string]bool {
		out := map[string]bool{}
		for _, d := range render(t, release, append(append([]string{}, allFeatures...), args...)...) {
			if clusterScoped[d.Kind] {
				out[d.Kind+"/"+d.Name] = true
			}
		}
		return out
	}
	for _, mode := range [][]string{nil, {"--set", "controller.watchNamespace=" + releaseNS}} {
		a := names("team-a", mode...)
		b := names("team-b", mode...)
		require.NotEmpty(t, a)
		for n := range a {
			assert.False(t, b[n], "mode %v: releases team-a and team-b both render %s", mode, n)
		}
	}
}

// ── C08-api-config-07: CRDs ship with the chart ───────────────────────────────

// TestChartShipsGeneratedCRDs: a fresh `helm install` must create the CRDs
// (Helm installs crds/ before templates), crds/ must match the generated
// config/crd/bases byte for byte, and every kardinal.io object the chart
// renders must have its CRD in crds/.
func TestChartShipsGeneratedCRDs(t *testing.T) {
	root := repoRoot(t)
	generated, err := filepath.Glob(filepath.Join(root, "config", "crd", "bases", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, generated)
	shipped, err := filepath.Glob(filepath.Join(chartPath(t), "crds", "*.yaml"))
	require.NoError(t, err)

	base := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			out = append(out, filepath.Base(p))
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, base(generated), base(shipped),
		"chart/kardinal-promoter/crds must hold exactly the generated CRDs (run `make manifests`)")

	kinds := map[string]bool{}
	for _, g := range generated {
		want, err := os.ReadFile(g)
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(chartPath(t), "crds", filepath.Base(g)))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got),
			"%s is stale in chart/kardinal-promoter/crds (run `make manifests`)", filepath.Base(g))
		var crd struct {
			Spec struct {
				Names struct {
					Kind string `json:"kind"`
				} `json:"names"`
			} `json:"spec"`
		}
		require.NoError(t, yaml.Unmarshal(want, &crd))
		kinds[crd.Spec.Names.Kind] = true
	}

	out, err := helmTemplate(t, "kardinal-promoter", append([]string{"--include-crds"}, allFeatures...)...)
	require.NoError(t, err, out)
	docs := parseDocs(t, out)
	assert.Len(t, docsOfKind(docs, "CustomResourceDefinition"), len(generated),
		"helm install must create every kardinal CRD")
	for _, d := range docs {
		if strings.HasPrefix(d.APIVersion, "kardinal.io/") {
			assert.True(t, kinds[d.Kind], "chart renders %s %s but ships no CRD for it", d.Kind, d.Name)
		}
	}
}

// ── C08-api-config-08, -09, -15: RBAC ─────────────────────────────────────────

type rbacView struct {
	roles        map[string]rbacv1.Role // namespace/name
	clusterRoles map[string]rbacv1.ClusterRole
	crbs         []rbacv1.ClusterRoleBinding
	rbs          []rbacv1.RoleBinding
}

func newRBACView(t *testing.T, docs []renderedDoc) rbacView {
	t.Helper()
	v := rbacView{roles: map[string]rbacv1.Role{}, clusterRoles: map[string]rbacv1.ClusterRole{}}
	for _, d := range docs {
		switch d.Kind {
		case "Role":
			var r rbacv1.Role
			decodeStrict(t, d, &r)
			v.roles[r.Namespace+"/"+r.Name] = r
		case "ClusterRole":
			var r rbacv1.ClusterRole
			decodeStrict(t, d, &r)
			v.clusterRoles[r.Name] = r
		case "ClusterRoleBinding":
			var b rbacv1.ClusterRoleBinding
			decodeStrict(t, d, &b)
			v.crbs = append(v.crbs, b)
		case "RoleBinding":
			var b rbacv1.RoleBinding
			decodeStrict(t, d, &b)
			v.rbs = append(v.rbs, b)
		}
	}
	return v
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s || x == "*" {
			return true
		}
	}
	return false
}

func ruleAllows(rules []rbacv1.PolicyRule, group, resource, verb, name string) bool {
	for _, r := range rules {
		if !has(r.APIGroups, group) || !has(r.Resources, resource) || !has(r.Verbs, verb) {
			continue
		}
		if len(r.ResourceNames) > 0 && (name == "" || !has(r.ResourceNames, name)) {
			continue
		}
		return true
	}
	return false
}

func boundTo(subjects []rbacv1.Subject, saNS, sa string) bool {
	for _, s := range subjects {
		if s.Kind == rbacv1.ServiceAccountKind && s.Name == sa && s.Namespace == saNS {
			return true
		}
	}
	return false
}

// allowed evaluates Kubernetes RBAC for the ServiceAccount saNS/sa. ns is ""
// for a cluster-scoped resource. name is "" for create and list.
func (v rbacView) allowed(saNS, sa, ns, group, resource, verb, name string) bool {
	for _, b := range v.crbs {
		if b.RoleRef.Kind == "ClusterRole" && boundTo(b.Subjects, saNS, sa) &&
			ruleAllows(v.clusterRoles[b.RoleRef.Name].Rules, group, resource, verb, name) {
			return true
		}
	}
	if ns == "" {
		return false
	}
	for _, b := range v.rbs {
		if b.Namespace != ns || !boundTo(b.Subjects, saNS, sa) {
			continue
		}
		var rules []rbacv1.PolicyRule
		switch b.RoleRef.Kind {
		case "Role":
			rules = v.roles[ns+"/"+b.RoleRef.Name].Rules
		case "ClusterRole":
			rules = v.clusterRoles[b.RoleRef.Name].Rules
		}
		if ruleAllows(rules, group, resource, verb, name) {
			return true
		}
	}
	return false
}

// accessScope says where the controller makes a call.
type accessScope int

const (
	inWatched accessScope = iota // every namespace the controller reconciles
	inRelease                    // the controller's own namespace
	inCluster                    // cluster-scoped resource
)

type apiAccess struct {
	group, resource string
	verbs           []string
	scope           accessScope
	name            string // resourceName for get/update/patch; create is never named
	source          string
}

var kardinalNamespacedKinds = []string{
	"pipelines", "bundles", "policygates", "rollbackpolicies", "subscriptions",
	"promotionsteps", "prstatuses", "metricchecks",
	"scheduleclocks", "notificationhooks",
}

var rwVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
var readVerbs = []string{"get", "list", "watch"}

// controllerAccess is the API access the controller makes with default values.
// Keep it in step with the code: a new client call needs a row here and a
// rule in templates/_rbac.tpl.
func controllerAccess() []apiAccess {
	acc := []apiAccess{
		{"events.k8s.io", "events", []string{"create", "patch"}, inWatched, "", "GetEventRecorder (main.go): reconciler Events"},
		{"", "events", []string{"list", "create", "patch"}, inWatched, "", "UI step events list (ui_api.go); leader election Events (controller-runtime)"},
		{"", "secrets", []string{"get"}, inWatched, "", "Pipeline git secret (promotionstep Get), SCM SecretWatcher (Get; Secrets are uncached)"},
		{"kardinal.io", "auditevents", []string{"get", "list", "watch", "create"}, inWatched, "", "audit.go"},
		{"kro.run", "graphs", rwVerbs, inWatched, "", "pkg/graph client; get: promotionstep finalizer.go stepComeback"},
		{"kro.run", "graphs/status", []string{"get"}, inWatched, "", "pkg/graph client"},
		{"", "serviceaccounts", []string{"get", "create"}, inWatched, "", "graph identity.go"},
		{"rbac.authorization.k8s.io", "rolebindings", []string{"get", "list", "create", "update", "delete"}, inWatched, "", "graph identity.go; delete prunes reader bindings (fix/audit-graph); list: graphcleanup sweep.go (cluster mode)"},
		{"rbac.authorization.k8s.io", "clusterroles", []string{"bind"}, inWatched, "kardinal-promoter-graph-applier", "graph identity.go"},
		{"rbac.authorization.k8s.io", "clusterroles", []string{"bind"}, inWatched, "kardinal-promoter-graph-reader", "graph identity.go"},
		{"apps", "deployments", readVerbs, inWatched, "", "health adapter resource"},
		{"apps", "replicasets", []string{"get"}, inWatched, "", "health adapters resource and flux: deadlineOfReplicaSet (uncached dynamic Get)"},
		{"", "pods", []string{"list"}, inWatched, "", "health adapter resource: podProblemLookup (uncached dynamic List of the new ReplicaSet's pods)"},
		{"argoproj.io", "applications", readVerbs, inWatched, "", "health adapter argocd, argocd update strategy"},
		{"argoproj.io", "rollouts", readVerbs, inWatched, "", "health adapter argoRollouts"},
		{"kustomize.toolkit.fluxcd.io", "kustomizations", readVerbs, inWatched, "", "health adapter flux"},
		{"flagger.app", "canaries", readVerbs, inWatched, "", "health adapter flagger"},
		{"coordination.k8s.io", "leases", []string{"get", "list", "watch", "create", "update", "patch", "delete"}, inRelease, "", "leader election"},
		{"", "configmaps", []string{"create"}, inRelease, "", "ensureVersionConfigMap"},
		{"", "configmaps", []string{"get", "update", "patch"}, inRelease, "kardinal-version", "ensureVersionConfigMap"},
		{"kardinal.io", "changewindows", readVerbs, inCluster, "", "policygate buildChangeWindowContext (cluster-scoped kind)"},
		{"kardinal.io", "changewindows/status", []string{"get", "update", "patch"}, inCluster, "", "changewindow reconciler status writer (fix/audit-gates)"},
		{"kardinal.io", "scmproviders", readVerbs, inWatched, "", "scm registry.go GetProvider; scmprovider reconciler"},
		{"kardinal.io", "scmproviders/status", []string{"get", "update", "patch"}, inWatched, "", "scmprovider reconciler Ready condition"},
		{"kardinal.io", "clusterscmproviders", readVerbs, inCluster, "", "scm registry.go GetProvider; scmprovider reconciler"},
		{"kardinal.io", "clusterscmproviders/status", []string{"get", "update", "patch"}, inCluster, "", "scmprovider reconciler Ready condition"},
		// Namespace mode limits it to the watched namespace (releaseNS in
		// TestChartRBACGrantsControllerAccess); TestChartRBACNamespaceGet checks both modes.
		{"", "namespaces", []string{"get"}, inCluster, releaseNS, "graphcleanup reconciler.go namespaceTerminating; bundle reconciler.go namespaceDeleting; promotionstep finalizer.go stepComeback"},
	}
	for _, k := range kardinalNamespacedKinds {
		acc = append(acc,
			apiAccess{"kardinal.io", k, rwVerbs, inWatched, "", "reconcilers"},
			apiAccess{"kardinal.io", k + "/status", []string{"get", "update", "patch"}, inWatched, "", "reconcilers"})
	}
	return acc
}

// optionalAccess is access granted only when its value is set.
var optionalAccess = []struct {
	set string
	acc []apiAccess
}{
	{"ui.auth.tokenReview=true", []apiAccess{
		{"authentication.k8s.io", "tokenreviews", []string{"create"}, inCluster, "", "pkg/uiauth TokenReview"},
		{"authorization.k8s.io", "subjectaccessreviews", []string{"create"}, inCluster, "", "pkg/uiauth SubjectAccessReview"},
	}},
	{"rbac.argocdApplicationsWrite=true", []apiAccess{
		{"argoproj.io", "applications", []string{"patch"}, inWatched, "", "steps argocd_set_image.go"},
	}},
}

func checkAccess(t *testing.T, v rbacView, mode string, watched []string, acc apiAccess, want bool) {
	t.Helper()
	var namespaces []string
	switch acc.scope {
	case inWatched:
		namespaces = watched
	case inRelease:
		namespaces = []string{releaseNS}
	case inCluster:
		namespaces = []string{""}
	}
	for _, ns := range namespaces {
		for _, verb := range acc.verbs {
			got := v.allowed(releaseNS, "kardinal-promoter", ns, acc.group, acc.resource, verb, acc.name)
			assert.Equal(t, want, got, "%s: %s %s/%s %q in namespace %q (%s)",
				mode, verb, acc.group, acc.resource, acc.name, ns, acc.source)
		}
	}
}

// TestChartRBACGrantsControllerAccess checks every API call the controller
// makes against the rendered RBAC, in cluster mode and namespace mode, with
// the optional grants off and on.
func TestChartRBACGrantsControllerAccess(t *testing.T) {
	modes := []struct {
		name    string
		args    []string
		watched []string
	}{
		{"cluster mode", nil, []string{"team-a", "argocd", releaseNS}},
		{"namespace mode", []string{"--set", "controller.watchNamespace=" + releaseNS}, []string{releaseNS}},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			v := newRBACView(t, render(t, "kardinal-promoter", m.args...))
			for _, acc := range controllerAccess() {
				checkAccess(t, v, m.name, m.watched, acc, true)
			}
			for _, opt := range optionalAccess {
				for _, acc := range opt.acc {
					checkAccess(t, v, m.name+" default (no "+opt.set+")", m.watched, acc, false)
				}
				on := newRBACView(t, render(t, "kardinal-promoter", append(m.args, "--set", opt.set)...))
				for _, acc := range opt.acc {
					checkAccess(t, on, m.name+" --set "+opt.set, m.watched, acc, true)
				}
			}
		})
	}
}

// TestChartRBACLeastPrivilege: grants the code does not use are gone.
func TestChartRBACLeastPrivilege(t *testing.T) {
	v := newRBACView(t, render(t, "kardinal-promoter", allFeatures...))
	sa := "kardinal-promoter"
	denied := []struct {
		ns, group, resource, verb, name string
	}{
		{"", "", "namespaces", "list", ""},
		{"", "", "namespaces", "watch", ""},
		{"", "", "namespaces", "patch", "team-a"},
		{"", "", "namespaces", "delete", "team-a"},
		{"team-a", "", "configmaps", "create", ""},
		{"team-a", "", "configmaps", "patch", "some-app-config"},
		{releaseNS, "", "configmaps", "patch", "some-app-config"},
		// ConfigMaps are uncached and the only one read is kardinal-version,
		// by name in the release namespace: no list, watch or get elsewhere.
		{"team-a", "", "configmaps", "get", "some-app-config"},
		{"team-a", "", "configmaps", "list", ""},
		{"team-a", "", "configmaps", "watch", ""},
		{releaseNS, "", "configmaps", "list", ""},
		{releaseNS, "", "configmaps", "get", "some-app-config"},
		{"team-a", "coordination.k8s.io", "leases", "update", "kardinal-promoter-leader"},
		{"", "kardinal.io", "changewindows", "create", ""},
		{"", "kardinal.io", "changewindows", "delete", "freeze"},
		// SCM providers are written by their owners only: the controller
		// reads them and writes their status.
		{"team-a", "kardinal.io", "scmproviders", "create", ""},
		{"team-a", "kardinal.io", "scmproviders", "update", "github"},
		{"", "kardinal.io", "clusterscmproviders", "create", ""},
		{"", "kardinal.io", "clusterscmproviders", "delete", "github"},
		{"team-a", "", "secrets", "create", ""},
		// #1266: Secret reads are uncached Gets; list and watch would let the
		// controller enumerate every Secret it can reach.
		{"team-a", "", "secrets", "list", ""},
		{"team-a", "", "secrets", "watch", ""},
		{releaseNS, "", "secrets", "list", ""},
		{releaseNS, "", "secrets", "watch", ""},
		{"team-a", "rbac.authorization.k8s.io", "clusterroles", "bind", "cluster-admin"},
		// The integration-test step was removed (#1278); rbac.integrationTestJobs
		// is a no-op, so even with it set the controller gets no Job access.
		{"team-a", "batch", "jobs", "create", ""},
		{"team-a", "batch", "jobs", "delete", ""},
		{releaseNS, "batch", "jobs", "create", ""},
	}
	for _, d := range denied {
		assert.False(t, v.allowed(releaseNS, sa, d.ns, d.group, d.resource, d.verb, d.name),
			"controller must not be allowed to %s %s/%s %q in %q", d.verb, d.group, d.resource, d.name, d.ns)
	}
}

// TestChartRBACNamespaceGet: the Graph cleanup reconciler reads the namespace
// of each deleted Graph. Cluster mode may read any namespace; namespace mode
// only the watched one.
func TestChartRBACNamespaceGet(t *testing.T) {
	sa := "kardinal-promoter"
	cluster := newRBACView(t, render(t, "kardinal-promoter"))
	assert.True(t, cluster.allowed(releaseNS, sa, "", "", "namespaces", "get", "team-a"), "cluster mode")
	assert.True(t, cluster.allowed(releaseNS, sa, "", "rbac.authorization.k8s.io", "rolebindings", "list", ""),
		"cluster mode: the sweep lists RoleBindings cluster-wide")

	scoped := newRBACView(t, render(t, "kardinal-promoter", "--set", "controller.watchNamespace="+releaseNS))
	assert.True(t, scoped.allowed(releaseNS, sa, "", "", "namespaces", "get", releaseNS), "namespace mode: the watched namespace")
	assert.False(t, scoped.allowed(releaseNS, sa, "", "", "namespaces", "get", "team-a"), "namespace mode: another namespace")
	assert.False(t, scoped.allowed(releaseNS, sa, "", "", "namespaces", "get", ""), "namespace mode: any namespace")
	assert.False(t, scoped.allowed(releaseNS, sa, "", "rbac.authorization.k8s.io", "rolebindings", "list", ""),
		"namespace mode: no cluster-wide RoleBinding list")
}

// TestChartRBACSCMTokenSecret covers #1266: the release-namespace Role gets a
// get on the SCM token Secret, by name, so the SecretWatcher can reload a
// rotated token whatever the namespaced rules cover. With no Secret name the
// rule is not rendered: empty resourceNames would mean every Secret.
func TestChartRBACSCMTokenSecret(t *testing.T) {
	const role = releaseNS + "/kardinal-promoter-leader-election"
	secretRules := func(v rbacView) []rbacv1.PolicyRule {
		var out []rbacv1.PolicyRule
		for _, r := range v.roles[role].Rules {
			if has(r.Resources, "secrets") {
				out = append(out, r)
			}
		}
		return out
	}

	tests := []struct {
		name     string
		args     []string
		wantName string
	}{
		{"no token configured", nil, ""},
		{"existing Secret", []string{"--set", "github.secretRef.name=scm-token"}, "scm-token"},
		{"chart-owned Secret", []string{"--set", "github.token=not-a-real-token"}, "kardinal-promoter-github-token"},
		{"namespace mode", []string{"--set", "github.secretRef.name=scm-token", "--set", "controller.watchNamespace=" + releaseNS}, "scm-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newRBACView(t, render(t, "kardinal-promoter", tt.args...))
			require.Contains(t, v.roles, role)
			rules := secretRules(v)
			if tt.wantName == "" {
				assert.Empty(t, rules)
				return
			}
			require.Len(t, rules, 1)
			assert.Equal(t, []string{"get"}, rules[0].Verbs)
			assert.Equal(t, []string{tt.wantName}, rules[0].ResourceNames)
			assert.True(t, v.allowed(releaseNS, "kardinal-promoter", releaseNS, "", "secrets", "get", tt.wantName))
			for _, verb := range []string{"list", "watch", "update", "delete"} {
				assert.False(t, v.allowed(releaseNS, "kardinal-promoter", releaseNS, "", "secrets", verb, tt.wantName), verb)
			}
		})
	}
}

// TestChartNamespaceModeWiring: in namespace mode the controller's cache
// holds only the watch namespace, so the chart must point --policy-namespaces
// there (the default, platform-policies, is outside the cache and failed every
// translation), set POD_NAMESPACE, and reject values it cannot serve.
func TestChartNamespaceModeWiring(t *testing.T) {
	docs := render(t, "kardinal-promoter", "--set", "controller.watchNamespace="+releaseNS)
	c := controllerContainer(t, docs)
	args := argValues(c)
	assert.Equal(t, releaseNS, args["policy-namespaces"])
	assert.Equal(t, releaseNS, args["watch-namespace"])
	env := envByName(c)
	require.Contains(t, env, "POD_NAMESPACE")
	require.NotNil(t, env["POD_NAMESPACE"].ValueFrom)
	assert.Equal(t, "metadata.namespace", env["POD_NAMESPACE"].ValueFrom.FieldRef.FieldPath)

	// Cluster mode keeps the controller default unless policyNamespaces is set.
	c = controllerContainer(t, render(t, "kardinal-promoter"))
	assert.NotContains(t, argValues(c), "policy-namespaces")
	assert.Contains(t, envByName(c), "POD_NAMESPACE")
	c = controllerContainer(t, render(t, "kardinal-promoter",
		"--set", "controller.policyNamespaces={platform-policies,org-gates}"))
	assert.Equal(t, "platform-policies,org-gates", argValues(c)["policy-namespaces"])

	for _, bad := range [][]string{
		{"--set", "controller.watchNamespace=team-a"}, // release namespace differs
		{"--set", "controller.watchNamespace=" + releaseNS, "--set", "controller.policyNamespaces={platform-policies}"},
	} {
		out, err := helmTemplate(t, "kardinal-promoter", bad...)
		assert.Error(t, err, "helm template %v must fail:\n%s", bad, out)
	}
}

// ── C08-api-config-10: UI and webhook ports ───────────────────────────────────

func TestChartExposesUIAndWebhookPorts(t *testing.T) {
	docs := render(t, "kardinal-promoter", "--set", "networkPolicy.enabled=true")
	c := controllerContainer(t, docs)
	ports := map[string]int32{}
	for _, p := range c.Ports {
		ports[p.Name] = p.ContainerPort
	}
	assert.Equal(t, map[string]int32{"metrics": 8080, "health": 8081, "ui": 8082, "webhook": 8083}, ports)
	args := argValues(c)
	assert.Equal(t, ":8082", args["ui-listen-address"])
	assert.Equal(t, ":8083", args["webhook-bind-address"])

	svcs := docsOfKind(docs, "Service")
	require.Len(t, svcs, 1)
	var svc corev1.Service
	decodeStrict(t, svcs[0], &svc)
	assert.Equal(t, "kardinal-promoter", svc.Name, "docs port-forward svc/kardinal-promoter")
	svcPorts := map[string]string{}
	for _, p := range svc.Spec.Ports {
		svcPorts[p.Name] = p.TargetPort.String()
	}
	assert.Equal(t, map[string]string{"metrics": "metrics", "health": "health", "ui": "ui", "webhook": "webhook"}, svcPorts)

	nps := docsOfKind(docs, "NetworkPolicy")
	require.Len(t, nps, 1)
	var np networkingv1.NetworkPolicy
	decodeStrict(t, nps[0], &np)
	ingress := map[int32]bool{}
	for _, r := range np.Spec.Ingress {
		for _, p := range r.Ports {
			ingress[p.Port.IntVal] = true
		}
	}
	for _, p := range []int32{8080, 8081, 8082, 8083} {
		assert.True(t, ingress[p], "NetworkPolicy must admit port %d", p)
	}

	c = controllerContainer(t, render(t, "kardinal-promoter",
		"--set", "service.uiPort=9082", "--set", "service.webhookPort=9083"))
	args = argValues(c)
	assert.Equal(t, ":9082", args["ui-listen-address"])
	assert.Equal(t, ":9083", args["webhook-bind-address"])
}

// The metrics and health servers listen on metricsBindAddress and
// healthProbeBindAddress, so the container ports, the probes (port: health)
// and the NetworkPolicy must follow those addresses, not the Service ports.
// Before the fix a custom bind address left the probes on a port nothing
// listened on and the install never became ready.
func TestChartPortsFollowBindAddresses(t *testing.T) {
	docs := render(t, "kardinal-promoter", "--set", "networkPolicy.enabled=true",
		"--set", "metricsBindAddress=:9100", "--set", "healthProbeBindAddress=0.0.0.0:9101",
		"--set", "service.metricsPort=9090", "--set", "service.healthPort=9091")
	c := controllerContainer(t, docs)
	ports := map[string]int32{}
	for _, p := range c.Ports {
		ports[p.Name] = p.ContainerPort
	}
	assert.Equal(t, map[string]int32{"metrics": 9100, "health": 9101, "ui": 8082, "webhook": 8083}, ports)
	args := argValues(c)
	assert.Equal(t, ":9100", args["metrics-bind-address"])
	assert.Equal(t, "0.0.0.0:9101", args["health-probe-bind-address"])
	assert.Equal(t, "health", c.LivenessProbe.HTTPGet.Port.String())
	assert.Equal(t, "health", c.ReadinessProbe.HTTPGet.Port.String())

	svcs := docsOfKind(docs, "Service")
	require.Len(t, svcs, 1)
	var svc corev1.Service
	decodeStrict(t, svcs[0], &svc)
	svcPorts := map[string]int32{}
	for _, p := range svc.Spec.Ports {
		svcPorts[p.Name] = p.Port
		assert.Equal(t, p.Name, p.TargetPort.String(), "Service port %s targets the named container port", p.Name)
	}
	assert.Equal(t, map[string]int32{"metrics": 9090, "health": 9091, "ui": 8082, "webhook": 8083}, svcPorts)

	nps := docsOfKind(docs, "NetworkPolicy")
	require.Len(t, nps, 1)
	var np networkingv1.NetworkPolicy
	decodeStrict(t, nps[0], &np)
	var ingress []int32
	for _, r := range np.Spec.Ingress {
		for _, p := range r.Ports {
			ingress = append(ingress, p.Port.IntVal)
		}
	}
	assert.ElementsMatch(t, []int32{9100, 9101, 8082, 8083}, ingress,
		"NetworkPolicy ports are Pod ports, so they must be the container ports")

	// Port 0 disables controller-runtime's metrics server; the container port
	// falls back to the Service port instead of rendering an invalid 0.
	c = controllerContainer(t, render(t, "kardinal-promoter", "--set-string", "metricsBindAddress=0"))
	for _, p := range c.Ports {
		if p.Name == "metrics" {
			assert.Equal(t, int32(8080), p.ContainerPort)
		}
	}
}

// terminationGracePeriodSeconds 0 is a valid value (kill at once); the chart
// used `default 60`, which treats 0 as unset and rendered 60.
func TestChartTerminationGracePeriod(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int64
	}{
		{nil, 60},
		{[]string{"--set", "terminationGracePeriodSeconds=0"}, 0},
		{[]string{"--set", "terminationGracePeriodSeconds=120"}, 120},
		{[]string{"--set", "terminationGracePeriodSeconds=null"}, 60},
	} {
		deps := docsOfKind(render(t, "kardinal-promoter", tc.args...), "Deployment")
		require.Len(t, deps, 1)
		var dep appsv1.Deployment
		decodeStrict(t, deps[0], &dep)
		require.NotNil(t, dep.Spec.Template.Spec.TerminationGracePeriodSeconds, "%v", tc.args)
		assert.Equal(t, tc.want, *dep.Spec.Template.Spec.TerminationGracePeriodSeconds, "%v", tc.args)
	}
}

// shutdownDelaySeconds is a preStop sleep, so a Pod being deleted serves
// until Services stop routing to it; 0 removes the hook, and null is the
// default.
func TestChartShutdownDelay(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"sleep", "5"}},
		{[]string{"--set", "shutdownDelaySeconds=0"}, nil},
		{[]string{"--set", "shutdownDelaySeconds=12"}, []string{"sleep", "12"}},
		{[]string{"--set", "shutdownDelaySeconds=null"}, []string{"sleep", "5"}},
	} {
		c := controllerContainer(t, render(t, "kardinal-promoter", tc.args...))
		if tc.want == nil {
			assert.Nil(t, c.Lifecycle, "%v", tc.args)
			continue
		}
		if assert.NotNil(t, c.Lifecycle, "%v", tc.args) && assert.NotNil(t, c.Lifecycle.PreStop, "%v", tc.args) &&
			assert.NotNil(t, c.Lifecycle.PreStop.Exec, "%v", tc.args) {
			assert.Equal(t, tc.want, c.Lifecycle.PreStop.Exec.Command, "%v", tc.args)
		}
	}
	out, err := helmTemplate(t, "kardinal-promoter", "--set", "shutdownDelaySeconds=-1")
	require.Error(t, err)
	// Helm 3.14 and 3.22 (CI) word the schema error differently.
	assert.Regexp(t, `shutdownDelaySeconds(: Must be greater than or equal to 0|': minimum: got -1, want 0)`, out)
}

// ── C08-api-config-11: values wired to real controller flags ─────────────────

var flagDef = regexp.MustCompile(`flag\.\w+Var\(\s*&[\w.]+,\s*"([a-z0-9-]+)"`)
var envRead = regexp.MustCompile(`os\.Getenv\("([A-Z0-9_]+)"\)`)

// controllerFlags returns every flag cmd/kardinal-controller defines,
// including controller-runtime's zap flags.
func controllerFlags(t *testing.T) map[string]bool {
	t.Helper()
	flags := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "cmd", "kardinal-controller", "*.go"))
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range flagDef.FindAllSubmatch(src, -1) {
			flags[string(m[1])] = true
		}
	}
	fs := flag.NewFlagSet("zap", flag.ContinueOnError)
	(&czap.Options{}).BindFlags(fs)
	fs.VisitAll(func(f *flag.Flag) { flags[f.Name] = true })
	require.Contains(t, flags, "leader-elect", "flag scan found nothing — regexp out of date?")
	return flags
}

func controllerEnvReads(t *testing.T) map[string]bool {
	t.Helper()
	env := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "cmd", "kardinal-controller", "*.go"))
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range envRead.FindAllSubmatch(src, -1) {
			env[string(m[1])] = true
		}
	}
	require.Contains(t, env, "GITHUB_TOKEN", "env scan found nothing — regexp out of date?")
	return env
}

// everyValue sets every value that renders a flag or env var.
var everyValue = []string{
	"--set", "controller.tlsCertFile=/tls/tls.crt",
	"--set", "controller.tlsKeyFile=/tls/tls.key",
	"--set", "controller.extraVolumes[0].name=tls",
	"--set", "controller.extraVolumes[0].secret.secretName=kardinal-tls",
	"--set", "controller.extraVolumeMounts[0].name=tls",
	"--set", "controller.extraVolumeMounts[0].mountPath=/tls",
	"--set", "controller.policyNamespaces={platform-policies}",
	"--set", "scm.provider=gitlab",
	"--set", "scm.apiURL=https://gitlab.example.com",
	"--set", "scm.allowedRepositories={gitlab.example.com/acme/*,gitlab.example.com/platform/**}",
	"--set", "github.secretRef.name=scm-token",
	"--set", "webhook.secretRef.name=webhook-secret",
	"--set", "bundleAPI.tokenSecretRef.name=bundle-token",
	"--set", "ui.auth.tokenSecretRef.name=ui-token",
	"--set", "ui.auth.tokenReview=true",
	"--set", "ui.corsAllowedOrigins={https://a.example.com,https://b.example.com}",
}

// TestChartFlagsAndEnvExistInController: every rendered --flag is defined by
// the controller (an unknown flag makes it exit), and every env var is read.
func TestChartFlagsAndEnvExistInController(t *testing.T) {
	flags := controllerFlags(t)
	env := controllerEnvReads(t)
	for _, args := range [][]string{nil, everyValue, {"--set", "controller.watchNamespace=" + releaseNS}} {
		c := controllerContainer(t, render(t, "kardinal-promoter", args...))
		for f := range argValues(c) {
			assert.True(t, flags[f], "args %v: chart renders --%s, which the controller does not define", args, f)
		}
		for _, e := range c.Env {
			assert.True(t, env[e.Name], "args %v: chart sets %s, which the controller never reads", args, e.Name)
		}
	}
}

// TestChartValuesWireControllerFlags maps each documented value to the flag or
// env var the controller reads.
func TestChartValuesWireControllerFlags(t *testing.T) {
	c := controllerContainer(t, render(t, "kardinal-promoter", everyValue...))
	args := argValues(c)
	env := envByName(c)

	wantArgs := map[string]string{
		"policy-namespaces":        "platform-policies",
		"scm-provider":             "gitlab",
		"scm-api-url":              "https://gitlab.example.com",
		"scm-allowed-repositories": "gitlab.example.com/acme/*,gitlab.example.com/platform/**",
		"ui-tokenreview-auth":      "true",
		"cors-allowed-origins":     "https://a.example.com,https://b.example.com",
	}
	for k, v := range wantArgs {
		assert.Equal(t, v, args[k], "--%s", k)
	}
	wantSecretEnv := map[string][2]string{
		"GITHUB_TOKEN":            {"scm-token", "token"},
		"KARDINAL_WEBHOOK_SECRET": {"webhook-secret", "secret"},
		"KARDINAL_BUNDLE_TOKEN":   {"bundle-token", "token"},
		"KARDINAL_UI_TOKEN":       {"ui-token", "token"},
	}
	for name, ref := range wantSecretEnv {
		e, ok := env[name]
		require.True(t, ok, "%s must be set", name)
		require.NotNil(t, e.ValueFrom, "%s must come from a Secret", name)
		require.NotNil(t, e.ValueFrom.SecretKeyRef, "%s must come from a Secret", name)
		assert.Equal(t, ref[0], e.ValueFrom.SecretKeyRef.Name, name)
		assert.Equal(t, ref[1], e.ValueFrom.SecretKeyRef.Key, name)
	}
	assert.Equal(t, "/tls/tls.crt", env["KARDINAL_TLS_CERT_FILE"].Value)
	assert.Equal(t, "/tls/tls.key", env["KARDINAL_TLS_KEY_FILE"].Value)
	assert.Equal(t, "scm-token", env["KARDINAL_SCM_TOKEN_SECRET_NAME"].Value)

	// extraArgs, extraEnv, extraVolumes and extraVolumeMounts pass through.
	docs := render(t, "kardinal-promoter",
		"--set", "controller.extraArgs={--cors-allowed-origins=https://kardinal.example.com}",
		"--set", "controller.extraEnv[0].name=HTTPS_PROXY",
		"--set", "controller.extraEnv[0].value=http://proxy:3128",
		"--set", "controller.extraVolumes[0].name=tls",
		"--set", "controller.extraVolumes[0].secret.secretName=kardinal-tls",
		"--set", "controller.extraVolumeMounts[0].name=tls",
		"--set", "controller.extraVolumeMounts[0].mountPath=/tls")
	c = controllerContainer(t, docs)
	assert.Contains(t, c.Args, "--cors-allowed-origins=https://kardinal.example.com")
	assert.Equal(t, "http://proxy:3128", envByName(c)["HTTPS_PROXY"].Value)
	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	assert.Equal(t, "/tls", mounts["tls"])
}

// TestChartTLSFilesSetTogether: the controller exits at startup when only one
// of --tls-cert-file and --tls-key-file is set, so the chart refuses a
// controller.tlsCertFile without controller.tlsKeyFile (and the reverse)
// before anything is applied, naming the value that is missing its pair.
func TestChartTLSFilesSetTogether(t *testing.T) {
	for set, only := range map[string]string{
		"controller.tlsCertFile=/tls/tls.crt": "tlsCertFile",
		"controller.tlsKeyFile=/tls/tls.key":  "tlsKeyFile",
	} {
		out, err := helmTemplate(t, "kardinal-promoter", "--set", set)
		require.Error(t, err, "--set %s alone must fail:\n%s", set, out)
		assert.Contains(t, out, "controller.tlsCertFile and controller.tlsKeyFile must be set together (only "+only+" is set)")
	}
	env := envByName(controllerContainer(t, render(t, "kardinal-promoter")))
	assert.NotContains(t, env, "KARDINAL_TLS_CERT_FILE", "no TLS by default")
	assert.NotContains(t, env, "KARDINAL_TLS_KEY_FILE", "no TLS by default")
}

// TestChartTLSFilesInASecret: the controller crash-loops when it cannot open
// --tls-cert-file or --tls-key-file, so the chart refuses TLS paths that are
// not in a secret, projected or csi volume mounted with controller.extraVolumes
// and extraVolumeMounts, naming the value and its path. A directory mount and
// subPath mounts of the two files both render.
func TestChartTLSFilesInASecret(t *testing.T) {
	tls := func(cert, key string) []string {
		return []string{"--set", "controller.tlsCertFile=" + cert, "--set", "controller.tlsKeyFile=" + key}
	}
	volume := func(source string) []string {
		return []string{"--set", "controller.extraVolumes[0].name=tls", "--set", "controller.extraVolumes[0]." + source}
	}
	mount := func(i int, at, subPath string) []string {
		m := fmt.Sprintf("controller.extraVolumeMounts[%d].", i)
		args := []string{"--set", m + "name=tls", "--set", m + "mountPath=" + at}
		if subPath != "" {
			args = append(args, "--set", m+"subPath="+subPath)
		}
		return args
	}
	mountExpr := func(i int, at, subPathExpr string) []string {
		m := fmt.Sprintf("controller.extraVolumeMounts[%d].", i)
		return []string{"--set", m + "name=tls", "--set", m + "mountPath=" + at, "--set", m + "subPathExpr=" + subPathExpr}
	}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	secret := volume("secret.secretName=kardinal-tls")
	notInSecret := func(value, path string) string {
		return "controller." + value + " (" + path + ") is not in a mounted Secret"
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no mount":           {tls("/tls/tls.crt", "/tls/tls.key"), notInSecret("tlsCertFile", "/tls/tls.crt")},
		"key outside":        {join(tls("/tls/tls.crt", "/etc/tls.key"), secret, mount(0, "/tls", "")), notInSecret("tlsKeyFile", "/etc/tls.key")},
		"sibling directory":  {join(tls("/tlsx/tls.crt", "/tlsx/tls.key"), secret, mount(0, "/tls", "")), notInSecret("tlsCertFile", "/tlsx/tls.crt")},
		"path is the mount":  {join(tls("/tls", "/tls/tls.key"), secret, mount(0, "/tls", "")), notInSecret("tlsCertFile", "/tls")},
		"configMap volume":   {join(tls("/tls/tls.crt", "/tls/tls.key"), volume("configMap.name=tls"), mount(0, "/tls", "")), `controller.tlsCertFile (/tls/tls.crt) is in volume "tls", which is not a secret, projected or csi volume`},
		"mount of no volume": {join(tls("/tls/tls.crt", "/tls/tls.key"), mount(0, "/tls", "")), `controller.tlsCertFile (/tls/tls.crt) is mounted from volume "tls", which controller.extraVolumes does not define`},
	} {
		out, err := helmTemplate(t, "kardinal-promoter", c.args...)
		require.Error(t, err, "%s must fail:\n%s", name, out)
		assert.Contains(t, out, c.want, name)
	}
	for name, args := range map[string][]string{
		"secret directory":  join(tls("/tls/tls.crt", "/tls/tls.key"), secret, mount(0, "/tls/", "")),
		"subPath files":     join(tls("/etc/c.crt", "/etc/c.key"), secret, mount(0, "/etc/c.crt", "tls.crt"), mount(1, "/etc/c.key", "tls.key")),
		"subPathExpr files": join(tls("/etc/c.crt", "/etc/c.key"), secret, mountExpr(0, "/etc/c.crt", "$(POD_NAME)/tls.crt"), mountExpr(1, "/etc/c.key", "$(POD_NAME)/tls.key")),
		"projected":         join(tls("/tls/tls.crt", "/tls/tls.key"), volume("projected.sources[0].secret.name=kardinal-tls"), mount(0, "/tls", "")),
		"csi":               join(tls("/tls/tls.crt", "/tls/tls.key"), volume("csi.driver=csi.cert-manager.io"), mount(0, "/tls", "")),
	} {
		env := envByName(controllerContainer(t, render(t, "kardinal-promoter", args...)))
		assert.NotEmpty(t, env["KARDINAL_TLS_CERT_FILE"].Value, name)
		assert.NotEmpty(t, env["KARDINAL_TLS_KEY_FILE"].Value, name)
	}
}

// TestChartRejectsUnknownValues: values.schema.json fails unknown keys, so the
// value names the docs used to give can no longer be silently ignored.
// controller.shard was removed with distributed mode (#1321).
func TestChartRejectsUnknownValues(t *testing.T) {
	for _, set := range []string{
		"controller.shard=eu",
		"controller.github.token.secretName=github-token",
		"controller.remoteKubeconfig.secretRef.name=kubeconfig",
		"controller.uiAuthToken=abc",
		"logLevel=verbose",
		"githubToken=x",
	} {
		out, err := helmTemplate(t, "kardinal-promoter", "--set", set)
		assert.Error(t, err, "--set %s must be rejected:\n%s", set, out)
	}
}

// TestChartAcceptsRepoSetKeys: every --set this repo passes to this chart
// (Makefile, hack/, workflows, demo/, docs) still renders.
func TestChartAcceptsRepoSetKeys(t *testing.T) {
	for _, set := range []string{
		"image.repository=ghcr.io/pnz1990/kardinal-promoter",
		"image.tag=dev",
		"image.pullPolicy=Never",
		"github.secretRef.name=github-token",
		"validatingAdmissionPolicy.enabled=false",
		"networkPolicy.enabled=true",
		"demo.enabled=true",
		"controller.watchNamespace=" + releaseNS,
		// Set together and in a mounted Secret, as hack/e2e/components/ui.sh
		// does (TestChartTLSFilesSetTogether, TestChartTLSFilesInASecret).
		"controller.extraVolumes[0].name=tls,controller.extraVolumes[0].secret.secretName=kui-tls-cert," +
			"controller.extraVolumeMounts[0].name=tls,controller.extraVolumeMounts[0].mountPath=/etc/kardinal/tls," +
			"controller.tlsCertFile=/etc/kardinal/tls/tls.crt,controller.tlsKeyFile=/etc/kardinal/tls/tls.key",
		"prometheusRule.enabled=true",
		"prometheusRule.additionalLabels.release=kube-prometheus-stack",
		"grafanaDashboard.enabled=true",
		"grafanaDashboard.sidecarLabel.grafana_dashboard=1",
		"replicaCount=2",
		"pdb.enabled=true",
		"topologySpread.enabled=true",
		"graph.aggregateToKro=false",
		"scheduleClock.enabled=false",
	} {
		out, err := helmTemplate(t, "kardinal-promoter", "--set", set)
		assert.NoError(t, err, "--set %s:\n%s", set, out)
	}
	out, err := exec.Command(helmBin(t), "lint", chartPath(t), "--strict").CombinedOutput()
	assert.NoError(t, err, "helm lint --strict:\n%s", out)
}

// ── C08-api-config-17: NetworkPolicy ──────────────────────────────────────────

// TestChartNetworkPolicyIsValid: the policy must decode strictly (the kro rule
// was a bare namespaceSelector, not a field of an egress rule) and every rule
// must be restrictive (an empty rule allows everything).
func TestChartNetworkPolicyIsValid(t *testing.T) {
	docs := render(t, "kardinal-promoter",
		"--set", "networkPolicy.enabled=true",
		"--set", "networkPolicy.ingressFrom.metrics[0].namespaceSelector.matchLabels.name=monitoring",
		"--set", "networkPolicy.extraEgress[0].ports[0].port=9090")
	nps := docsOfKind(docs, "NetworkPolicy")
	require.Len(t, nps, 1)
	var np networkingv1.NetworkPolicy
	decodeStrict(t, nps[0], &np)

	for i, r := range np.Spec.Egress {
		assert.False(t, len(r.To) == 0 && len(r.Ports) == 0, "egress rule %d allows all egress", i)
	}
	kro := false
	for _, r := range np.Spec.Egress {
		for _, to := range r.To {
			if to.NamespaceSelector != nil &&
				to.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "kro-system" {
				kro = true
			}
		}
	}
	assert.True(t, kro, "egress must admit the kro namespace through a `to` peer")
	seen443 := 0
	for _, r := range np.Spec.Egress {
		if len(r.To) > 0 {
			continue
		}
		for _, p := range r.Ports {
			if p.Port != nil && p.Port.IntVal == 443 {
				seen443++
			}
		}
	}
	assert.Equal(t, 1, seen443, "port 443 egress must appear once")
	extra := false
	for _, r := range np.Spec.Egress {
		for _, p := range r.Ports {
			if p.Port != nil && p.Port.IntVal == 9090 {
				extra = true
			}
		}
	}
	assert.True(t, extra, "networkPolicy.extraEgress must be rendered")
	restricted := false
	for _, r := range np.Spec.Ingress {
		for _, p := range r.Ports {
			if p.Port.IntVal == 8080 && len(r.From) == 1 && r.From[0].NamespaceSelector != nil {
				restricted = true
			}
		}
	}
	assert.True(t, restricted, "networkPolicy.ingressFrom.metrics must restrict the metrics port")
}

// ── C08-api-config-18: GitHub token never in the Deployment ──────────────────

func TestChartGitHubTokenNotPlaintext(t *testing.T) {
	const dummy = "dummy-not-a-real-token-c08-18"
	docs := render(t, "kardinal-promoter", "--set", "github.token="+dummy, "--set", "demo.enabled=true")
	for _, d := range docs {
		if d.Kind == "Secret" {
			continue
		}
		assert.NotContains(t, string(d.raw), dummy, "%s %s must not contain the token", d.Kind, d.Name)
	}
	var secret corev1.Secret
	found := false
	for _, d := range docsOfKind(docs, "Secret") {
		if d.Name == "kardinal-promoter-github-token" {
			decodeStrict(t, d, &secret)
			found = true
		}
	}
	require.True(t, found, "github.token must render the Secret kardinal-promoter-github-token")
	assert.Contains(t, secret.StringData, "token")

	c := controllerContainer(t, docs)
	env := envByName(c)
	require.NotNil(t, env["GITHUB_TOKEN"].ValueFrom)
	assert.Equal(t, "kardinal-promoter-github-token", env["GITHUB_TOKEN"].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "kardinal-promoter-github-token", env["KARDINAL_SCM_TOKEN_SECRET_NAME"].Value,
		"the chart Secret gets the same rotation watcher as github.secretRef")

	// The demo Pipeline uses the chart Secret for git.
	for _, d := range docsOfKind(docs, "Pipeline") {
		assert.Contains(t, string(d.raw), `"name":"kardinal-promoter-github-token"`)
	}

	out, err := helmTemplate(t, "kardinal-promoter",
		"--set", "github.token="+dummy, "--set", "github.secretRef.name=github-token")
	assert.Error(t, err, "github.token and github.secretRef.name together must fail")
	assert.NotContains(t, out, dummy)

	// GITHUB_TOKEN uses a secretKeyRef, which reads only the Pod's namespace,
	// so a Secret in another namespace would split the startup token from
	// the rotation watcher.
	render(t, "kardinal-promoter", "--set", "github.secretRef.name=github-token", "--set", "github.secretRef.namespace="+releaseNS)
	out, err = helmTemplate(t, "kardinal-promoter", "--set", "github.secretRef.name=github-token", "--set", "github.secretRef.namespace=team-a")
	assert.Error(t, err, "github.secretRef.namespace outside the release namespace must fail:\n%s", out)
}

// ── C14a-specify-169: the demo Pipeline's git repo is configurable ────────────

// TestChartDemoGitURLConfigurable: the demo Pipeline pushes to spec.git.url,
// so users must be able to point it at a fork they can write to.
func TestChartDemoGitURLConfigurable(t *testing.T) {
	gitURL := func(docs []renderedDoc) string {
		t.Helper()
		pipelines := docsOfKind(docs, "Pipeline")
		require.Len(t, pipelines, 1, "demo.enabled=true renders one Pipeline")
		var p struct {
			Spec struct {
				Git struct {
					URL string `json:"url"`
				} `json:"git"`
			} `json:"spec"`
		}
		require.NoError(t, json.Unmarshal(pipelines[0].raw, &p))
		return p.Spec.Git.URL
	}

	assert.Equal(t, "https://github.com/pnz1990/kardinal-demo",
		gitURL(render(t, "kardinal-promoter", "--set", "demo.enabled=true")),
		"default is the reference repo")

	const fork = "https://github.com/example/kardinal-demo"
	assert.Equal(t, fork,
		gitURL(render(t, "kardinal-promoter", "--set", "demo.enabled=true", "--set", "demo.git.url="+fork)))

	out, err := helmTemplate(t, "kardinal-promoter", "--set", "demo.enabled=true", "--set", "demo.git.url=")
	assert.Error(t, err, "an empty demo.git.url must fail:\n%s", out)
}

// ── C08-api-config-31: logLevel sets both loggers ─────────────────────────────

func TestChartLogLevelSetsBothLoggers(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		c := controllerContainer(t, render(t, "kardinal-promoter", "--set", "logLevel="+level))
		args := argValues(c)
		assert.Equal(t, level, args["log-level"], "zerolog --log-level")
		_, err := zerolog.ParseLevel(args["log-level"])
		assert.NoError(t, err)

		fs := flag.NewFlagSet("zap", flag.ContinueOnError)
		(&czap.Options{}).BindFlags(fs)
		assert.NoError(t, fs.Set("zap-log-level", args["zap-log-level"]),
			"logLevel=%s renders --zap-log-level=%s, which controller-runtime rejects", level, args["zap-log-level"])
	}
}

// ── C08-api-config-29: dashboard copies in sync ───────────────────────────────

func TestChartDashboardInSync(t *testing.T) {
	root := repoRoot(t)
	chartCopy, err := os.ReadFile(filepath.Join(chartPath(t), "dashboards", "kardinal-promoter-dashboard.json"))
	require.NoError(t, err)
	docsCopy, err := os.ReadFile(filepath.Join(root, "config", "monitoring", "kardinal-promoter-dashboard.json"))
	require.NoError(t, err)
	assert.Equal(t, string(docsCopy), string(chartCopy),
		"config/monitoring and chart/kardinal-promoter/dashboards hold the same dashboard; keep them identical")
	assert.True(t, json.Valid(chartCopy), "dashboard must be valid JSON")
}

// ── C08-api-config-20: PrometheusRule uses metrics that exist ────────────────

func TestChartPrometheusRuleAlertsCanFire(t *testing.T) {
	out, err := helmTemplate(t, "kardinal-promoter", "--set", "prometheusRule.enabled=true")
	require.NoError(t, err, out)
	// controller_runtime_webhook_requests_total is only emitted by the
	// controller-runtime webhook server; kardinal serves webhooks from a
	// plain net/http mux, so an alert on it can never fire.
	assert.NotContains(t, out, "controller_runtime_webhook_requests_total")
	// up == 0 never fires once the target is gone; absent() does.
	assert.Contains(t, out, "absent(up{")
}

// TestChartRequiresKubernetes130: the chart's kubeVersion refuses Kubernetes
// older than 1.30, where kro's CRDs (CRD selectableFields) cannot be
// installed, and accepts 1.30 and later, including a managed cluster's
// pre-release version such as v1.30.5-eks-ce1d5eb. Covers UPG-OLDK8S-01.
func TestChartRequiresKubernetes130(t *testing.T) {
	for _, tc := range []struct {
		kube string
		ok   bool
	}{
		{kube: "1.29.0"},
		{kube: "v1.29.15-gke.1234000"},
		{kube: "1.30.0", ok: true},
		{kube: "v1.30.5-eks-ce1d5eb", ok: true},
		{kube: "1.37.0", ok: true},
	} {
		t.Run(tc.kube, func(t *testing.T) {
			out, err := helmTemplate(t, "kardinal-promoter", "--kube-version", tc.kube)
			if tc.ok {
				require.NoError(t, err, out)
				return
			}
			require.Error(t, err, "helm template --kube-version %s must fail", tc.kube)
			assert.Contains(t, out, "chart requires kubeVersion: >=1.30.0-0")
		})
	}
}

// TestChartGateStatusHeartbeat: controller.gateStatusHeartbeat sets
// --gate-status-heartbeat; empty keeps the controller default (10m), and the
// schema refuses a value that is not a Go duration.
func TestChartGateStatusHeartbeat(t *testing.T) {
	c := controllerContainer(t, render(t, "kardinal-promoter"))
	assert.NotContains(t, argValues(c), "gate-status-heartbeat")
	for _, v := range []string{"0s", "30m", "1h30m"} {
		c = controllerContainer(t, render(t, "kardinal-promoter", "--set", "controller.gateStatusHeartbeat="+v))
		assert.Equal(t, v, argValues(c)["gate-status-heartbeat"])
	}
	out, err := helmTemplate(t, "kardinal-promoter", "--set", "controller.gateStatusHeartbeat=10 minutes")
	assert.Error(t, err, "a value that is not a Go duration must fail:\n%s", out)
}
