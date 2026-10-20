// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/unicode/norm"

	"github.com/kardinal-promoter/kardinal-promoter/hack/changelog/fragments"
)

// siteURL is the published docs site. Older changelog sections link to it
// with absolute URLs; release.yml's notes step now turns every relative link
// of the notes into one, for the GitHub release body (#1829).
const siteURL = "https://pnz1990.github.io/kardinal-promoter/"

var (
	// mdLink is a markdown link's target (an optional title after it).
	mdLink = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	// codeSpan is an inline code span, whose text is not a link.
	codeSpan = regexp.MustCompile("`[^`]*`")
	// atxHeading is an ATX heading line.
	atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	// attrID is an attr_list block ({: #id .class}) with an id.
	attrID = regexp.MustCompile(`\{:?\s*[^}]*#([A-Za-z][\w-]*)[^}]*\}\s*$`)
	// htmlID is an id attribute in inline HTML.
	htmlID = regexp.MustCompile(`\bid="([^"]+)"`)
)

// TestChangelogLinksResolve checks every link in docs/changelog.md to a docs
// page: relative links (guides/security.md#user-roles), links to the
// published site (https://pnz1990.github.io/kardinal-promoter/upgrade/#…)
// and links within the changelog (#before-you-upgrade). The page must exist
// in docs/ and not be a redirect, and an anchor must be a heading or id on
// that page. mkdocs --strict checks relative links, but not the site URLs,
// and those broke when pages were split: the v0.10.0 and v0.9.0 notes linked
// to installation/#upgrading-from-v090 after the upgrade guide moved to its
// own page.
func TestChangelogLinksResolve(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	docs := filepath.Join(root, "docs")
	// The page as the docs site shows it: the docs/changelog.d entries too.
	data, err := fragments.Assembled(root)
	require.NoError(t, err)
	redirects := mkdocsRedirects(t, filepath.Join(root, "mkdocs.yml"))
	links := markdownLinks(data)
	require.Greater(t, len(links), 50, "too few links found in docs/changelog.md; is the scan broken?")
	checked := 0
	for _, l := range links {
		problem, ok := docsLinkProblem(docs, "changelog.md", l.target, redirects)
		if !ok {
			continue // not a docs page
		}
		checked++
		assert.Empty(t, problem, "docs/changelog.md:%d: link %s", l.line, l.target)
	}
	require.Greater(t, checked, 50, "too few docs links checked; is the scan broken?")
}

