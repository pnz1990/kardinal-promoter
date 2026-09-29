// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kardinalDemoFiles is the file list of github.com/pnz1990/kardinal-demo at main.
// Refresh it (read-only) with:
//
//	gh api 'repos/pnz1990/kardinal-demo/git/trees/main?recursive=1' --jq '.tree[] | select(.type=="blob") | .path'
var kardinalDemoFiles = []string{
	"environments/prod/deployment.yaml",
	"environments/prod/kustomization.yaml",
	"environments/test/deployment.yaml",
	"environments/test/kustomization.yaml",
	"environments/uat/deployment.yaml",
	"environments/uat/kustomization.yaml",
}

const (
	kardinalDemoRepo     = "github.com/pnz1990/kardinal-demo"
	kardinalPromoterRepo = "github.com/pnz1990/kardinal-promoter"
	// placeholderOwner marks a repository the reader must replace. Examples
	// that need environments kardinal-demo lacks use it.
	placeholderOwner = "github.com/myorg/"
)

// repoTree answers path questions about one repository.
type repoTree interface {
	// matches returns the directories and files that match a path.Match pattern.
	matches(pattern string) []string
}

type fileList struct{ entries map[string]bool }

func newFileList(files []string) fileList {
	fl := fileList{entries: map[string]bool{}}
	for _, f := range files {
		fl.entries[f] = true
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			fl.entries[d] = true
		}
	}
	return fl
}

