// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hygiene fails on dead code and dead files (make hygiene, CI job hygiene):
//
//   - Go functions no main reaches (deadcode -tags kms_aws,kms_gcp ./cmd/...
//     ./hack/..., the image's tags), and test code no test reaches (deadcode
//     -test -tags e2e,kms_aws,kms_gcp ./...). A function only
//     tests call belongs in an export_test.go or _internal_test.go file of its
//     package, not in the production code.
//   - docs pages that are neither in mkdocs.yml nor linked from another page;
//     files under docs/assets, docs/stylesheets and docs/javascripts that
//     nothing names;
//   - scripts and Go programs under hack/ and scripts/ that nothing calls;
//   - committed build output of the docs site: mkdocs.yml's site_dir (site/
//     by default), or a sitemap.xml or search/search_index.json anywhere.
//     docs.yml builds and deploys the site from source.
//
// hack/hygiene/allow.txt lists the intentional exceptions, each with its
// reason; an entry that no longer matches anything is reported too, so the
// list cannot rot. The web UI's unused files and exports are knip's
// (web/knip.json, npm run knip).
//
//	go run ./hack/hygiene [-deadcode <binary>] [-skip-deadcode]
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// allowFile lists the exceptions: "<kind> <name>  # <reason>" per line.
const allowFile = "hack/hygiene/allow.txt"

// deadcodeFormat prints one "<package path>.<function>" per unreachable function.
const deadcodeFormat = `{{range .Funcs}}{{$.Path}}.{{.Name}}{{"\n"}}{{end}}`

var (
	// mdLink and htmlRef are the link targets of a markdown or HTML file.
	mdLink  = regexp.MustCompile(`\]\(([^)\s]+)`)
	htmlRef = regexp.MustCompile(`(?i)\b(?:href|src|srcset)=["']([^"']+)["']`)
	// mkdocsPage is a page path in mkdocs.yml (nav, redirects).
	mkdocsPage = regexp.MustCompile(`[\w./-]+\.md\b`)
	// script is a file under hack/ or scripts/ that something must call.
	script = regexp.MustCompile(`^(hack|scripts)/.*\.(sh|mjs|mts|js|py)$`)
	// binary is a file whose text is not searched for references.
	binary = regexp.MustCompile(`(?i)\.(png|webp|jpe?g|gif|ico|woff2?|ttf|otf|gz|tgz)$`)
)

// repo is the tracked files of the repository and the text of each text file.
type repo struct {
	files []string
	text  map[string]string
}

func loadRepo(root string) (*repo, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	r := &repo{text: map[string]string{}}
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			continue // deleted in the working tree
		}
		r.files = append(r.files, f)
		if binary.MatchString(f) || strings.HasPrefix(f, "web/dist/") || strings.HasSuffix(f, "package-lock.json") || strings.HasSuffix(f, "go.sum") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		r.text[f] = string(b)
	}
	sort.Strings(r.files)
	return r, nil
}

// mentioned reports whether a text file other than those skip accepts
// contains needle.
func (r *repo) mentioned(needle string, skip func(string) bool, in func(string) bool) bool {
	for f, t := range r.text {
		if skip(f) || (in != nil && !in(f)) {
			continue
		}
		if strings.Contains(t, needle) {
			return true
		}
	}
	return false
}

// siteDir is mkdocs.yml's site_dir.
var siteDir = regexp.MustCompile(`(?m)^site_dir:\s*["']?([^"'\s#]+)`)

