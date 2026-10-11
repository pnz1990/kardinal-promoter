// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// check-site-offline fails when the built docs site loads a script, a
// stylesheet or a font from another host, or has a Mermaid diagram on a page
// without the self-hosted Mermaid script (Material would fetch it from
// unpkg.com at run time), or loads a local script the site does not have. The site self-hosts its fonts and
// Mermaid, so a reader's browser contacts only the site (and api.github.com,
// which the repository widget calls from JavaScript, not through a tag).
//
//	go run ./hack/check-site-offline site/
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// <script src="https://…">, unquoted as minify writes it too.
	scriptSrc = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc=["']?(https?:)?//[^"'\s>]+`)
	// <link rel=stylesheet|preload|modulepreload|preconnect href="https://…">.
	linkHref = regexp.MustCompile(`(?i)<link\b[^>]*>`)
	external = regexp.MustCompile(`(?i)\bhref=["']?(https?:)?//`)
	assetRel = regexp.MustCompile(`(?i)\brel=["']?(stylesheet|preload|modulepreload|preconnect|dns-prefetch)\b`)
	// A page with a Mermaid diagram, and the local Mermaid script it needs:
	// without one, Material loads Mermaid from unpkg.com at run time.
	mermaidBlock  = regexp.MustCompile(`(?i)<(pre|div)\b[^>]*\bclass=["']?mermaid\b`)
	mermaidScript = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc=["']?[^"'\s>:]*mermaid[^"'\s>]*\.js`)
	// Every script source, to check that a local one exists in the site.
	anyScript = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc=["']?([^"'\s>]+)`)
	// url(https://…) and @import "https://…" in CSS.
	cssURL = regexp.MustCompile(`(?i)(url\(\s*["']?|@import\s+["'])(https?:)?//`)
)

// problems lists the external asset references in one file.
func problems(name string, body string) []string {
	var out []string
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		for _, m := range scriptSrc.FindAllString(body, -1) {
			out = append(out, name+": external script "+m)
		}
		for _, m := range linkHref.FindAllString(body, -1) {
			if external.MatchString(m) && assetRel.MatchString(m) {
				out = append(out, name+": external asset "+m)
			}
		}
		for _, m := range cssURL.FindAllString(body, -1) {
			out = append(out, name+": external CSS URL "+m)
		}
		if mermaidBlock.MatchString(body) && !mermaidScript.MatchString(body) {
			out = append(out, name+": a Mermaid diagram without the self-hosted Mermaid script (Material would load it from unpkg.com)")
		}
	case ".css":
		for _, m := range cssURL.FindAllString(body, -1) {
			out = append(out, name+": external CSS URL "+m)
		}
	}
	return out
}

// missingScripts lists the local scripts an HTML page loads that are not in
// the site (a vendored file that was not committed, say).
func missingScripts(root, rel, body string) []string {
	var out []string
	for _, m := range anyScript.FindAllStringSubmatch(body, -1) {
		src := m[1]
		if strings.Contains(src, "//") || strings.HasPrefix(src, "data:") {
			continue
		}
		src, _, _ = strings.Cut(src, "?")
		src, _, _ = strings.Cut(src, "#")
		candidates := []string{filepath.Join(root, filepath.Dir(rel), filepath.FromSlash(src))}
		if strings.HasPrefix(src, "/") {
			// A root-relative path: the site is served at its site_url path
			// (/kardinal-promoter/ on GitHub Pages), so also try without the
			// first segment.
			p := strings.TrimPrefix(src, "/")
			candidates = []string{filepath.Join(root, filepath.FromSlash(p))}
			if _, rest, ok := strings.Cut(p, "/"); ok {
				candidates = append(candidates, filepath.Join(root, filepath.FromSlash(rest)))
			}
		}
		found := false
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				found = true
			}
		}
		if !found {
			out = append(out, rel+": the script "+m[1]+" is not in the site")
		}
	}
	return out
}

func check(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, problems(rel, string(b))...)
		if strings.EqualFold(filepath.Ext(rel), ".html") {
			out = append(out, missingScripts(root, rel, string(b))...)
		}
		return nil
	})
	return out, err
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: check-site-offline <site dir>")
		os.Exit(2)
	}
	found, err := check(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, p := range found {
		fmt.Println(p)
	}
	if len(found) > 0 {
		fmt.Fprintf(os.Stderr, "%d external script, stylesheet or font reference(s): self-host them\n", len(found))
		os.Exit(1)
	}
	fmt.Println("the site loads no external scripts, stylesheets or fonts")
}
