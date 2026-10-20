// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTUFFetchThroughOneFetcher (#2218): every Sigstore trusted root kardinal
// fetches through TUF goes through pkg/tuffetch, where a 404 ends a
// download and every download is bounded. go-tuf's own fetcher package is
// imported nowhere else (its NewFetcherWithHTTPClient retries a 404 for up
// to 15 minutes), and a file that fetches a trusted root with TUF options
// sets a tuffetch.Fetcher.
//
// Covers TUF-FETCH-03.
func TestTUFFetchThroughOneFetcher(t *testing.T) {
	root := repoRoot(t)
	const gotuf = "github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
	fetches := 0
	for _, dir := range []string{"pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == gotuf && !strings.HasPrefix(rel, filepath.Join("pkg", "tuffetch")) {
					t.Errorf("%s imports %s: fetch through pkg/tuffetch instead", rel, gotuf)
				}
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "FetchTrustedRootWithOptions(") {
				fetches++
				assert.Contains(t, string(src), "tuffetch.Fetcher{", "%s fetches a trusted root with TUF: set opts.Fetcher to a tuffetch.Fetcher", rel)
			}
			return nil
		})
		require.NoError(t, err)
	}
	assert.Positive(t, fetches, "the check found the trusted-root fetches")
}
