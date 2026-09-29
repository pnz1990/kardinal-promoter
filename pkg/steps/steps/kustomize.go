// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package steps

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&kustomizeSetImageStep{})
}

// kustomizeSetImageStep edits the environment's kustomization file to update
// the images list. It is the pure-Go equivalent of
// `kustomize edit set image <repository>:<tag>@<digest>` (#494).
//
// The kustomization images list format:
//
//	images:
//	- name: ghcr.io/myorg/my-app     # the image name used in the manifests
//	  newName: my-registry/my-app    # optional: override registry/name
//	  newTag: v1.2.3                 # tag override
//	  digest: sha256:abc123          # digest override (takes precedence over tag)
//
// kustomize matches an entry on `name` only, against the image name in the
// manifests. So for every Bundle image the step always writes (or updates) an
// entry whose `name` is the full repository, as `kustomize edit set image`
// does; that is the entry that rewrites manifests using `image: <repository>`.
// Entries that alias the repository are updated with the same tag or digest
// too, so manifests that use their name keep promoting: an entry whose
// `newName` is the repository, and an older short-name entry (`name: app`, no
// `newName`) when exactly one Bundle image has that short name. The latter
// gets `newName: <repository>`. A short-name entry whose `newName` points at
// another repository is left alone (C05-steps-09, C05-steps-15).
//
// The file is edited through the YAML node API, so comments and key order are
// kept (C05-steps-31). kustomization.yaml, kustomization.yml and Kustomization
// are recognised; the step fails when the directory has none (C05-steps-16).
type kustomizeSetImageStep struct{}

func (s *kustomizeSetImageStep) Name() string { return "kustomize-set-image" }

func (s *kustomizeSetImageStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
	}

	fail := func(err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("kustomize-set-image: %v", err)},
			permanentIfEscape(fmt.Errorf("kustomize-set-image: %w", err))
	}
	envRel, err := envSubdir(state)
	if err != nil {
		return fail(err)
	}
	root, err := openCheckout(state)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = root.Close() }()

	file, updated, err := setImagesInKustomization(root, envRel, state.Bundle.Images)
	if err != nil {
		return fail(err)
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("updated %d images in %s", updated, filepath.ToSlash(file)),
	}, nil
}

// kustomizationFileNames are the file names kustomize recognises, in its
// lookup order.
var kustomizationFileNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// findKustomization returns the path (relative to root) of the kustomization
// file in dir.
func findKustomization(root *os.Root, dir string) (string, error) {
	for _, name := range kustomizationFileNames {
		p := filepath.Join(dir, name)
		_, err := root.Stat(p)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", filepath.ToSlash(p), err)
		}
	}
	return "", fmt.Errorf("no kustomization file (%s) in %s",
		strings.Join(kustomizationFileNames, ", "), filepath.ToSlash(dir))
}

// setImagesInKustomization updates or adds one images entry per image in the
// kustomization file of dir and writes the file back. It returns the file
// path and the number of images written.
func setImagesInKustomization(root *os.Root, dir string, images []v1alpha1.ImageRef) (string, int, error) {
	file, err := findKustomization(root, dir)
	if err != nil {
		return "", 0, err
	}
	raw, err := root.ReadFile(file)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", filepath.ToSlash(file), err)
	}
	doc, err := parseYAMLMapping(raw)
	if err != nil {
		return "", 0, fmt.Errorf("parse %s: %w", filepath.ToSlash(file), err)
	}

	list := mapValue(doc.root(), "images")
	if list == nil {
		list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		doc.root().Content = append(doc.root().Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "images"}, list)
	}
	if list.Kind == yaml.ScalarNode && list.Tag == "!!null" {
		*list = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	if list.Kind != yaml.SequenceNode {
		return "", 0, fmt.Errorf("%s: images is not a list", filepath.ToSlash(file))
	}

	shortNames := map[string]int{}
	for _, img := range images {
		if img.Repository != "" {
			shortNames[imageShortName(img.Repository)]++
		}
	}

	updated := 0
	for _, img := range images {
		if img.Repository == "" {
			continue
		}
		if img.Tag == "" && img.Digest == "" {
			return "", 0, fmt.Errorf("image %s has neither a tag nor a digest", img.Repository)
		}
		aliases := imageAliasEntries(list, img.Repository, shortNames[imageShortName(img.Repository)] == 1)
		full := imageEntryByName(list, img.Repository)
		if full == nil {
			full = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setMapScalar(full, "name", img.Repository)
			list.Content = append(list.Content, full)
		}
		for _, entry := range append(aliases, full) {
			if entry != full && scalarValue(entry, "newName") == "" {
				setMapScalar(entry, "newName", img.Repository)
			}
			setOrDelete(entry, "newTag", img.Tag)
			setOrDelete(entry, "digest", img.Digest)
		}
		updated++
	}
	if updated == 0 {
		return "", 0, fmt.Errorf("the Bundle has no image with a repository")
	}

	out, err := doc.encode()
	if err != nil {
		return "", 0, fmt.Errorf("encode %s: %w", filepath.ToSlash(file), err)
	}
	if err := root.WriteFile(file, out, 0o644); err != nil {
		return "", 0, fmt.Errorf("write %s: %w", filepath.ToSlash(file), err)
	}
	return file, updated, nil
}

// imageEntryByName returns the images entry whose name is repository, or nil.
func imageEntryByName(list *yaml.Node, repository string) *yaml.Node {
	for _, e := range list.Content {
		if e.Kind == yaml.MappingNode && scalarValue(e, "name") == repository {
			return e
		}
	}
	return nil
}

// imageAliasEntries returns the entries, other than the full-name one, that
// resolve to repository: those whose newName is the repository and, when
// shortUnique, those whose name is the repository's short name and that have
// no newName. kustomize applies them to manifests that use their name.
func imageAliasEntries(list *yaml.Node, repository string, shortUnique bool) []*yaml.Node {
	short := imageShortName(repository)
	var out []*yaml.Node
	for _, e := range list.Content {
		if e.Kind != yaml.MappingNode {
			continue
		}
		name, newName := scalarValue(e, "name"), scalarValue(e, "newName")
		switch {
		case name == repository:
			// The full-name entry, handled by the caller.
		case newName == repository:
			out = append(out, e)
		case shortUnique && short != repository && name == short && newName == "":
			out = append(out, e)
		}
	}
	return out
}

// setOrDelete sets key to value, or removes key when value is empty.
func setOrDelete(m *yaml.Node, key, value string) {
	if value == "" {
		deleteMapKey(m, key)
		return
	}
	setMapScalar(m, key, value)
}

// imageShortName returns the last path segment of a repository,
// e.g. "ghcr.io/myorg/myapp" → "myapp".
func imageShortName(repo string) string {
	parts := strings.Split(repo, "/")
	return parts[len(parts)-1]
}