// TestDocsLinkProblem checks the link resolution on a small docs tree.
func TestDocsLinkProblem(t *testing.T) {
	docs := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(docs, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("changelog.md", "# Changelog\n\n## [v1.0.0] — 2026-10-01\n\n### Before you upgrade\n\n#### From v0.9.0\n\n## [v0.9.0]\n\n### Before you upgrade\n")
	write("upgrade.md", "# Upgrade\n\n## Upgrading from v0.10.0\n\n```bash\n# not a heading\n```\n\n<!-- a comment\n## Commented out\n-->\n\n## The `kardinal doctor` check <!-- note -->\n\n## Same\n\n## Same\n\n## Same_1\n\n## Taken\n\n### Custom { #my-id }\n\n<a id=\"html-id\"></a><a id=\"taken\"></a>\n")
	write("index.md", "# Home\n\n## Why kardinal?\n")
	write("guides/security.md", "# Security\n\n## User roles\n")
	write("reference/openapi.json", "{}")
	redirects := map[string]string{"faq.md": "troubleshooting.md"}

	cases := []struct {
		target, problem string
		docsLink        bool
	}{
		{target: "upgrade.md", docsLink: true},
		{target: "upgrade.md#upgrading-from-v0100", docsLink: true},
		{target: "upgrade.md#the-kardinal-doctor-check", docsLink: true},
		{target: "upgrade.md#same_1", docsLink: true},
		{target: "upgrade.md#my-id", docsLink: true},
		{target: "upgrade.md#html-id", docsLink: true},
		{target: "upgrade.md#not-a-heading", docsLink: true, problem: `no heading or id "not-a-heading" in upgrade.md`},
		{target: "upgrade.md#same_2", docsLink: true},
		{target: "upgrade.md#same_1_1", docsLink: true, problem: `no heading or id "same_1_1" in upgrade.md`},
		{target: "upgrade.md#same_3", docsLink: true, problem: `no heading or id "same_3" in upgrade.md`},
		{target: "upgrade.md#taken", docsLink: true},
		{target: "upgrade.md#taken_1", docsLink: true, problem: `no heading or id "taken_1" in upgrade.md`},
		{target: "upgrade.md#commented-out", docsLink: true, problem: `no heading or id "commented-out" in upgrade.md`},
		{target: "http://pnz1990.github.io/kardinal-promoter/upgrade/#taken", docsLink: true},
		{target: "https://pnz1990.github.io/kardinal-promoter", docsLink: true},
		{target: "https://pnz1990.github.io/kardinal-promoter#why-kardinal", docsLink: true},
		{target: "http://example.com/kardinal-promoter/"},
		{target: "guides/security.md#user-roles", docsLink: true},
		{target: "reference/openapi.json", docsLink: true},
		{target: "missing.md", docsLink: true, problem: "no page docs/missing.md"},
		{target: "faq.md", docsLink: true, problem: "faq.md is a redirect to troubleshooting.md; link to that page"},
		{target: "#before-you-upgrade_1", docsLink: true},
		{target: "#from-v090", docsLink: true},
		{target: "#from-v0100", docsLink: true, problem: `no heading or id "from-v0100" in changelog.md`},
		{target: siteURL, docsLink: true},
		{target: siteURL + "#why-kardinal", docsLink: true},
		{target: siteURL + "upgrade/#upgrading-from-v0100", docsLink: true},
		{target: siteURL + "guides/security/#user-roles", docsLink: true},
		{target: siteURL + "installation/#upgrading-from-v090", docsLink: true, problem: "no page docs/installation.md"},
		{target: siteURL + "upgrade/#upgrading-from-v090", docsLink: true, problem: `no heading or id "upgrading-from-v090" in upgrade.md`},
		{target: siteURL + "upgrade", docsLink: true, problem: "a site URL of a page ends in /"},
		{target: "https://github.com/pnz1990/kardinal-promoter/issues/1"},
		{target: "mailto:someone@example.com"},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			problem, ok := docsLinkProblem(docs, "changelog.md", c.target, redirects)
			assert.Equal(t, c.docsLink, ok)
			assert.Equal(t, c.problem, problem)
		})
	}
}

// TestHeadingSlug checks the anchors Python-Markdown's toc extension (the
// docs site's) gives headings.
func TestHeadingSlug(t *testing.T) {
	for heading, want := range map[string]string{
		"From v0.9.0":                     "from-v090",
		"[v0.10.0] — 2026-10-11":          "v0100-2026-10-11",
		"Upgrading from v0.10.0":          "upgrading-from-v0100",
		"The `kardinal doctor` check":     "the-kardinal-doctor-check",
		"Symptom: Pipeline Ready=False":   "symptom-pipeline-readyfalse",
		"The controller's own SCM token":  "the-controllers-own-scm-token",
		"[Unreleased]":                    "unreleased",
		"Waiting for the result: `ship`":  "waiting-for-the-result-ship",
		"**Bold** and [a link](x.md) too": "bold-and-a-link-too",
		"Café  au -- lait":                "cafe-au-lait",
	} {
		assert.Equal(t, want, headingSlug(heading), heading)
	}
}

type mdLinkRef struct {
	line   int
	target string
}

// markdownLinks lists the link targets of a markdown text outside code
// blocks, HTML comments and code spans.
func markdownLinks(text string) []mdLinkRef {
	var out []mdLinkRef
	fence, comment := "", false
	for i, line := range strings.Split(text, "\n") {
		if comment {
			j := strings.Index(line, "-->")
			if j < 0 {
				continue
			}
			comment, line = false, line[j+3:]
		}
		if f := fenceMark(line); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(f, fence):
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		line = htmlComment.ReplaceAllString(line, "")
		if j := strings.Index(line, "<!--"); j >= 0 {
			comment, line = true, line[:j]
		}
		for _, m := range mdLink.FindAllStringSubmatch(codeSpan.ReplaceAllString(line, ""), -1) {
			out = append(out, mdLinkRef{line: i + 1, target: m[1]})
		}
	}
	return out
}

