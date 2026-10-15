// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentidentity

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewEnsureIndexForTest is the replica's start-up index build, for tests.
func NewEnsureIndexForTest(c client.Client, name string) interface {
	Start(ctx context.Context) error
	NeedLeaderElection() bool
} {
	return &ensureIndex{r: &IndexReconciler{Client: c, Name: name}}
}
