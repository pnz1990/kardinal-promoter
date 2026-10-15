//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/docswalk"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// checkWalkManifest holds the walked pages to docswalk.Manifest, as
// test/examples TestDocsWalkManifest does on every pull request.
func checkWalkManifest(t *testing.T) {
	t.Helper()
	problems, err := docswalk.Problems(framework.RepoRoot(t))
	require.NoError(t, err)
	assert.Empty(t, problems)
}
