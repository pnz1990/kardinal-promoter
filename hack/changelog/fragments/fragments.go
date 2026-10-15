// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package fragments reads, renders and releases the changelog's unreleased
// entries, one file each in docs/changelog.d (hack/changelog).
package fragments

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Dir holds the fragments, Changelog the changelog, relative to the root.
const (
	Dir       = "docs/changelog.d"
	Changelog = "docs/changelog.md"
	// Marker, under ## [Unreleased] in the changelog, is where the docs build
	// puts the fragments.
	Marker = "<!-- changelog.d: the unreleased entries are the files in docs/changelog.d (CONTRIBUTING.md, Changelog) -->"
	// SkipLabel opts a pull request out of the fragment check.
	SkipLabel = "no-changelog"
)

// Sections in the order a release lists them, and their headings. Upgrade is
// the release's "Before you upgrade".
var Sections = []string{"Upgrade", "Added", "Changed", "Deprecated", "Removed", "Fixed", "Security", "Documentation", "Development"}

var headings = map[string]string{"Upgrade": "Before you upgrade"}

func heading(section string) string {
	if h, ok := headings[section]; ok {
		return h
	}
	return section
}

// Fragment is one change.
type Fragment struct {
	File    string // base name
	Section string
	Body    string // markdown, trimmed
}

var (
	frontMatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n(.*)\z`)
	fileName    = regexp.MustCompile(`^[0-9]+(-[a-z0-9][a-z0-9-]*)?\.md$`)
	entryLine   = regexp.MustCompile(`^- \*\*[^*]`)
)

// Parse reads a fragment.
func Parse(name, text string) (Fragment, error) {
	f := Fragment{File: name}
	if !fileName.MatchString(name) {
		return f, fmt.Errorf("%s: name it <issue or PR number>-<slug>.md, e.g. 1835-fleet-pause.md", name)
	}
	m := frontMatter.FindStringSubmatch(strings.ReplaceAll(text, "\r\n", "\n"))
	if m == nil {
		return f, fmt.Errorf("%s: no front matter: start with ---, section: <%s>, ---", name, strings.Join(Sections, "|"))
	}
	for _, l := range strings.Split(m[1], "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(k) != "section" {
			return f, fmt.Errorf("%s: front matter line %q: only section: is allowed", name, l)
		}
		f.Section = strings.TrimSpace(v)
	}
	known := false
	for _, s := range Sections {
		known = known || s == f.Section
	}
	if !known {
		return f, fmt.Errorf("%s: section %q: want one of %s", name, f.Section, strings.Join(Sections, ", "))
	}
	f.Body = strings.TrimSpace(m[2])
	if f.Body == "" {
		return f, fmt.Errorf("%s: no entry", name)
	}
	if !entryLine.MatchString(f.Body) {
		return f, fmt.Errorf("%s: the entry starts with - **Title**", name)
	}
	return f, nil
}

// leadingNumber orders 99-x before 1000-y.
func leadingNumber(name string) int {
	n, _ := strconv.Atoi(strings.SplitN(strings.TrimSuffix(name, ".md"), "-", 2)[0])
	return n
}

// Read reads every fragment of root's Dir, in order: by leading number, then name.
func Read(root string) ([]Fragment, error) {
	entries, err := os.ReadDir(filepath.Join(root, Dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", Dir, err)
	}
	var out []Fragment
	var errs []error
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README.md" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, Dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read fragment: %w", err)
		}
		f, err := Parse(e.Name(), string(data))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, f)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := leadingNumber(out[i].File), leadingNumber(out[j].File)
		if a != b {
			return a < b
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// Render is the fragments as changelog sections: "### <heading>", a blank
// line, the entries, a blank line between sections; "" for none.
func Render(fs []Fragment) string {
	var b strings.Builder
	for _, s := range Sections {
		var bodies []string
		for _, f := range fs {
			if f.Section == s {
				bodies = append(bodies, f.Body)
			}
		}
		if len(bodies) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n", heading(s), strings.Join(bodies, "\n"))
	}
	return b.String()
}

var (
	unreleased  = regexp.MustCompile(`(?m)^## \[Unreleased\]\n`)
	nextRelease = regexp.MustCompile(`(?m)^## \[`)
	linkRef     = regexp.MustCompile(`(?m)^\[Unreleased\]: (https://github\.com/[^/]+/[^/]+)/compare/(v[^.]+\.[^.]+\.[^.]+)\.\.\.HEAD$`)
	version     = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)
)

