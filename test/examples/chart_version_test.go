// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const chartURL = "oci://ghcr.io/pnz1990/charts/kardinal-promoter"

// chartVersionFlag matches the --version value of a helm command.
var chartVersionFlag = regexp.MustCompile(`--version[= ](\S+)`)

// cliDownload matches the release tag of a documented CLI download URL.
var cliDownload = regexp.MustCompile(`github\.com/pnz1990/kardinal-promoter/releases/download/([^/\s]+)/`)

// TestDocumentedChartCommandsPinVersion checks that every documented helm
// command on the published chart names a version. Without --version helm
// picks the newest final chart, so a user of a release candidate would
// install, or upgrade down to, an older release. All pins name the same
// version (or the <version> placeholder), so a release changes them together,
// and every documented CLI download names the same release. No command uses
// --reuse-values: it keeps the old chart's defaults, so an upgrade never gets
// a new default and fails the schema on a key the new chart removed.
func TestDocumentedChartCommandsPinVersion(t *testing.T) {
	root := repoRoot(t)
	files := []string{"README.md", "chart/kardinal-promoter/values.yaml"}
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// The changelog quotes past releases; the design docs are history.
		if rel != "docs/changelog.md" && !strings.HasPrefix(rel, "docs/design/") {
			files = append(files, rel)
		}
		return nil
	}))

	versions := map[string][]string{}
	downloads := map[string][]string{}
	found := 0
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, err)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if m := cliDownload.FindStringSubmatch(line); m != nil {
				downloads[m[1]] = append(downloads[m[1]], fmt.Sprintf("%s:%d", rel, i+1))
			}
			if !strings.Contains(line, chartURL) {
				continue
			}
			found++
			// The command continues while a line ends in a backslash.
			cmd := line
			for j := i; strings.HasSuffix(strings.TrimSpace(lines[j]), `\`) && j+1 < len(lines); j++ {
				cmd += "\n" + lines[j+1]
			}
			where := fmt.Sprintf("%s:%d", rel, i+1)
			assert.NotContains(t, cmd, "--reuse-values", "%s: use --reset-then-reuse-values; --reuse-values keeps the old chart's defaults", where)
			m := chartVersionFlag.FindStringSubmatch(cmd)
			if !assert.NotNil(t, m, "%s: helm command on %s has no --version", where, chartURL) {
				continue
			}
			v := strings.Trim(m[1], "`\"'")
			if v != "<version>" {
				versions[v] = append(versions[v], where)
			}
		}
	}
	require.Greater(t, found, 10, "too few chart commands found; is the scan broken?")
	if assert.Len(t, versions, 1, "documented chart commands pin different versions: %v", versions) {
		for v := range versions {
			require.NotEmpty(t, downloads, "no documented CLI download found; is the scan broken?")
			for tag, where := range downloads {
				assert.Equal(t, "v"+v, tag, "CLI download at %v names a different release than the chart pin", where)
			}
		}
	}
}