// fenceMark is the ``` or ~~~ run that opens or closes a code block on
// line, or "".
func fenceMark(line string) string {
	s := strings.TrimLeft(line, " ")
	for _, c := range []string{"`", "~"} {
		if strings.HasPrefix(s, c+c+c) {
			return strings.Repeat(c, len(s)-len(strings.TrimLeft(s, c)))
		}
	}
	return ""
}

// docsLinkProblem resolves target, a link on the docs page from (relative to
// docs), and returns what is wrong with it ("" when it resolves). ok is false
// when target is not a link to a docs page (another site, mailto).
func docsLinkProblem(docs, from, target string, redirects map[string]string) (problem string, ok bool) {
	var page, anchor string
	if t, ok := strings.CutPrefix(target, "http://"); ok && strings.HasPrefix("https://"+t, strings.TrimSuffix(siteURL, "/")) {
		target = "https://" + t // the site redirects http to https
	}
	if target == strings.TrimSuffix(siteURL, "/") || strings.HasPrefix(target, strings.TrimSuffix(siteURL, "/")+"#") {
		target = siteURL + strings.TrimPrefix(target, strings.TrimSuffix(siteURL, "/"))
	}
	switch {
	case strings.HasPrefix(target, siteURL):
		rest := strings.TrimPrefix(target, siteURL)
		rest, anchor, _ = strings.Cut(rest, "#")
		switch {
		case rest == "":
			page = "index.md"
		case strings.HasSuffix(rest, "/"):
			page = strings.TrimSuffix(rest, "/") + ".md"
			if _, err := os.Stat(filepath.Join(docs, page)); err != nil {
				if _, err := os.Stat(filepath.Join(docs, strings.TrimSuffix(rest, "/"), "index.md")); err == nil {
					page = path.Join(strings.TrimSuffix(rest, "/"), "index.md")
				}
			}
		case path.Ext(rest) != "" && path.Ext(rest) != ".md":
			page = rest // a file the site serves as it is (reference/openapi.json)
		default:
			return "a site URL of a page ends in /", true
		}
	case strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:"):
		return "", false
	case strings.HasPrefix(target, "#"):
		page, anchor = from, strings.TrimPrefix(target, "#")
	default:
		page, anchor, _ = strings.Cut(target, "#")
		page = path.Clean(path.Join(path.Dir(from), page))
	}
	if to, ok := redirects[page]; ok {
		return fmt.Sprintf("%s is a redirect to %s; link to that page", page, to), true
	}
	data, err := os.ReadFile(filepath.Join(docs, filepath.FromSlash(page)))
	if err != nil {
		return "no page docs/" + page, true
	}
	if anchor == "" || path.Ext(page) != ".md" {
		return "", true
	}
	if !memoAnchors(string(data))[anchor] {
		return fmt.Sprintf("no heading or id %q in %s", anchor, page), true
	}
	return "", true
}

// pageAnchors are the anchors of a markdown page: attr_list ids, inline HTML
// ids, and the ids the toc extension gives the other headings. As in
// Python-Markdown, the attr_list ids are taken first (inline HTML is not
// parsed, so its ids take nothing), and a heading id that is taken (or empty)
// gets _1, or its _N suffix raised by one, until it is free. Code blocks and
// HTML comments are skipped.
func pageAnchors(text string) map[string]bool {
	used, html := map[string]bool{}, []string{}
	var titles []string
	for _, line := range contentLines(text) {
		// Each regexp only on a line it can match: every link to a page
		// reads its anchors (#2292).
		if strings.Contains(line, `id="`) {
			for _, m := range htmlID.FindAllStringSubmatch(line, -1) {
				html = append(html, m[1])
			}
		}
		var m []string
		if strings.HasPrefix(line, "#") {
			m = atxHeading.FindStringSubmatch(line)
		}
		if m == nil {
			if !strings.Contains(line, "{") {
				continue
			}
			if a := attrID.FindStringSubmatch(line); a != nil {
				used[a[1]] = true
			}
			continue
		}
		if a := attrID.FindStringSubmatch(m[2]); a != nil {
			used[a[1]] = true
			continue
		}
		titles = append(titles, m[2])
	}
	for _, title := range titles {
		used[uniqueID(headingSlug(title), used)] = true
	}
	for _, id := range html {
		used[id] = true
	}
	return used
}

