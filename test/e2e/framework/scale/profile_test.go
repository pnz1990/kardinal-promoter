// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, p Profile)
		wantErr string
	}{{
		name: "ci by default",
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, "ci", p.Name)
			assert.Equal(t, 120, p.LongChainStages)
			assert.Equal(t, 15, p.BigWaveWidth)
			// Sized for a GitHub runner with the -race controller (measured
			// p50 8-10 s, p99 13 s, Bundle p99 29-36 s; 1 + 9 Argo CD waves
			// of 15 did not fit Settle).
			assert.Equal(t, 15*time.Second, p.SLO.StepP50)
			assert.Equal(t, 30*time.Second, p.SLO.StepP99)
			assert.Equal(t, 75*time.Second, p.SLO.BundleP99)
			assert.Equal(t, 1+4*10, 1+p.ArgoWaves*p.ArgoWaveWidth)
		},
	}, {
		name: "the Argo CD fan-out is overridable",
		env:  map[string]string{"KARDINAL_E2E_SCALE_ARGO_WAVES": "2", "KARDINAL_E2E_SCALE_ARGO_WAVE_WIDTH": "3"},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, 2, p.ArgoWaves)
			assert.Equal(t, 3, p.ArgoWaveWidth)
		},
	}, {
		name: "full",
		env:  map[string]string{EnvProfile: "full"},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, 1+10*15, 1+p.ArgoWaves*p.ArgoWaveWidth, "full keeps the 151 Argo CD environments")
			assert.Equal(t, 10*time.Second, p.SLO.StepP50, "full keeps the strict objective")
			assert.Equal(t, 100, p.ChainStages)
			assert.Equal(t, 200, p.Pipelines)
			assert.Equal(t, 1000, p.BurstBundles)
			assert.Equal(t, 0.9, p.TenantBacklogRatio, "the release gate judges B's steps against A's backlog")
			assert.Equal(t, 149, p.TenantRepos)
		},
	}, {
		name: "ci leaves B's steps against A's backlog unjudged",
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, 30, p.TenantRepos)
			assert.Zero(t, p.TenantBacklogRatio)
			assert.Zero(t, p.TenantReposStepP99, "TenantStepP99 applies")
		},
	}, {
		name: "nightly is ci with a deeper ManyRepos backlog",
		env:  map[string]string{EnvProfile: "nightly"},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, "nightly", p.Name)
			assert.Equal(t, 60, p.TenantRepos)
			assert.Equal(t, 0.9, p.TenantBacklogRatio)
			assert.Equal(t, 35*time.Second, p.TenantReposStepP99)
			ci := profiles["ci"]
			ci.Name, ci.TenantRepos, ci.TenantBacklogRatio, ci.TenantReposStepP99 = p.Name, p.TenantRepos, p.TenantBacklogRatio, p.TenantReposStepP99
			assert.Equal(t, ci, p, "every other size is ci's")
		},
	}, {
		name: "the backlog ratio and ManyRepos' step p99 are overridable",
		env:  map[string]string{"KARDINAL_E2E_SCALE_TENANT_BACKLOG_RATIO": "0.8", "KARDINAL_E2E_SCALE_TENANT_REPOS_STEP_P99": "40s", "KARDINAL_E2E_SCALE_TENANT_REPOS": "45"},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, 0.8, p.TenantBacklogRatio)
			assert.Equal(t, 40*time.Second, p.TenantReposStepP99)
			assert.Equal(t, 45, p.TenantRepos)
			assert.Equal(t, 30, p.TenantWave, "TenantRepos sizes ManyRepos alone")
		},
	}, {
		name: "soak is full with a 30-minute load",
		env:  map[string]string{EnvProfile: "soak"},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, 5.0, p.SustainedRate)
			assert.Equal(t, 30*time.Minute, p.SustainedFor)
			assert.Equal(t, 1000, p.BurstBundles)
		},
	}, {
		name: "overrides",
		env: map[string]string{
			EnvProfile: "full", "KARDINAL_E2E_SCALE_SUSTAINED_RATE": "0.25",
			"KARDINAL_E2E_SCALE_SUSTAINED_FOR": "1h", "KARDINAL_E2E_SCALE_PIPELINES": "500",
		},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, "full+overrides", p.Name)
			assert.Equal(t, 0.25, p.SustainedRate)
			assert.Equal(t, time.Hour, p.SustainedFor)
			assert.Equal(t, 500, p.Pipelines)
			assert.Equal(t, 1000, p.BurstBundles, "other sizes stay")
		},
	}, {
		name:    "unknown profile",
		env:     map[string]string{EnvProfile: "huge"},
		wantErr: `want ci, nightly, full or soak`,
	}, {
		name:    "a count below one",
		env:     map[string]string{"KARDINAL_E2E_SCALE_FAN_IN": "0"},
		wantErr: "KARDINAL_E2E_SCALE_FAN_IN",
	}, {
		name:    "a bad duration",
		env:     map[string]string{"KARDINAL_E2E_SCALE_SETTLE": "soon"},
		wantErr: "KARDINAL_E2E_SCALE_SETTLE",
	}, {
		name:    "a negative backlog ratio",
		env:     map[string]string{"KARDINAL_E2E_SCALE_TENANT_BACKLOG_RATIO": "-1"},
		wantErr: "must not be negative",
	}, {
		name:    "a rate of zero",
		env:     map[string]string{"KARDINAL_E2E_SCALE_SUSTAINED_RATE": "0"},
		wantErr: "must be positive",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvProfile, "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			p, err := Load()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			tc.check(t, p)
			assert.Contains(t, p.String(), "profile "+p.Name)
		})
	}
}

func TestKnownBugMessage(t *testing.T) {
	assert.Equal(t, "KNOWN BUG #1473 https://github.com/pnz1990/kardinal-promoter/issues/1473: too many",
		KnownBugMessage(1473, "too many"))
}

// TestSettleSerial: a test whose Bundle makes N promotions in a row (a
// chain's stages, N environments taking turns on one branch) settles within
// max(Settle, N x SerialStep). The ci LongChain (120 stages) and BigWaves
// (151 environments on one branch) failed the flat 10-minute Settle while
// still promoting a stage every ~5.6 s on a GitHub runner.
func TestSettleSerial(t *testing.T) {
	p, err := Load()
	require.NoError(t, err)
	require.Equal(t, "ci", p.Name)
	assert.Equal(t, 12*time.Second, p.SerialStep)
	cases := []struct {
		n    int
		want time.Duration
	}{
		{n: 10, want: p.Settle},                      // short: the profile's Settle
		{n: 120, want: 24 * time.Minute},             // TestScale_TopologyLongChain
		{n: 1 + 10*15, want: 151 * 12 * time.Second}, // TestScale_TopologyBigWaves
	}
	for _, tc := range cases {
		r := &Run{T: t, P: p, Extra: map[string]interface{}{}}
		r.SettleSerial(tc.n)
		assert.Equal(t, tc.want, r.settle, "%d serial promotions", tc.n)
		// Measured: 120 stages took 11m12s, so the chain fits with margin.
		if tc.n == 120 {
			assert.Greater(t, r.settle, 11*time.Minute+12*time.Second)
		}
	}
	t.Setenv("KARDINAL_E2E_SCALE_SERIAL_STEP", "3s")
	p, err = Load()
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, p.SerialStep, "overridable")
}