func (fl fileList) matches(pattern string) []string {
	var out []string
	for e := range fl.entries {
		if ok, _ := path.Match(pattern, e); ok {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

type localTree struct{ root string }

func (lt localTree) matches(pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(lt.root, filepath.FromSlash(pattern)))
	out := make([]string, 0, len(m))
	for _, p := range m {
		rel, _ := filepath.Rel(lt.root, p)
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func normalizeRepo(url string) string {
	u := strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	for _, p := range []string{"https://", "http://", "ssh://git@", "git@"} {
		u = strings.TrimPrefix(u, p)
	}
	return strings.Replace(u, "github.com:", "github.com/", 1)
}

func normalizePath(p string) string {
	p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
	if p == "" {
		return "."
	}
	return p
}

// pathRef is one "this path exists in this repository" claim in a manifest.
type pathRef struct {
	where, repo, path string
}

// problem returns why the claim does not hold, or "". Placeholder
// repositories are not checked.
func (r pathRef) problem(trees map[string]repoTree) string {
	repo := normalizeRepo(r.repo)
	if strings.HasPrefix(repo, placeholderOwner) {
		return ""
	}
	tree, ok := trees[repo]
	if !ok {
		return fmt.Sprintf("%s: repository %s is neither kardinal-demo, this repository, nor a %s placeholder",
			r.where, r.repo, placeholderOwner)
	}
	p := normalizePath(r.path)
	if p != "." && len(tree.matches(p)) == 0 {
		return fmt.Sprintf("%s: %s does not exist in %s", r.where, p, repo)
	}
	return ""
}

var templateVar = regexp.MustCompile(`\{\{\s*([\w.]+)\s*\}\}`)

// expand substitutes ApplicationSet {{var}} parameters.
func expand(s string, params map[string]string) (string, error) {
	var missing []string
	out := templateVar.ReplaceAllStringFunc(s, func(m string) string {
		key := templateVar.FindStringSubmatch(m)[1]
		v, ok := params[key]
		if !ok {
			missing = append(missing, key)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unresolved ApplicationSet parameters %v in %q", missing, s)
	}
	return out, nil
}

// sources returns spec.source and spec.sources of an Argo CD Application spec.
func sources(spec interface{}) []map[string]interface{} {
	out := list(get(spec, "sources"))
	if s, ok := get(spec, "source").(map[string]interface{}); ok {
		out = append(out, s)
	}
	return out
}

// appSourceRefs returns the path claims of one Argo CD Application spec.
// A valueFiles entry "$<ref>/<file>" is resolved against the source whose
// ref is <ref>.
func appSourceRefs(where string, spec interface{}) []pathRef {
	srcs := sources(spec)
	refs := map[string]string{}
	for _, s := range srcs {
		if ref := str(s, "ref"); ref != "" {
			refs[ref] = str(s, "repoURL")
		}
	}
	var out []pathRef
	for _, s := range srcs {
		repo := str(s, "repoURL")
		if str(s, "chart") != "" {
			continue // a Helm repository chart, not a git path
		}
		if p := str(s, "path"); p != "" {
			out = append(out, pathRef{where: where, repo: repo, path: p})
		}
		vfs, _ := get(s, "helm", "valueFiles").([]interface{})
		for _, v := range vfs {
			vf := fmt.Sprint(v)
			if strings.HasPrefix(vf, "$") {
				name, file, _ := strings.Cut(strings.TrimPrefix(vf, "$"), "/")
				refRepo, ok := refs[name]
				if !ok {
					out = append(out, pathRef{where: where, repo: "unresolved $" + name, path: file})
					continue
				}
				out = append(out, pathRef{where: where, repo: refRepo, path: file})
			} else {
				out = append(out, pathRef{where: where, repo: repo, path: path.Join(str(s, "path"), vf)})
			}
		}
	}
	return out
}

// appSetParams returns one parameter set per generated Application.
func appSetParams(t *testing.T, where string, spec interface{}, trees map[string]repoTree) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, g := range list(get(spec, "generators")) {
		for _, el := range list(get(g, "list", "elements")) {
			params := map[string]string{}
			for k, v := range el {
				params[k] = fmt.Sprint(v)
			}
			out = append(out, params)
		}
		if git, ok := g["git"].(map[string]interface{}); ok {
			repo := str(git, "repoURL")
			for _, dir := range list(git["directories"]) {
				pattern := normalizePath(str(dir, "path"))
				ref := pathRef{where: where + " (git generator)", repo: repo, path: pattern}
				assert.Empty(t, ref.problem(trees))
				tree, ok := trees[normalizeRepo(repo)]
				if !ok {
					continue
				}
				for _, m := range tree.matches(pattern) {
					out = append(out, map[string]string{"path": m, "path.basename": path.Base(m)})
				}
			}
		}
	}
	return out
}

// pathRefs collects the path claims in every example and demo manifest.
func pathRefs(t *testing.T, docs []doc, trees map[string]repoTree) []pathRef {
	t.Helper()
	var refs []pathRef
	fluxRepos := map[string]string{} // "<source>/<name>" -> url
	for _, d := range docs {
		if str(d.obj, "kind") == "GitRepository" && strings.HasPrefix(str(d.obj, "apiVersion"), "source.toolkit.fluxcd.io/") {
			fluxRepos[d.source+"/"+str(d.obj, "metadata", "name")] = str(d.obj, "spec", "url")
		}
	}
	for _, d := range docs {
		where := d.String()
		apiVersion, kind := str(d.obj, "apiVersion"), str(d.obj, "kind")
		switch {
		case strings.HasPrefix(apiVersion, "kardinal.io/") && kind == "Pipeline":
			repo := str(d.obj, "spec", "git", "url")
			if str(d.obj, "spec", "git", "layout") == "branch" {
				continue // environment paths are read from per-environment branches
			}
			for _, env := range list(get(d.obj, "spec", "environments")) {
				p := str(env, "path")
				if p == "" {
					p = "environments/" + str(env, "name")
				}
				refs = append(refs, pathRef{where: where + " env " + str(env, "name"), repo: repo, path: p})
			}
		case strings.HasPrefix(apiVersion, "kardinal.io/") && kind == "Subscription" && str(d.obj, "spec", "type") == "git":
			glob := str(d.obj, "spec", "git", "pathGlob")
			prefix := glob
			if i := strings.IndexAny(glob, "*?["); i >= 0 {
				prefix = path.Dir(glob[:i] + "x")
			}
			refs = append(refs, pathRef{where: where, repo: str(d.obj, "spec", "git", "repoURL"), path: prefix})
		case strings.HasPrefix(apiVersion, "argoproj.io/") && kind == "Application":
			refs = append(refs, appSourceRefs(where, get(d.obj, "spec"))...)
		case strings.HasPrefix(apiVersion, "argoproj.io/") && kind == "ApplicationSet":
			params := appSetParams(t, where, get(d.obj, "spec"), trees)
			assert.NotEmpty(t, params, "%s: the generators produce no Applications", where)
			for _, p := range params {
				for _, ref := range appSourceRefs(where, get(d.obj, "spec", "template", "spec")) {
					for _, field := range []*string{&ref.repo, &ref.path} {
						v, err := expand(*field, p)
						if assert.NoError(t, err, where) {
							*field = v
						}
					}
					refs = append(refs, ref)
				}
			}
		case strings.HasPrefix(apiVersion, "kustomize.toolkit.fluxcd.io/") && kind == "Kustomization":
			name := str(d.obj, "spec", "sourceRef", "name")
			repo, ok := fluxRepos[d.source+"/"+name]
			if !assert.True(t, ok, "%s: GitRepository %q is not in the same file", where, name) {
				continue
			}
			refs = append(refs, pathRef{where: where, repo: repo, path: str(d.obj, "spec", "path")})
		}
	}
	return refs
}

func repoTrees(t *testing.T) map[string]repoTree {
	t.Helper()
	return map[string]repoTree{
		kardinalDemoRepo:     newFileList(kardinalDemoFiles),
		kardinalPromoterRepo: localTree{root: repoRoot(t)},
	}
}

// TestExamplePathsExist checks that every path a Pipeline, Subscription,
// Argo CD Application or ApplicationSet, or Flux Kustomization names exists in
// the repository it points at.
func TestExamplePathsExist(t *testing.T) {
	trees := repoTrees(t)
	refs := pathRefs(t, allDocs(t), trees)
	require.Greater(t, len(refs), 20, "too few path references found; is the walk broken?")
	for _, r := range refs {
		assert.Empty(t, r.problem(trees))
	}
}

func TestPathCheckCatchesKnownMistakes(t *testing.T) {
	trees := repoTrees(t)
	tests := []struct {
		name string
		ref  pathRef
		want bool
	}{
		{"kardinal-demo overlay exists", pathRef{repo: "https://github.com/pnz1990/kardinal-demo", path: "./environments/uat"}, true},
		{"kardinal-demo has no staging overlay", pathRef{repo: "https://github.com/pnz1990/kardinal-demo", path: "environments/staging"}, false},
		{"kardinal-demo has no teams folder", pathRef{repo: "https://github.com/pnz1990/kardinal-demo.git", path: "teams/*"}, false},
		{"local chart exists", pathRef{repo: "https://github.com/pnz1990/kardinal-promoter", path: "examples/multi-tenant/chart"}, true},
		{"placeholder repo is skipped", pathRef{repo: "https://github.com/myorg/gitops-repo", path: "anything"}, true},
		{"unknown repo fails", pathRef{repo: "https://github.com/someone/else", path: "x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.ref.problem(trees) == "", tt.ref.problem(trees))
		})
	}
}

// TestExampleHealthTargetsExist checks, per example directory, that the
// Argo CD Applications and Flux Kustomizations the directory ships have the
// names the health adapters look up: <pipeline>-<env> in argocd or
// flux-system.
func TestExampleHealthTargetsExist(t *testing.T) {
	type target struct{ kind, namespace, name string }
	group := func(source string) string {
		source = strings.TrimPrefix(source, "helm template ")
		parts := strings.SplitN(source, "/", 3)
		if parts[0] == "examples" && len(parts) > 1 {
			return "examples/" + parts[1]
		}
		return parts[0]
	}
	have := map[string]map[target]bool{}
	want := map[string][]target{}
	add := func(g string, tg target) {
		if have[g] == nil {
			have[g] = map[target]bool{}
		}
		have[g][tg] = true
	}
	trees := repoTrees(t)
	for _, d := range allDocs(t) {
		g := group(d.source)
		apiVersion, kind := str(d.obj, "apiVersion"), str(d.obj, "kind")
		ns := str(d.obj, "metadata", "namespace")
		switch {
		case strings.HasPrefix(apiVersion, "argoproj.io/") && kind == "Application":
			add(g, target{"Application", ns, str(d.obj, "metadata", "name")})
		case strings.HasPrefix(apiVersion, "argoproj.io/") && kind == "ApplicationSet":
			for _, p := range appSetParams(t, d.String(), get(d.obj, "spec"), trees) {
				name, err := expand(str(d.obj, "spec", "template", "metadata", "name"), p)
				require.NoError(t, err, d.String())
				add(g, target{"Application", ns, name})
			}
		case strings.HasPrefix(apiVersion, "kustomize.toolkit.fluxcd.io/") && kind == "Kustomization":
			add(g, target{"Kustomization", ns, str(d.obj, "metadata", "name")})
		case strings.HasPrefix(apiVersion, "kardinal.io/") && kind == "Pipeline":
			pipeline := str(d.obj, "metadata", "name")
			for _, env := range list(get(d.obj, "spec", "environments")) {
				name := pipeline + "-" + str(env, "name")
				switch str(env, "health", "type") {
				case "argocd":
					want[g] = append(want[g], target{"Application", "argocd", name})
				case "flux":
					want[g] = append(want[g], target{"Kustomization", "flux-system", name})
				}
			}
		}
	}
	var checked int
	for g, targets := range want {
		for _, tg := range targets {
			shipsKind := false
			for h := range have[g] {
				shipsKind = shipsKind || h.kind == tg.kind
			}
			if !shipsKind {
				continue // the directory relies on objects it does not ship
			}
			checked++
			assert.True(t, have[g][tg], "%s: health check looks up %s %s/%s, which the directory does not create",
				g, tg.kind, tg.namespace, tg.name)
		}
	}
	assert.Greater(t, checked, 5, "too few health targets checked; is the walk broken?")
}
