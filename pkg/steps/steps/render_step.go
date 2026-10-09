// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	sigsyaml "sigs.k8s.io/yaml"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// RenderManifestsStepName is the step that renders layout: branch.
const RenderManifestsStepName = "render-manifests"

// outputMarkerDigest is the sha256 of the marker render-manifests wrote.
const outputMarkerDigest = "markerDigest"

// renderMarkerPath is the file in the rendered branch that records the last
// render: what it was rendered from and the sha256 of every file it wrote.
const renderMarkerPath = ".kardinal/rendered.yaml"

// renderMarker is the content of renderMarkerPath.
type renderMarker struct {
	Pipeline    string            `json:"pipeline"`
	Namespace   string            `json:"namespace,omitempty"`
	Environment string            `json:"environment"`
	Bundle      string            `json:"bundle,omitempty"`
	DryCommit   string            `json:"dryCommit,omitempty"`
	DryPath     string            `json:"dryPath,omitempty"`
	Renderer    string            `json:"renderer,omitempty"`
	Files       map[string]string `json:"files"`
}

const markerHeader = "# Written by kardinal-promoter for layout: branch. Do not edit: kardinal compares\n" +
	"# the files below with their sha256 to find changes made outside a promotion.\n"

func writeMarker(root *os.Root, m renderMarker) error {
	b, err := sigsyaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode %s: %w", renderMarkerPath, err)
	}
	if err := root.MkdirAll(path.Dir(renderMarkerPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", path.Dir(renderMarkerPath), err)
	}
	if err := root.WriteFile(renderMarkerPath, append([]byte(markerHeader), b...), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", renderMarkerPath, err)
	}
	return nil
}

// readMarker returns the marker of the rendered branch checkout and its raw
// bytes, ok=false when there is none.
func readMarker(root *os.Root) (renderMarker, []byte, bool, error) {
	raw, err := root.ReadFile(renderMarkerPath)
	if errors.Is(err, fs.ErrNotExist) {
		return renderMarker{}, nil, false, nil
	}
	if err != nil {
		return renderMarker{}, nil, false, fmt.Errorf("read %s: %w", renderMarkerPath, err)
	}
	m, err := parseMarker(raw)
	return m, raw, true, err
}

// parseMarker decodes a render marker.
func parseMarker(raw []byte) (renderMarker, error) {
	var m renderMarker
	if err := sigsyaml.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("parse %s: %w", renderMarkerPath, err)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m, nil
}

// renderNamespace is the Pipeline namespace the marker records.
func renderNamespace(state *parentsteps.StepState) string {
	if state.Render != nil {
		return state.Render.Namespace
	}
	return ""
}

// markerOwner returns "" when marker m was written for this Pipeline
// environment, or whose it is. A marker without a namespace was written
// before the namespace was recorded and is matched on the rest.
func markerOwner(m renderMarker, state *parentsteps.StepState) string {
	ns := renderNamespace(state)
	if m.Pipeline == state.PipelineName && m.Environment == state.Environment.Name &&
		(m.Namespace == "" || ns == "" || m.Namespace == ns) {
		return ""
	}
	// Another namespace's Pipeline is not named: its name is not this
	// tenant's to read.
	if m.Namespace != "" && ns != "" && m.Namespace != ns {
		return "it was rendered for a Pipeline in another namespace"
	}
	owner := m.Pipeline
	if m.Namespace != "" {
		owner = m.Namespace + "/" + m.Pipeline
	}
	return fmt.Sprintf("it was rendered for Pipeline %s environment %s", owner, m.Environment)
}

// manifestLike reports whether a file in the rendered branch is one Argo CD
// or Flux would apply (YAML or JSON), outside .git and .kardinal.
func manifestLike(p string) bool {
	if strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, ".kardinal/") {
		return false
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}

// checkoutManifests lists the manifest-like files of the rendered branch
// checkout (manifestLike), by slash path.
func checkoutManifests(root *os.Root) ([]string, error) {
	var out []string
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if manifestLike(p) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list rendered branch: %w", err)
	}
	return out, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// detectDrift compares the rendered branch checkout with its marker and