// Release writes fs into changelog as version, released on date: a
// "## [version] — date" section under [Unreleased] (which keeps only the
// marker), and the compare links. It refuses a changelog with entries
// written under [Unreleased] itself, which the fragments replaced.
func Release(changelog string, fs []Fragment, ver, date string) (string, error) {
	if !version.MatchString(ver) {
		return "", fmt.Errorf("version %q: want vX.Y.Z", ver)
	}
	if strings.Contains(changelog, "## ["+ver+"]") {
		return "", fmt.Errorf("%s already has a ## [%s] section", Changelog, ver)
	}
	if len(fs) == 0 {
		return "", fmt.Errorf("no fragments in %s: nothing to release", Dir)
	}
	loc := unreleased.FindStringIndex(changelog)
	if loc == nil {
		return "", fmt.Errorf("%s has no ## [Unreleased] heading", Changelog)
	}
	rest := changelog[loc[1]:]
	end := len(rest)
	if n := nextRelease.FindStringIndex(rest); n != nil {
		end = n[0]
	}
	if body := strings.TrimSpace(strings.Replace(rest[:end], Marker, "", 1)); body != "" {
		return "", fmt.Errorf("%s has entries under [Unreleased] itself; move them into %s first", Changelog, Dir)
	}
	section := fmt.Sprintf("## [%s] — %s\n\n%s", ver, date, Render(fs))
	out := changelog[:loc[1]] + "\n" + Marker + "\n\n" + section + "\n" + rest[end:]
	if m := linkRef.FindStringSubmatchIndex(out); m != nil {
		repo, prev := out[m[2]:m[3]], out[m[4]:m[5]]
		links := fmt.Sprintf("[Unreleased]: %s/compare/%s...HEAD\n[%s]: %s/compare/%s...%s", repo, ver, ver, repo, prev, ver)
		out = out[:m[0]] + links + out[m[1]:]
	}
	return out, nil
}

// UserFacing says whether a changed file is one a user sees: the product, the
// chart, the CRDs, the UI or the user docs. Tests, CI, hack/ and the
// contributor docs are not.
func UserFacing(f string) bool {
	switch {
	case strings.HasPrefix(f, Dir+"/"), f == Changelog,
		strings.HasPrefix(f, "docs/aide/"), strings.HasPrefix(f, "docs/design/"),
		strings.HasSuffix(f, "_test.go"), strings.HasPrefix(f, "web/test/"), strings.HasPrefix(f, "web/dist/"):
		return false
	}
	for _, p := range []string{"api/", "cmd/", "pkg/", "chart/", "config/", "web/src/", "docs/"} {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	return false
}

// Check returns "" when the changed files need no fragment or add one, else
// why the change needs one.
func Check(changed []string, labels []string) string {
	for _, l := range labels {
		if l == SkipLabel {
			return ""
		}
	}
	var facing []string
	for _, f := range changed {
		if path.Dir(f) == Dir && strings.HasSuffix(f, ".md") && path.Base(f) != "README.md" {
			return ""
		}
		if UserFacing(f) {
			facing = append(facing, f)
		}
	}
	if len(facing) == 0 {
		return ""
	}
	if len(facing) > 5 {
		facing = append(facing[:5], "...")
	}
	return fmt.Sprintf("this change touches what users see (%s) and adds no %s/<issue>-<slug>.md; add one (CONTRIBUTING.md, Changelog), or label the pull request %s",
		strings.Join(facing, ", "), Dir, SkipLabel)
}

// Assembled is root's changelog with the fragments in place of the marker,
// as the docs site shows it: what the changelog checks read.
func Assembled(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, Changelog))
	if err != nil {
		return "", fmt.Errorf("read changelog: %w", err)
	}
	fs, err := Read(root)
	if err != nil {
		return "", err
	}
	return strings.Replace(string(data), Marker, strings.TrimSuffix(Render(fs), "\n"), 1), nil
}
