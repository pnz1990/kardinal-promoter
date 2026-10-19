// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRepo is a repo of the given text files (and binary files, nil text).
func fakeRepo(files map[string]string, binaries ...string) *repo {
	r := &repo{text: map[string]string{}}
	for f, t := range files {
		r.files = append(r.files, f)
		r.text[f] = t
	}
	r.files = append(r.files, binaries...)
	sort.Strings(r.files)
	return r
}

func TestOrphanPages(t *testing.T) {
	r := fakeRepo(map[string]string{
		"mkdocs.yml":            "nav:\n  - Home: index.md\n  - Guide: guides/a.md\nplugins:\n  - redirects:\n      redirect_maps:\n        old.md: index.md\n",
		"docs/index.md":         "See [b](guides/b.md#part) and <a href=\"c.md\">c</a>.",
		"docs/guides/a.md":      "[self](a.md) [up](../d.md?x)",
		"docs/guides/b.md":      "",
		"docs/c.md":             "",
		"docs/d.md":             "",
		"docs/lonely.md":        "[me](lonely.md) [web](https://example.com/lonely.md)",
		"docs/aide/vision.md":   "",
		"docs/guides/self.md":   "[self](./self.md)",
		"README.md":             "[r](docs/fromreadme.md)",
		"docs/fromreadme.md":    "",
		"overrides/home.html":   "<a href='../docs/fromtheme.md'>",
		"docs/fromtheme.md":     "",
		"docs/reference/api.md": "",
	})
	assert.Equal(t, []string{
		"file docs/guides/self.md: a docs page neither in mkdocs.yml nor linked from another page",
		"file docs/lonely.md: a docs page neither in mkdocs.yml nor linked from another page",
		"file docs/reference/api.md: a docs page neither in mkdocs.yml nor linked from another page",
	}, orphanPages(r))
}

// TestBuildOutput: a committed docs site (mkdocs.yml's site_dir, or mkdocs'
// sitemap.xml or search index anywhere) is reported once per directory.
func TestBuildOutput(t *testing.T) {
	assert.Empty(t, buildOutput(fakeRepo(map[string]string{"mkdocs.yml": "site_name: x\n", "docs/index.md": "", "docs/sitemap.md": ""})))
	assert.Equal(t, []string{
		"file site/: committed docs-site build output (mkdocs); docs.yml builds the site from source: git rm -r it and keep it in .gitignore",
	}, buildOutput(fakeRepo(map[string]string{"mkdocs.yml": "site_name: x\n", "site/index.html": "", "site/a/index.html": ""})))
	got := buildOutput(fakeRepo(map[string]string{
		"mkdocs.yml":                   "site_name: x\nsite_dir: public/\n",
		"public/index.html":            "",
		"out/sitemap.xml":              "",
		"old/search/search_index.json": "",
		"site/kept.txt":                "", // not the site_dir any more
	}))
	assert.Len(t, got, 3)
	for i, d := range []string{"file old/:", "file out/:", "file public/:"} {
		assert.True(t, strings.HasPrefix(got[i], d), got[i])
	}
}

func TestOrphanAssets(t *testing.T) {
	r := fakeRepo(map[string]string{
		"docs/approvals.md":                    `<source srcset="../assets/demos/a-dark.webp">`,
		"docs/stylesheets/kardinal.css":        `@font-face { src: url(../assets/fonts/Font.woff2) }`,
		"mkdocs.yml":                           "extra_css:\n  - stylesheets/kardinal.css\n",
		"docs/javascripts/unused.js":           "",
		"hack/docs-station-map/station_map.py": "copy('station-map.css')",
		"docs/stylesheets/station-map.css":     "",
		"web/src/x.ts":                         "unused.js orphan.png",
	}, "docs/assets/demos/a-dark.webp", "docs/assets/fonts/Font.woff2", "docs/assets/orphan.png")
	assert.Equal(t, []string{
		"file docs/assets/orphan.png: a docs asset nothing names",
		"file docs/javascripts/unused.js: a docs asset nothing names",
	}, orphanAssets(r))
}