// buildOutput reports committed docs-site build output, once per directory:
// the files under mkdocs.yml's site_dir, and any directory holding mkdocs'
// sitemap.xml or search/search_index.json.
func buildOutput(r *repo) []string {
	dir := "site"
	if m := siteDir.FindStringSubmatch(r.text["mkdocs.yml"]); m != nil {
		dir = strings.TrimSuffix(m[1], "/")
	}
	dirs := map[string]bool{}
	for _, f := range r.files {
		switch {
		case strings.HasPrefix(f, dir+"/"):
			dirs[dir+"/"] = true
		case path.Base(f) == "sitemap.xml":
			dirs[path.Dir(f)+"/"] = true
		case strings.HasSuffix(f, "/search/search_index.json") || f == "search/search_index.json":
			dirs[path.Dir(path.Dir(f))+"/"] = true
		}
	}
	var out []string
	for d := range dirs {
		out = append(out, "file "+d+": committed docs-site build output (mkdocs); docs.yml builds the site from source: git rm -r it and keep it in .gitignore")
	}
	sort.Strings(out)
	return out
}

// orphanPages lists docs pages that are neither in mkdocs.yml nor linked from
// another docs page, the theme overrides or the README. docs/aide is not
// published (exclude_docs), and docs/changelog.d holds changelog entries.
func orphanPages(r *repo) []string {
	inMkdocs := map[string]bool{}
	for _, p := range mkdocsPage.FindAllString(r.text["mkdocs.yml"], -1) {
		inMkdocs[path.Join("docs", p)] = true
	}
	linked := map[string]bool{}
	for f, t := range r.text {
		if !strings.HasPrefix(f, "docs/") && !strings.HasPrefix(f, "overrides/") && f != "README.md" {
			continue
		}
		for _, target := range linkTargets(t) {
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if p := path.Clean(path.Join(path.Dir(f), target)); p != f {
				linked[p] = true
			}
		}
	}
	var out []string
	for _, f := range r.files {
		// docs/changelog.d holds changelog entries the changelog page lists
		// (hack/docs-changelog), not pages.
		if !strings.HasPrefix(f, "docs/") || !strings.HasSuffix(f, ".md") || strings.HasPrefix(f, "docs/aide/") ||
			strings.HasPrefix(f, "docs/changelog.d/") {
			continue
		}
		if !inMkdocs[f] && !linked[f] {
			out = append(out, "file "+f+": a docs page neither in mkdocs.yml nor linked from another page")
		}
	}
	return out
}

// linkTargets are the link targets in t without their anchors, each srcset
// candidate separately.
func linkTargets(t string) []string {
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if i := strings.IndexAny(u, "#? "); i >= 0 {
			u = u[:i]
		}
		if u != "" {
			out = append(out, u)
		}
	}
	for _, m := range mdLink.FindAllStringSubmatch(t, -1) {
		add(m[1])
	}
	for _, m := range htmlRef.FindAllStringSubmatch(t, -1) {
		for _, u := range strings.Split(m[1], ",") {
			add(u)
		}
	}
	return out
}

// orphanAssets lists the files under docs/assets, docs/stylesheets and
// docs/javascripts whose name no docs page, theme override, mkdocs.yml or docs
// build hook contains.
func orphanAssets(r *repo) []string {
	site := func(f string) bool {
		return strings.HasPrefix(f, "docs/") || strings.HasPrefix(f, "overrides/") || f == "mkdocs.yml" ||
			strings.HasPrefix(f, "hack/docs-station-map/") || f == "README.md"
	}
	var out []string
	for _, f := range r.files {
		if !strings.HasPrefix(f, "docs/assets/") && !strings.HasPrefix(f, "docs/stylesheets/") && !strings.HasPrefix(f, "docs/javascripts/") {
			continue
		}
		if !r.mentioned(path.Base(f), func(g string) bool { return g == f }, site) {
			out = append(out, "file "+f+": a docs asset nothing names")
		}
	}
	return out
}

