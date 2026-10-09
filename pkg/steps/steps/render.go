// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	sigsyaml "sigs.k8s.io/yaml"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// Render limits. The DRY source and the rendered output live in the
// controller's memory while a render runs, so both are bounded, and a render
// that takes longer than renderTimeout fails the step.
const (
	maxRenderInputBytes  = 64 << 20
	maxRenderInputFiles  = 20000
	maxRenderOutputBytes = 16 << 20
	maxRenderedObjects   = 5000
	renderTimeout        = 60 * time.Second
)

// renderer names the engine that renders an environment path.
type renderer string

const (
	rendererKustomize renderer = "kustomize"
	rendererHelm      renderer = "helm"
)

// detectRenderer picks the engine from the environment directory: a
// kustomization file means kustomize build, a Chart.yaml helm template.
func detectRenderer(dir string) (renderer, error) {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if fileExists(filepath.Join(dir, name)) {
			return rendererKustomize, nil
		}
	}
	if fileExists(filepath.Join(dir, "Chart.yaml")) {
		return rendererHelm, nil
	}
	return "", parentsteps.Permanent(errors.New("layout: branch renders a kustomization (kustomization.yaml) or a " +
		"Helm chart (Chart.yaml) at the environment path; found neither"))
}

func fileExists(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

// loadSourceTree reads the DRY checkout at root into an in-memory file
// system, skipping .git. Symbolic links are not followed: one is an error,
// so a link cannot pull in a file from outside the checkout. The size and
// file count are bounded.
func loadSourceTree(root string) (filesys.FileSystem, error) {
	mem := filesys.MakeFsInMemory()
	var total int64
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != root {
				return filepath.SkipDir
			}
			return mem.MkdirAll(filepath.Join("/", rel))
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return parentsteps.Permanent(fmt.Errorf("symbolic link %s in the DRY source is not supported", filepath.ToSlash(rel)))
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		total += info.Size()
		if files > maxRenderInputFiles || total > maxRenderInputBytes {
			return parentsteps.Permanent(fmt.Errorf("the DRY source is larger than the render limit (%d files, %d MiB)",
				maxRenderInputFiles, maxRenderInputBytes>>20))
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return mem.WriteFile(filepath.Join("/", rel), b)
	})
	if err != nil {
		return nil, err
	}
	return mem, nil
}

// remoteKustomizeRef reports whether a kustomization entry is a remote
// reference (a git repository or URL), which kustomize would fetch by
// running git. Renders use only the DRY checkout.
func remoteKustomizeRef(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@") || strings.Contains(s, "?ref=") ||
		strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "gitlab.com/") || strings.HasPrefix(s, "bitbucket.org/")
}

// checkKustomizations refuses remote resources, components and bases, and
// helmCharts (chart inflation runs the helm binary), in every kustomization
// of the in-memory tree.
func checkKustomizations(mem filesys.FileSystem) error {
	return mem.Walk("/", func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch path.Base(p) {
		case "kustomization.yaml", "kustomization.yml", "Kustomization":
		default:
			return nil
		}
		raw, err := mem.ReadFile(p)
		if err != nil {
			return err
		}
		var k struct {
			Resources  []string      `json:"resources"`
			Components []string      `json:"components"`
			Bases      []string      `json:"bases"`
			HelmCharts []interface{} `json:"helmCharts"`
		}
		if err := sigsyaml.Unmarshal(raw, &k); err != nil {
			return parentsteps.Permanent(fmt.Errorf("parse %s: %w", p, err))
		}
		if len(k.HelmCharts) > 0 {
			return parentsteps.Permanent(fmt.Errorf("%s: helmCharts is not supported by layout: branch; "+
				"put the chart at the environment path instead", p))
		}
		for _, list := range [][]string{k.Resources, k.Components, k.Bases} {
			for _, r := range list {
				if remoteKustomizeRef(r) {
					return parentsteps.Permanent(fmt.Errorf("%s: remote resource %q is not supported by layout: branch; "+
						"vendor it into the repository", p, r))
				}
			}
		}
		return nil
	})
}

// renderKustomize runs kustomize build in process (sigs.k8s.io/kustomize/api)
// on the DRY tree, with plugins disabled and load restrictions on.
func renderKustomize(src, envRel string) ([][]byte, error) {
	mem, err := loadSourceTree(src)
	if err != nil {
		return nil, err
	}
	if err := checkKustomizations(mem); err != nil {
		return nil, err
	}
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	rm, err := k.Run(mem, filepath.Join("/", envRel))
	if err != nil {
		return nil, parentsteps.Permanent(fmt.Errorf("kustomize build %s: %w", filepath.ToSlash(envRel), err))
	}
	var out [][]byte
	for _, r := range rm.Resources() {
		b, err := r.AsYAML()
		if err != nil {
			return nil, fmt.Errorf("kustomize build: encode %s: %w", r.CurId(), err)
		}
		out = append(out, b)
	}
	return out, nil
}

// helmOptions are the release values of a helm render.
type helmOptions struct {
	releaseName string
	namespace   string
	valuesFiles []string
}

