// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnvtestCoversTheSupportedKubernetes (#1835): the envtest job installs
// the CRDs and admission policies on ENVTEST_K8S_MIN, which must be the
// minor the chart's kubeVersion names as its oldest, and on ENVTEST_K8S_MAX,
// which must be newer; CI runs the job (make envtest) on both.
func TestEnvtestCoversTheSupportedKubernetes(t *testing.T) {
	v := toolVersions(t)
	version := regexp.MustCompile(`^1\.(\d+)\.\d+$`)
	minM := version.FindStringSubmatch(v["ENVTEST_K8S_MIN"])
	maxM := version.FindStringSubmatch(v["ENVTEST_K8S_MAX"])
	require.NotNil(t, minM, "ENVTEST_K8S_MIN %q", v["ENVTEST_K8S_MIN"])
	require.NotNil(t, maxM, "ENVTEST_K8S_MAX %q", v["ENVTEST_K8S_MAX"])
	assert.NotEmpty(t, v["SETUP_ENVTEST_VERSION"])

	assert.Equal(t, strconv.Itoa(oldestMinor(t)), minM[1], "ENVTEST_K8S_MIN is the chart's oldest supported minor")
	lo, _ := strconv.Atoi(minM[1])
	hi, _ := strconv.Atoi(maxM[1])
	assert.Greater(t, hi, lo, "ENVTEST_K8S_MAX is newer than ENVTEST_K8S_MIN")

	ci, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(ci), "make envtest ENVTEST_K8S=", "CI runs make envtest")
	assert.Contains(t, string(ci), "k8s: [min, max]")
}

// oldestMinor is the oldest Kubernetes minor kardinal supports: the chart's
// kubeVersion (">=1.<minor>.0-0").
func oldestMinor(t *testing.T) int {
	t.Helper()
	chart, err := os.ReadFile(filepath.Join(repoRoot(t), "chart", "kardinal-promoter", "Chart.yaml"))
	require.NoError(t, err)
	kube := regexp.MustCompile(`(?m)^kubeVersion:\s*"?>=\s*1\.(\d+)\.`).FindSubmatch(chart)
	require.NotNil(t, kube, "Chart.yaml kubeVersion is >=1.<minor>...")
	n, err := strconv.Atoi(string(kube[1]))
	require.NoError(t, err)
	return n
}
