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
		},
	}, {
		name: "full",
		env:  map[string]string{EnvProfile: "full"},
		check: func(t *testing.T, p Profile) {
			assert.Equal(t, 100, p.ChainStages)
			assert.Equal(t, 200, p.Pipelines)
			assert.Equal(t, 1000, p.BurstBundles)
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
		wantErr: `want ci, full or soak`,
	}, {
		name:    "a count below one",
		env:     map[string]string{"KARDINAL_E2E_SCALE_FAN_IN": "0"},
		wantErr: "KARDINAL_E2E_SCALE_FAN_IN",
	}, {
		name:    "a bad duration",
		env:     map[string]string{"KARDINAL_E2E_SCALE_SETTLE": "soon"},
		wantErr: "KARDINAL_E2E_SCALE_SETTLE",
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
