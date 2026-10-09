// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifestFiles: one file per object, namespaced under its namespace,
// sorted, a group suffix for a kind/name clash across API groups, and the
// object count limit.
func TestManifestFiles(t *testing.T) {
	docs := [][]byte{
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: web, namespace: prod}\n"),
		[]byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, namespace: prod}\n"),
		[]byte("apiVersion: v1\nkind: Namespace\nmetadata: {name: prod}\n"),
		[]byte("apiVersion: example.com/v1\nkind: Deployment\nmetadata: {name: web, namespace: prod}\n"),
	}
	files, err := manifestFiles(docs)
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.path)
	}
	assert.Equal(t, []string{"namespace-prod.yaml", "prod/deployment-web.example.com.yaml", "prod/deployment-web.yaml",
		"prod/service-web.yaml"}, paths)

	_, err = manifestFiles([][]byte{[]byte("kind: ConfigMap\n")})
	assert.ErrorContains(t, err, "no kind or metadata.name")

	many := make([][]byte, maxRenderedObjects+1)
	for i := range many {
		many[i] = []byte(fmt.Sprintf("kind: ConfigMap\nmetadata: {name: c%d}\n", i))
	}
	_, err = manifestFiles(many)
	assert.ErrorContains(t, err, "more than the limit")

	big := []byte("kind: ConfigMap\nmetadata: {name: big}\ndata: {x: \"" + strings.Repeat("x", maxRenderOutputBytes) + "\"}\n")
	_, err = manifestFiles([][]byte{big})
	assert.ErrorContains(t, err, "larger than 16 MiB")
}

// TestFileSafe keeps names on one path segment.
func TestFileSafe(t *testing.T) {
	assert.Equal(t, "a_b", fileSafe("a/b"))
	assert.Equal(t, "_", fileSafe(".."))
	assert.Equal(t, "web-1.example", fileSafe("Web-1.Example"))
}

// TestRemoteKustomizeRef recognises the remote forms kustomize would fetch.
func TestRemoteKustomizeRef(t *testing.T) {
	for _, r := range []string{"https://github.com/o/r//base", "git@github.com:o/r.git", "github.com/o/r/base?ref=v1", "ssh://h/r"} {
		assert.True(t, remoteKustomizeRef(r), r)
	}
	for _, r := range []string{"../base", "deployment.yaml", "components/x"} {
		assert.False(t, remoteKustomizeRef(r), r)
	}
}

// TestTrailers parses the last paragraph of a commit message.
func TestTrailers(t *testing.T) {
	msg := "[kardinal] Promote b to prod\n\nBundle: b\nPipeline: p\n\nKardinal-Dry-Commit: abc\nKardinal-Bundle: b\n"
	assert.Equal(t, map[string]string{"Kardinal-Dry-Commit": "abc", "Kardinal-Bundle": "b"}, trailers(msg))
}
