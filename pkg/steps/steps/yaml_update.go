// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
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

// tempSuffix names the temporary file written next to each edited file.
const tempSuffix = ".kardinal-tmp"

// removeTemps removes the temporary files of a failed write.
func removeTemps(root *os.Root, order []string) {
	for _, rel := range order {
		_ = root.Remove(rel + tempSuffix)
	}
}

// Test seams: the encoder and the rename, so tests can make the re-parse
// check and a rename fail.
var (
	encodeYAMLDoc = func(d *yamlDoc) ([]byte, error) { return d.encode() }
	renameInRoot  = func(root *os.Root, from, to string) error { return root.Rename(from, to) }
)

// writeExclusive writes data to a new file name in root with mode perm. A
// file or symbolic link already at name (left by a crash, or committed to the
// repository) is removed first, and the file is created with O_EXCL, so the
// write never goes through a link.
func writeExclusive(root *os.Root, name string, data []byte, perm os.FileMode) error {
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	// The umask may have narrowed perm; the edited file keeps its mode.
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// noSymlinkPath refuses rel when any of its components, directories
// included, is a symbolic link, or a directory component is not a directory.
// os.Root keeps a link inside the checkout, but a link could still let two
// entries edit one file under two names, or one environment edit another's.
func noSymlinkPath(root *os.Root, rel string) (os.FileInfo, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		p := filepath.FromSlash(strings.Join(parts[:i+1], "/"))
		info, err := root.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.ToSlash(rel), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, parentsteps.Permanent(fmt.Errorf("%s is a symbolic link, which yaml-update does not follow",
				filepath.ToSlash(p)))
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, parentsteps.Permanent(fmt.Errorf("%s is not a directory", filepath.ToSlash(p)))
		}
		if i == len(parts)-1 {
			return info, nil
		}
	}
	return nil, parentsteps.Permanent(fmt.Errorf("path is empty"))
}

// noMergeKey refuses a mapping with a merge key (<<): its values come from
// another mapping, so a key set or read here may not be the one consumers
// see.
func noMergeKey(m *yaml.Node, path string) error {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Tag == "!!merge" || (k.Value == "<<" && k.Style == 0) {
			return fmt.Errorf("%s: merge keys (<<) on the path are not supported", path)
		}
	}
	return nil
}

// duplicateKey returns the first mapping key that appears twice in the same
// mapping anywhere in n. YAML forbids it, and tools disagree about which
// value wins, so the value yaml-update sets might not be the one deployed.
func duplicateKey(n *yaml.Node) (string, int, bool) {
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode {
				if seen[k.Value] {
					return k.Value, k.Line, true
				}
				seen[k.Value] = true
			}
		}
	}
	for _, c := range n.Content {
		if k, line, ok := duplicateKey(c); ok {
			return k, line, true
		}
	}
	return "", 0, false
}

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
		if err := noAnchor(node, path); err != nil {
			return err
		}
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: not a mapping at %q", path, seg.key)
		}
		if err := noMergeKey(node, path); err != nil {
			return err
		}
		if last {
			v := mapValue(node, seg.key)
			if v != nil {
				if err := noAnchor(v, path); err != nil {
					return err
				}
			}
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
			if err := noAnchor(next, path); err != nil {
				return err
			}
			if next.Kind != yaml.SequenceNode {
				return fmt.Errorf("%s: %q is not a list", path, seg.key)
			}
			if idx >= len(next.Content) {
				return fmt.Errorf("%s: %s has %d elements, no [%d]", path, seg.key, len(next.Content), idx)
			}
			if i == len(segs)-1 && j == len(seg.indexes)-1 {
				el := next.Content[idx]
				if err := noAnchor(el, path); err != nil {
					return err
				}
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

// noAnchor refuses a node that is an alias or carries an anchor: editing it
// would change every place that refers to it, or nothing at all.
func noAnchor(n *yaml.Node, path string) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return fmt.Errorf("%s: anchors and aliases (& and *) on the path are not supported", path)
	}
	return nil
}

// getYAMLPath returns the scalar at path, for checking a written file.
func getYAMLPath(root *yaml.Node, path string) (string, bool) {
	segs, err := parseYAMLPath(path)
	if err != nil {
		return "", false
	}
	node := root
	for _, seg := range segs {
		if node.Kind != yaml.MappingNode {
			return "", false
		}
		if node = mapValue(node, seg.key); node == nil {
			return "", false
		}
		for _, idx := range seg.indexes {
			if node.Kind != yaml.SequenceNode || idx >= len(node.Content) {
				return "", false
			}
			node = node.Content[idx]
		}
	}
	if node.Kind != yaml.ScalarNode {
		return "", false
	}
	return node.Value, true
}

