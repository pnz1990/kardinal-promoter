// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package scale is the harness of the live scale suite (hack/e2e/up.sh
// scale, the TestScale_ tests): Pipelines of production size and shape on
// one git server, Bundles created in bursts and at a sustained rate, the
// chaos the tests inject (controller leader kills, kro restarts, git server
// latency and outages through Toxiproxy, API server throttling), and the
// sizes of all of it, from a profile.
//
// The tests check what they did with package invariants.
package scale

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
)

// EnvProfile names the profile: ci (the default, sized for a 4-CPU CI
// runner and the e2e-live job's hour), full (the sizes of a large
// production company: 120-stage chains, 150 environments, 200 Pipelines,
// 1,000 Bundles) or soak (full, with a 30-minute sustained load).
const EnvProfile = "KARDINAL_E2E_SCALE_PROFILE"

// Profile holds every size the scale tests use. Each field can also be set
// on its own with KARDINAL_E2E_SCALE_<FIELD> (field name in upper snake
// case, e.g. KARDINAL_E2E_SCALE_SUSTAINED_RATE=5).
type Profile struct {
	Name string

	// ChainStages is the length of the linear chain a Bundle promotes
	// through end to end.
	ChainStages int
	// LongChainStages is the chain length a large company asks for (over
	// 100 stages); TestScale_TopologyLongChain checks the API accepts it.
	LongChainStages int
	// Waves and WaveWidth shape the fan-out: Waves waves of WaveWidth
	// regions each, every region of a wave depending on every region of the
	// wave before (wave:).
	Waves, WaveWidth int
	// BigWaves and BigWaveWidth are the fan-out a large company asks for (10
	// waves of 15 regions, 150 environments).
	BigWaves, BigWaveWidth int
	// LatticeDepth layers of LatticeWidth environments, each depending on
	// every environment of the layer before (a diamond lattice).
	LatticeDepth, LatticeWidth int
	// FanIn parallel environments that one final environment depends on.
	FanIn int
	// SharedPipelines Pipelines write one repo and branch at once.
	SharedPipelines int

	// Pipelines is the number of Pipelines TestScale_LoadPipelines creates,
	// each of PipelineEnvs environments, one Bundle each.
	Pipelines, PipelineEnvs int
	// BurstBundles Bundles are created at once over BurstPipelines Pipelines.
	BurstBundles, BurstPipelines int
	// SustainedRate Bundles a second for SustainedFor, over
	// SustainedPipelines Pipelines.
	SustainedRate      float64
	SustainedFor       time.Duration
	SustainedPipelines int
	// RapidFire Bundles in a row on one Pipeline (supersession).
	RapidFire int

	// ChaosFor is how long a chaos test keeps injecting faults while its load
	// runs; ChaosPipelines Pipelines carry that load.
	ChaosFor       time.Duration
	ChaosPipelines int

	// Settle bounds how long every Bundle of a test may take to reach a
	// terminal phase once the test stops creating them.
	Settle time.Duration

	// TenantWave is the width of the one-branch wave tenant A promotes in
	// TestScale_TwoTenants while tenant B, in another namespace, promotes a
	// small Pipeline; TenantStartWithin bounds how long B's steps may wait
	// for their first reconcile (#1577).
	TenantWave        int
	TenantStartWithin time.Duration
	// TenantBBundles three-environment Pipelines, one Bundle each, 5 s
	// apart, are what tenant B promotes in TestScale_TenantFairness* while
	// A's load runs; TenantStepP99 bounds the p99 of B's step latency
	// (creation to Verified) meanwhile (#1577).
	TenantBBundles int
	TenantStepP99  time.Duration
	// TenantBundleP99 bounds the p99 of B's Bundles end to end: what B
	// waits for between its steps, in the queues A fills.
	TenantBundleP99 time.Duration

	// SLOPipelines Pipelines of PipelineEnvs automatic environments get one
	// Bundle each at once in TestScale_LatencySLO, which holds the
	// controller to SLO (test/e2e/README.md#latency-slo).
	SLOPipelines int
	SLO          invariants.SLO
}

// profiles are the named profiles. ci is sized for a GitHub-hosted runner
// (4 vCPUs, 16 GB) running the -race controller, four tests at a time, and
// keeps each test to a few minutes; full is the production-size run (24 GB kind node, about an
// hour for the whole suite on 32 cores); soak is full with a 30-minute
// sustained load at 5 Bundles a second.
var profiles = map[string]Profile{
	"ci": {
		ChainStages: 30, LongChainStages: 120,
		Waves: 3, WaveWidth: 4, BigWaves: 10, BigWaveWidth: 15,
		LatticeDepth: 3, LatticeWidth: 3, FanIn: 10, SharedPipelines: 3,
		Pipelines: 20, PipelineEnvs: 3, BurstBundles: 100, BurstPipelines: 10,
		SustainedRate: 0.5, SustainedFor: 2 * time.Minute, SustainedPipelines: 8,
		RapidFire: 10, ChaosFor: 3 * time.Minute, ChaosPipelines: 8,
		TenantWave: 30, TenantStartWithin: 20 * time.Second, TenantBBundles: 3, TenantStepP99: 20 * time.Second, TenantBundleP99: 90 * time.Second,
		Settle:       10 * time.Minute,
		SLOPipelines: 20,
		SLO:          invariants.SLO{StepP50: 5 * time.Second, StepP99: 15 * time.Second, BundleP99: 45 * time.Second},
	},
	"full": {
		ChainStages: 100, LongChainStages: 120,
		Waves: 9, WaveWidth: 11, BigWaves: 10, BigWaveWidth: 15,
		LatticeDepth: 6, LatticeWidth: 5, FanIn: 50, SharedPipelines: 5,
		Pipelines: 200, PipelineEnvs: 3, BurstBundles: 1000, BurstPipelines: 100,
		SustainedRate: 2, SustainedFor: 10 * time.Minute, SustainedPipelines: 50,
		RapidFire: 40, ChaosFor: 10 * time.Minute, ChaosPipelines: 40,
		TenantWave: 149, TenantStartWithin: 20 * time.Second, TenantBBundles: 5, TenantStepP99: 10 * time.Second, TenantBundleP99: 35 * time.Second,
		Settle:       45 * time.Minute,
		SLOPipelines: 200,
		SLO:          invariants.SLO{StepP50: 10 * time.Second, StepP99: 30 * time.Second, BundleP99: 2 * time.Minute},
	},
}

