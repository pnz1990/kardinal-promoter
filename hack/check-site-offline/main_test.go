// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProblems(t *testing.T) {
	for _, tc := range []struct {
		name, file, body string
		want             int
	}{
		{"local script", "a.html", `<script src="../assets/vendor/mermaid/mermaid.min.js"></script>`, 0},
		{"external script", "a.html", `<script src="https://unpkg.com/mermaid@11/dist/mermaid.min.js"></script>`, 1},
		{"unquoted external script", "a.html", `<script src=https://cdn.example/x.js></script>`, 1},
		{"protocol-relative script", "a.html", `<script src="//cdn.example/x.js"></script>`, 1},
		{"google fonts stylesheet", "a.html", `<link rel="stylesheet" href="https://fonts.googleapis.com/css?family=Inter">`, 1},
		{"preconnect", "a.html", `<link rel=preconnect href=https://fonts.gstatic.com>`, 1},
		{"external link to a page", "a.html", `<link rel="canonical" href="https://pnz1990.github.io/kardinal-promoter/">`, 0},
		{"anchor to github", "a.html", `<a href="https://github.com/pnz1990/kardinal-promoter">repo</a>`, 0},
		{"inline style url", "a.html", `<div style="background:url(https://x.example/a.png)"></div>`, 1},
		{"css font", "a.css", `@font-face{src:url("https://fonts.gstatic.com/a.woff2")}`, 1},
		{"css import", "a.css", `@import "https://x.example/a.css";`, 1},
		{"local css font", "a.css", `@font-face{src:url("../assets/fonts/a.woff2")}`, 0},
		{"mermaid with the local script", "a.html", `<pre class="mermaid"><code>graph</code></pre><script src="../assets/vendor/mermaid/mermaid-11.17.2.min.js"></script>`, 0},
		{"mermaid without it", "a.html", `<pre class=mermaid><code>graph</code></pre>`, 1},
		{"preload", "a.html", `<link rel="preload" href="https://cdn.example/f.woff2" as="font">`, 1},
		{"modulepreload", "a.html", `<link rel=modulepreload href=https://cdn.example/m.js>`, 1},
		{"dns-prefetch", "a.html", `<link rel="dns-prefetch" href="//cdn.example">`, 1},
		{"div mermaid without the script", "a.html", `<div class="mermaid">graph LR</div>`, 1},
		{"@import in a style element", "a.html", `<style>@import url("https://fonts.googleapis.com/css2?family=Inter");</style>`, 1},
		{"a script that calls api.github.com", "app.js", `fetch("https://api.github.com/repos/pnz1990/kardinal-promoter")`, 0},
		{"javascript is not scanned", "bundle.js", `fetch("https://unpkg.com/mermaid@11/dist/mermaid.min.js")`, 0},
	} {
		assert.Len(t, problems(tc.file, tc.body), tc.want, tc.name)
	}
}

func TestCheckMissingLocalScript(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "concepts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "assets", "here.js"), []byte("1"), 0o644))
	page := `<script src="../assets/here.js"></script><script src="/kardinal-promoter/assets/here.js"></script>` +
		`<script src="../assets/vendor/mermaid/mermaid.min.js"></script>`
	require.NoError(t, os.WriteFile(filepath.Join(root, "concepts", "index.html"), []byte(page), 0o644))
	found, err := check(root)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Contains(t, found[0], "mermaid.min.js is not in the site")
}
