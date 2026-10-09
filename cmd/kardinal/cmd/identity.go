// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// Identity is the user the Kubernetes API server authenticates the CLI as.
type Identity struct {
	Username string
	Groups   []string
}

// identityOf is how commands read the caller's identity: whoAmI, replaced
// in tests that run a command against a fake client.
var identityOf = whoAmI

// errNoIdentity is returned when the API server answers a SelfSubjectReview
// without a username.
var errNoIdentity = errors.New("the API server returned no username for this kubeconfig")

// whoAmI asks the API server who the CLI's credentials belong to, with a
// SelfSubjectReview (authentication.k8s.io/v1, Kubernetes 1.28+), the same
// call `kubectl auth whoami` makes. Every record that names a person
// (spec.rejected.by, overrides, approvals) is written with this username,
// because the chart's ValidatingAdmissionPolicy admits it only when it equals
// the requesting user. The local OS user is never used: it is not what the
// cluster authenticated.
func whoAmI(ctx context.Context, c sigs_client.Client) (Identity, error) {
	review := &authenticationv1.SelfSubjectReview{}
	if err := c.Create(ctx, review); err != nil {
		return Identity{}, fmt.Errorf("read your identity (SelfSubjectReview): %w", err)
	}
	info := review.Status.UserInfo
	if info.Username == "" {
		return Identity{}, errNoIdentity
	}
	return Identity{Username: info.Username, Groups: info.Groups}, nil
}