// orphanScripts lists the scripts and Go programs under hack/ and scripts/
// that nothing calls: no tracked file names their path, and no other file of
// their top directory (hack/e2e, hack/record-demos, ...) names their file
// name, which is how a script calls its siblings.
func orphanScripts(r *repo) []string {
	var cands []string
	mains := map[string]bool{}
	for _, f := range r.files {
		switch {
		case script.MatchString(f):
			cands = append(cands, f)
		case strings.HasPrefix(f, "hack/") && path.Base(f) == "main.go":
			mains[path.Dir(f)] = true
		}
	}
	for d := range mains {
		cands = append(cands, d)
	}
	sort.Strings(cands)
	var out []string
	for _, c := range cands {
		self := func(f string) bool { return f == c || strings.HasPrefix(f, c+"/") }
		if r.mentioned(c, self, nil) {
			continue
		}
		parts := strings.SplitN(c, "/", 3)
		top := parts[0]
		if len(parts) > 2 {
			top = parts[0] + "/" + parts[1]
		}
		if r.mentioned(path.Base(c), self, func(f string) bool { return strings.HasPrefix(f, top+"/") }) {
			continue
		}
		out = append(out, "file "+c+": nothing calls it (no Makefile target, workflow, doc or script names it)")
	}
	return out
}

// deadcode lists the functions no main reaches (production) and the test
// code no test reaches, as "deadcode <package>.<function>".
func deadcode(root, bin string) ([]string, error) {
	seen := map[string]bool{}
	for _, args := range [][]string{
		// The tags the controller image is built with (the KMS receipt
		// signers, Dockerfile), and e2e for the live suites.
		{"-tags", "kms_aws,kms_gcp", "-f", deadcodeFormat, "./cmd/...", "./hack/..."},
		{"-test", "-tags", "e2e,kms_aws,kms_gcp", "-f", deadcodeFormat, "./..."},
	} {
		cmd := exec.Command(bin, args...)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, stderr.String())
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				seen["deadcode "+l] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k+": no main or test reaches it; remove it, or move a test-only helper into an export_test.go")
	}
	sort.Strings(out)
	return out, nil
}

// readAllow reads the allowlist: "<kind> <name>" keys. Every entry needs a
// reason after "#".
func readAllow(file string) (map[string]bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("open allowlist: %w", err)
	}
	defer func() { _ = f.Close() }()
	allow := map[string]bool{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, reason, ok := strings.Cut(line, "#")
		fields := strings.Fields(entry)
		if !ok || strings.TrimSpace(reason) == "" || len(fields) != 2 || (fields[0] != "deadcode" && fields[0] != "file") {
			return nil, fmt.Errorf("%s:%d: want \"deadcode|file <name>  # <reason>\", got %q", file, n, line)
		}
		allow[fields[0]+" "+fields[1]] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	return allow, nil
}

// filter drops the allowed findings and adds a finding for every allowlist
// entry of a checked kind that matched nothing.
func filter(found []string, allow map[string]bool, kinds map[string]bool) []string {
	matched := map[string]bool{}
	var out []string
	for _, p := range found {
		key, _, _ := strings.Cut(p, ":")
		if allow[key] {
			matched[key] = true
			continue
		}
		out = append(out, p)
	}
	for key := range allow {
		kind, _, _ := strings.Cut(key, " ")
		if kinds[kind] && !matched[key] {
			out = append(out, key+": in "+allowFile+" but nothing reports it any more; remove the entry")
		}
	}
	sort.Strings(out)
	return out
}

func main() {
	root := flag.String("root", ".", "repository root")
	bin := flag.String("deadcode", "deadcode", "deadcode binary (golang.org/x/tools/cmd/deadcode)")
	skipDeadcode := flag.Bool("skip-deadcode", false, "skip the Go dead-code check")
	flag.Parse()

	r, err := loadRepo(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	allow, err := readAllow(filepath.Join(*root, allowFile))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	found := append(append(append(orphanPages(r), orphanAssets(r)...), orphanScripts(r)...), buildOutput(r)...)
	kinds := map[string]bool{"file": true}
	if !*skipDeadcode {
		dc, err := deadcode(*root, *bin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		found = append(found, dc...)
		kinds["deadcode"] = true
	}
	problems := filter(found, allow, kinds)
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "%d dead function(s) or file(s); exceptions go in %s with a reason\n", len(problems), allowFile)
		os.Exit(1)
	}
	fmt.Println("no dead code and no dead files")
}