// returns what changed since the last render: a file kardinal wrote that was
// edited or deleted, and any YAML or JSON file kardinal did not write (Argo
// CD or Flux would apply it with the render). Other files (a CODEOWNERS, a
// README) are not drift. A branch with files but no marker was not written by
// kardinal.
func detectDrift(root *os.Root, marker renderMarker, hasMarker bool, next []renderedFile) ([]string, error) {
	var drift []string
	if !hasMarker {
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			return nil, fmt.Errorf("list rendered branch: %w", err)
		}
		for _, e := range entries {
			if e.Name() != ".git" {
				return []string{"no " + renderMarkerPath + " (the branch was not written by kardinal)"}, nil
			}
		}
		return nil, nil
	}
	for p, want := range marker.Files {
		b, err := root.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			drift = append(drift, p+" deleted")
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", p, err)
		case digest(b) != want:
			drift = append(drift, p+" changed")
		}
	}
	manifests, err := checkoutManifests(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range manifests {
		seen[p] = true
		if _, ours := marker.Files[p]; !ours {
			drift = append(drift, p+" added outside kardinal")
		}
	}
	for _, f := range next {
		if _, ours := marker.Files[f.path]; ours || seen[f.path] {
			continue
		}
		if _, err := root.Lstat(f.path); err == nil {
			drift = append(drift, f.path+" added outside kardinal")
		}
	}
	sort.Strings(drift)
	return drift, nil
}

// renderManifestsStep renders the environment path of the DRY source
// (kustomize build, or helm template for a chart) and writes the plain
// manifests into the rendered branch checkout, one file per object, with the
// render marker. git-commit then commits them with the DRY commit in its
// trailers.
//
// Before writing it checks the branch for drift (detectDrift); with
// render.onDrift fail (the default) a drifted branch fails the step and
// nothing is written. Files of the previous render that the new render does
// not produce are deleted; files kardinal never wrote are kept.
//
// The render runs in process (sigs.k8s.io/kustomize/api, helm.sh/helm/v4): no
// kustomize or helm binary, no plugins, no remote resources, no cluster
// lookups, with the input, output and time limits of render.go.
type renderManifestsStep struct{}

func init() {
	parentsteps.Register(&renderManifestsStep{})
}

func (s *renderManifestsStep) Name() string { return RenderManifestsStepName }

