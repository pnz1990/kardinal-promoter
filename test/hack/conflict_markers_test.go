// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"bytes"
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
