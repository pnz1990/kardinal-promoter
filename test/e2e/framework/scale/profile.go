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
// runner and the e2e-live job's hour), nightly (ci with a deeper backlog in
// TestScale_TenantFairnessManyRepos, for the scheduled e2e-live run), full
// (the sizes of a large production company: 120-stage chains, 150
// environments, 200 Pipelines, 1,000 Bundles) or soak (full, with a 30-minute
// sustained load).
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
	// ArgoWaves and ArgoWaveWidth are TestScale_TopologyArgoCD's fan-out,
	// one Argo CD Application per environment. Argo CD syncs every wave
	// before the next can start, so on a GitHub runner each wave of the full
	// fan-out took about a minute and the 151 environments did not fit the
	// ci profile's Settle; ci runs a smaller one (41), full the 151.
	ArgoWaves, ArgoWaveWidth int
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

	// PackEvents AuditEvents TestScale_EvidencePack exports with kardinal
	// audit evidence-pack (#2047). The CLI reads them a page at a time, so
	// its peak memory does not grow with the count: exporting a namespace
	// of PackEvents may take at most PackMaxRSSGrowthMiB more than one of a
	// tenth as many, and never more than PackMaxRSSMiB. PackMaxTime bounds
	// the export, about ten times what it takes.
	PackEvents          int
	PackMaxRSSMiB       int
	PackMaxRSSGrowthMiB int
	PackMaxTime         time.Duration
	// PackVersions versions in PackNamespaces namespaces, each with an
	// Approval, a gate instance and two receipts, exported cluster-wide by
	// TestScale_EvidencePackVersions (#2238) within PackVersionsMaxTime: 10s
	// in ci, 10x the 0.96s a ci run measured for 500; 100s in full, 10x a
	// first full run's 10.3s for 5,000 (6.8s since). With the memory checks
	// of PackMaxRSSGrowthMiB (against a tenth of the versions) and
	// PackMaxRSSMiB.
	PackVersions        int
	PackNamespaces      int
	PackVersionsMaxTime time.Duration
	// ReceiptDrainVersions bounds how long the controller takes to sign and
	// store the scenario's receipts (2 x PackVersions) before the exports.
	ReceiptDrainVersions time.Duration

	// Settle bounds how long every Bundle of a test may take to reach a
	// terminal phase once the test stops creating them.
	Settle time.Duration
	// SerialStep is the time one promotion on a test's critical path may
	// take: a stage of a chain (its step, then the Graph creating the next
	// one), or a turn on a branch that every environment of the test pushes
	// to, one at a time (#1578). A test whose Bundle has N such promotions
	// in a row settles within max(Settle, N x SerialStep) (Run.SettleSerial):
	// TestScale_TopologyLongChain's 120 stages at the ~5.6 s a stage took on
	// a GitHub runner need 11 minutes, past the ci Settle of 10.
	SerialStep time.Duration

	// TenantWave is the width of the one-branch wave tenant A promotes in
	// TestScale_TwoTenants while tenant B, in another namespace, promotes a
	// small Pipeline; TenantStartWithin bounds how long B's steps may wait
	// for their first reconcile (#1577).
	TenantWave        int
	TenantStartWithin time.Duration
	// TenantRepos five-environment Pipelines, each on its own repository,
	// are tenant A's load in TestScale_TenantFairnessManyRepos, all
	// promoted at once: as many of A's steps due at a time, the backlog
	// the fair queue must serve B ahead of (#2252).
	TenantRepos int
	// TenantBacklogRatio is the most B's slowest step under A's load may
	// take in TestScale_TenantFairnessManyRepos, as a fraction of A's
	// median step, when A's load is a backlog (JudgeFairness). 0 leaves it
	// unjudged: at the ci profile's 30 repositories A's backlog is too
	// shallow to tell the fair queue from controller-runtime's default one
	// on a GitHub runner (#2257), so PRs judge starvation and the absolute
	// SLOs, and the nightly profile this.
	TenantBacklogRatio float64
	// TenantReposStepP99 bounds the p99 of B's step latency in
	// TestScale_TenantFairnessManyRepos when set, instead of TenantStepP99:
	// a deeper backlog slows B's own steps too.
	TenantReposStepP99 time.Duration
	// TenantBBundles three-environment Pipelines are what tenant B
	// promotes in TestScale_TenantFairness*: a Bundle on each, 2 s apart,
	// alone, then another while A's load runs (JudgeFairness, #2252);
	// TenantStepP99 bounds the p99 of B's step latency (creation to
	// Verified) (#1577).
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
// hour for the whole suite on 32 cores); nightly is ci with a deeper
// backlog in TestScale_TenantFairnessManyRepos (init); soak is full with a
// 30-minute sustained load at 5 Bundles a second.
var profiles = map[string]Profile{
	"ci": {
		ChainStages: 30, LongChainStages: 120,
		Waves: 3, WaveWidth: 4, BigWaves: 10, BigWaveWidth: 15, ArgoWaves: 4, ArgoWaveWidth: 10,
		LatticeDepth: 3, LatticeWidth: 3, FanIn: 10, SharedPipelines: 3,
		Pipelines: 20, PipelineEnvs: 3, BurstBundles: 100, BurstPipelines: 10,
		SustainedRate: 0.5, SustainedFor: 2 * time.Minute, SustainedPipelines: 8,
		RapidFire: 10, ChaosFor: 3 * time.Minute, ChaosPipelines: 8,
		// The CLI peaks at about 64 MiB live (98 MiB in BenchmarkEvidencePack,
		// test binary included) at 10k and at 100k alike; a pack that held
		// the events in memory grows by about 1 KiB each, +9 MiB from 1k to
		// 10k. The export takes 1.2 s live.
		PackEvents: 10_000, PackMaxRSSMiB: 192, PackMaxRSSGrowthMiB: 4, PackMaxTime: 15 * time.Second,
		PackVersions: 500, PackNamespaces: 10, PackVersionsMaxTime: 10 * time.Second, ReceiptDrainVersions: 5 * time.Minute,
		TenantWave: 30, TenantRepos: 30, TenantStartWithin: 20 * time.Second, TenantBBundles: 3, TenantStepP99: 20 * time.Second, TenantBundleP99: 90 * time.Second,
		Settle: 10 * time.Minute,
		// Twice the stage time measured on a GitHub runner with the -race
		// controller (LongChain 2026-10-12: 120 stages in 11m12s, ~5.6 s).
		SerialStep:   12 * time.Second,
		SLOPipelines: 20,
		// Measured on GitHub runners with the -race controller (nightly
		// and v0.10.0 tag, 2026-10-10/11): auto step p50 8-10 s, p99 13 s,
		// Bundle p99 29-36 s; a 32-core host measures 1-2 s. The objective
		// keeps about 1.5-2x margin over the runner; full stays strict.
		SLO: invariants.SLO{StepP50: 15 * time.Second, StepP99: 30 * time.Second, BundleP99: 75 * time.Second},
	},
	"full": {
		ChainStages: 100, LongChainStages: 120,
		Waves: 9, WaveWidth: 11, BigWaves: 10, BigWaveWidth: 15, ArgoWaves: 10, ArgoWaveWidth: 15,
		LatticeDepth: 6, LatticeWidth: 5, FanIn: 50, SharedPipelines: 5,
		Pipelines: 200, PipelineEnvs: 3, BurstBundles: 1000, BurstPipelines: 100,
		SustainedRate: 2, SustainedFor: 10 * time.Minute, SustainedPipelines: 50,
		RapidFire: 40, ChaosFor: 10 * time.Minute, ChaosPipelines: 40,
		// 12.5 s live at 100k.
		PackEvents: 100_000, PackMaxRSSMiB: 192, PackMaxRSSGrowthMiB: 4, PackMaxTime: 2 * time.Minute,
		PackVersions: 5_000, PackNamespaces: 50, PackVersionsMaxTime: 100 * time.Second, ReceiptDrainVersions: 20 * time.Minute,
		TenantWave: 149, TenantRepos: 149, TenantStartWithin: 20 * time.Second, TenantBBundles: 5, TenantStepP99: 10 * time.Second, TenantBundleP99: 35 * time.Second,
		// 149 repositories on the release gate's 32 cores are a backlog (A's
		// median step 8-9 s against B's 2 s alone): with the fair queue B's
		// slowest step took 0.5 of A's median (2 runs), without it 1.22 and
		// 2.33, and TenantStepP99 and TenantBundleP99 failed both (B's step
		// p99 11 and 21 s, Bundle p99 43 and 66 s; 4 s and 13-15 s fair).
		TenantBacklogRatio: 0.9,
		Settle:             45 * time.Minute,
		SerialStep:         12 * time.Second,
		SLOPipelines:       200,
		SLO:                invariants.SLO{StepP50: 10 * time.Second, StepP99: 30 * time.Second, BundleP99: 2 * time.Minute},
	},
}