func (s *renderManifestsStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	fail := func(err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, permanentIfEscape(err)
	}
	if os.Getenv(parentsteps.RenderJobEnv) != "1" || state.Render == nil {
		return fail(parentsteps.Permanent(errors.New("render-manifests runs only in the kardinal-render Job, never in the controller")))
	}
	if !layoutBranch(state) {
		return fail(parentsteps.Permanent(errors.New("render-manifests runs only with layout: branch")))
	}
	envRel, err := envSubdir(state)
	if err != nil {
		return fail(err)
	}
	dryRoot := parentsteps.DrySourceDir(state.WorkDir)
	envDir, err := confinedRealPath(dryRoot, envRel)
	if err != nil {
		return fail(err)
	}
	kind, err := detectRenderer(envDir)
	if err != nil {
		return fail(err)
	}
	docs, err := renderWithTimeout(ctx, func(ctx context.Context) ([][]byte, error) {
		if kind == rendererHelm {
			return renderHelm(ctx, dryRoot, envRel, helmRenderOptions(state))
		}
		return renderKustomize(dryRoot, envRel)
	})
	if err != nil {
		return fail(err)
	}
	files, err := manifestFiles(docs)
	if err != nil {
		return fail(err)
	}

	root, err := os.OpenRoot(state.WorkDir)
	if err != nil {
		return fail(fmt.Errorf("open rendered branch checkout: %w", err))
	}
	defer func() { _ = root.Close() }()
	marker, rawMarker, hasMarker, err := readMarker(root)
	if err != nil {
		return fail(parentsteps.Permanent(err))
	}
	if hasMarker {
		// A rendered branch belongs to one Pipeline environment: two that
		// render to the same branch would overwrite each other's render.
		if why := markerOwner(marker, state); why != "" {
			return fail(parentsteps.Permanent(fmt.Errorf("rendered branch %s is not this environment's: %s; "+
				"set render.branch to a branch of its own", state.Git.Branch, why)))
		}
	}
	drift, err := detectDrift(root, marker, hasMarker, files)
	if err != nil {
		return fail(err)
	}
	// The marker itself is anchored: a push that edits files and the marker
	// together is still drift, unless the marker is one kardinal wrote.
	outputs := map[string]string{"renderer": string(kind), "renderedFiles": fmt.Sprint(len(files))}
	// A marker of a render whose result was lost (its Bundle is
	// unconfirmed) is kardinal's too when the files it lists are unchanged.
	if known := state.Render.KnownMarkerDigests; hasMarker && len(known) > 0 && !slices.Contains(known, digest(rawMarker)) {
		if len(drift) == 0 && marker.Bundle != "" && slices.Contains(state.Render.UnconfirmedBundles, marker.Bundle) {
			outputs["markerAdopted"] = marker.Bundle
		} else {
			drift = append([]string{renderMarkerPath + " is not one kardinal wrote (sha256 " + shortSHA(digest(rawMarker)) + ")"}, drift...)
		}
	}
	if len(drift) > 0 {
		summary := strings.Join(firstN(drift, 5), "; ")
		if len(drift) > 5 {
			summary += fmt.Sprintf("; and %d more", len(drift)-5)
		}
		if onDrift(state) != "overwrite" {
			return fail(parentsteps.Permanent(fmt.Errorf("rendered branch %s was changed outside kardinal: %s "+
				"(set render.onDrift: overwrite to render over it, or restore it)", state.Git.Branch, summary)))
		}
		outputs["driftOverwritten"] = summary
	}

	// Delete what the previous render wrote and this one does not (and,
	// overwriting drift, every manifest kardinal did not write), then write
	// the new files and the marker.
	next := map[string]bool{}
	for _, f := range files {
		next[f.path] = true
	}
	stale := make([]string, 0, len(marker.Files))
	for p := range marker.Files {
		stale = append(stale, p)
	}
	if len(drift) > 0 {
		manifests, err := checkoutManifests(root)
		if err != nil {
			return fail(err)
		}
		stale = append(stale, manifests...)
	}
	for _, p := range stale {
		if !next[p] {
			if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fail(fmt.Errorf("remove %s: %w", p, err))
			}
		}
	}
	m := renderMarker{Pipeline: state.PipelineName, Namespace: renderNamespace(state), Environment: state.Environment.Name, Bundle: state.BundleName,
		DryCommit: state.Outputs[outputDryCommit], DryPath: filepath.ToSlash(envRel), Renderer: string(kind),
		Files: map[string]string{}}
	for _, f := range files {
		if dir := path.Dir(f.path); dir != "." {
			if err := root.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
				return fail(fmt.Errorf("create %s: %w", dir, err))
			}
		}
		if err := root.WriteFile(filepath.FromSlash(f.path), f.content, 0o644); err != nil {
			return fail(fmt.Errorf("write %s: %w", f.path, err))
		}
		m.Files[f.path] = digest(f.content)
	}
	if err := writeMarker(root, m); err != nil {
		return fail(err)
	}
	written, err := root.ReadFile(renderMarkerPath)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", renderMarkerPath, err))
	}
	outputs[outputMarkerDigest] = digest(written)
	outputs["renderedObjects"] = fmt.Sprint(len(files))
	msg := fmt.Sprintf("rendered %d objects from %s@%s with %s into %s", len(files), filepath.ToSlash(envRel),
		shortSHA(state.Outputs[outputDryCommit]), kind, state.Git.Branch)
	if s := outputs["driftOverwritten"]; s != "" {
		msg += " (overwrote changes made outside kardinal: " + s + ")"
	}
	return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: msg, Outputs: outputs}, nil
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func onDrift(state *parentsteps.StepState) string {
	if r := state.Environment.Render; r != nil && r.OnDrift != "" {
		return r.OnDrift
	}
	return "fail"
}

// helmRenderOptions are render.helm's settings with their defaults.
func helmRenderOptions(state *parentsteps.StepState) helmOptions {
	o := helmOptions{releaseName: state.PipelineName, namespace: state.Environment.Name}
	if r := state.Environment.Render; r != nil {
		o.nondeterminst = r.AllowNondeterministic
	}
	if r := state.Environment.Render; r != nil && r.Helm != nil {
		if r.Helm.ReleaseName != "" {
			o.releaseName = r.Helm.ReleaseName
		}
		if r.Helm.Namespace != "" {
			o.namespace = r.Helm.Namespace
		}
		o.valuesFiles = append(o.valuesFiles, r.Helm.ValuesFiles...)
	}
	if len(o.valuesFiles) == 0 {
		if h := state.Environment.Update.Helm; h != nil && h.ValuesFile != "" && h.ValuesFile != "values.yaml" {
			o.valuesFiles = []string{h.ValuesFile}
		}
	}
	return o
}
