// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func TestBundleVersion(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.BundleSpec
		want string
	}{
		{name: "tag", spec: v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "sha-1a2b3c4"}}},
			want: "sha-1a2b3c4"},
		{name: "two images", spec: v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{
			{Repository: "r/api", Tag: "1.2"}, {Repository: "r/web", Tag: "3.4"}}}, want: "1.2, 3.4"},
		{name: "digest only", spec: v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{
			{Repository: "r/app", Digest: "sha256:0123456789abcdef"}}}, want: "sha256:0123456"},
		{name: "config", spec: v1alpha1.BundleSpec{Type: "config",
			ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "abcdef0123456789"}}, want: "config abcdef0"},
		{name: "mixed", spec: v1alpha1.BundleSpec{Type: "mixed",
			Images:    []v1alpha1.ImageRef{{Repository: "r/app", Tag: "2"}},
			ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "abc"}}, want: "2, config abc"},
		{name: "nothing", spec: v1alpha1.BundleSpec{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bundleVersion(&v1alpha1.Bundle{Spec: tt.spec}))
		})
	}
	assert.Empty(t, bundleVersion(nil))
	assert.Equal(t, "none", deployedLabel("", nil))
	assert.Equal(t, "gone", deployedLabel("gone", map[string]*v1alpha1.Bundle{}), "a deleted Bundle keeps its name")
}

// deployedFixture: b1 (tag 1.0) is Verified in test, uat and prod. b2 (tag
// 2.0), the current Bundle, is Verified in test, waits for its PR in uat, and
// has only a gate instance in prod. canary has never been promoted to.
func deployedFixture() []sigs_client.Object {
	old := policyTestNow.Add(-2 * time.Hour)
	recent := policyTestNow.Add(-10 * time.Minute)
	b1 := explainBundle("b1", "Verified", old)
	b1.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "1.0"}}
	b2 := explainBundle("b2", "Promoting", recent)
	b2.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "2.0"}}
	return []sigs_client.Object{
		policyPipeline("demo", "test", "uat", "prod", "canary"),
		b1, b2,
		explainStep("demo", "b1", "test", "Verified", "", old),
		explainStep("demo", "b1", "uat", "Verified", "", old),
		explainStep("demo", "b1", "prod", "Verified", "", old),
		explainStep("demo", "b2", "test", "Verified", "", recent),
		explainStep("demo", "b2", "uat", "WaitingForMerge", "", recent),
		explainGateInstance("demo", "b2", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
			"!schedule.isWeekend = false"),
		explainGateInstance("demo", "b2", "canary", "no-weekend-deploys", "!schedule.isWeekend", false, true,
			"!schedule.isWeekend = false"),
	}
}

// explain names the Bundle of every row and, for an environment where the
// current Bundle's change has not landed, the Bundle deployed there now.
func TestExplain_BundleColumnAndDeployed(t *testing.T) {
	out, err := runExplain(t, policyClient(t, deployedFixture()...), "demo", "", false)
	require.NoError(t, err)
	table, deployed, ok := strings.Cut(out, "\n\n")
	require.True(t, ok, "a deployed block follows the table:\n%s", out)

	lines := strings.Split(strings.TrimSpace(table), "\n")
	require.Len(t, lines, 5, out)
	assert.Regexp(t, `^ENVIRONMENT +VERSION +TYPE +NAME +STATE +EXPRESSION +REASON$`, lines[0])
	for _, l := range lines[1:] {
		assert.Equal(t, []string{"2.0", "(b2)"}, strings.Fields(l)[1:3], "row names the current version and its Bundle: %q", l)
	}

	// test: b2 landed, no line. uat: b2 waits for merge, b1 still runs.
	// prod: b2 has not reached it. canary: nothing landed yet.
	assert.Equal(t, []string{
		"canary   deployed: none",
		"prod     deployed: 1.0 (b1)",
		"uat      deployed: 1.0 (b1)",
	}, strings.Split(strings.TrimSpace(deployed), "\n"))

	out, err = runExplain(t, policyClient(t, deployedFixture()...), "demo", "test", false)
	require.NoError(t, err)
	assert.NotContains(t, out, "deployed:", "b2 landed in test")
	out, err = runExplain(t, policyClient(t, deployedFixture()...), "demo", "prod", false)
	require.NoError(t, err)
	assert.Contains(t, out, "\n\nprod   deployed: 1.0 (b1)\n")
	assert.NotContains(t, out, "uat")

	// Color only touches the STATE cell; the VERSION column does not shift it.
	out, err = runExplain(t, policyClient(t, deployedFixture()...), "demo", "prod", true)
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`prod +2\.0 \(b2\) +PolicyGate +no-weekend-deploys +\x1b\[[0-9;]*mNot ready\x1b\[0m`), out)
}

