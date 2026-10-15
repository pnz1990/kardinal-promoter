// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package docswalk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBashBlocks(t *testing.T) {
	text := "x\n```bash\nkardinal why\n```\n```text\nout\n```\n- item\n\n    ```bash\n    kubectl get pods\n    # out\n    ```\n"
	assert.Equal(t, []string{"kardinal why\n", "kubectl get pods\n# out\n"}, BashBlocks(text))
}

func TestProblems(t *testing.T) {
	saved := Manifest
	t.Cleanup(func() { Manifest = saved })
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	write := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "p.md"), []byte(body), 0o600))
	}
	Manifest = map[string][]Block{"p.md": {{Match: "kardinal why"}, {Match: "kind create", Skip: "the suite's cluster"}}}

	write("```bash\nkardinal why demo prod\n```\n```bash\nkind create cluster\n```\n")
	problems, err := Problems(root)
	require.NoError(t, err)
	assert.Empty(t, problems)

	// A new block, a block two entries match, and an entry with no block.
	write("```bash\nkardinal why demo prod\nkind create cluster\n```\n```bash\nkardinal status demo\n```\n")
	problems, err = Problems(root)
	require.NoError(t, err)
	assert.Len(t, problems, 2)
	assert.Contains(t, problems[0], "bash block 1 matches 2 entries")
	assert.Contains(t, problems[1], "bash block 2 matches 0 entries")

	write("```bash\nkardinal why demo prod\n```\n")
	problems, err = Problems(root)
	require.NoError(t, err)
	assert.Equal(t, []string{`docs/p.md: Manifest entry "kind create" matches no block; remove it`}, problems)

	_, err = Problems(t.TempDir())
	assert.Error(t, err, "a page that is missing")
}
