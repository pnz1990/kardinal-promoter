// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// The YAML path grammar of update.yaml.updates[].path and
// update.helm.chartVersionPath (one grammar, one parser):
//
//	path     = ["."] segment *("." segment)
//	segment  = key *("[" (index | selector) "]")
//	key      = 1*(ALPHA / DIGIT / "_" / "-")
//	index    = 1*9DIGIT                     ; a list element by position, no leading 0
//	selector = key "=" value                ; the list element whose field key is value
//	value    = 1*(ALPHA / DIGIT / "_" / "-" / "." / "/" / ":" / "@")
//
// for example "image.tag", "spec.template.spec.containers[0].image",
// "spec.template.spec.containers[name=app].image" or
// ".dependencies[name=podinfo].version". A key that is all digits is a
// mapping key in a mapping and a list index in a list, so ".dependencies.0.version"
// and ".dependencies[0].version" are the same path; a missing or null value
// before one is not created (it is an error), so a digits key never turns a
// missing list into a mapping. The CRD patterns of both
// fields accept exactly this grammar.

// yamlPathSeg is one step of a parsed path: a mapping key, a list index, or
// the list element whose field matchField has the value matchValue.
type yamlPathSeg struct {
	key        string
	index      int // >= 0 for a list index
	matchField string
	matchValue string
}

func (g yamlPathSeg) String() string {
	switch {
	case g.matchField != "":
		return "[" + g.matchField + "=" + g.matchValue + "]"
	case g.index >= 0:
		return "[" + strconv.Itoa(g.index) + "]"
	default:
		return g.key
	}
}

var (
	yamlPathKey   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	yamlPathValue = regexp.MustCompile(`^[A-Za-z0-9_./:@-]+$`)
)

// parseYAMLPath parses path (see the grammar above) into its steps.
func parseYAMLPath(path string) ([]yamlPathSeg, error) {
	p := strings.TrimPrefix(path, ".")
	if p == "" {
		return nil, fmt.Errorf("path is empty")
	}
	invalid := func(why string) error {
		return fmt.Errorf("invalid path %q: %s; use keys separated by \".\", [N] for a list index and [field=value] for a list element", path, why)
	}
	var segs []yamlPathSeg
	for {
		end := strings.IndexAny(p, ".[")
		if end < 0 {
			end = len(p)
		}
		key := p[:end]
		if !yamlPathKey.MatchString(key) {
			return nil, invalid(fmt.Sprintf("bad key %q", key))
		}
		segs = append(segs, yamlPathSeg{key: key, index: -1})
		p = p[end:]
		for strings.HasPrefix(p, "[") {
			rb := strings.IndexByte(p, ']')
			if rb < 0 {
				return nil, invalid("unclosed [")
			}
			inner := p[1:rb]
			if field, value, ok := strings.Cut(inner, "="); ok {
				if !yamlPathKey.MatchString(field) || !yamlPathValue.MatchString(value) {
					return nil, invalid(fmt.Sprintf("bad selector [%s]", inner))
				}
				segs = append(segs, yamlPathSeg{index: -1, matchField: field, matchValue: value})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 || inner != strconv.Itoa(n) || len(inner) > 9 {
					return nil, invalid(fmt.Sprintf("bad index [%s]", inner))
				}
				segs = append(segs, yamlPathSeg{index: n})
			}
			p = p[rb+1:]
		}
		if p == "" {
			return segs, nil
		}
		if !strings.HasPrefix(p, ".") || len(p) == 1 {
			return nil, invalid("a key must follow \".\"")
		}
		p = p[1:]
	}
}

// asIndex turns a digits-only key into a list index, for a path step that
// reaches a list.
func asIndex(g yamlPathSeg) (yamlPathSeg, bool) {
	if g.key == "" {
		return g, true
	}
	n, err := strconv.Atoi(g.key)
	if err != nil || n < 0 || g.key != strconv.Itoa(n) {
		return g, false
	}
	return yamlPathSeg{index: n}, true
}

// pathString renders segs[:n] for errors.
func pathString(segs []yamlPathSeg, n int) string {
	var b strings.Builder
	for i, g := range segs[:n] {
		if g.key != "" && i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(g.String())
	}
	return b.String()
}