// maxYAMLUpdateFile bounds a file yaml-update reads.
const maxYAMLUpdateFile = 4 << 20

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

	type edit struct{ path, value string }
	docs := map[string]*yamlDoc{}
	originals := map[string][]byte{}
	modes := map[string]os.FileMode{}
	edits := map[string][]edit{}
	var order []string
	var applied []string
	for i, u := range cfg.Updates {
		value, err := yamlUpdateValue(state.Bundle.Images, u)
		if err != nil {
			return fail(parentsteps.Permanent(fmt.Errorf("update.yaml.updates[%d]: %w", i, err)))
		}
		// The file is checked on its own first, so it can neither be absolute
		// nor leave the environment directory.
		fileRel, err := confinedRel(u.File)
		if err != nil {
			return fail(parentsteps.Permanent(fmt.Errorf("update.yaml.updates[%d].file: %w", i, err)))
		}
		rel := filepath.Join(envRel, fileRel)
		doc, ok := docs[rel]
		if !ok {
			// A symbolic link anywhere on the path, the environment
			// directory included, would let two entries edit one file under
			// two names, or point at another environment: refuse it.
			info, err := noSymlinkPath(root, rel)
			if err != nil {
				return fail(err)
			}
			switch {
			case !info.Mode().IsRegular():
				return fail(parentsteps.Permanent(fmt.Errorf("%s is not a regular file", filepath.ToSlash(rel))))
			case info.Size() > maxYAMLUpdateFile:
				return fail(parentsteps.Permanent(fmt.Errorf("%s is larger than %d MiB", filepath.ToSlash(rel), maxYAMLUpdateFile>>20)))
			}
			raw, err := root.ReadFile(rel)
			if err != nil {
				return fail(fmt.Errorf("read %s: %w", filepath.ToSlash(rel), err))
			}
			if doc, err = parseYAMLMapping(raw); err != nil {
				return fail(parentsteps.Permanent(fmt.Errorf("parse %s: %w", filepath.ToSlash(rel), err)))
			}
			if key, line, dup := duplicateKey(doc.root()); dup {
				return fail(parentsteps.Permanent(fmt.Errorf("parse %s: key %q appears twice in one mapping (line %d)",
					filepath.ToSlash(rel), key, line)))
			}
			docs[rel], originals[rel], modes[rel] = doc, raw, info.Mode().Perm()
			order = append(order, rel)
		}
		if err := setYAMLPath(doc.root(), u.Path, value); err != nil {
			return fail(parentsteps.Permanent(fmt.Errorf("update.yaml.updates[%d]: set %s in %s: %w",
				i, u.Path, filepath.ToSlash(rel), err)))
		}
		edits[rel] = append(edits[rel], edit{u.Path, value})
		applied = append(applied, fmt.Sprintf("%s:%s=%s", u.File, u.Path, value))
	}

	// Encode every file and parse the result again: each must still be one
	// document with every value where it was set, before anything is written.
	sort.Strings(order)
	outs := map[string][]byte{}
	for _, rel := range order {
		out, err := encodeYAMLDoc(docs[rel])
		if err != nil {
			return fail(fmt.Errorf("encode %s: %w", filepath.ToSlash(rel), err))
		}
		check, err := parseYAMLMapping(out)
		if err != nil {
			return fail(fmt.Errorf("re-parse %s: %w", filepath.ToSlash(rel), err))
		}
		for _, e := range edits[rel] {
			if got, ok := getYAMLPath(check.root(), e.path); !ok || got != e.value {
				return fail(fmt.Errorf("re-parse %s: %s is %q, not %q", filepath.ToSlash(rel), e.path, got, e.value))
			}
		}
		outs[rel] = out
	}

	// Write each file to a temporary file next to it, then rename them all;
	// when a rename fails, the files already replaced get their old content
	// back, so the checkout is never left half edited.
	for _, rel := range order {
		if err := writeExclusive(root, rel+tempSuffix, outs[rel], modes[rel]); err != nil {
			removeTemps(root, order)
			return fail(fmt.Errorf("write %s: %w", filepath.ToSlash(rel), err))
		}
	}
	for i, rel := range order {
		if err := renameInRoot(root, rel+tempSuffix, rel); err != nil {
			for _, done := range order[:i] {
				if rbErr := writeExclusive(root, done+tempSuffix, originals[done], modes[done]); rbErr == nil {
					_ = renameInRoot(root, done+tempSuffix, done)
				}
			}
			removeTemps(root, order)
			return fail(fmt.Errorf("write %s: %w", filepath.ToSlash(rel), err))
		}
	}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("set %s", strings.Join(applied, ", ")),
		Outputs: map[string]string{"yamlFiles": strings.Join(order, ",")},
	}, nil
}
