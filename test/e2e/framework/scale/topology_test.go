// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopologies(t *testing.T) {
	cases := []struct {
		name         string
		envs, edges  int
		first, last  string
		lastDepsWant []string
	}{
		{name: "chain", envs: 120, edges: 119, first: "s001", last: "s120"},
		{name: "waves", envs: 151, edges: 15 + 9*15*15, first: "canary", last: "w10-r15"},
		{name: "lattice", envs: 1 + 3*2 + 1, edges: 2 + 2*2*2 + 2, first: "entry", last: "exit", lastDepsWant: []string{"l3-n1", "l3-n2"}},
		{name: "fan-in", envs: 52, edges: 100, first: "build", last: "prod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := Chain(120)
			switch tc.name {
			case "waves":
				spec = Waves(10, 15)
			case "lattice":
				spec = Lattice(3, 2)
			case "fan-in":
				spec = FanIn(50)
			}
			require.Len(t, spec, tc.envs)
			assert.Equal(t, tc.first, spec[0].Name)
			assert.Equal(t, tc.last, spec[len(spec)-1].Name)
			assert.Equal(t, tc.edges, Edges(spec))
			if tc.lastDepsWant != nil {
				assert.Equal(t, tc.lastDepsWant, spec[len(spec)-1].DependsOn)
			}
			seen := map[string]bool{}
			for _, n := range Names(spec) {
				assert.False(t, seen[n], "duplicate name %s", n)
				assert.LessOrEqual(t, len(n), 63)
				seen[n] = true
			}
		})
	}
	w := Waves(2, 3)
	assert.Equal(t, 0, w[0].Wave)
	assert.Equal(t, 1, w[1].Wave)
	assert.Equal(t, 2, w[6].Wave)
}

func TestParallel(t *testing.T) {
	var ran atomic.Int32
	err := Parallel(4, 100, func(i int) error {
		ran.Add(1)
		if i == 42 {
			return errors.New("boom")
		}
		return nil
	})
	assert.EqualError(t, err, "boom")
	assert.Equal(t, int32(100), ran.Load(), "every item runs even after an error")
	assert.NoError(t, Parallel(3, 0, func(int) error { return errors.New("never") }))
}
