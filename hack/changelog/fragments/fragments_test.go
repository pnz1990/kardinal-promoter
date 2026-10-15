// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fragments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name, file, text string
		want             Fragment
		wantErr          string
	}{
		{name: "an entry", file: "12-x.md", text: "---\nsection: Fixed\n---\n\n- **T** (#12) — text\n  more\n",
			want: Fragment{File: "12-x.md", Section: "Fixed", Body: "- **T** (#12) — text\n  more"}},
		{name: "CRLF", file: "12.md", text: "---\r\nsection: Added\r\n---\r\n- **T** — x\r\n",
			want: Fragment{File: "12.md", Section: "Added", Body: "- **T** — x"}},
		{name: "two entries", file: "7-a-b.md", text: "---\nsection: Upgrade\n---\n- **A** — 1\n- **B** — 2\n",
			want: Fragment{File: "7-a-b.md", Section: "Upgrade", Body: "- **A** — 1\n- **B** — 2"}},
		{name: "a name with no number", file: "fleet.md", text: "---\nsection: Added\n---\n- **T** — x\n", wantErr: "name it <issue or PR number>-<slug>.md"},
		{name: "an upper-case name", file: "12-Fleet.md", text: "---\nsection: Added\n---\n- **T** — x\n", wantErr: "name it"},
		{name: "no front matter", file: "12.md", text: "- **T** — x\n", wantErr: "no front matter"},
		{name: "an unknown section", file: "12.md", text: "---\nsection: Features\n---\n- **T** — x\n", wantErr: `section "Features"`},
		{name: "another key", file: "12.md", text: "---\nsection: Added\nauthor: me\n---\n- **T** — x\n", wantErr: "only section: is allowed"},
		{name: "no entry", file: "12.md", text: "---\nsection: Added\n---\n\n", wantErr: "no entry"},
		{name: "not a titled entry", file: "12.md", text: "---\nsection: Added\n---\nsome prose\n", wantErr: "starts with - **Title**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.file, tt.text)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func writeFragments(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, Dir), 0o755))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, Dir, name), []byte(body), 0o600))
	}
	return root
}

