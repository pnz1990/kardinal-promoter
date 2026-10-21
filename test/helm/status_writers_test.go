// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// statusWriterKinds are the kardinal kinds whose status <release>-status-writers
// lets only the controller write (#2446): every namespaced kind with a status
// subresource that no other policy covers, and the MetricChecks and
// ChangeRequests people write. TestStatusWriters_Sweep places every status
// subresource of config/crd/bases.
var statusWriterKinds = []string{
	"bundles", "changerequests", "metricchecks", "notificationhooks", "pipelines", "policygates",
	"rollbackpolicies", "scheduleclocks", "scmproviders", "subscriptions",
}

// clusterStatusKinds are the cluster-scoped kinds with a status subresource,
// which <release>-status-writers leaves out: a namespace RoleBinding (how
// <release>-admin-extra and the built-in admin reach a team) cannot grant
// them, only cluster-wide RBAC can, and every kardinal release's controller
// writes them, while a binding's namespaceSelector does not limit a
// cluster-scoped object to one release. agentindexes has its own policy
// (<release>-agent-index).
var clusterStatusKinds = []string{"agentidentities", "agentindexes", "changewindows", "clusterscmproviders", "steptemplates"}

// statusFixture is a minimal valid object of a kind, and a status merge
// patch the kind's schema accepts.
type statusFixture struct {
	kind   string
	spec   map[string]interface{}
	status string
}

var statusFixtures = map[string]statusFixture{
	"bundles": {"Bundle", map[string]interface{}{"type": "image", "pipeline": "p",
		"images": []interface{}{map[string]interface{}{"repository": "ghcr.io/x/app", "tag": "1.0.0"}}}, `{"version":"forged"}`},
	"changerequests": {"ChangeRequest", map[string]interface{}{"provider": "jira", "jira": map[string]interface{}{
		"baseURL": "https://jira.example.com", "project": "OPS", "credentialsSecretRef": map[string]interface{}{"name": "jira", "key": "token"}}},
		`{"ready":true}`},
	"metricchecks": {"MetricCheck", map[string]interface{}{"provider": "prometheus", "prometheusURL": "http://prometheus:9090",
		"query": "up", "threshold": map[string]interface{}{"operator": "gt", "value": 0}}, `{"lastValue":"1","reason":"forged"}`},
	"notificationhooks": {"NotificationHook", map[string]interface{}{"events": []interface{}{"Bundle.Verified"},
		"webhook": map[string]interface{}{"url": "https://hooks.example.com"}}, `{"failureMessage":"forged"}`},
	"pipelines": {"Pipeline", map[string]interface{}{"git": map[string]interface{}{"url": "https://git.example.com/r.git"},
		"environments": []interface{}{map[string]interface{}{"name": "test"}}}, `{"summary":"forged"}`},
	"policygates":      {"PolicyGate", map[string]interface{}{"expression": "false"}, `{"ready":true,"reason":"forged"}`},
	"rollbackpolicies": {"RollbackPolicy", map[string]interface{}{"bundleRef": "b", "environment": "prod", "pipelineName": "p"}, `{"shouldRollback":true}`},
	"scheduleclocks":   {"ScheduleClock", map[string]interface{}{"interval": "1m"}, `{"tick":"forged"}`},
	"scmproviders": {"ScmProvider", map[string]interface{}{"type": "github", "secretRef": map[string]interface{}{"name": "gh"}},
		`{"observedGeneration":7}`},
	"subscriptions": {"Subscription", map[string]interface{}{"pipeline": "p", "type": "image",
		"image": map[string]interface{}{"registry": "ghcr.io/x/app"}}, `{"lastSeenDigest":"sha256:forged"}`},
}

// statusCRDs returns the plural and scope of every CRD in config/crd/bases
// that has a status subresource.
func statusCRDs(t *testing.T) map[string]apiextensionsv1.ResourceScope {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "config", "crd", "bases")
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := map[string]apiextensionsv1.ResourceScope{}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		require.NoError(t, err)
		var crd apiextensionsv1.CustomResourceDefinition
		require.NoError(t, yaml.Unmarshal(raw, &crd), f.Name())
		for _, v := range crd.Spec.Versions {
			if v.Subresources != nil && v.Subresources.Status != nil {
				out[crd.Spec.Names.Plural] = crd.Spec.Scope
			}
		}
	}
	require.NotEmpty(t, out)
	return out
}

// statusRules returns, per rendered ValidatingAdmissionPolicy, the
// kardinal.io status subresources it matches on UPDATE.
func statusRules(t *testing.T, args ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, d := range render(t, "kardinal-promoter", args...) {
		if d.Kind != "ValidatingAdmissionPolicy" {
			continue
		}
		var vap admissionregistrationv1.ValidatingAdmissionPolicy
		decodeStrict(t, d, &vap)
		if vap.Spec.MatchConstraints == nil {
			continue
		}
		for _, r := range vap.Spec.MatchConstraints.ResourceRules {
			update := false
			for _, op := range r.Operations {
				update = update || op == admissionregistrationv1.Update || op == admissionregistrationv1.OperationAll
			}
			if !update || !slices.Contains(r.APIGroups, "kardinal.io") {
				continue
			}
			for _, res := range r.Resources {
				if strings.HasSuffix(res, "/status") {
					out[vap.Name] = append(out[vap.Name], res)
				}
			}
		}
	}
	return out
}

