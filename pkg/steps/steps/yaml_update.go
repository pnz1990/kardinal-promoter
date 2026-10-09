// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// YAMLUpdateStepName is the step of update.strategy yaml.
const YAMLUpdateStepName = "yaml-update"

func init() {
	parentsteps.Register(&yamlUpdateStep{})
}

// yamlUpdateStep writes values of the Bundle's images into any YAML paths of
// any files in the environment directory (update.yaml.updates).
//
// Every edit is computed before anything is written: an edit that cannot be
// applied (unknown image, missing list element, a path through a scalar, a
// digest the image does not have) fails the step with no file changed. Files
// are edited through the YAML node API, so comments and key order are kept,
// and all file IO is confined to the checkout. Idempotent: running it twice
// gives the same files.
type yamlUpdateStep struct{}

func (s *yamlUpdateStep) Name() string { return YAMLUpdateStepName }

// pathSegment is one key of a YAML path, with the list indexes after it.
type pathSegment struct {
	key     string
	indexes []int
}

var segmentRE = regexp.MustCompile(`^([A-Za-z0-9_-]+)((?:\[[0-9]+\])*)$`)
var indexRE = regexp.MustCompile(`\[([0-9]+)\]`)

// parseYAMLPath parses "a.b[0].c" into segments.
func parseYAMLPath(p string) ([]pathSegment, error) {
	if p == "" {
		return nil, fmt.Errorf("path is empty")
	}
	var out []pathSegment
	for _, part := range strings.Split(p, ".") {
		m := segmentRE.FindStringSubmatch(part)
		if m == nil {
			return nil, fmt.Errorf("invalid path %q: use keys separated by \".\" and [N] list indexes", p)
		}
		seg := pathSegment{key: m[1]}
		for _, im := range indexRE.FindAllStringSubmatch(m[2], -1) {
			n, err := strconv.Atoi(im[1])
			if err != nil {
				return nil, fmt.Errorf("invalid index in path %q", p)
			}
			seg.indexes = append(seg.indexes, n)
		}
		out = append(out, seg)
	}
	return out, nil
}

// setYAMLPath sets the scalar at path in mapping root. Missing mapping keys
// are created; list elements must exist. It fails instead of replacing a
// mapping or list, so a wrong path cannot destroy values.
func setYAMLPath(root *yaml.Node, path string, value string) error {
	segs, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	node := root
	for i, seg := range segs {
		last := i == len(segs)-1 && len(seg.indexes) == 0
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: not a mapping at %q", path, seg.key)
		}
		if last {
			v := mapValue(node, seg.key)
			if v != nil && v.Kind != yaml.ScalarNode {
				return fmt.Errorf("%s is not a scalar", path)
			}
			if v == nil {
				// A new key in a flow mapping ({}) is written in block style.
				node.Style &^= yaml.FlowStyle
			}
			setMapScalar(node, seg.key, value)
			return nil
		}
		next := mapValue(node, seg.key)
		switch {
		case next == nil && len(seg.indexes) > 0:
			return fmt.Errorf("%s: %q is not a list", path, seg.key)
		case next == nil:
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: seg.key}, next)
		case next.Kind == yaml.ScalarNode && next.Tag == "!!null" && len(seg.indexes) == 0:
			*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		for j, idx := range seg.indexes {
			if next.Kind != yaml.SequenceNode {
				return fmt.Errorf("%s: %q is not a list", path, seg.key)
			}
			if idx >= len(next.Content) {
				return fmt.Errorf("%s: %s has %d elements, no [%d]", path, seg.key, len(next.Content), idx)
			}
			if i == len(segs)-1 && j == len(seg.indexes)-1 {
				el := next.Content[idx]
				if el.Kind != yaml.ScalarNode {
					return fmt.Errorf("%s is not a scalar", path)
				}
				style := el.Style &^ (yaml.LiteralStyle | yaml.FoldedStyle | yaml.FlowStyle)
				*el = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style,
					HeadComment: el.HeadComment, LineComment: el.LineComment, FootComment: el.FootComment}
				return nil
			}
			next = next.Content[idx]
		}
		node = next
	}
	return nil
}

