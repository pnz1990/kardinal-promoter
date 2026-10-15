// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package enterprise

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/engine"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"sigs.k8s.io/yaml"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

const release = "kardinal-promoter"

// chartObjects renders the chart in process (helm.sh/helm/v4, the engine
// helm template uses) with its default values and returns the ClusterRoles
// and ValidatingAdmissionPolicies by name.
// chart is what the rendered chart holds of RBAC and admission.
type chart struct {
	roles    map[string]*rbacv1.ClusterRole
	crbs     []rbacv1.ClusterRoleBinding
	vaps     map[string]*admissionregistrationv1.ValidatingAdmissionPolicy
	bindings map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding
}

func chartObjects(t *testing.T) chart {
	t.Helper()
	chrt, err := loader.Load(filepath.Join(scenario.RepoRoot(), "chart", "kardinal-promoter"))
	require.NoError(t, err)
	vals, err := chartutil.ToRenderValues(chrt, map[string]any{}, common.ReleaseOptions{Name: release, Namespace: "kardinal-system",
		IsInstall: true}, common.DefaultCapabilities)
	require.NoError(t, err)
	files, err := engine.Render(chrt, vals)
	require.NoError(t, err)
	roles := map[string]*rbacv1.ClusterRole{}
	vaps := map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{}
	var crbs []rbacv1.ClusterRoleBinding
	vapbs := map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	for name, content := range files {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		for _, doc := range strings.Split(content, "\n---") {
			var head struct {
				Kind string `json:"kind"`
			}
			if err := yaml.Unmarshal([]byte(doc), &head); err != nil || head.Kind == "" {
				continue
			}
			switch head.Kind {
			case "ClusterRole":
				var r rbacv1.ClusterRole
				require.NoError(t, utilyaml.Unmarshal([]byte(doc), &r), name)
				roles[r.Name] = &r
			case "ClusterRoleBinding":
				var b rbacv1.ClusterRoleBinding
				require.NoError(t, utilyaml.Unmarshal([]byte(doc), &b), name)
				crbs = append(crbs, b)
			case "ValidatingAdmissionPolicyBinding":
				var b admissionregistrationv1.ValidatingAdmissionPolicyBinding
				require.NoError(t, utilyaml.Unmarshal([]byte(doc), &b), name)
				vapbs[b.Name] = &b
			case "ValidatingAdmissionPolicy":
				var v admissionregistrationv1.ValidatingAdmissionPolicy
				require.NoError(t, utilyaml.Unmarshal([]byte(doc), &v), name)
				vaps[v.Name] = &v
			}
		}
	}
	return chart{roles: roles, crbs: crbs, vaps: vaps, bindings: vapbs}
}

func rm(i int) (string, []string) {
	return fmt.Sprintf("rm-%02d@corp.example.com", i), []string{team(i) + "-release", "system:authenticated"}
}

func dev(i int) (string, []string) {
	return fmt.Sprintf("dev-%02d@corp.example.com", i), []string{team(i) + "-dev", "system:authenticated"}
}

// teamBindings binds, in each team's namespace, <release>-approver to the
// team's release managers and <release>-viewer to its developers.
func teamBindings(teams int) []rbacv1.RoleBinding {
	var out []rbacv1.RoleBinding
	for i := 1; i <= teams; i++ {
		ns := team(i)
		out = append(out,
			rbacv1.RoleBinding{ObjectMeta: objectMeta(ns, "release-managers"), RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: release + "-approver"},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: ns + "-release"}}},
			rbacv1.RoleBinding{ObjectMeta: objectMeta(ns, "developers"), RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: release + "-viewer"},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: ns + "-dev"}}})
	}
	return out
}