// setYAMLPath sets the scalar at path in mapping root. Missing mapping keys
// are created; list elements (an index or a selector) must exist. It fails
// instead of replacing a mapping or list, so a wrong path cannot destroy
// values, and refuses anchors, aliases and merge keys on the path. The
// replaced scalar keeps its comments and quoting.
func setYAMLPath(root *yaml.Node, path string, value string) error {
	segs, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	node := root
	for i, g := range segs {
		last := i == len(segs)-1
		if err := noAnchor(node, path); err != nil {
			return err
		}
		var next *yaml.Node
		if node.Kind == yaml.SequenceNode {
			idx, ok := asIndex(g)
			if !ok {
				return fmt.Errorf("%s: %s is a list; pick an element with [N] or [field=value]", path, describe(segs, i))
			}
			g = idx
		}
		switch {
		case g.key != "":
			if node.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: %s is not a mapping", path, describe(segs, i))
			}
			if err := noMergeKey(node, path); err != nil {
				return err
			}
			next = mapValue(node, g.key)
			if last {
				if next != nil {
					if err := noAnchor(next, path); err != nil {
						return err
					}
					if next.Kind != yaml.ScalarNode {
						return fmt.Errorf("%s is not a scalar", path)
					}
				} else {
					// A new key in a flow mapping ({}) is written in block style.
					node.Style &^= yaml.FlowStyle
				}
				setMapScalar(node, g.key, value)
				return nil
			}
			// The next step indexes a list ([N], [f=v], or a digits-only key):
			// a missing or null value is not created as a mapping, which
			// would write {"0": ...} where a list was meant.
			_, digits := asIndex(segs[i+1])
			listNext := digits || (next != nil && next.Kind == yaml.SequenceNode)
			isNull := next != nil && next.Kind == yaml.ScalarNode && next.Tag == "!!null"
			switch {
			case (next == nil || isNull) && listNext:
				return fmt.Errorf("%s: %s does not exist", path, pathString(segs, i+1))
			case next == nil:
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: g.key}, next)
			case next.Kind == yaml.ScalarNode && next.Tag == "!!null" && !listNext:
				*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
		default:
			if node.Kind != yaml.SequenceNode {
				return fmt.Errorf("%s: %s is not a list", path, describe(segs, i))
			}
			next, err = listElement(node, g, path, segs, i)
			if err != nil {
				return err
			}
			if last {
				if err := noAnchor(next, path); err != nil {
					return err
				}
				if next.Kind != yaml.ScalarNode {
					return fmt.Errorf("%s is not a scalar", path)
				}
				style := next.Style &^ (yaml.LiteralStyle | yaml.FoldedStyle | yaml.FlowStyle)
				*next = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style,
					HeadComment: next.HeadComment, LineComment: next.LineComment, FootComment: next.FootComment}
				return nil
			}
		}
		node = next
	}
	return nil
}

// describe names what segs[:i] points at, for errors: "the document" at the
// top.
func describe(segs []yamlPathSeg, i int) string {
	if i == 0 {
		return "the document"
	}
	return pathString(segs, i)
}

// listElement returns the element of list that g (an index or a selector)
// picks.
func listElement(list *yaml.Node, g yamlPathSeg, path string, segs []yamlPathSeg, i int) (*yaml.Node, error) {
	if g.matchField != "" {
		for _, el := range list.Content {
			if el.Kind == yaml.MappingNode && scalarValue(el, g.matchField) == g.matchValue {
				return el, nil
			}
		}
		return nil, fmt.Errorf("%s: %s has no element with %s %q", path, pathString(segs, i), g.matchField, g.matchValue)
	}
	if g.index >= len(list.Content) {
		return nil, fmt.Errorf("%s: %s has %d elements, no [%d]", path, pathString(segs, i), len(list.Content), g.index)
	}
	return list.Content[g.index], nil
}

// getYAMLPath returns the scalar at path, for checking a written file.
func getYAMLPath(root *yaml.Node, path string) (string, bool) {
	segs, err := parseYAMLPath(path)
	if err != nil {
		return "", false
	}
	node := root
	for i, g := range segs {
		if node.Kind == yaml.SequenceNode {
			var ok bool
			if g, ok = asIndex(g); !ok {
				return "", false
			}
		}
		switch {
		case g.key != "":
			if node.Kind != yaml.MappingNode {
				return "", false
			}
			node = mapValue(node, g.key)
		case node.Kind == yaml.SequenceNode:
			node, err = listElement(node, g, path, segs, i)
			if err != nil {
				return "", false
			}
		default:
			return "", false
		}
		if node == nil {
			return "", false
		}
	}
	if node.Kind != yaml.ScalarNode {
		return "", false
	}
	return node.Value, true
}
