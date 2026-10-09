//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The topology zoo: Pipelines of the shapes large companies run, one Bundle
// (or a few) through each, then the invariants. Each test runs in parallel
// with the other topology and race tests.

// TestScale_TopologyChain promotes one Bundle through a linear chain of the
// profile's ChainStages environments (100, the most a Pipeline accepts) and
// checks the invariants. Covers SCALE-TOPO-CHAIN-01.
func TestScale_TopologyChain(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Chain(r.P.ChainStages)
	p := r.Fleet.Pipeline(t, "chain", envs)
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.Note("stages", len(envs))
	r.Finish()
}

// tooManyEnvironments is the bug the API server's 100-environment cap is
// filed as: a Pipeline cannot have the 120-stage chain or the 150-region
// fan-out a large company runs.
const tooManyEnvironments = 1473

// applyBig creates a Pipeline over its own repo. A refusal of the
// environment count is the known bug; any other error fails the test.
func applyBig(t *testing.T, r *scale.Run, name string, envs []v1alpha1.EnvironmentSpec) *v1alpha1.Pipeline {
	t.Helper()
	repo := r.Fleet.Repo(t, r.Fleet.NS+"-"+name, map[string][]string{name: scale.Names(envs)})
	p := r.Fleet.PipelineSpec(name, repo, envs)
	err := r.E.Client.Create(context.Background(), p)
	if apierrors.IsInvalid(err) {
		t.Logf("the API server refused a %d-environment Pipeline: %v", len(envs), err)
		scale.KnownBug(t, tooManyEnvironments, fmt.Sprintf("a Pipeline of %d environments is refused (spec.environments has maxItems 100)", len(envs)))
	}
	if err != nil {
		t.Fatalf("create Pipeline %s: %v", name, err)
	}
	r.Fleet.Track(p, repo)
	return p
}

// TestScale_TopologyLongChain is the chain a large company asks for: the
// profile's LongChainStages (120) environments in a line, one Bundle through
// all of them. Covers SCALE-TOPO-LONGCHAIN-01.
func TestScale_TopologyLongChain(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Chain(r.P.LongChainStages)
	p := applyBig(t, r, "longchain", envs)
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.Note("stages", len(envs))
	r.Finish()
}

// TestScale_TopologyWaves is a fan-out: canary, then Waves waves of
// WaveWidth regions (wave:), every region of a wave on every region of the
// wave before, so each wave's regions push to one branch at once.
// Covers SCALE-TOPO-WAVES-01.
func TestScale_TopologyWaves(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Waves(r.P.Waves, r.P.WaveWidth)
	p := r.Fleet.Pipeline(t, "waves", envs)
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.Note("environments", len(envs))
	r.Note("edges", scale.Edges(envs))
	r.Finish()
}

// TestScale_TopologyBigWaves is the fan-out a large company asks for: canary
// and BigWaves (10) waves of BigWaveWidth (15) regions, 151 environments.
// Covers SCALE-TOPO-BIGWAVES-01.
func TestScale_TopologyBigWaves(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Waves(r.P.BigWaves, r.P.BigWaveWidth)
	p := applyBig(t, r, "bigwaves", envs)
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.Note("environments", len(envs))
	r.Note("edges", scale.Edges(envs))
	r.Finish()
}

// TestScale_TopologyLattice is a diamond lattice: entry, LatticeDepth layers
// of LatticeWidth environments each depending on the whole layer before,
// exit. Covers SCALE-TOPO-LATTICE-01.
func TestScale_TopologyLattice(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Lattice(r.P.LatticeDepth, r.P.LatticeWidth)
	p := r.Fleet.Pipeline(t, "lattice", envs)
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.Note("environments", len(envs))
	r.Note("edges", scale.Edges(envs))
	r.Finish()
}

// TestScale_TopologyFanIn is build, FanIn parallel environments, and prod
// depending on all of them: prod's step must not exist before every leaf is
// Verified. Covers SCALE-TOPO-FANIN-01.
func TestScale_TopologyFanIn(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.FanIn(r.P.FanIn)
	p := r.Fleet.Pipeline(t, "fanin", envs)
	b := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.Note("environments", len(envs))
	r.Finish()

	steps, err := r.E.Steps(context.Background(), r.Fleet.NS, p.Name, b.Name)
	if err != nil {
		t.Fatal(err)
	}
	var prodCreated, lastLeaf time.Time
	for i := range steps {
		s := &steps[i]
		if s.Spec.Environment == "prod" {
			prodCreated = s.CreationTimestamp.Time
			continue
		}
		if at, ok := lifecycle.VerifiedTime(s); ok && s.Spec.Environment != "build" && at.After(lastLeaf) {
			lastLeaf = at
		}
	}
	// Both times are whole seconds.
	if prodCreated.IsZero() || prodCreated.Before(lastLeaf) {
		t.Errorf("prod's step was created at %s, before the last leaf was Verified at %s", prodCreated, lastLeaf)
	}
}

// TestScale_TopologyMixedApproval is a chain where every third environment
// needs a PR review and the rest promote automatically; a reviewer merges
// each PR as it opens. Two Bundles go through one after the other.
// Covers SCALE-TOPO-MIXED-01.
func TestScale_TopologyMixedApproval(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Chain(12)
	for i := range envs {
		if i%3 == 2 {
			envs[i].Approval = "pr-review"
		}
	}
	p := r.Fleet.Pipeline(t, "mixed", envs)
	rev := scale.Review(t, r.E.Git, nil, r.Fleet.Targets()[0].Repo)
	b1 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.E.WaitBundlePhase(t, r.Fleet.NS, b1.Name, "Verified", r.P.Settle)
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 2))
	r.Fleet.WaitSettled(t, r.P.Settle)
	rev.Stop()
	r.Note("prsMerged", rev.Merged())
	if rev.Merged() != 8 {
		t.Errorf("the reviewer merged %d PRs, want 8 (4 pr-review environments, 2 Bundles)", rev.Merged())
	}
	r.Finish()
}

// TestScale_TopologySharedRepo has SharedPipelines Pipelines, each a chain
// of five environments under its own path, writing one repo and branch, and
// three Bundles per Pipeline created at once, so every push races the
// others'. Covers SCALE-TOPO-SHARED-01.
func TestScale_TopologySharedRepo(t *testing.T) {
	r := scale.BeginParallel(t)
	envs := scale.Chain(5)
	all := map[string][]string{}
	var names []string
	for i := 1; i <= r.P.SharedPipelines; i++ {
		name := fmt.Sprintf("shared-%02d", i)
		names = append(names, name)
		all[name] = scale.Names(envs)
	}
	repo := r.Fleet.Repo(t, r.Fleet.NS+"-shared", all)
	for _, name := range names {
		r.Fleet.Apply(t, r.Fleet.PipelineSpec(name, repo, envs), repo)
	}
	n := len(names) * 3
	err := scale.Parallel(n, n, func(i int) error {
		name := names[i%len(names)]
		_, err := r.Fleet.CreateBundle(context.Background(), name, scale.Tag(name, i/len(names)+1))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Note("pipelines", len(names))
	r.Finish()
}