func init() {
	// nightly is ci with TestScale_TenantFairnessManyRepos' backlog deep
	// enough to judge B's steps against A's (#2257): twice ci's
	// repositories. On a GitHub runner (3 runs each), with the fair queue
	// B's slowest step took 0.56 to 0.63 of A's median step there; without
	// it (controller-runtime's default queue) 1.24 to 1.38, and every B
	// Bundle's slowest step 1.14 or more: the ratio bound, 0.9, is between.
	// B's step p99 took 15 to 24 s with the fair queue and 46 to 54 s
	// without, so TenantReposStepP99 is 35 s. It adds about 45 to 150 s to
	// the scale suite (ManyRepos 262-327 s against 177-219 s), so PRs keep
	// ci and the scheduled e2e-live run takes nightly.
	nightly := profiles["ci"]
	nightly.TenantRepos, nightly.TenantBacklogRatio, nightly.TenantReposStepP99 = 60, 0.9, 35*time.Second
	profiles["nightly"] = nightly

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
		return Profile{}, fmt.Errorf("%s=%q: want ci, nightly, full or soak", EnvProfile, name)
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
		"ARGO_WAVES": &p.ArgoWaves, "ARGO_WAVE_WIDTH": &p.ArgoWaveWidth,
		"LATTICE_DEPTH": &p.LatticeDepth, "LATTICE_WIDTH": &p.LatticeWidth, "FAN_IN": &p.FanIn,
		"SHARED_PIPELINES": &p.SharedPipelines, "PIPELINES": &p.Pipelines, "PIPELINE_ENVS": &p.PipelineEnvs,
		"BURST_BUNDLES": &p.BurstBundles, "BURST_PIPELINES": &p.BurstPipelines,
		"SUSTAINED_PIPELINES": &p.SustainedPipelines, "RAPID_FIRE": &p.RapidFire,
		"CHAOS_PIPELINES": &p.ChaosPipelines, "SLO_PIPELINES": &p.SLOPipelines, "TENANT_WAVE": &p.TenantWave, "TENANT_REPOS": &p.TenantRepos,
	}
	durations := map[string]*time.Duration{
		"SUSTAINED_FOR": &p.SustainedFor, "CHAOS_FOR": &p.ChaosFor, "SETTLE": &p.Settle, "SERIAL_STEP": &p.SerialStep,
		"TENANT_START_WITHIN": &p.TenantStartWithin, "TENANT_REPOS_STEP_P99": &p.TenantReposStepP99,
		"SLO_STEP_P50": &p.SLO.StepP50, "SLO_STEP_P99": &p.SLO.StepP99, "SLO_BUNDLE_P99": &p.SLO.BundleP99,
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
	out = append(out, override{"TENANT_BACKLOG_RATIO", func(s string) error {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && f < 0 {
			err = fmt.Errorf("must not be negative")
		}
		p.TenantBacklogRatio = f
		return err
	}})
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
		fmt.Sprintf("tenant wave %d (repos %d)", p.TenantWave, p.TenantRepos),
	}, ", ")
}
