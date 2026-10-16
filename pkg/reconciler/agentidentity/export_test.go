// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentidentity

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// NewEnsureIndexForTest is the replica's start-up index build, for tests.
func NewEnsureIndexForTest(c client.Client, name string) interface {
	Start(ctx context.Context) error
	NeedLeaderElection() bool
} {
	return &ensureIndex{r: &IndexReconciler{Client: c, Name: name}}
}

// WriteStatusesForTest writes the statuses of ids as one build would, from
// their own Evaluate.
func WriteStatusesForTest(ctx context.Context, c client.Client, ids []v1alpha1.AgentIdentity) error {
	_, err := writeStatuses(ctx, c, ids, actor.Evaluate(ids, actor.MaxIndexBytes))
	return err
}
