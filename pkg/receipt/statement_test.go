// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestGateTemplateDigestMatches: the digest the Graph builder stamps on a
// gate instance is the one a receipt records for its template.
func TestGateTemplateDigestMatches(t *testing.T) {
	spec := v1alpha1.PolicyGateSpec{Expression: "approvals.count >= 1", Message: "m",
		Approval: &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"sre"}}}
	d, err := TemplateDigest(spec)
	require.NoError(t, err)
	assert.Equal(t, d, graph.GateTemplateDigest(spec))
}
