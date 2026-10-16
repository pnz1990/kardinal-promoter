// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGitLabImageIsPatched: the live suites run GitLab 19.4.2 or later.
// Older GitLab releases have a CVE, and the e2e GitLab is reachable from the
// test cluster with real tokens.
func TestGitLabImageIsPatched(t *testing.T) {
	src, err := os.ReadFile("../../hack/e2e/versions.env")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^GITLAB_IMAGE=\S*gitlab-ce:(\d+)\.(\d+)\.(\d+)-ce\.\d+$`).FindSubmatch(src)
	require.NotNil(t, m, "hack/e2e/versions.env has no GITLAB_IMAGE=...gitlab-ce:X.Y.Z-ce.N line")
	v := [3]int{}
	for i := range v {
		v[i], err = strconv.Atoi(string(m[i+1]))
		require.NoError(t, err)
	}
	min := [3]int{19, 4, 2}
	ok := v[0] > min[0] || (v[0] == min[0] && (v[1] > min[1] || (v[1] == min[1] && v[2] >= min[2])))
	require.Truef(t, ok, "GITLAB_IMAGE is gitlab-ce %d.%d.%d; the live suites need 19.4.2 or later", v[0], v[1], v[2])
}