// yamlUpdateValue returns the value an update writes for the Bundle images.
func yamlUpdateValue(images []v1alpha1.ImageRef, u v1alpha1.YAMLUpdate) (string, error) {
	var img *v1alpha1.ImageRef
	switch {
	case u.Image != "":
		for i := range images {
			if images[i].Repository == u.Image {
				img = &images[i]
				break
			}
		}
		if img == nil {
			return "", fmt.Errorf("the Bundle has no image %s", u.Image)
		}
	case len(images) == 1:
		img = &images[0]
	default:
		return "", fmt.Errorf("the Bundle has %d images: set image to choose one", len(images))
	}
	need := func(v, what string) (string, error) {
		if v == "" {
			return "", fmt.Errorf("image %s has no %s", img.Repository, what)
		}
		return v, nil
	}
	switch u.Value {
	case "", "tag":
		return need(img.Tag, "tag")
	case "digest":
		return need(img.Digest, "digest")
	case "tagWithDigest":
		if _, err := need(img.Tag, "tag"); err != nil {
			return "", err
		}
		if _, err := need(img.Digest, "digest"); err != nil {
			return "", err
		}
		return img.Tag + "@" + img.Digest, nil
	case "image":
		if _, err := need(img.Tag, "tag"); err != nil {
			return "", err
		}
		return img.Repository + ":" + img.Tag, nil
	case "imageWithDigest":
		if _, err := need(img.Digest, "digest"); err != nil {
			return "", err
		}
		if img.Tag == "" {
			return img.Repository + "@" + img.Digest, nil
		}
		return img.Repository + ":" + img.Tag + "@" + img.Digest, nil
	default:
		return "", fmt.Errorf("unknown value %q", u.Value)
	}
}

func (s *yamlUpdateStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	fail := func(err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, permanentIfEscape(err)
	}
	cfg := state.Environment.Update.YAML
	if cfg == nil || len(cfg.Updates) == 0 {
		return fail(parentsteps.Permanent(fmt.Errorf("update.strategy yaml needs update.yaml.updates")))
	}
	if len(state.Bundle.Images) == 0 {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no images to update"}, nil
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

	docs := map[string]*yamlDoc{}
	var order []string
	var applied []string
	for i, u := range cfg.Updates {
		value, err := yamlUpdateValue(state.Bundle.Images, u)
		if err != nil {
			return fail(parentsteps.Permanent(fmt.Errorf("update.yaml.updates[%d]: %w", i, err)))
		}
		rel, err := confinedRel(filepath.Join(envRel, filepath.FromSlash(u.File)))
		if err != nil {
			return fail(fmt.Errorf("update.yaml.updates[%d].file: %w", i, err))
		}
		doc, ok := docs[rel]
		if !ok {
			raw, err := root.ReadFile(rel)
			if err != nil {
				return fail(fmt.Errorf("read %s: %w", filepath.ToSlash(rel), err))
			}
			if doc, err = parseYAMLMapping(raw); err != nil {
				return fail(parentsteps.Permanent(fmt.Errorf("parse %s: %w", filepath.ToSlash(rel), err)))
			}
			docs[rel] = doc
			order = append(order, rel)
		}
		if err := setYAMLPath(doc.root(), u.Path, value); err != nil {
			return fail(parentsteps.Permanent(fmt.Errorf("update.yaml.updates[%d]: set %s in %s: %w",
				i, u.Path, filepath.ToSlash(rel), err)))
		}
		applied = append(applied, fmt.Sprintf("%s:%s=%s", u.File, u.Path, value))
	}

	// Every edit is valid: write the files.
	sort.Strings(order)
	for _, rel := range order {
		out, err := docs[rel].encode()
		if err != nil {
			return fail(fmt.Errorf("encode %s: %w", filepath.ToSlash(rel), err))
		}
		if err := root.WriteFile(rel, out, 0o644); err != nil {
			return fail(fmt.Errorf("write %s: %w", filepath.ToSlash(rel), err))
		}
	}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("set %s", strings.Join(applied, ", ")),
		Outputs: map[string]string{"yamlFiles": strings.Join(order, ",")},
	}, nil
}