func TestOrphanScripts(t *testing.T) {
	r := fakeRepo(map[string]string{
		"Makefile":                        "e2e:\n\tbash hack/e2e/run.sh\n\tgo run ./hack/gen-docs\n",
		"hack/e2e/run.sh":                 ". \"$dir/components/argocd.sh\"\nsource lib.sh",
		"hack/e2e/lib.sh":                 "",
		"hack/e2e/components/argocd.sh":   "",
		"hack/e2e/components/orphan.sh":   "",
		"hack/gen-docs/main.go":           "package main",
		"hack/gen-docs/helper.go":         "package main",
		"hack/unused-tool/main.go":        "package main // hack/unused-tool",
		"hack/record/record.mjs":          "import './flows.mjs'",
		"hack/record/flows.mjs":           "",
		".github/workflows/ci.yml":        "run: node hack/record/record.mjs && bash scripts/check.sh",
		"scripts/check.sh":                "",
		"scripts/stale.py":                "# scripts/stale.py: nothing runs me",
		"hack/boilerplate.go.txt":         "",
		"hack/e2e/components/argocd.yaml": "",
		"docs/x.md":                       "",
		"hack/selfref.sh":                 "# usage: hack/selfref.sh",
	})
	assert.Equal(t, []string{
		"file hack/e2e/components/orphan.sh: nothing calls it (no Makefile target, workflow, doc or script names it)",
		"file hack/selfref.sh: nothing calls it (no Makefile target, workflow, doc or script names it)",
		"file hack/unused-tool: nothing calls it (no Makefile target, workflow, doc or script names it)",
		"file scripts/stale.py: nothing calls it (no Makefile target, workflow, doc or script names it)",
	}, orphanScripts(r))
}

func TestReadAllow(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "allow.txt")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}
	allow, err := readAllow(write("# comment\n\ndeadcode example.com/p.F  # a reason\nfile docs/a.txt # license\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"deadcode example.com/p.F": true, "file docs/a.txt": true}, allow)

	for name, body := range map[string]string{
		"no reason":    "deadcode example.com/p.F\n",
		"empty reason": "deadcode example.com/p.F  #  \n",
		"unknown kind": "func example.com/p.F  # why\n",
		"missing name": "file  # why\n",
		"two names":    "file a b  # why\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readAllow(write(body))
			assert.Error(t, err)
		})
	}
	_, err = readAllow(filepath.Join(dir, "missing.txt"))
	assert.Error(t, err)
}

func TestFilter(t *testing.T) {
	found := []string{
		"deadcode p.Allowed: no main or test reaches it",
		"deadcode p.New: no main or test reaches it",
		"file docs/x.png: a docs asset nothing names",
	}
	allow := map[string]bool{"deadcode p.Allowed": true, "deadcode p.Gone": true, "file docs/gone.txt": true}

	assert.Equal(t, []string{
		"deadcode p.Gone: in " + allowFile + " but nothing reports it any more; remove the entry",
		"deadcode p.New: no main or test reaches it",
		"file docs/gone.txt: in " + allowFile + " but nothing reports it any more; remove the entry",
		"file docs/x.png: a docs asset nothing names",
	}, filter(found, allow, map[string]bool{"deadcode": true, "file": true}))

	// With -skip-deadcode, the deadcode entries are not reported stale.
	assert.Equal(t, []string{
		"deadcode p.New: no main or test reaches it",
		"file docs/gone.txt: in " + allowFile + " but nothing reports it any more; remove the entry",
		"file docs/x.png: a docs asset nothing names",
	}, filter(found, allow, map[string]bool{"file": true}))
}

func TestLinkTargets(t *testing.T) {
	assert.Equal(t, []string{"a.md", "b.md", "c.webp", "d.png", "e.html"},
		linkTargets(`[a](a.md#x) [b](b.md "title") <img srcset="c.webp 1x, d.png 2x"> <a href='e.html?q'>`))
}