// tenancyViolations returns every request of a team member that a enterprise
// must refuse but authz allows, and every own-team request it refuses: for
// every pair of teams, team A's release manager reads, approves, rejects
// and overrides in A only, edits no Pipeline anywhere; A's developer reads
// in A only and approves and rejects nowhere.
func tenancyViolations(authz authorizer.Authorizer, teams int) []string {
	var out []string
	check := func(username string, groups []string, verb, resource, sub, ns string, want bool) {
		d, _, _ := authz.Authorize(context.Background(), authorizer.AttributesRecord{User: &user.DefaultInfo{Name: username, Groups: groups},
			Verb: verb, APIGroup: "kardinal.io", APIVersion: "v1alpha1", Resource: resource, Subresource: sub, Namespace: ns,
			ResourceRequest: true})
		if got := d == authorizer.DecisionAllow; got != want {
			out = append(out, fmt.Sprintf("%s %s %s/%s in %s: allowed=%v, want %v", username, verb, resource, sub, ns, got, want))
		}
	}
	reads := []string{"pipelines", "bundles", "policygates", "promotionsteps", "approvals", "auditevents", "prstatuses"}
	for i := 1; i <= teams; i++ {
		for j := 1; j <= teams; j++ {
			own, ns := i == j, team(j)
			u, g := rm(i)
			for _, res := range reads {
				for _, verb := range []string{"get", "list", "watch"} {
					check(u, g, verb, res, "", ns, own)
				}
			}
			check(u, g, "create", "approvals", "", ns, own)
			check(u, g, "update", "bundles", "reject", ns, own)
			check(u, g, "update", "policygates", "override", ns, own)
			check(u, g, "patch", "pipelines", "", ns, false)
			d, dg := dev(i)
			check(d, dg, "list", "bundles", "", ns, own)
			check(d, dg, "create", "approvals", "", ns, false)
			check(d, dg, "update", "bundles", "reject", ns, false)
		}
	}
	return out
}

