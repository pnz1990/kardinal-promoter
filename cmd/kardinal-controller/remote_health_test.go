// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoteClusters_EKSFlags (#2008, #2193): EKS tokens are signed when
// either --health-eks-role-arns or --health-eks-role-arns-by-namespace
// names a role, and not at all (EKS nil) when both are empty, the default;
// a malformed namespace entry stops the controller at start-up.
func TestRemoteClusters_EKSFlags(t *testing.T) {
	off, err := remoteClusters("", "")
	require.NoError(t, err)
	assert.Nil(t, off.EKS, "off by default")

	global, err := remoteClusters("arn:aws:iam::111122223333:role/kardinal-health-*", "")
	require.NoError(t, err)
	require.NotNil(t, global.EKS)
	assert.True(t, global.EKS.Allowed("team-a", "arn:aws:iam::111122223333:role/kardinal-health-team-a"))

	byNS, err := remoteClusters("", "team-a=arn:aws:iam::111122223333:role/kardinal-health-team-a,"+
		"team-b=arn:aws:iam::111122223333:role/kardinal-health-team-b")
	require.NoError(t, err)
	require.NotNil(t, byNS.EKS, "a namespace entry alone turns it on")
	assert.True(t, byNS.EKS.Allowed("team-a", "arn:aws:iam::111122223333:role/kardinal-health-team-a"))
	assert.False(t, byNS.EKS.Allowed("team-b", "arn:aws:iam::111122223333:role/kardinal-health-team-a"),
		"team-b may not name team-a's role")

	_, err = remoteClusters("", "arn:aws:iam::111122223333:role/no-namespace")
	assert.Error(t, err)
}