func init() {
	soak := profiles["full"]
	soak.SustainedRate, soak.SustainedFor, soak.SustainedPipelines = 5, 30*time.Minute, 100
	soak.Settle = 60 * time.Minute
	profiles["soak"] = soak
}

// Load returns the profile EnvProfile names (default ci) with the
// KARDINAL_E2E_SCALE_<FIELD> overrides applied.
func Load() (Profile, error) {
	name := os.Getenv(EnvProfile)
	if name == "" {
		name = "ci"
	}
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("%s=%q: want ci, full or soak", EnvProfile, name)
	}
	p.Name = name
	for _, o := range overrides(&p) {
		raw := os.Getenv("KARDINAL_E2E_SCALE_" + o.name)
		if raw == "" {
			continue
		}
		if err := o.set(raw); err != nil {
			return Profile{}, fmt.Errorf("KARDINAL_E2E_SCALE_%s=%q: %w", o.name, raw, err)
		}
		p.Name = name + "+overrides"
	}
	return p, nil
}

type override struct {
	name string
	set  func(string) error
}

func overrides(p *Profile) []override {
	ints := map[string]*int{
		"CHAIN_STAGES": &p.ChainStages, "LONG_CHAIN_STAGES": &p.LongChainStages,
		"WAVES": &p.Waves, "WAVE_WIDTH": &p.WaveWidth, "BIG_WAVES": &p.BigWaves, "BIG_WAVE_WIDTH": &p.BigWaveWidth,
		"LATTICE_DEPTH": &p.LatticeDepth, "LATTICE_WIDTH": &p.LatticeWidth, "FAN_IN": &p.FanIn,
		"SHARED_PIPELINES": &p.SharedPipelines, "PIPELINES": &p.Pipelines, "PIPELINE_ENVS": &p.PipelineEnvs,
		"BURST_BUNDLES": &p.BurstBundles, "BURST_PIPELINES": &p.BurstPipelines,
		"SUSTAINED_PIPELINES": &p.SustainedPipelines, "RAPID_FIRE": &p.RapidFire,
		"CHAOS_PIPELINES": &p.ChaosPipelines, "SLO_PIPELINES": &p.SLOPipelines, "TENANT_WAVE": &p.TenantWave,
	}
	durations := map[string]*time.Duration{
		"SUSTAINED_FOR": &p.SustainedFor, "CHAOS_FOR": &p.ChaosFor, "SETTLE": &p.Settle,
		"TENANT_START_WITHIN": &p.TenantStartWithin,
		"SLO_STEP_P50":        &p.SLO.StepP50, "SLO_STEP_P99": &p.SLO.StepP99, "SLO_BUNDLE_P99": &p.SLO.BundleP99,
	}
	var out []override
	for name, ptr := range ints {
		ptr := ptr
		out = append(out, override{name, func(s string) error {
			n, err := strconv.Atoi(s)
			if err == nil && n < 1 {
				err = fmt.Errorf("must be at least 1")
			}
			*ptr = n
			return err
		}})
	}
	for name, ptr := range durations {
		ptr := ptr
		out = append(out, override{name, func(s string) error {
			d, err := time.ParseDuration(s)
			*ptr = d
			return err
		}})
	}
	out = append(out, override{"SUSTAINED_RATE", func(s string) error {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && f <= 0 {
			err = fmt.Errorf("must be positive")
		}
		p.SustainedRate = f
		return err
	}})
	return out
}

// String is the profile's sizes, for a test's log and report.
func (p Profile) String() string {
	return strings.Join([]string{
		"profile " + p.Name,
		fmt.Sprintf("chain %d (long %d)", p.ChainStages, p.LongChainStages),
		fmt.Sprintf("waves %dx%d (big %dx%d)", p.Waves, p.WaveWidth, p.BigWaves, p.BigWaveWidth),
		fmt.Sprintf("lattice %dx%d", p.LatticeDepth, p.LatticeWidth),
		fmt.Sprintf("fan-in %d", p.FanIn),
		fmt.Sprintf("%d Pipelines x %d envs", p.Pipelines, p.PipelineEnvs),
		fmt.Sprintf("burst %d Bundles over %d Pipelines", p.BurstBundles, p.BurstPipelines),
		fmt.Sprintf("sustained %.2f/s for %s over %d Pipelines", p.SustainedRate, p.SustainedFor, p.SustainedPipelines),
		fmt.Sprintf("chaos %s over %d Pipelines", p.ChaosFor, p.ChaosPipelines),
		fmt.Sprintf("tenant wave %d", p.TenantWave),
	}, ", ")
}
