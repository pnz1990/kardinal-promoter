// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package enterprise

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
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
	"sigs.k8s.io/yaml"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

const release = "kardinal-promoter"

// chartObjects renders the chart in process (helm.sh/helm/v4, the engine
// helm template uses) with its default values and returns the ClusterRoles
// and ValidatingAdmissionPolicies by name.
func chartObjects(t *testing.T) (map[string]*rbacv1.ClusterRole, map[string]*admissionregistrationv1.ValidatingAdmissionPolicy,
	[]rbacv1.ClusterRoleBinding) {
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
			case "ValidatingAdmissionPolicy":
				var v admissionregistrationv1.ValidatingAdmissionPolicy
				require.NoError(t, utilyaml.Unmarshal([]byte(doc), &v), name)
				vaps[v.Name] = &v
			}
		}
	}
	return roles, vaps, crbs
}

// authorizer decides requests the way the API server's RBAC authorizer does
// for RoleBindings to ClusterRoles: a binding in the request's namespace to
// one of the subject's user or groups grants the role's rules there.
type authorizer struct {
	roles    map[string]*rbacv1.ClusterRole
	bindings []rbacv1.RoleBinding
}

type request struct {
	user      string
	groups    []string
	verb      string
	resource  string // resource or resource/subresource
	namespace string
}

func (a *authorizer) allowed(r request) bool {
	for _, b := range a.bindings {
		if b.Namespace != r.namespace || b.RoleRef.Kind != "ClusterRole" {
			continue
		}
		subject := false
		for _, s := range b.Subjects {
			subject = subject || (s.Kind == rbacv1.UserKind && s.Name == r.user) ||
				(s.Kind == rbacv1.GroupKind && slices.Contains(r.groups, s.Name))
		}
		role := a.roles[b.RoleRef.Name]
		if !subject || role == nil {
			continue
		}
		for _, rule := range role.Rules {
			if matches(rule.APIGroups, "kardinal.io") && matches(rule.Resources, r.resource) && matches(rule.Verbs, r.verb) &&
				len(rule.ResourceNames) == 0 {
				return true
			}
		}
	}
	return false
}

func matches(list []string, v string) bool {
	return slices.Contains(list, "*") || slices.Contains(list, v)
}

// admits evaluates a ValidatingAdmissionPolicy's variables and validations
// on a request, as the API server does: all must be true.
func admits(t *testing.T, vap *admissionregistrationv1.ValidatingAdmissionPolicy, op string, object map[string]interface{},
	user string, groups []string) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType),
		cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType), ext.Strings())
	require.NoError(t, err)
	gs := make([]interface{}, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, g)
	}
	md, _ := object["metadata"].(map[string]interface{})
	variables := map[string]interface{}{}
	vars := map[string]interface{}{"object": object, "oldObject": nil, "variables": variables,
		"request": map[string]interface{}{"operation": op, "namespace": md["namespace"],
			"userInfo": map[string]interface{}{"username": user, "groups": gs}}}
	eval := func(expr string) interface{} {
		ast, iss := env.Compile(expr)
		require.NoError(t, iss.Err(), expr)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(vars)
		require.NoError(t, err, expr)
		return out.Value()
	}
	for _, v := range vap.Spec.Variables {
		variables[v.Name] = eval(v.Expression)
	}
	for _, v := range vap.Spec.Validations {
		if eval(v.Expression) != true {
			return false
		}
	}
	return true
}