// renderHelm runs helm template in process (helm.sh/helm/v4): the chart at
// chartDir with its values.yaml and valuesFiles, offline (no lookup, no
// dependency download: subcharts must be in charts/), with the default
// capabilities. Hooks and NOTES.txt are not part of the output.
func renderHelm(ctx context.Context, chartDir string, opts helmOptions) ([][]byte, error) {
	chrt, err := loader.LoadDir(chartDir)
	if err != nil {
		return nil, parentsteps.Permanent(fmt.Errorf("load chart: %w", err))
	}
	vals := map[string]interface{}{}
	for _, f := range opts.valuesFiles {
		rel, err := confinedRel(f)
		if err != nil {
			return nil, fmt.Errorf("values file: %w", err)
		}
		raw, err := os.ReadFile(filepath.Join(chartDir, rel))
		if err != nil {
			return nil, parentsteps.Permanent(fmt.Errorf("read values file %s: %w", f, err))
		}
		v, err := common.ReadValues(raw)
		if err != nil {
			return nil, parentsteps.Permanent(fmt.Errorf("parse values file %s: %w", f, err))
		}
		vals = chartutil.MergeTables(v.AsMap(), vals)
	}
	if err := chartv2util.ProcessDependencies(chrt, vals); err != nil {
		return nil, parentsteps.Permanent(fmt.Errorf("chart dependencies: %w", err))
	}
	rv, err := chartutil.ToRenderValues(chrt, vals, common.ReleaseOptions{
		Name: opts.releaseName, Namespace: opts.namespace, IsInstall: true,
	}, common.DefaultCapabilities)
	if err != nil {
		return nil, parentsteps.Permanent(fmt.Errorf("chart values: %w", err))
	}
	files, err := engine.Engine{Strict: false}.RenderWithContext(ctx, chrt, rv)
	if err != nil {
		return nil, parentsteps.Permanent(fmt.Errorf("helm template: %w", err))
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out [][]byte
	for _, n := range names {
		if strings.HasSuffix(n, "NOTES.txt") || strings.HasPrefix(path.Base(n), "_") {
			continue
		}
		for _, doc := range splitYAMLDocs(files[n]) {
			if isHook(doc) {
				continue
			}
			out = append(out, doc)
		}
	}
	return out, nil
}

// splitYAMLDocs splits a multi-document stream, dropping empty documents.
func splitYAMLDocs(s string) [][]byte {
	var out [][]byte
	for _, d := range strings.Split("\n"+s, "\n---") {
		d = strings.TrimSpace(d)
		if d == "" || strings.Trim(d, "-\n ") == "" {
			continue
		}
		var obj map[string]interface{}
		if err := sigsyaml.Unmarshal([]byte(d), &obj); err != nil || len(obj) == 0 {
			continue
		}
		out = append(out, []byte(d+"\n"))
	}
	return out
}

// isHook reports whether a rendered document is a Helm hook, which helm
// template leaves out of the manifest too.
func isHook(doc []byte) bool {
	var obj struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	_ = sigsyaml.Unmarshal(doc, &obj)
	_, ok := obj.Metadata.Annotations["helm.sh/hook"]
	return ok
}

// renderedFile is one manifest written to the rendered branch.
type renderedFile struct {
	path    string
	content []byte
}

// manifestFiles turns rendered documents into one file per object:
// <kind>-<name>.yaml at the root for cluster-scoped objects, under
// <namespace>/ for namespaced ones. Two objects that would share a file (the
// same kind and name in two API groups) get the group in the name.
func manifestFiles(docs [][]byte) ([]renderedFile, error) {
	if len(docs) > maxRenderedObjects {
		return nil, parentsteps.Permanent(fmt.Errorf("the render produced %d objects, more than the limit of %d",
			len(docs), maxRenderedObjects))
	}
	type meta struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	var files []renderedFile
	used := map[string]int{}
	var total int
	for _, d := range docs {
		var m meta
		if err := sigsyaml.Unmarshal(d, &m); err != nil {
			return nil, parentsteps.Permanent(fmt.Errorf("rendered object is not YAML: %w", err))
		}
		if m.Kind == "" || m.Metadata.Name == "" {
			return nil, parentsteps.Permanent(errors.New("rendered object has no kind or metadata.name"))
		}
		dir := ""
		if m.Metadata.Namespace != "" {
			dir = fileSafe(m.Metadata.Namespace)
		}
		name := fileSafe(strings.ToLower(m.Kind)) + "-" + fileSafe(m.Metadata.Name)
		if group := strings.Split(m.APIVersion, "/"); len(group) == 2 && used[dirJoin(dir, name+".yaml")] > 0 {
			name += "." + fileSafe(group[0])
		}
		p := dirJoin(dir, name+".yaml")
		if used[p] > 0 {
			return nil, parentsteps.Permanent(fmt.Errorf("two rendered objects map to %s", p))
		}
		used[p]++
		total += len(d)
		if total > maxRenderOutputBytes {
			return nil, parentsteps.Permanent(fmt.Errorf("the rendered manifests are larger than %d MiB", maxRenderOutputBytes>>20))
		}
		files = append(files, renderedFile{path: p, content: d})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func dirJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// fileSafe keeps [a-z0-9._-] and replaces anything else with "_".
func fileSafe(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if out == "" {
		return "_"
	}
	return out
}

// renderWithTimeout runs fn with renderTimeout. kustomize has no
// cancellation: a render that overruns fails the step at once and finishes
// in the background, bounded by the input limits.
func renderWithTimeout(ctx context.Context, fn func(context.Context) ([][]byte, error)) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	type result struct {
		docs [][]byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		docs, err := fn(ctx)
		ch <- result{docs, err}
	}()
	select {
	case r := <-ch:
		return r.docs, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("render did not finish within %s", renderTimeout)
	}
}

// bytesEqual is bytes.Equal, named for the marker comparison.
var bytesEqual = bytes.Equal
