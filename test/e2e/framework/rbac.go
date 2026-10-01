// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Access is one API request a SubjectAccessReview asks about. An empty
// Namespace is a cluster-wide request (every namespace, or a cluster-scoped
// kind).
type Access struct {
	Verb        string
	Group       string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
}

func (a Access) String() string {
	r := a.Resource
	if a.Group != "" {
		r += "." + a.Group
	}
	if a.Subresource != "" {
		r += "/" + a.Subresource
	}
	if a.Name != "" {
		r += " " + a.Name
	}
	where := "cluster-wide"
	if a.Namespace != "" {
		where = "in " + a.Namespace
	}
	return fmt.Sprintf("%s %s %s", a.Verb, r, where)
}

// ServiceAccountUser is the user name the API server gives ServiceAccount
// name in ns.
func ServiceAccountUser(ns, name string) string {
	return "system:serviceaccount:" + ns + ":" + name
}

// Can asks the API server, with a SubjectAccessReview, whether ServiceAccount
// user (see ServiceAccountUser) may make request a. It answers what RBAC
// grants the identity, the same check the API server makes for its requests.
func (e *Env) Can(t *testing.T, user string, a Access) bool {
	t.Helper()
	groups := []string{"system:serviceaccounts", "system:authenticated"}
	if rest, ok := strings.CutPrefix(user, "system:serviceaccount:"); ok {
		if ns, _, ok := strings.Cut(rest, ":"); ok {
			groups = append(groups, "system:serviceaccounts:"+ns)
		}
	}
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:   user,
		Groups: groups,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: a.Namespace, Verb: a.Verb, Group: a.Group, Resource: a.Resource,
			Subresource: a.Subresource, Name: a.Name,
		},
	}}
	got, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(context.Background(), sar, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("SubjectAccessReview %s for %s: %v", a, user, err)
	}
	return got.Status.Allowed
}
