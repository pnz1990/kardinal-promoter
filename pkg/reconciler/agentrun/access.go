// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentrun

import (
	"context"
	"fmt"
	"net"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// SubjectAccessReviews is the Authorizer that asks the API server.
type SubjectAccessReviews struct {
	Client client.Client
}

// Allowed reports whether user may do verb on the resource in namespace.
func (s SubjectAccessReviews) Allowed(ctx context.Context, user actor.User, verb, group, resource, subresource, namespace string) (bool, error) {
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user.Username, Groups: user.Groups,
		ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: verb, Group: group,
			Resource: resource, Subresource: subresource},
	}}
	if err := s.Client.Create(ctx, sar); err != nil {
		return false, fmt.Errorf("subjectaccessreview %s %s: %w", verb, resource, err)
	}
	return sar.Status.Allowed, nil
}

// parseCIDR returns s as a CIDR: s itself when it is one, a single address
// (/32 or /128) when it is an IP.
func parseCIDR(s string) (string, error) {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return "", fmt.Errorf("not a CIDR: %w", err)
		}
		return n.String(), nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "", fmt.Errorf("%q is not an IP address or CIDR", s)
	}
	if ip.To4() != nil {
		return ip.String() + "/32", nil
	}
	return ip.String() + "/128", nil
}
