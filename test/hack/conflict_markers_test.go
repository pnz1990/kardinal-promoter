// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// conflictMarker is a line git writes for a merge conflict.
var conflictMarker = regexp.MustCompile(`^(<<<<<<<|=======|>>>>>>>)( |$)`)

// TestNoConflictMarkers fails on a merge conflict marker in any tracked text
// file. A resolution that kept a marker reached main once, in
// docs/changelog.md, and nothing caught it.
func TestNoConflictMarkers(t *testing.T) {
	root := repoRoot(t)
	ls := exec.Command("git", "ls-files", "-z")
	ls.Dir = root
	out, err := ls.Output()
	require.NoError(t, err)
	var found []string
	for _, path := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if path == "" || strings.HasPrefix(path, "web/dist/") {
			continue // built assets
		}
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue // removed in the work tree, or binary
		}
		for i, line := range strings.Split(string(data), "\n") {
			if conflictMarker.MatchString(strings.TrimSuffix(line, "\r")) {
				found = append(found, path+":"+strconv.Itoa(i+1)+": "+line)
			}
		}
	}
	require.Empty(t, found, "merge conflict markers in tracked files")
}

// TestChangelogNoDuplicateEntries fails when a section of docs/changelog.md
// (a ### heading under one release, however often the heading repeats) lists
// the same entry twice: two bullets with the same bold title. Merges that kept both sides of a changelog
// conflict did that more than once.
func TestChangelogNoDuplicateEntries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "changelog.md"))
	require.NoError(t, err)
	title := regexp.MustCompile(`^- \*\*(.+?)\*\*`)
	var dups []string
	release, section := "", ""
	// Keyed by the section's heading text, not its position: a release with
	// two "### Added" headings is one Added section.
	seen := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			release, section, seen = line, "", map[string]int{}
		case strings.HasPrefix(line, "### "):
			section = line
		}
		m := title.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := section + "\x00" + m[1]
		if first, ok := seen[key]; ok {
			dups = append(dups, fmt.Sprintf("%s %s: %q at lines %d and %d", release, section, m[1], first, i+1))
			continue
		}
		seen[key] = i + 1
	}
	require.Empty(t, dups, "duplicate changelog entries")
}
