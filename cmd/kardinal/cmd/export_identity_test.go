// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"testing"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// StubIdentity makes commands see user as the authenticated caller until t
// ends, for tests against a fake client, which cannot answer a
// SelfSubjectReview.
func StubIdentity(t testing.TB, user string) {
	t.Helper()
	old := identityOf
	identityOf = func(context.Context, sigs_client.Client) (Identity, error) {
		return Identity{Username: user}, nil
	}
	t.Cleanup(func() { identityOf = old })
}
