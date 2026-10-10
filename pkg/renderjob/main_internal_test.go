// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestMain_LocksTheNetworkAndMapsExitCodes (QA round 3 on #1515): Main
// locks the network to the Config's git URL before it renders (a lock that
// fails stops it before any render), passes the mounted token, writes the
// result or the redacted error to the termination message, and exits 0, 1,
// or ExitPermanent (2) for a render that failed for good.
func TestMain_LocksTheNetworkAndMapsExitCodes(t *testing.T) {
	cfg := Config{Pipeline: "web", Environment: "prod", BundleName: "web-v2",
		Git: v1alpha1.RenderRunGit{URL: "https://git.example.com/org/repo.git"}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	tests := []struct {
		name     string
		config   string
		lockErr  error
		runErr   error
		wantExit int
		wantRun  bool
		wantMsg  string
	}{
		{name: "success", config: string(raw), wantExit: 0, wantRun: true, wantMsg: `"commitSHA":"c0ffee"`},
		{name: "permanent", config: string(raw), runErr: fmt.Errorf("render: %w", steps.Permanent(errors.New("drift"))),
			wantExit: ExitPermanent, wantRun: true, wantMsg: "drift"},
		{name: "retryable", config: string(raw), runErr: errors.New("clone https://x:s3cret@git.example.com/r.git: timeout"),
			wantExit: 1, wantRun: true, wantMsg: "timeout"},
		{name: "lock fails", config: string(raw), lockErr: errors.New("the render Job reaches git over http(s) only"),
			wantExit: 1, wantMsg: "http(s) only"},
		{name: "bad config", config: "{", wantExit: 1, wantMsg: "read " + ConfigEnv},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			msg := filepath.Join(dir, "termination-log")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "token"), []byte("tok3n\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "sshPrivateKey"), []byte("key"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "knownHosts"), []byte("hosts"), 0o600))
			var locked []string
			ran := false
			pLock, pRun, pGit, pMsg, pDir, pWork := lockNetwork, runRender, newGit, messagePath, secretDir, workDir
			t.Cleanup(func() {
				lockNetwork, runRender, newGit, messagePath, secretDir, workDir = pLock, pRun, pGit, pMsg, pDir, pWork
			})
			lockNetwork = func(url string) error {
				locked = append(locked, url)
				return tc.lockErr
			}
			runRender = func(_ context.Context, c Config, wd string, auth scm.GitAuth, _ scm.GitClient) (Result, error) {
				ran = true
				assert.Equal(t, []string{cfg.Git.URL}, locked, "the network is locked before the render")
				assert.Equal(t, "1", os.Getenv(steps.RenderJobEnv))
				assert.Equal(t, scm.GitAuth{Token: "tok3n", SSHPrivateKey: []byte("key"), SSHKnownHosts: []byte("hosts")}, auth,
					"the git Secret's mounted keys")
				assert.Equal(t, dir, wd)
				assert.Equal(t, cfg.BundleName, c.BundleName)
				return Result{RenderRunResult: v1alpha1.RenderRunResult{CommitSHA: "c0ffee"}}, tc.runErr
			}
			newGit = func() scm.GitClient { return nil }
			messagePath, secretDir, workDir = msg, dir, dir
			t.Setenv(ConfigEnv, tc.config)
			t.Setenv(steps.RenderJobEnv, "")

			assert.Equal(t, tc.wantExit, Main(context.Background()))
			assert.Equal(t, tc.wantRun, ran)
			b, err := os.ReadFile(msg)
			require.NoError(t, err)
			assert.Contains(t, string(b), tc.wantMsg)
			assert.NotContains(t, string(b), "s3cret", "the error is redacted")
		})
	}
}
