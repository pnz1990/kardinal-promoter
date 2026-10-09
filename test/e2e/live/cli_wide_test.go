//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestCLI_GetPipelinesWide checks the layout of a Pipeline with more than 8
// environments (#1579): podinfo is test, then r1…r9 in wave 1 after it. Once
// a Bundle is Verified everywhere, kardinal get pipelines shows one summary
// row (ENVS, PROGRESS, FURTHEST) and the note, kardinal get pipelines podinfo
// shows every environment as a column in DAG order, and a second Bundle is
// listed first by kardinal get bundles, as a table and as -o json.
//
// Covers CLI-GET-PIPELINES-02.
func TestCLI_GetPipelinesWide(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	wave := []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8", "r9"}
	envs := append([]string{"test"}, wave...)
	a := newArgoApp(t, e, envs...)
	p := a.pipeline(nil)
	for _, env := range wave {
		envSpec(t, p, env).Wave = 1
	}
	a.apply(t, p)
	waitPipelineValid(t, e, a.ns, pipelineName)
	b1 := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil).Name
	for _, env := range envs {
		e.WaitStepState(t, a.ns, pipelineName, b1, env, "Verified", promoteTimeout)
	}

	out := c.Must(a.ns, "get", "pipelines")
	header := cliCellGap.Split(strings.SplitN(out, "\n", 2)[0], -1)
	assert.Equal(t, []string{"PIPELINE", "BUNDLE", "ENVS", "PROGRESS", "FURTHEST", "SUB", "AGE"}, header, out)
	row := framework.TableRow(out, "PIPELINE", pipelineName)
	require.NotNil(t, row, out)
	assert.Equal(t, b1, row["BUNDLE"])
	assert.Equal(t, "10", row["ENVS"])
	assert.Equal(t, "10 Verified", row["PROGRESS"])
	assert.Equal(t, "r9", row["FURTHEST"], "the wave's last environment in spec order")
	assert.Contains(t, out, "More than 8 environments: one row per Pipeline. Every environment of one: kardinal get pipelines <name>")

	one := c.Must(a.ns, "get", "pipelines", pipelineName)
	header = cliCellGap.Split(strings.SplitN(one, "\n", 2)[0], -1)
	assert.Equal(t, []string{"PIPELINE", "BUNDLE", "TEST", "R1", "R2", "R3", "R4", "R5", "R6", "R7", "R8", "R9", "SUB", "AGE"}, header,
		"every environment, in DAG order")
	row = framework.TableRow(one, "PIPELINE", pipelineName)
	require.NotNil(t, row, one)
	for _, env := range envs {
		assert.Equal(t, "Verified", row[strings.ToUpper(env)], env)
	}

	// A newer Bundle is listed first, by the table and by -o json.
	b2 := createBundle(t, e, a.ns, pipelineName, "", fixtures.V3, nil).Name
	lines := strings.Split(strings.TrimSpace(c.Must(a.ns, "get", "bundles")), "\n")
	require.Len(t, lines, 3, lines)
	assert.True(t, strings.HasPrefix(lines[1], b2+" "), "newest first: %v", lines)
	assert.True(t, strings.HasPrefix(lines[2], b1+" "), "newest first: %v", lines)
	var bl []v1alpha1.Bundle
	require.NoError(t, json.Unmarshal([]byte(c.Must(a.ns, "get", "bundles", "-o", "json")), &bl))
	require.Len(t, bl, 2)
	assert.Equal(t, []string{b2, b1}, []string{bl[0].Name, bl[1].Name})
}