func TestReadAndRender(t *testing.T) {
	root := writeFragments(t, map[string]string{
		"1000-big.md":  "---\nsection: Added\n---\n- **Big** (#1000) — b\n",
		"99-small.md":  "---\nsection: Added\n---\n- **Small** (#99) — s\n",
		"99-a.md":      "---\nsection: Added\n---\n- **A** (#99) — a\n",
		"5-fix.md":     "---\nsection: Fixed\n---\n- **Fix** — f\n",
		"6-upgrade.md": "---\nsection: Upgrade\n---\n- **Read this** — u\n",
		"README.md":    "how to write a fragment\n",
		".hidden":      "x",
	})
	fs, err := Read(root)
	require.NoError(t, err)
	var names []string
	for _, f := range fs {
		names = append(names, f.File)
	}
	assert.Equal(t, []string{"5-fix.md", "6-upgrade.md", "99-a.md", "99-small.md", "1000-big.md"}, names,
		"by number, then name; README.md and dot files are not fragments")
	assert.Equal(t, "### Before you upgrade\n\n- **Read this** — u\n\n"+
		"### Added\n\n- **A** (#99) — a\n- **Small** (#99) — s\n- **Big** (#1000) — b\n\n"+
		"### Fixed\n\n- **Fix** — f\n", Render(fs))
	assert.Empty(t, Render(nil))

	none, err := Read(t.TempDir())
	require.NoError(t, err, "no docs/changelog.d is no fragments")
	assert.Empty(t, none)

	bad := writeFragments(t, map[string]string{"1-a.md": "nope", "2-b.md": "---\nsection: X\n---\n- **T** — x\n"})
	_, err = Read(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1-a.md: no front matter")
	assert.Contains(t, err.Error(), `2-b.md: section "X"`, "every bad fragment is reported")
}

const changelog = `# Changelog

---

## [Unreleased]

` + Marker + `

## [v0.11.0] — 2026-10-12

### Added

- **Old** — o

[Unreleased]: https://github.com/o/r/compare/v0.11.0...HEAD
[v0.11.0]: https://github.com/o/r/compare/v0.10.0...v0.11.0
`

func TestRelease(t *testing.T) {
	fs := []Fragment{{File: "1-a.md", Section: "Added", Body: "- **New** — n"}, {File: "2-b.md", Section: "Fixed", Body: "- **Fix** — f"}}
	out, err := Release(changelog, fs, "v0.12.0", "2026-11-01")
	require.NoError(t, err)
	assert.Equal(t, `# Changelog

---

## [Unreleased]

`+Marker+`

## [v0.12.0] — 2026-11-01

### Added

- **New** — n

### Fixed

- **Fix** — f

## [v0.11.0] — 2026-10-12

### Added

- **Old** — o

[Unreleased]: https://github.com/o/r/compare/v0.12.0...HEAD
[v0.12.0]: https://github.com/o/r/compare/v0.11.0...v0.12.0
[v0.11.0]: https://github.com/o/r/compare/v0.10.0...v0.11.0
`, out)

	for _, tc := range []struct{ name, changelog, version, wantErr string }{
		{"a bad version", changelog, "0.12", "want vX.Y.Z"},
		{"a version already released", changelog, "v0.11.0", "already has a ## [v0.11.0] section"},
		{"no Unreleased heading", strings.Replace(changelog, "## [Unreleased]\n", "", 1), "v0.12.0", "no ## [Unreleased] heading"},
		{"an entry under Unreleased", strings.Replace(changelog, Marker, Marker+"\n\n### Fixed\n\n- **Stray** — s", 1), "v0.12.0", "entries under [Unreleased] itself"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Release(tc.changelog, fs, tc.version, "2026-11-01")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
	_, err = Release(changelog, nil, "v0.12.0", "2026-11-01")
	assert.ErrorContains(t, err, "nothing to release")
}

func TestAssembled(t *testing.T) {
	root := writeFragments(t, map[string]string{"3-x.md": "---\nsection: Added\n---\n- **X** — x\n"})
	require.NoError(t, os.WriteFile(filepath.Join(root, Changelog), []byte(changelog), 0o600))
	got, err := Assembled(root)
	require.NoError(t, err)
	assert.Contains(t, got, "## [Unreleased]\n\n### Added\n\n- **X** — x\n\n## [v0.11.0]")
	assert.NotContains(t, got, Marker)
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		changed []string
		labels  []string
		wantMsg string
	}{
		{"a product change with a fragment", []string{"pkg/a.go", "docs/changelog.d/12-a.md"}, nil, ""},
		{"a product change without one", []string{"pkg/a.go", "pkg/a_test.go"}, nil, "touches what users see (pkg/a.go)"},
		{"labelled no-changelog", []string{"pkg/a.go"}, []string{"kardinal", "no-changelog"}, ""},
		{"tests only", []string{"pkg/a_test.go", "test/e2e/live/x_test.go", "test/hack/y_test.go"}, nil, ""},
		{"CI and tooling only", []string{".github/workflows/ci.yml", "hack/citool/main.go", "Makefile", "go.mod", "go.sum"}, nil, ""},
		{"contributor docs only", []string{"docs/design/23-x.md", "docs/aide/vision.md", "CONTRIBUTING.md"}, nil, ""},
		{"the UI build output and its tests", []string{"web/dist/index.html", "web/test/x.spec.ts", "web/package.json"}, nil, ""},
		{"user docs", []string{"docs/quickstart.md"}, nil, "docs/quickstart.md"},
		{"the chart", []string{"chart/kardinal-promoter/values.yaml"}, nil, "chart/kardinal-promoter/values.yaml"},
		{"the UI", []string{"web/src/App.tsx"}, nil, "web/src/App.tsx"},
		{"a README in changelog.d is not a fragment", []string{"api/v1alpha1/x.go", "docs/changelog.d/README.md"}, nil, "api/v1alpha1/x.go"},
		{"many files", []string{"pkg/1.go", "pkg/2.go", "pkg/3.go", "pkg/4.go", "pkg/5.go", "pkg/6.go"}, nil, "pkg/5.go, ..."},
		// git rm of another change's fragment is not this change's entry.
		{"a deleted fragment", []string{"pkg/a.go", "docs/changelog.d/1850-gone.md"}, nil, "touches what users see (pkg/a.go)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check(tt.changed, tt.labels, func(f string) bool { return !strings.HasSuffix(f, "-gone.md") })
			if tt.wantMsg == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tt.wantMsg)
			assert.Contains(t, got, "label the pull request no-changelog")
		})
	}
}
