// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/docswalk"
)

// TestDocsWalkManifest fails a docs change that adds, splits or removes a bash
// block on a page the live docs walkthrough runs, until the block is run or
// skipped with a reason in test/e2e/docswalk: otherwise only the nightly
// docs-walkthrough suite would notice.
func TestDocsWalkManifest(t *testing.T) {
	problems, err := docswalk.Problems(repoRoot(t))
	require.NoError(t, err)
	assert.Empty(t, problems)
}
