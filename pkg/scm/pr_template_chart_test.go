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

// TestRenderPRBody_Chart: a chart Bundle's PR body lists the chart, version,
// repository and digest; its rollback note says it restores the chart
// version; BundleVersion names "<chart> <version>".
func TestRenderPRBody_Chart(t *testing.T) {
	spec := v1alpha1.BundleSpec{Type: "chart", Chart: &v1alpha1.ChartRef{
		Name: "podinfo", Version: "6.15.0", RepoURL: "oci://ghcr.io/org/charts", Digest: "sha256:abc"}}
	assert.Equal(t, "podinfo 6.15.0", scm.BundleVersion(spec))

	body, err := scm.RenderPRBody(scm.PRBody{PipelineName: "app", Environment: "prod", BundleName: "app-chart", Bundle: spec})
	require.NoError(t, err)
	assert.Contains(t, body, "| Chart | Version | Repository | Digest |\n|---|---|---|---|\n"+
		"| podinfo | 6.15.0 | oci://ghcr.io/org/charts | sha256:abc |\n")

	body, err = scm.RenderPRBody(scm.PRBody{PipelineName: "app", Environment: "prod", BundleName: "app-rollback-x",
		RollbackOf: "app-chart-old", RestoredVersion: scm.BundleVersion(spec), Bundle: spec})
	require.NoError(t, err)
	assert.Contains(t, body, "> **This is a rollback PR.** It restores the chart version of bundle app-chart-old in environment prod.\n")
}