// TestEnterprise_TenantRBAC: with the chart's user roles bound per team
// namespace (release managers: <release>-approver, developers:
// <release>-viewer) and the chart's own ClusterRoleBindings, the API
// server's RBAC authorizer (authz_test.go) lets no member of any of 20 teams
// read, approve, reject or override in another team's namespace, developers
// approve and reject nowhere and approvers edit no Pipeline; the check fails
// with one ClusterRoleBinding of the approver role. The API server's
// ValidatingAdmissionPolicy plugin, with the chart's policies and bindings,
// refuses an Approval in another user's name or with groups the requester
// does not have, and a rejection in another user's name; without the
// bindings, or with Audit-only bindings, it refuses none.
// Covers SCEN-TENANT-RBAC-01.
func TestEnterprise_TenantRBAC(t *testing.T) {
	c := chartObjects(t)
	for _, name := range []string{"-viewer", "-approver", "-promoter"} {
		require.Contains(t, c.roles, release+name)
	}
	// The chart binds cluster-wide only its own ServiceAccounts, and the
	// agent directory (docs/actors.md): every authenticated user reads the
	// cluster-scoped agent registry, and nothing else.
	for _, b := range c.crbs {
		if b.Name == release+"-agent-directory-reader" {
			role := c.roles[b.RoleRef.Name]
			require.NotNil(t, role, b.RoleRef.Name)
			for _, r := range role.Rules {
				assert.ElementsMatch(t, []string{"agentidentities", "agentindexes"}, r.Resources)
				assert.ElementsMatch(t, []string{"get", "list", "watch"}, r.Verbs)
			}
			continue
		}
		for _, sub := range b.Subjects {
			assert.Equal(t, rbacv1.ServiceAccountKind, sub.Kind, "ClusterRoleBinding %s binds %s %s", b.Name, sub.Kind, sub.Name)
		}
	}
	teams := full.Teams
	authz := &rbacAuthorizer{clusterRoles: c.roles, clusterRoleBindings: c.crbs, roleBindings: teamBindings(teams)}
	assert.Empty(t, tenancyViolations(authz, teams))

	// The same check with one cluster-wide binding of the approver role:
	// team-01's release managers approve everywhere.
	leaky := *authz
	leaky.clusterRoleBindings = append(append([]rbacv1.ClusterRoleBinding(nil), c.crbs...), rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "leak"}, RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: release + "-approver"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: team(1) + "-release"}}})
	assert.NotEmpty(t, tenancyViolations(&leaky, teams), "a cluster-wide approver binding must show as a tenancy violation")

	u1, g1 := rm(1)
	u2, g2 := rm(2)
	approval := func(username string, groups []string) map[string]interface{} {
		return map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Approval",
			"metadata": map[string]interface{}{"namespace": team(1), "name": "a",
				"labels": map[string]interface{}{"kardinal.io/bundle": "app-01-1-1-0", "kardinal.io/environment": "prod"}},
			"spec": map[string]interface{}{"bundle": "app-01-1-1-0", "bundleUID": "u1", "environment": "prod", "user": username,
				"groups": toIfaces(groups), "decision": "approve"},
		}
	}
	bundle := func(by string) map[string]interface{} {
		b := map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
			"metadata": map[string]interface{}{"namespace": team(1), "name": "app-01-1-1-0"},
			"spec":     map[string]interface{}{"type": "image", "pipeline": "app-01"}}
		if by != "" {
			b["spec"].(map[string]interface{})["rejected"] = map[string]interface{}{"by": by, "reason": "bad"}
		}
		return b
	}
	var namespaces []string
	for i := 1; i <= teams; i++ {
		namespaces = append(namespaces, team(i))
	}
	policies := []string{release + "-approvals", release + "-bundle-rejection"}
	cases := []struct {
		name string
		run  func(s *admissionServer) error
		deny bool
	}{
		{"an Approval in your own name", func(s *admissionServer) error {
			return s.admit(admission.Create, approvalsGVR, approval(u1, g1[:1]), nil, u1, g1)
		}, false},
		{"an Approval in another user's name", func(s *admissionServer) error {
			return s.admit(admission.Create, approvalsGVR, approval(u2, g2[:1]), nil, u1, g1)
		}, true},
		{"an Approval with another team's group", func(s *admissionServer) error {
			return s.admit(admission.Create, approvalsGVR, approval(u1, g2[:1]), nil, u1, g1)
		}, true},
		{"a rejection in your own name", func(s *admissionServer) error {
			return s.admit(admission.Update, bundlesGVR, bundle(u1), bundle(""), u1, g1)
		}, false},
		{"a rejection in another user's name", func(s *admissionServer) error {
			return s.admit(admission.Update, bundlesGVR, bundle(u2), bundle(""), u1, g1)
		}, true},
	}
	for _, setup := range []struct {
		name     string
		bindings bool
		edit     func(*admissionregistrationv1.ValidatingAdmissionPolicyBinding)
		enforced bool
	}{
		{"chart bindings", true, nil, true},
		{"no bindings", false, nil, false},
		{"Audit-only bindings", true, func(b *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.ValidationActions = []admissionregistrationv1.ValidationAction{admissionregistrationv1.Audit}
		}, false},
	} {
		t.Run(setup.name, func(t *testing.T) {
			bindings := c.bindings
			if !setup.bindings {
				bindings = nil
			}
			s := newAdmissionServer(t, policyObjects(namespaces, c.vaps, bindings, policies, setup.edit), authz)
			for _, tc := range cases {
				err := tc.run(s)
				if tc.deny && setup.enforced {
					if assert.Error(t, err, tc.name) {
						// Denied by a validation, not by a policy that failed to load.
						assert.Contains(t, err.Error(), "denied request", tc.name)
						assert.NotContains(t, err.Error(), "failed to configure", tc.name)
					}
				} else {
					assert.NoError(t, err, tc.name)
				}
			}
		})
	}
}

func toIfaces(s []string) []interface{} {
	out := make([]interface{}, 0, len(s))
	for _, v := range s {
		out = append(out, v)
	}
	return out
}

func objectMeta(ns, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: ns, Name: name}
}
