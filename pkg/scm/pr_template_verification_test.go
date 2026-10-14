// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestPRBody_Verification: a verify gate's verdicts are in the PR body, one
// row each, with the verifier, the result, whether it counted, the summary,
// the evidence link and the findings; a body without verify gates has no
// Verification section.
func TestPRBody_Verification(t *testing.T) {
	spec := v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: "1.5.0"}}}
	body, err := scm.RenderPRBody(scm.PRBody{PipelineName: "app", Environment: "prod", BundleName: "app-v1", Bundle: spec,
		Verifications: []scm.Verification{{Gate: "prod-needs-qa", Check: "exploratory-qa", Environment: "uat",
			Reason: "verified: 2 of 2 (qa-explorer ×2) (version 1.5.0)",
			Verdicts: []v1alpha1.GateVerdictStatus{
				{Name: "app-r1-a1", Verifier: "agent qa-explorer for bob", Result: "pass", Counted: true, Run: 1, Attempt: 1,
					Summary: "41 pages, all good", EvidenceURL: "https://ci.example.com/1", Findings: 1},
				{Name: "app-self", Verifier: "agent fixer for alice", Result: "pass",
					Reason: "not counted: the author's party (agent fixer for alice)"},
			}}}})
	require.NoError(t, err)
	assert.Contains(t, body, "### Verification: exploratory-qa in uat")
	assert.Contains(t, body, "verified: 2 of 2 (qa-explorer ×2) (version 1.5.0) (prod-needs-qa)")
	assert.Contains(t, body, "| app-r1-a1 (run 1, attempt 1) | agent qa-explorer for bob | pass | yes | 41 pages, all good | [link](https://ci.example.com/1) | 1 |")
	assert.Contains(t, body, "| app-self | agent fixer for alice | pass | no: not counted: the author's party (agent fixer for alice) | — | — | 0 |")

	plain, err := scm.RenderPRBody(scm.PRBody{PipelineName: "app", Environment: "prod", BundleName: "app-v1", Bundle: spec})
	require.NoError(t, err)
	assert.NotContains(t, plain, "### Verification")
}
