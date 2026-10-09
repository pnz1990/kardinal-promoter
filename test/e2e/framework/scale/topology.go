// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The topologies build a Pipeline's environments: names, approval and the
// DAG (dependsOn, wave). Fleet.Pipeline fills in path, update and health.

// Chain is n environments in a line, s001 -> s002 -> ... (list order, no
// dependsOn: the Pipeline's sequential default).
func Chain(n int) []v1alpha1.EnvironmentSpec {
	envs := make([]v1alpha1.EnvironmentSpec, n)
	for i := range envs {
		envs[i].Name = fmt.Sprintf("s%03d", i+1)
	}
	return envs
}

// Waves is a canary environment followed by waves waves of width regions
// (w01-r01 ... w10-r15): every region of a wave depends on every region of
// the wave before, and the first wave on canary (wave: plus the list-order
// edge to the last environment without a wave).
func Waves(waves, width int) []v1alpha1.EnvironmentSpec {
	envs := []v1alpha1.EnvironmentSpec{{Name: "canary"}}
	for w := 1; w <= waves; w++ {
		for r := 1; r <= width; r++ {
			envs = append(envs, v1alpha1.EnvironmentSpec{Name: fmt.Sprintf("w%02d-r%02d", w, r), Wave: w})
		}
	}
	return envs
}

// Lattice is a diamond lattice: entry, then depth layers of width
// environments (l1-n1 ...) each depending on every environment of the layer
// before, then exit depending on the whole last layer.
func Lattice(depth, width int) []v1alpha1.EnvironmentSpec {
	envs := []v1alpha1.EnvironmentSpec{{Name: "entry"}}
	prev := []string{"entry"}
	for l := 1; l <= depth; l++ {
		var layer []string
		for n := 1; n <= width; n++ {
			name := fmt.Sprintf("l%d-n%d", l, n)
			envs = append(envs, v1alpha1.EnvironmentSpec{Name: name, DependsOn: append([]string(nil), prev...)})
			layer = append(layer, name)
		}
		prev = layer
	}
	return append(envs, v1alpha1.EnvironmentSpec{Name: "exit", DependsOn: prev})
}

// FanIn is build, then n parallel environments (leaf-01 ...) on build, then
// prod depending on all n.
func FanIn(n int) []v1alpha1.EnvironmentSpec {
	envs := []v1alpha1.EnvironmentSpec{{Name: "build"}}
	leaves := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("leaf-%02d", i)
		envs = append(envs, v1alpha1.EnvironmentSpec{Name: name, DependsOn: []string{"build"}})
		leaves = append(leaves, name)
	}
	return append(envs, v1alpha1.EnvironmentSpec{Name: "prod", DependsOn: leaves})
}

// Names returns the environment names of envs.
func Names(envs []v1alpha1.EnvironmentSpec) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Name
	}
	return out
}

// Edges counts the dependency edges of envs as the Pipeline resolves them:
// explicit dependsOn, else the previous wave, else the previous environment.
// It is the Graph's edge count before gates, for the report.
func Edges(envs []v1alpha1.EnvironmentSpec) int {
	n := 0
	byWave := map[int]int{}
	for _, e := range envs {
		if e.Wave > 0 {
			byWave[e.Wave]++
		}
	}
	for i, e := range envs {
		switch {
		case len(e.DependsOn) > 0:
			n += len(e.DependsOn)
		case e.Wave > 1:
			n += byWave[e.Wave-1]
		case i > 0:
			n++
		}
	}
	return n
}
