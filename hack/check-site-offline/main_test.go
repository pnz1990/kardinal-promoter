// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		{"javascript is not scanned", "bundle.js", `fetch("https://unpkg.com/mermaid@11/dist/mermaid.min.js")`, 0},
	} {
		assert.Len(t, problems(tc.file, tc.body), tc.want, tc.name)
	}
}
