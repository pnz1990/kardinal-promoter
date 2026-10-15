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
	// raw and orig are the file as read and an unedited parse of it, for
	// spliceEncode; orig is nil when the document cannot be spliced.
	raw  []byte
	orig *yaml.Node
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
	} else if len(d.doc.Content[0].Content) > 0 {
		var orig yaml.Node
		if err := yaml.Unmarshal(raw, &orig); err == nil {
			d.raw, d.orig = raw, &orig
		}
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
//
// When it can, it writes only what an edit changed (spliceEncode, #1907):
// the file's other bytes stay as they were. Otherwise, or when the splice
// would not read back as the edited document, the whole document is
// encoded.
func (d *yamlDoc) encode() ([]byte, error) {
	if d.orig != nil {
		if out, ok := spliceEncode(d.raw, d.orig, &d.doc, d.compactSeq); ok {
			if same, err := sameData(out, &d.doc); err == nil && same {
				return out, nil
			}
		}
	}
	keepFlowNulls(&d.doc, false)
	out, err := encodeNode(&d.doc, d.compactSeq)
	if err != nil {
		return nil, err
	}
	// The encoder does not round-trip every scalar (a folded block scalar
	// with more-indented lines, #1907): refuse to write a file whose other
	// values would change, rather than change them.
	if same, err := sameData(out, &d.doc); err != nil || !same {
		return nil, errReencodeChanges
	}
	// Nor is it stable: a comment in a flow collection can come out with a
	// blank line that the next encode drops (#1965), so the next run of the
	// same edit would write other bytes. Encode what was written until it
	// no longer changes: what the step writes is what it would write again.
	for range 3 {
		var again yaml.Node
		if err := yaml.Unmarshal(out, &again); err != nil {
			return nil, errReencodeChanges
		}
		keepFlowNulls(&again, false)
		next, err := encodeNode(&again, d.compactSeq)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(next, out) {
			return out, nil
		}
		if same, err := sameData(next, &d.doc); err != nil || !same {
			return nil, errReencodeChanges
		}
		out = next
	}
	return nil, errReencodeChanges
}

// encodeNode encodes n with two-space indentation and, when compactSeq,
// sequences not indented under their key.
func encodeNode(n *yaml.Node, compactSeq bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if compactSeq {
		enc.CompactSeqIndent()
	}
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var errReencodeChanges = errors.New("writing the file back would change or break values: the YAML encoder " +
	"cannot reproduce the document as edited (a folded block scalar with more-indented lines, or a value it " +
	"cannot write as valid YAML); rewrite the file in a plainer style")

// keepFlowNulls spells out the empty nulls inside flow collections. The
// parser reads the value of "{a}" or "{a: }" as a null with no text, and the
// encoder writes such a null in flow style as ”, an empty string: every
// edit of the file turned those nulls into strings (#1842). In block style
// the encoder writes "a:" back, so only flow collections need it.
func keepFlowNulls(n *yaml.Node, inFlow bool) {
	inFlow = inFlow || n.Style&yaml.FlowStyle != 0
	for _, c := range n.Content {
		if inFlow && c.Kind == yaml.ScalarNode && c.Tag == "!!null" && c.Value == "" {
			c.Value = "null"
		}
		keepFlowNulls(c, inFlow)
	}
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
		*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: stringStyle(value, style),
			HeadComment: v.HeadComment, LineComment: v.LineComment, FootComment: v.FootComment}
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key, Style: stringStyle(key, 0)},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: stringStyle(value, 0)})
}

// stringStyle is style, or double quotes when value written plain would
// read back as something else than a string: "<<" (a merge key, which the
// encoder tags !!merge), and whatever else it resolves to another tag.
func stringStyle(value string, style yaml.Style) yaml.Style {
	if style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
		return style
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(value), &n); err != nil || len(n.Content) != 1 ||
		n.Content[0].Kind != yaml.ScalarNode || n.Content[0].Tag != "!!str" || n.Content[0].Value != value {
		return yaml.DoubleQuotedStyle
	}
	return style
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