// anchorsMemo holds pageAnchors of each page text read: a page's links to
// one page share them (#2292).
var anchorsMemo sync.Map

// memoAnchors is pageAnchors(text), computed once per text.
func memoAnchors(text string) map[string]bool {
	if v, ok := anchorsMemo.Load(text); ok {
		return v.(map[string]bool)
	}
	a := pageAnchors(text)
	anchorsMemo.Store(text, a)
	return a
}

// idCount is an id with a _N suffix.
var idCount = regexp.MustCompile(`^(.*)_([0-9]+)$`)

// uniqueID is Python-Markdown's toc unique(): id, or while it is taken or
// empty, id with _1 added or its _N raised by one.
func uniqueID(id string, used map[string]bool) string {
	for used[id] || id == "" {
		if m := idCount.FindStringSubmatch(id); m != nil {
			n, _ := strconv.Atoi(m[2])
			id = fmt.Sprintf("%s_%d", m[1], n+1)
		} else {
			id += "_1"
		}
	}
	return id
}

// contentLines are the lines of a markdown text outside fenced code blocks
// and HTML comments, which render no heading or link.
func contentLines(text string) []string {
	var out []string
	fence, comment := "", false
	for _, line := range strings.Split(text, "\n") {
		if comment {
			if i := strings.Index(line, "-->"); i >= 0 {
				comment, line = false, line[i+3:]
			} else {
				continue
			}
		}
		if fence == "" {
			if f := fenceMark(line); f != "" {
				fence = f
				continue
			}
			if strings.Contains(line, "<!--") {
				line = htmlComment.ReplaceAllString(line, "")
			}
			if i := strings.Index(line, "<!--"); i >= 0 {
				comment, line = true, line[:i]
			}
			out = append(out, line)
			continue
		}
		if f := fenceMark(line); f != "" && strings.HasPrefix(f, fence) {
			fence = ""
		}
	}
	return out
}

// htmlComment is an HTML comment on one line.
var htmlComment = regexp.MustCompile(`<!--.*?-->`)

var (
	mdLinkText = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag    = regexp.MustCompile(`<[^>]+>`)
	slugDrop   = regexp.MustCompile(`[^\w\s-]`)
	slugJoin   = regexp.MustCompile(`[-\s]+`)
)

// headingSlug is the id Python-Markdown's toc extension gives a heading: its
// text (links as their text, no markup), NFKD-folded to ASCII, with every
// character but word characters, spaces and hyphens dropped, lower case,
// and runs of spaces and hyphens joined by one hyphen.
func headingSlug(heading string) string {
	s := mdLinkText.ReplaceAllString(heading, "$1")
	s = htmlTag.ReplaceAllString(s, "")
	s = strings.NewReplacer("`", "", "**", "", "__", "").Replace(s)
	var ascii strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if r < unicode.MaxASCII {
			ascii.WriteRune(r)
		}
	}
	s = strings.ToLower(strings.TrimSpace(slugDrop.ReplaceAllString(ascii.String(), "")))
	return slugJoin.ReplaceAllString(s, "-")
}

// mkdocsRedirects reads the redirect_maps of mkdocs.yml's redirects plugin:
// old page to the page it redirects to.
func mkdocsRedirects(t *testing.T, mkdocs string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(mkdocs)
	require.NoError(t, err)
	out := map[string]string{}
	in, indent := false, 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		lead := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case trimmed == "redirect_maps:":
			in, indent = true, lead
		case in && (trimmed == "" || lead <= indent):
			in = false
		case in:
			from, to, ok := strings.Cut(trimmed, ":")
			if ok {
				out[strings.TrimSpace(from)] = strings.TrimSpace(to)
			}
		}
	}
	require.NotEmpty(t, out, "mkdocs.yml has no redirect_maps; is the parse broken?")
	return out
}