// TestRepoIsClean runs the file checks on this repository (the deadcode check
// needs the deadcode binary: make hygiene-go).
func TestRepoIsClean(t *testing.T) {
	root := filepath.Join("..", "..")
	r, err := loadRepo(root)
	require.NoError(t, err)
	allow, err := readAllow(filepath.Join(root, allowFile))
	require.NoError(t, err)
	assert.Empty(t, filter(fileFindings(root, r), allow, map[string]bool{"file": true}))
}

// TestBulkyFiles: a committed ELF binary, and a file over maxFileSize, are
// reported; small text files and a large file under the limit are not.
func TestBulkyFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name string, data []byte) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), data, 0o644))
	}
	write("release-gate", append([]byte("\x7fELF"), make([]byte, 100)...))
	write("docs/big.webp", make([]byte, maxFileSize+1))
	write("docs/ok.webp", make([]byte, maxFileSize))
	write("README.md", []byte("hello"))
	r := &repo{files: []string{"README.md", "docs/big.webp", "docs/ok.webp", "release-gate"}}
	got := bulkyFiles(root, r)
	require.Len(t, got, 2, "%v", got)
	assert.True(t, strings.HasPrefix(got[0], "file docs/big.webp: "), got[0])
	assert.True(t, strings.HasPrefix(got[1], "file release-gate: a committed ELF binary"), got[1])
}

// TestUnguardedKube: a package whose tests can reach a call that loads the
// ambient kubeconfig (in the package, or in any package its test binary
// imports) needs the kubetest guard in its test files; one without tests,
// or whose tests reach none, does not.
func TestUnguardedKube(t *testing.T) {
	files := map[string]string{
		"cli/root.go":         "func c() { clientcmd.NewDefaultClientConfigLoadingRules() }",
		"cli/root_test.go":    "func TestX(t *testing.T) {}",
		"ctrl/main.go":        "func main() { cfg := ctrl.GetConfigOrDie(); _ = cfg }",
		"ctrl/main_test.go":   "func TestMain(m *testing.M) { kubetest.Isolate(); os.Exit(m.Run()) }",
		"user/user_test.go":   "func TestY(t *testing.T) { cli.Run() }",
		"guarded/a_test.go":   "func TestMain(m *testing.M) { kubetest.Main(m) }",
		"incluster/x.go":      "func x() { rest.InClusterConfig() }",
		"notests/x.go":        "func x() { rest.InClusterConfig() }",
		"plain/p.go":          "func p() { mgr.GetConfig() }", // a method, not the ambient config
		"plain/p_test.go":     "func TestP(t *testing.T) {}",
		"incluster/x_test.go": "func TestX(t *testing.T) {}",
	}
	read := func(f string) string { return files[f] }
	pkgs := []goPkg{
		{path: "m/cli", dir: "cli", files: []string{"root.go"}, testFiles: []string{"root_test.go"}},
		{path: "m/ctrl", dir: "ctrl", files: []string{"main.go"}, testFiles: []string{"main_test.go"}},
		{path: "m/user", dir: "user", testFiles: []string{"user_test.go"}, testDeps: []string{"fmt", "m/lib", "m/cli"}},
		{path: "m/guarded", dir: "guarded", testFiles: []string{"a_test.go"}, testDeps: []string{"m/cli"}},
		{path: "m/incluster", dir: "incluster", files: []string{"x.go"}, testFiles: []string{"x_test.go"}},
		{path: "m/notests", dir: "notests", files: []string{"x.go"}},
		{path: "m/plain", dir: "plain", files: []string{"p.go"}, testFiles: []string{"p_test.go"}},
	}
	got := unguardedKube(pkgs, read)
	for i, g := range got {
		got[i], _, _ = strings.Cut(g, ":")
	}
	assert.Equal(t, []string{"kubeguard m/cli", "kubeguard m/incluster", "kubeguard m/user"}, got)
	assert.Contains(t, unguardedKube(pkgs, read)[2], "kubeguard m/user: its tests can reach m/cli")
}
