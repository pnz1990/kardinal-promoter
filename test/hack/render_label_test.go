// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestRenderLabelOnReleaseBranch (#2346): on a release/vX.Y branch (CI's
// GITHUB_BASE_REF for a pull request, else GITHUB_REF_NAME), graph.RenderLabel
// is the label the branch's newest release tag writes, or a label main never
// uses, "N-vX.Y.Z". A bare integer that differs from the tag's is refused: it
// is one main uses or will use for another rendering. Skips elsewhere and
// when the tags are not fetched.
func TestRenderLabelOnReleaseBranch(t *testing.T) {
	branch := os.Getenv("GITHUB_BASE_REF")
	if branch == "" {
		branch = os.Getenv("GITHUB_REF_NAME")
	}
	m := regexp.MustCompile(`^release/(v\d+\.\d+)$`).FindStringSubmatch(branch)
	if m == nil {
		t.Skipf("not a release branch: %q", branch)
	}
	root := repoRoot(t)
	out, _ := exec.Command("git", "-C", root, "tag", "--merged", "HEAD", "--list", m[1]+".*", "--sort=-v:refname").Output()
	tag := ""
	for _, name := range strings.Fields(string(out)) {
		if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(name) {
			tag = name
			break
		}
	}
	if tag == "" {
		t.Skipf("no release tag of %s fetched", m[1])
	}
	src, err := exec.Command("git", "-C", root, "show", tag+":pkg/graph/gates.go").Output()
	if err != nil {
		t.Skipf("%s has no pkg/graph/gates.go: %v", tag, err)
	}
	released := "" // a release before #2271 writes no label
	if l := regexp.MustCompile(`(?m)^const RenderLabel = "([^"]+)"`).FindSubmatch(src); l != nil {
		released = string(l[1])
	} else if v := regexp.MustCompile(`(?m)^const RenderVersion = (\d+)`).FindSubmatch(src); v != nil {
		released = string(v[1])
	}
	if graph.RenderLabel != released {
		assert.Regexp(t, `^\d+-v\d+\.\d+\.\d+$`, graph.RenderLabel, "%s: RenderLabel %q differs from %s's %q; a backport "+
			"that changes the rendering sets a label main never uses, N-vX.Y.Z (graph.RenderLabel, #2346)", branch, graph.RenderLabel, tag, released)
	}
}
