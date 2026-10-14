// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// The rule evaluation below is the Kubernetes RBAC authorizer's, copied from
// kubernetes v1.37.1 (Copyright The Kubernetes Authors, Apache License 2.0):
// RuleAllows from plugin/pkg/auth/authorizer/rbac/rbac.go, the matchers from
// pkg/apis/rbac/v1/evaluation_helpers.go and appliesToUser from
// pkg/registry/rbac/validation/rule.go. That package lives in
// k8s.io/kubernetes, which is not importable as a module; the code is kept
// verbatim except for names, so the authorizer decides as the API server's.

package enterprise

import (
	"context"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

// rbacAuthorizer is the API server's RBAC authorizer over fixed Roles,
// ClusterRoles and bindings (DefaultRuleResolver.VisitRulesFor: every
// ClusterRoleBinding, then the RoleBindings of the request's namespace).
type rbacAuthorizer struct {
	clusterRoles        map[string]*rbacv1.ClusterRole
	roles               map[string]*rbacv1.Role // namespace/name
	clusterRoleBindings []rbacv1.ClusterRoleBinding
	roleBindings        []rbacv1.RoleBinding
}

var _ authorizer.Authorizer = (*rbacAuthorizer)(nil)

func (r *rbacAuthorizer) rulesFor(ref rbacv1.RoleRef, namespace string) []rbacv1.PolicyRule {
	switch ref.Kind {
	case "ClusterRole":
		if cr := r.clusterRoles[ref.Name]; cr != nil {
			return cr.Rules
		}
	case "Role":
		if ro := r.roles[namespace+"/"+ref.Name]; ro != nil {
			return ro.Rules
		}
	}
	return nil
}

func (r *rbacAuthorizer) Authorize(_ context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	for _, b := range r.clusterRoleBindings {
		if appliesTo(a.GetUser(), b.Subjects, "") && rulesAllow(a, r.rulesFor(b.RoleRef, "")...) {
			return authorizer.DecisionAllow, "ClusterRoleBinding " + b.Name, nil
		}
	}
	if ns := a.GetNamespace(); ns != "" {
		for _, b := range r.roleBindings {
			if b.Namespace == ns && appliesTo(a.GetUser(), b.Subjects, ns) && rulesAllow(a, r.rulesFor(b.RoleRef, ns)...) {
				return authorizer.DecisionAllow, "RoleBinding " + ns + "/" + b.Name, nil
			}
		}
	}
	return authorizer.DecisionNoOpinion, "", nil
}

// ConditionsAwareAuthorize: RBAC is not conditions-aware.
func (r *rbacAuthorizer) ConditionsAwareAuthorize(ctx context.Context, a authorizer.Attributes) authorizer.ConditionsAwareDecision {
	return authorizer.ConditionsAwareDecisionFromParts(r.Authorize(ctx, a))
}

// EvaluateConditions is not supported, as by the RBAC authorizer.
func (*rbacAuthorizer) EvaluateConditions(context.Context, authorizer.ConditionsAwareDecision, authorizer.ConditionsData) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "", authorizer.ErrorConditionEvaluationNotSupported
}

func appliesTo(u user.Info, subjects []rbacv1.Subject, namespace string) bool {
	for _, s := range subjects {
		if appliesToUser(u, s, namespace) {
			return true
		}
	}
	return false
}

func appliesToUser(u user.Info, subject rbacv1.Subject, namespace string) bool {
	switch subject.Kind {
	case rbacv1.UserKind:
		return u.GetName() == subject.Name
	case rbacv1.GroupKind:
		for _, g := range u.GetGroups() {
			if g == subject.Name {
				return true
			}
		}
		return false
	case rbacv1.ServiceAccountKind:
		saNamespace := namespace
		if len(subject.Namespace) > 0 {
			saNamespace = subject.Namespace
		}
		if len(saNamespace) == 0 {
			return false
		}
		return serviceaccount.MatchesUsername(saNamespace, subject.Name, u.GetName())
	default:
		return false
	}
}

func rulesAllow(a authorizer.Attributes, rules ...rbacv1.PolicyRule) bool {
	for i := range rules {
		if ruleAllows(a, &rules[i]) {
			return true
		}
	}
	return false
}

func ruleAllows(a authorizer.Attributes, rule *rbacv1.PolicyRule) bool {
	if a.IsResourceRequest() {
		combinedResource := a.GetResource()
		if len(a.GetSubresource()) > 0 {
			combinedResource = a.GetResource() + "/" + a.GetSubresource()
		}
		return verbMatches(rule, a.GetVerb()) &&
			apiGroupMatches(rule, a.GetAPIGroup()) &&
			resourceMatches(rule, combinedResource, a.GetSubresource()) &&
			resourceNameMatches(rule, a.GetName())
	}
	return verbMatches(rule, a.GetVerb()) && nonResourceURLMatches(rule, a.GetPath())
}

func verbMatches(rule *rbacv1.PolicyRule, requestedVerb string) bool {
	for _, v := range rule.Verbs {
		if v == rbacv1.VerbAll || v == requestedVerb {
			return true
		}
	}
	return false
}

func apiGroupMatches(rule *rbacv1.PolicyRule, requestedGroup string) bool {
	for _, g := range rule.APIGroups {
		if g == rbacv1.APIGroupAll || g == requestedGroup {
			return true
		}
	}
	return false
}

func resourceMatches(rule *rbacv1.PolicyRule, combinedRequestedResource, requestedSubresource string) bool {
	for _, r := range rule.Resources {
		if r == rbacv1.ResourceAll || r == combinedRequestedResource {
			return true
		}
		if len(requestedSubresource) == 0 {
			continue
		}
		if len(r) == len(requestedSubresource)+2 && strings.HasPrefix(r, "*/") && strings.HasSuffix(r, requestedSubresource) {
			return true
		}
	}
	return false
}

func resourceNameMatches(rule *rbacv1.PolicyRule, requestedName string) bool {
	if len(rule.ResourceNames) == 0 {
		return true
	}
	for _, n := range rule.ResourceNames {
		if n == requestedName {
			return true
		}
	}
	return false
}

func nonResourceURLMatches(rule *rbacv1.PolicyRule, requestedURL string) bool {
	for _, u := range rule.NonResourceURLs {
		if u == rbacv1.NonResourceAll || u == requestedURL {
			return true
		}
		if strings.HasSuffix(u, "*") && strings.HasPrefix(requestedURL, strings.TrimRight(u, "*")) {
			return true
		}
	}
	return false
}