// TestStatusWriters_Sweep: every status subresource of a kardinal CRD is
// either written only by the controller under an admission policy, or
// cluster-scoped and named in clusterStatusKinds. A new CRD with a status
// fails here until it is placed.
//
// Covers STATUS-WRITERS-01.
func TestStatusWriters_Sweep(t *testing.T) {
	t.Parallel()
	crds := statusCRDs(t)
	rules := statusRules(t)

	var policy []string
	for _, k := range statusWriterKinds {
		policy = append(policy, k+"/status")
		assert.Equal(t, apiextensionsv1.NamespaceScoped, crds[k], "%s is a namespaced kind with a status", k)
	}
	assert.ElementsMatch(t, policy, rules["kardinal-promoter-status-writers"], "status-writers matches exactly the kinds of statusWriterKinds")

	var cluster []string
	for k, scope := range crds {
		if scope == apiextensionsv1.ClusterScoped {
			cluster = append(cluster, k)
			continue
		}
		var by []string
		for name, res := range rules {
			if slices.Contains(res, k+"/status") {
				by = append(by, name)
			}
		}
		sort.Strings(by)
		assert.NotEmpty(t, by, "%s/status: no policy keeps it the controller's; add it to status-writers", k)
	}
	assert.ElementsMatch(t, clusterStatusKinds, cluster, "the cluster-scoped kinds with a status")
	assert.Equal(t, []string{"agentindexes/status"}, rules["kardinal-promoter-agent-index"], "agentindexes has its own policy")
}

// TestStatusWriters_Admits: only this release's controller, and the
// usernames admission.controllerUsernames lists, write a covered status; a
// tenant, a cluster admin and kro's Graph ServiceAccount are refused.
//
// Covers STATUS-WRITERS-01.
func TestStatusWriters_Admits(t *testing.T) {
	t.Parallel()
	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	shardB := "system:serviceaccount:kardinal-b:kardinal-promoter"
	sa := []interface{}{"system:serviceaccounts", "system:authenticated"}
	vap := identityPolicy(t, "status-writers")
	listed := identityPolicy(t, "status-writers", "--set", "admission.controllerUsernames={"+shardB+"}")
	for _, kind := range statusWriterKinds {
		f := statusFixtures[kind]
		obj := func(status string) map[string]interface{} {
			return map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": f.kind,
				"metadata": map[string]interface{}{"name": "x", "namespace": "payments"},
				"spec":     f.spec, "status": map[string]interface{}{"reason": status}}
		}
		req := map[string]interface{}{"subResource": "status", "kind": map[string]interface{}{"kind": f.kind},
			"resource": map[string]interface{}{"group": "kardinal.io", "resource": kind}}
		cur, old := obj("forged"), obj("")
		for _, tc := range []struct {
			name   string
			vap    *admissionregistrationv1.ValidatingAdmissionPolicy
			user   string
			groups []interface{}
			want   bool
		}{
			{"the controller", vap, controller, sa, true},
			{"a tenant", vap, "alice", []interface{}{"system:authenticated"}, false},
			{"a cluster admin", vap, "kubernetes-admin", []interface{}{"system:masters", "system:authenticated"}, false},
			{"kro's Graph ServiceAccount", vap, "system:serviceaccount:payments:kardinal-graph", sa, false},
			{"another shard's controller, not listed", vap, shardB, sa, false},
			{"another shard's controller, listed", listed, shardB, sa, true},
			{"this release's controller, with another listed", listed, controller, sa, true},
		} {
			assert.Equal(t, tc.want, admitsGroups(t, tc.vap, cur, old, tc.user, tc.groups, req), "%s: %s", kind, tc.name)
		}
	}
	// Per-promotion MetricCheck and ChangeRequest instances are left to
	// graph-objects, which refuses the same writes, so one policy answers.
	graphObjects := identityPolicy(t, "graph-objects")
	for _, kind := range []string{"metricchecks", "changerequests"} {
		f := statusFixtures[kind]
		inst := map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": f.kind,
			"metadata": map[string]interface{}{"name": "x", "namespace": "payments",
				"labels": map[string]interface{}{"kardinal.io/bundle": "b1"}}, "spec": f.spec}
		req := map[string]interface{}{"subResource": "status", "kind": map[string]interface{}{"kind": f.kind},
			"resource": map[string]interface{}{"group": "kardinal.io", "resource": kind}}
		assert.True(t, admitsGroups(t, vap, inst, inst, "alice", []interface{}{"system:authenticated"}, req),
			"%s instance: status-writers leaves it to graph-objects", kind)
		assert.False(t, admitsGroups(t, graphObjects, inst, inst, "alice", []interface{}{"system:authenticated"}, req),
			"%s instance: graph-objects refuses a tenant's status write", kind)
		assert.True(t, admitsGroups(t, graphObjects, inst, inst, controller, sa, req), "%s instance: the controller's", kind)
	}
	// The exception is MetricChecks' and ChangeRequests' only: a gate
	// instance (labelled kardinal.io/bundle) is still status-writers', since
	// graph-objects does not match policygates/status (QA-2 on #2485).
	gate := map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "PolicyGate",
		"metadata": map[string]interface{}{"name": "x", "namespace": "payments",
			"labels": map[string]interface{}{"kardinal.io/bundle": "b1"}}, "spec": statusFixtures["policygates"].spec}
	gateReq := map[string]interface{}{"subResource": "status", "kind": map[string]interface{}{"kind": "PolicyGate"},
		"resource": map[string]interface{}{"group": "kardinal.io", "resource": "policygates"}}
	assert.False(t, admitsGroups(t, vap, gate, gate, "alice", []interface{}{"system:authenticated"}, gateReq),
		"a gate instance's status stays the controller's")
	assert.True(t, admitsGroups(t, vap, gate, gate, controller, sa, gateReq), "the controller writes a gate instance's status")
	require.NotNil(t, vap.Spec.MatchConstraints)
	for _, r := range vap.Spec.MatchConstraints.ResourceRules {
		assert.Equal(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Update}, r.Operations,
			"a status subresource takes only update and patch")
	}
}
