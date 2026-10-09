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
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// yamlDoc is a YAML document edited through the node API, so comments and key
// order survive the edit (C05-steps-31).
type yamlDoc struct {
	doc        yaml.Node
	compactSeq bool
}

// parseYAMLMapping parses raw as a YAML document whose top level is a mapping.
// An empty document is treated as an empty mapping.
//
// A stream with more than one document (---) is refused: decoding only the
// first and writing it back would silently drop the others. Empty documents
// after the first (a trailing "---", "--- null", or documents that hold only
// comments) hold no data and their separators are not written back. Their
// comments are: the parser attaches them to these documents ("--- # end",
// "---\n# x\n---"), so they are moved to the end of the first document
// instead of being dropped.
func parseYAMLMapping(raw []byte) (*yamlDoc, error) {
	d := &yamlDoc{compactSeq: compactSeqIndent(raw)}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&d.doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	var trailing []string // comments of the empty documents after the first
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !emptyDocument(&extra) {
			return nil, errMultiDocument
		}
		trailing = append(trailing, documentComments(&extra)...)
	}
	if d.doc.Kind == 0 {
		d.doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if d.doc.Kind != yaml.DocumentNode || len(d.doc.Content) != 1 || d.doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("top level is not a mapping")
	}
	if len(trailing) > 0 {
		d.doc.FootComment = strings.Join(append(nonEmpty(d.doc.FootComment), trailing...), "\n\n")
	}
	return d, nil
}

var errMultiDocument = errors.New("the file holds more than one YAML document (---), which is not supported")

// emptyDocument reports whether n is a document with no content: nothing,
// or a single null scalar ("---" alone, or "--- null") without an anchor.
// Comments do not count as content (documentComments returns them).
func emptyDocument(n *yaml.Node) bool {
	if n.Kind == 0 {
		return true
	}
	if n.Kind != yaml.DocumentNode {
		return false
	}
	switch len(n.Content) {
	case 0:
		return true
	case 1:
		c := n.Content[0]
		return c.Kind == yaml.ScalarNode && c.Tag == "!!null" && c.Anchor == ""
	}
	return false
}

// documentComments returns the comments of an empty document, in order.
func documentComments(n *yaml.Node) []string {
	out := nonEmpty(n.HeadComment, n.LineComment)
	for _, c := range n.Content {
		out = append(out, nonEmpty(c.HeadComment, c.LineComment, c.FootComment)...)
	}
	return append(out, nonEmpty(n.FootComment)...)
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// root returns the top-level mapping.
func (d *yamlDoc) root() *yaml.Node { return d.doc.Content[0] }

// encode serialises the document with two-space indentation, keeping the
// sequence indentation style of the original file.
func (d *yamlDoc) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if d.compactSeq {
		enc.CompactSeqIndent()
	}
	if err := enc.Encode(&d.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mapValue returns the value node of key in mapping m, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalarValue returns the value of key in mapping m when it is a scalar.
func scalarValue(m *yaml.Node, key string) string {
	if v := mapValue(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// setMapScalar sets key in mapping m to the string value, adding the key when
// it is absent. An existing quoting style is kept.
func setMapScalar(m *yaml.Node, key, value string) {
	if v := mapValue(m, key); v != nil {
		style := v.Style &^ (yaml.LiteralStyle | yaml.FoldedStyle | yaml.FlowStyle)
		*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style,
			HeadComment: v.HeadComment, LineComment: v.LineComment, FootComment: v.FootComment}
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// deleteMapKey removes key from mapping m.
func deleteMapKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// compactSeqIndent reports whether the file writes block sequences at the
// same indentation as their parent key ("key:\n- a"), which is what
// kustomize writes. It defaults to true when the file has no sequence.
func compactSeqIndent(raw []byte) bool {
	prevIndent, prevIsKey := -1, false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		if prevIsKey && (trimmed == "-" || strings.HasPrefix(trimmed, "- ")) {
			return indent == prevIndent
		}
		prevIndent = indent
		prevIsKey = strings.HasSuffix(strings.TrimRight(trimmed, " "), ":")
	}
	return true
}
