// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package coverage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuiteRuns(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hack", "e2e"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hack", "e2e", "up.sh"), []byte(`case "$SUITE" in
  core) COMPONENTS=(a.sh)
    RUN='^Test(Core|Gate)_' ;;
  gitea) COMPONENTS=(b.sh) RUN='^Test(Core|Gitea)_' ;;
  gitlab) COMPONENTS=(c.sh) RUN='^Test(Core|GitLab)_' ;;
  delivery) COMPONENTS=(d.sh)
    RUN='^TestDelivery_' ;;
esac
`), 0o600))
	runs, err := SuiteRuns(root)
	require.NoError(t, err)
	got := map[string]string{}
	for suite, re := range runs {
		got[suite] = re.String()
	}
	assert.Equal(t, map[string]string{
		"core":     "^Test(Core|Gate)_",
		"gitea":    "^Test(Core|Gitea)_",
		"gitlab":   "^Test(Core|GitLab)_",
		"delivery": "^TestDelivery_",
	}, got, "a suite's own RUN, not the next suite's")
}
