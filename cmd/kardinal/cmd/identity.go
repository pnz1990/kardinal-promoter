// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	authenticationv1 "k8s.io/api/authentication/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
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

// stampCreator records the authenticated user as the Bundle's creator
// (kardinal.io/created-by), which an approval gate's excludeAuthor reads, and
// stamps the Bundle with the caller's actor (spec.actor, docs/actors.md).
// The chart's admission policies refuse any other name. When the API server
// does not say who the caller is, the Bundle is created without either and a
// warning is printed: excludeAuthor then blocks the gate. It fails when the
// caller is an agent that may not act on this Pipeline.
func stampCreator(ctx context.Context, c sigs_client.Client, b *v1alpha1.Bundle) error {
	id, err := identityOf(ctx, c)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "warning: the version records no creator (%v); an approval gate with excludeAuthor will not pass it\n", err)
		return nil
	}
	lifecycle.StampCreatedBy(b, id.Username)
	stamp, err := stampOf(ctx, c, id, actor.Where{Namespace: b.Namespace, Pipeline: b.Spec.Pipeline})
	if err != nil {
		return err
	}
	b.Spec.Actor = stamp
	return nil
}

// creatorOf is the caller's username for kardinal.io/created-by, or "" with
// a warning on stderr when the API server cannot say.
func creatorOf(ctx context.Context, c sigs_client.Client) string {
	id, err := identityOf(ctx, c)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "warning: the version records no creator (%v); an approval gate with excludeAuthor will not pass it\n", err)
		return ""
	}
	return id.Username
}

// assertedOf is what the caller says about itself, from the environment
// (KARDINAL_ACTOR_MODEL, KARDINAL_ACTOR_SESSION, KARDINAL_ACTOR_CLIENT):
// recorded in the stamp's asserted fields, never trusted.
var assertedOf = func() v1alpha1.AssertedActor {
	return v1alpha1.AssertedActor{Model: os.Getenv("KARDINAL_ACTOR_MODEL"),
		Session: os.Getenv("KARDINAL_ACTOR_SESSION"), Client: os.Getenv("KARDINAL_ACTOR_CLIENT")}
}

// assertedFor is assertedOf for a call: under kardinal mcp, the client the
// tool call came from (ctx's mcpClientKey) when KARDINAL_ACTOR_CLIENT names
// none.
func assertedFor(ctx context.Context) v1alpha1.AssertedActor {
	a := assertedOf()
	if label, ok := ctx.Value(mcpClientKey{}).(string); ok && a.Client == "" {
		a.Client = label
	}
	return a
}

// stampOf is the actor stamp for a write by id at w (docs/actors.md): id
// through the cluster's agent index, with the asserted fields. When the
// index cannot be read the stamp is left out with a warning: the chart's
// actor policy admits that for people and bots, and tells an agent which
// stamp to write. It fails when id is an agent that may not act at w.
func stampOf(ctx context.Context, c sigs_client.Client, id Identity, w actor.Where) (*v1alpha1.Actor, error) {
	src := &actor.Source{Reader: c}
	r, err := src.Resolve(ctx, actor.User{Username: id.Username, Groups: id.Groups}, w)
	switch {
	case errors.Is(err, actor.ErrConflict), errors.Is(err, actor.ErrBlocked), errors.Is(err, actor.ErrOutOfScope):
		return nil, err
	case err != nil:
		_, _ = fmt.Fprintf(errOut, "warning: cannot read the agent index (%v); writing without an actor stamp\n", err)
		return nil, nil
	}
	a := assertedFor(ctx)
	return actor.Stamp(r, &a), nil
}

// changeBy is the spec.lastChange record of a pause, resume or hold release
// by this CLI's user: the username the API server authenticates, the actor
// stamp and reason (the Pipeline reconciler names them in the change's
// AuditEvent). Without an identity it warns and records none: the change is
// still made, and audited with an unknown actor.
func changeBy(ctx context.Context, c sigs_client.Client, reason string, w actor.Where) (*v1alpha1.PipelineChange, error) {
	id, err := identityOf(ctx, c)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "warning: the change records no actor (%v); its AuditEvent names none\n", err)
		return nil, nil
	}
	stamp, err := stampOf(ctx, c, id, w)
	if err != nil {
		return nil, err
	}
	return &v1alpha1.PipelineChange{By: id.Username, Reason: reason, Actor: stamp}, nil
}