// TestEnterprise_TenantRBAC: the chart binds no user cluster-wide, and with
// the chart's user roles bound per team
// namespace (the release managers of a team get <release>-approver, its
// developers <release>-viewer, in the team's namespace only), and the
// chart's admission policies, no team member can see, approve or reject
// anything of another team: for every pair of the 20 teams, team A's
// release manager may read, approve and reject in A and none of it in B,
// A's developer may read A only and approve nowhere, and the approvals
// policy refuses an Approval recorded in another user's name or with
// groups the requester does not have, and the bundle-rejection policy a
// rejection in another user's name. Covers SCEN-TENANT-RBAC-01.
func TestEnterprise_TenantRBAC(t *testing.T) {
	roles, vaps, crbs := chartObjects(t)
	// The chart binds cluster-wide only its own ServiceAccounts: a user
	// gets a role in a namespace by a RoleBinding there.
	for _, b := range crbs {
		for _, s := range b.Subjects {
			assert.Equal(t, rbacv1.ServiceAccountKind, s.Kind, "ClusterRoleBinding %s binds %s %s", b.Name, s.Kind, s.Name)
		}
	}
	for _, name := range []string{"-viewer", "-approver", "-promoter"} {
		require.Contains(t, roles, release+name)
	}
	approvals, rejection := vaps[release+"-approvals"], vaps[release+"-bundle-rejection"]
	require.NotNil(t, approvals)
	require.NotNil(t, rejection)

	teams := full.Teams
	a := &authorizer{roles: roles}
	rm := func(i int) (string, []string) {
		return fmt.Sprintf("rm-%02d@corp.example.com", i), []string{team(i) + "-release", "system:authenticated"}
	}
	dev := func(i int) (string, []string) {
		return fmt.Sprintf("dev-%02d@corp.example.com", i), []string{team(i) + "-dev", "system:authenticated"}
	}
	for i := 1; i <= teams; i++ {
		ns := team(i)
		a.bindings = append(a.bindings,
			rbacv1.RoleBinding{ObjectMeta: objectMeta(ns, "release-managers"), RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: release + "-approver"},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: ns + "-release"}}},
			rbacv1.RoleBinding{ObjectMeta: objectMeta(ns, "developers"), RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: release + "-viewer"},
				Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: ns + "-dev"}}})
	}
	reads := []string{"pipelines", "bundles", "policygates", "promotionsteps", "approvals", "auditevents", "prstatuses"}
	for i := 1; i <= teams; i++ {
		for j := 1; j <= teams; j++ {
			own := i == j
			ns := team(j)
			u, g := rm(i)
			for _, res := range reads {
				for _, verb := range []string{"get", "list", "watch"} {
					assert.Equal(t, own, a.allowed(request{u, g, verb, res, ns}), "%s %s %s in %s", u, verb, res, ns)
				}
			}
			assert.Equal(t, own, a.allowed(request{u, g, "create", "approvals", ns}), "%s approves in %s", u, ns)
			assert.Equal(t, own, a.allowed(request{u, g, "update", "bundles/reject", ns}), "%s rejects in %s", u, ns)
			assert.Equal(t, own, a.allowed(request{u, g, "update", "policygates/override", ns}), "%s overrides in %s", u, ns)
			assert.False(t, a.allowed(request{u, g, "patch", "pipelines", ns}), "%s edits a Pipeline in %s", u, ns)
			d, dg := dev(i)
			assert.Equal(t, own, a.allowed(request{d, dg, "list", "bundles", ns}), "%s lists Bundles in %s", d, ns)
			assert.False(t, a.allowed(request{d, dg, "create", "approvals", ns}), "%s approves in %s", d, ns)
			assert.False(t, a.allowed(request{d, dg, "update", "bundles/reject", ns}), "%s rejects in %s", d, ns)
		}
	}

	// The policies: an Approval and a rejection name their author.
	u1, g1 := rm(1)
	u2, g2 := rm(2)
	approval := func(user string, groups []string) map[string]interface{} {
		return map[string]interface{}{
			"metadata": map[string]interface{}{"namespace": team(1), "name": "a",
				"labels": map[string]interface{}{"kardinal.io/bundle": "app-01-1-1-0", "kardinal.io/environment": "prod"}},
			"spec": map[string]interface{}{"bundle": "app-01-1-1-0", "bundleUID": "u1", "environment": "prod", "user": user,
				"groups": toIfaces(groups), "decision": "approve"},
		}
	}
	assert.True(t, admits(t, approvals, "CREATE", approval(u1, g1[:1]), u1, g1), "an approval in your own name")
	assert.False(t, admits(t, approvals, "CREATE", approval(u2, g2[:1]), u1, g1), "an approval in another user's name")
	assert.False(t, admits(t, approvals, "CREATE", approval(u1, g2[:1]), u1, g1), "an approval with another team's group")
	rejected := func(by string) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"namespace": team(1), "name": "app-01-1-1-0"},
			"spec": map[string]interface{}{"pipeline": "app-01", "rejected": map[string]interface{}{"by": by, "reason": "bad"}}}
	}
	assert.True(t, admits(t, rejection, "UPDATE", rejected(u1), u1, g1), "a rejection in your own name")
	assert.False(t, admits(t, rejection, "UPDATE", rejected(u2), u1, g1), "a rejection in another user's name")
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
