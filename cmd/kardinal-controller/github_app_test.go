// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStaticSCMCredentials: the GitHub App flags go together; without them
// the token is used.
func TestStaticSCMCredentials(t *testing.T) {
	key := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(t, os.WriteFile(key, []byte("pem"), 0o600))

	c, err := staticSCMCredentials("ghp_x", 0, 0, "")
	require.NoError(t, err)
	assert.Equal(t, "ghp_x", c.Token)
	assert.Nil(t, c.GitHubApp)

	c, err = staticSCMCredentials("ignored", 12, 34, key)
	require.NoError(t, err)
	require.NotNil(t, c.GitHubApp)
	assert.EqualValues(t, 12, c.GitHubApp.AppID)
	assert.EqualValues(t, 34, c.GitHubApp.InstallationID)
	assert.Equal(t, []byte("pem"), c.GitHubApp.PrivateKey)

	for _, bad := range []struct {
		id, inst int64
		file     string
	}{{12, 0, key}, {0, 34, key}, {12, 34, ""}} {
		_, err := staticSCMCredentials("", bad.id, bad.inst, bad.file)
		assert.ErrorContains(t, err, "must be set together")
	}
	_, err = staticSCMCredentials("", 12, 34, filepath.Join(t.TempDir(), "missing.pem"))
	assert.ErrorContains(t, err, "read --github-app-private-key-file")

	assert.Equal(t, "https://ghe.example/api/v3", githubAPIURL("github", "https://ghe.example/api/v3"))
	assert.Equal(t, "", githubAPIURL("gitlab", "https://gitlab.example"))
	t.Setenv("GITHUB_APP_ID", " 99 ")
	assert.EqualValues(t, 99, envInt64("GITHUB_APP_ID"))
	t.Setenv("GITHUB_APP_ID", "x")
	assert.Zero(t, envInt64("GITHUB_APP_ID"))
}
