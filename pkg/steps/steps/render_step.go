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
	"sort"
	"strings"

	sigsyaml "sigs.k8s.io/yaml"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// RenderManifestsStepName is the step that renders layout: branch.
const RenderManifestsStepName = "render-manifests"

// renderMarkerPath is the file in the rendered branch that records the last
// render: what it was rendered from and the sha256 of every file it wrote.
const renderMarkerPath = ".kardinal/rendered.yaml"

// renderMarker is the content of renderMarkerPath.
type renderMarker struct {
	Pipeline    string            `json:"pipeline"`
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

// readMarker returns the marker of the rendered branch checkout, ok=false
// when there is none.
func readMarker(root *os.Root) (renderMarker, bool, error) {
	var m renderMarker
	raw, err := root.ReadFile(renderMarkerPath)
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("read %s: %w", renderMarkerPath, err)
	}
	if err := sigsyaml.Unmarshal(raw, &m); err != nil {
		return m, true, fmt.Errorf("parse %s: %w", renderMarkerPath, err)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m, true, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// detectDrift compares the rendered branch checkout with its marker and
// returns what changed since the last render: a file kardinal wrote that was
// edited or deleted, or a file kardinal is about to write that someone else
// added. Files kardinal never wrote (a CODEOWNERS, a README) are not drift.
// A branch with files but no marker was not written by kardinal.
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
	for _, f := range next {
		if _, ours := marker.Files[f.path]; ours {
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
			return renderHelm(ctx, envDir, helmRenderOptions(state))
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
	marker, hasMarker, err := readMarker(root)
	if err != nil {
		return fail(parentsteps.Permanent(err))
	}
	drift, err := detectDrift(root, marker, hasMarker, files)
	if err != nil {
		return fail(err)
	}
	outputs := map[string]string{"renderer": string(kind), "renderedFiles": fmt.Sprint(len(files))}
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

	// Delete what the previous render wrote and this one does not, then
	// write the new files and the marker.
	next := map[string]bool{}
	for _, f := range files {
		next[f.path] = true
	}
	for p := range marker.Files {
		if !next[p] {
			if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fail(fmt.Errorf("remove %s: %w", p, err))
			}
		}
	}
	m := renderMarker{Pipeline: state.PipelineName, Environment: state.Environment.Name, Bundle: state.BundleName,
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
