// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"testing"

	"github.com/stretchr/testify/assert"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestRejectionStamp (#2001 QA B3): a rejection's stamp is recorded only
// when it names the rejecting user.
func TestRejectionStamp(t *testing.T) {
	alice := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}
	assert.Equal(t, alice, rejectionStamp(&kardinalv1alpha1.BundleRejection{By: "alice", Actor: alice}))
	assert.Nil(t, rejectionStamp(&kardinalv1alpha1.BundleRejection{By: "bob", Actor: alice}))
	assert.Nil(t, rejectionStamp(&kardinalv1alpha1.BundleRejection{By: "bob"}))
}