// status lists what runs in every environment, including ones the newest
// Bundle has not reached, and the Bundle on its way in.
func TestStatusPipelineWriter_RunningAndIncoming(t *testing.T) {
	out := runStatusPipeline(t, deployedFixture()...)
	assert.Equal(t, []map[string]string{
		{"ENVIRONMENT": "test", "RUNNING": "2.0 (b2)", "INCOMING": "-", "STATE": "Live", "SINCE": "-", "PR": "-"},
		{"ENVIRONMENT": "uat", "RUNNING": "1.0 (b1)", "INCOMING": "2.0 (b2)", "STATE": "Waiting for PR merge", "SINCE": "-", "PR": "-"},
		{"ENVIRONMENT": "prod", "RUNNING": "1.0 (b1)", "INCOMING": "-", "STATE": "Live", "SINCE": "-", "PR": "-"},
		{"ENVIRONMENT": "canary", "RUNNING": "-", "INCOMING": "-", "STATE": "-", "SINCE": "-", "PR": "-"},
	}, statusRows(t, out), out)
	assertNoRawStates(t, out)
}

// mixedDeployedFixture: in prod the image Bundle i1 (tag 1.0) landed, then the
// config Bundle c1 (commit abcdef0...), then the image Bundle i2 (tag 2.0).
// Image and config Bundles do not supersede each other, so prod runs i2's
// image and c1's config. In uat only c1 landed, after i1. In test only i1.
// The current Bundle i3 waits for its PR in prod (#1353).
func mixedDeployedFixture() []sigs_client.Object {
	at := func(m int) time.Time { return policyTestNow.Add(time.Duration(m-60) * time.Minute) }
	i1 := explainBundle("i1", "Verified", at(1))
	i1.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "1.0"}}
	c1 := explainBundle("c1", "Verified", at(2))
	c1.Spec.Type = "config"
	c1.Spec.ConfigRef = &v1alpha1.ConfigRef{CommitSHA: "abcdef0123456789"}
	i2 := explainBundle("i2", "Verified", at(3))
	i2.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "2.0"}}
	i3 := explainBundle("i3", "Promoting", at(4))
	i3.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "3.0"}}
	return []sigs_client.Object{
		policyPipeline("demo", "test", "uat", "prod"),
		i1, c1, i2, i3,
		explainStep("demo", "i1", "test", "Verified", "", at(1)),
		explainStep("demo", "i1", "uat", "Verified", "", at(1)),
		explainStep("demo", "i1", "prod", "Verified", "", at(1)),
		explainStep("demo", "c1", "uat", "Verified", "", at(2)),
		explainStep("demo", "c1", "prod", "Verified", "", at(2)),
		explainStep("demo", "i2", "prod", "Verified", "", at(3)),
		explainStep("demo", "i3", "test", "Verified", "", at(4)),
		explainStep("demo", "i3", "uat", "Verified", "", at(4)),
		explainStep("demo", "i3", "prod", "WaitingForMerge", "", at(4)),
	}
}

// With a Verified image Bundle and a Verified config Bundle in one
// environment, explain names both: the newest one that landed, and where the
// rest of what runs there came from (#1353). status names the newest one.
func TestDeployed_ImageAndConfigBundles(t *testing.T) {
	out := runStatusPipeline(t, mixedDeployedFixture()...)
	rows := statusRows(t, out)
	require.Len(t, rows, 3, out)
	assert.Equal(t, "2.0 (i2)", rows[2]["RUNNING"], out)
	assert.Equal(t, "3.0 (i3)", rows[2]["INCOMING"], out)

	out, err := runExplain(t, policyClient(t, mixedDeployedFixture()...), "demo", "prod", false)
	require.NoError(t, err)
	assert.Contains(t, out, "\n\nprod   deployed: 2.0 (i2); config abcdef0 from c1\n")
}

// A config Bundle that landed last names the image Bundle whose images still
// run, by its tags only.
func TestDeployedLabel_ConfigOverImages(t *testing.T) {
	mixed := &v1alpha1.Bundle{Spec: v1alpha1.BundleSpec{Type: "mixed",
		Images:    []v1alpha1.ImageRef{{Repository: "r/app", Tag: "1.5"}},
		ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "0000000aaaa"}}}
	cfg := &v1alpha1.Bundle{Spec: v1alpha1.BundleSpec{Type: "config",
		ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "1111111bbbb"}}}
	byName := map[string]*v1alpha1.Bundle{"m1": mixed, "c2": cfg}
	assert.Equal(t, "config 1111111 (c2); images 1.5 from m1",
		deployedLabelOf(deployedEnv{bundle: "c2", imagesFrom: "m1"}, byName))
	assert.Equal(t, "1.5, config 0000000 (m1)", deployedLabelOf(deployedEnv{bundle: "m1"}, byName))
	assert.Equal(t, "none", deployedLabelOf(deployedEnv{}, byName))
}
