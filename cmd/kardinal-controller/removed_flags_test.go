// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"flag"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #1283: a controller started with a removed flag fails and says why;
// without one it starts.
func TestRemovedReaderFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{args: nil},
		{args: []string{"--graph-reader-namespaces=argocd"}, err: "--graph-reader-namespaces: removed (#1283)"},
		{args: []string{"--graph-reader-clusterrole=x", "--graph-reader-namespaces="},
			err: "--graph-reader-clusterrole, --graph-reader-namespaces: removed (#1283)"},
	} {
		fs := flag.NewFlagSet("c", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		check := removedReaderFlags(fs)
		require.NoError(t, fs.Parse(tc.args))
		err := check()
		if tc.err == "" {
			assert.NoError(t, err)
			continue
		}
		require.Error(t, err)
		assert.Contains(t, err.Error(), tc.err)
		assert.Contains(t, err.Error(), "graph.readerNamespaces")
	}
}
