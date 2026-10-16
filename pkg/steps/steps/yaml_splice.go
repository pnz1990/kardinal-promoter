// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// spliceEncode writes the edited document as the original bytes with only
// the mapping entries an edit changed replaced (#1907): an entry of a block
// mapping, at any depth of block mappings and of block sequences that kept
// their length, is the smallest unit rewritten, from its key to the end of
// its value. Every other byte of the file stays as it was: comments,
// quoting, indentation, block scalars and flow collections the edit did not
// touch are not re-encoded, so the encoder's own round-trip losses (a
// folded scalar's chomping and line breaks, an empty flow null) cannot
// change a value the step does not own.
//
// It returns ok false when it cannot splice (the document is not a block
// mapping, or an edit is outside what it handles); the caller encodes the
// whole document instead.
func spliceEncode(raw []byte, orig, edited *yaml.Node, compactSeq bool) ([]byte, bool) {
	if orig == nil || orig.Kind != yaml.DocumentNode || len(orig.Content) != 1 || len(edited.Content) != 1 {
		return nil, false
	}
	o, e := orig.Content[0], edited.Content[0]
	// A file without a final line break is encoded whole: a key added after
	// its last entry would change that entry's block scalar.
	if o.Kind != yaml.MappingNode || e.Kind != yaml.MappingNode || o.Style&yaml.FlowStyle != 0 || bytes.Contains(raw, []byte("\r")) ||
		!bytes.HasSuffix(raw, []byte("\n")) {
		return nil, false
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	s := &splicer{lines: lines, compactSeq: compactSeq}
	// The document ends at a "..." marker: what follows it is not the
	// mapping's, and an entry added at the end goes before it (#1965).
	bound := len(lines)
	for i, l := range lines {
		t := strings.TrimRight(l, "\n")
		if t == "---" || strings.HasPrefix(t, "--- ") {
			// A trailing empty document: the file is written whole, as before.
			break
		}
		if t == "..." || strings.HasPrefix(t, "... ") || strings.HasPrefix(t, "...\t") {
			bound = i
			break
		}
	}
	if !s.mapping(o, e, bound, 0) || s.failed {
		return nil, false
	}
	return s.apply(), true
}

// splice replaces the text from line start (1-based), column col (1-based),
// through the end of line end with text; end < start inserts text as new
// lines before line start.
type splice struct {
	start, col, end int
	text            string
	seq             int
}

type splicer struct {
	lines      []string
	compactSeq bool
	edits      []splice
	failed     bool
}

// mapping compares the block mappings o (original) and e (edited); bound
// is the last line o's entries may extend to, and col the column of o's keys
// (0 for an empty mapping: taken from its keys).
func (s *splicer) mapping(o, e *yaml.Node, bound, col int) bool {
	if o.Style&yaml.FlowStyle != 0 {
		return false
	}
	type entry struct {
		key, val      *yaml.Node
		start, finish int
	}
	var entries []entry
	for i := 0; i+1 < len(o.Content); i += 2 {
		entries = append(entries, entry{key: o.Content[i], val: o.Content[i+1]})
	}
	for i := range entries {
		next := bound + 1
		if i+1 < len(entries) {
			next = entries[i+1].key.Line - commentLines(entries[i+1].key.HeadComment)
		}
		entries[i].start = entries[i].key.Line
		entries[i].finish = s.trimEnd(entries[i].start, next-1, entries[i].key.Column, entries[i].val)
		if col == 0 {
			col = entries[i].key.Column
		}
	}
	byKey := map[string]int{}
	for i, en := range entries {
		if en.key.Kind != yaml.ScalarNode || en.key.Alias != nil {
			return false
		}
		if _, dup := byKey[en.key.Value]; dup {
			return false
		}
		byKey[en.key.Value] = i
	}
	seen := map[string]bool{}
	last := 0
	if len(entries) > 0 {
		last = entries[len(entries)-1].finish
	}
	for i := 0; i+1 < len(e.Content); i += 2 {
		k, v := e.Content[i], e.Content[i+1]
		seen[k.Value] = true
		j, ok := byKey[k.Value]
		if !ok {
			// A new key: after the mapping's last entry.
			if col == 0 || last == 0 {
				return false
			}
			text := s.entryText(k, v, col, true)
			at := last + 1
			if bound == len(s.lines) && s.blankFrom(at) {
				// Only blank lines follow (spaces or tabs, #1965): append at
				// the end of the file and leave them where they are.
				at = bound + 1
			}
			if strings.HasSuffix(text, "\n\n") && bound == len(s.lines) {
				// It ends in a "+" (keep) block scalar, which would take
				// the blank lines after it: at the end of the file.
				at = bound + 1
			} else if strings.HasSuffix(text, "\n\n") {
				return false
			}
			s.edits = append(s.edits, splice{start: at, end: at - 1, text: text})
			continue
		}
		en := entries[j]
		if sameNode(en.val, v) {
			continue
		}
		if s.descend(en.key, en.val, v, en.finish) {
			continue
		}
		s.edits = append(s.edits, splice{start: en.start, col: en.key.Column, end: en.finish, text: s.entryText(k, v, en.key.Column, false)})
	}
	for _, en := range entries {
		if !seen[en.key.Value] {
			// A deleted key, with its head comment.
			s.edits = append(s.edits, splice{start: en.start - commentLines(en.key.HeadComment), col: 1, end: en.finish})
		}
	}
	return true
}

// descend splices inside a changed value instead of replacing its entry:
// a block mapping, or a block sequence of the same length.
func (s *splicer) descend(key, o, e *yaml.Node, finish int) bool {
	if o.Kind != e.Kind || o.Tag != e.Tag || o.Anchor != "" || o.Alias != nil || o.Style&yaml.FlowStyle != 0 ||
		o.Line <= key.Line {
		return false
	}
	switch o.Kind {
	case yaml.MappingNode:
		return len(o.Content) > 0 && s.mapping(o, e, finish, 0)
	case yaml.SequenceNode:
		if len(o.Content) != len(e.Content) {
			return false
		}
		for i := range o.Content {
			oi, ei := o.Content[i], e.Content[i]
			if sameNode(oi, ei) {
				continue
			}
			end := finish
			if i+1 < len(o.Content) {
				end = s.trimEnd(oi.Line, o.Content[i+1].Line-commentLines(o.Content[i+1].HeadComment)-1, oi.Column, oi)
			}
			if oi.Kind != yaml.MappingNode || ei.Kind != yaml.MappingNode || oi.Style&yaml.FlowStyle != 0 ||
				oi.Anchor != "" || len(oi.Content) == 0 || !s.mapping(oi, ei, end, 0) {
				// An item that is not a block mapping: replace the whole
				// entry that holds the sequence.
				return false
			}
		}
		return true
	}
	return false
}

// quotedEnd is the line (1-based) of the closing quote of the quoted
// scalar n, or 0 when n is not quoted or the quote is not found.
func (s *splicer) quotedEnd(n *yaml.Node) int {
	if n == nil || n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 || n.Line < 1 || n.Line > len(s.lines) {
		return 0
	}
	q := byte('"')
	if n.Style&yaml.SingleQuotedStyle != 0 {
		q = '\''
	}
	line, col := n.Line, n.Column // col is the opening quote (1-based)
	text := s.lines[line-1]
	if col-1 >= len(text) || text[col-1] != q {
		return 0
	}
	i := col // the byte after the opening quote
	for {
		for ; i < len(text); i++ {
			switch {
			case q == '"' && text[i] == '\\':
				i++ // an escape: the next byte is not the closing quote
			case text[i] == q && q == '\'' && i+1 < len(text) && text[i+1] == '\'':
				i++ // '' is a quote inside the scalar
			case text[i] == q:
				return line
			}
		}
		line++
		if line > len(s.lines) {
			return 0
		}
		text, i = s.lines[line-1], 0
	}
}

// blankFrom reports whether every line from line (1-based) to the end of
// the file is blank: spaces and tabs only.
func (s *splicer) blankFrom(line int) bool {
	for l := line; l <= len(s.lines); l++ {
		if strings.TrimSpace(s.lines[l-1]) != "" {
			return false
		}
	}
	return true
}

// trimEnd returns the last line of an entry whose lines are [start, end],
// whose key is at column col and whose value is v: the blank and
// comment-only lines that end the range belong to what follows. After a
// block scalar, "#" lines indented deeper than the key are the scalar's
// text, and a "+" (keep) scalar holds the blank lines it ends with, so
// only the comment lines indented no deeper than the key, with what follows
// them, and (unless the scalar keeps them) the blank lines before those,
// are trimmed.
func (s *splicer) trimEnd(start, end, col int, v *yaml.Node) int {
	last := lastScalar(v)
	if last == nil || last.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
		// A quoted scalar may span lines, and a line of it may start with
		// "#" ("0: \"\n#\""): its text, not a comment (#1965).
		return max(s.trim(start, end, true), min(s.quotedEnd(last), end))
	}
	for l := end; l > start; l-- {
		line := s.lines[l-1]
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "#") || len(line)-len(strings.TrimLeft(line, " ")) >= col {
			break
		}
		end = l - 1
	}
	if keepChomping(s.lines[last.Line-1], last.Column) {
		return end
	}
	// A line of only spaces, more of them than the scalar's indentation, is
	// the scalar's text (folded ">\n 0\n  \n" is "0\n \n"), not a blank
	// line after it: an entry inserted before it would take it from the
	// scalar (#1965).
	indent, ok := s.blockIndent(last, scalarKeyColumn(v, col))
	for end > start {
		line := strings.TrimRight(s.lines[end-1], "\n")
		if strings.TrimSpace(line) != "" {
			break
		}
		if !ok || (strings.Trim(line, " ") == "" && len(line) > indent) {
			break
		}
		end--
	}
	return end
}

// blockIndent is the content indentation of the block scalar n: the
// header's indentation indicator added to the indentation of its key (at
// column keyCol, 0 when it has none), else the
// indentation of its first line that is not blank. ok is false when it has
// no such line: every line after the header may then be its text.
func (s *splicer) blockIndent(n *yaml.Node, keyCol int) (int, bool) {
	header := s.lines[n.Line-1]
	if n.Column-1 < len(header) {
		h := header[n.Column-1:]
		if i := strings.IndexAny(h, " \t#\n"); i >= 0 {
			h = h[:i]
		}
		for _, r := range h {
			if r >= '1' && r <= '9' {
				// Relative to the indentation of the scalar's parent: its
				// key's, wherever the header is ("0:\n >2", #1965). A
				// scalar in a sequence: unknown, so every line may be text.
				if keyCol < 1 {
					return 0, false
				}
				return keyCol - 1 + int(r-'0'), true
			}
		}
	}
	for l := n.Line; l < len(s.lines); l++ {
		line := strings.TrimRight(s.lines[l], "\n")
		if strings.TrimSpace(line) != "" {
			return len(line) - len(strings.TrimLeft(line, " ")), true
		}
	}
	return 0, false
}

// trim drops the blank lines, and with comments the comment-only lines, at
// the end of [start, end], and returns the new end.
func (s *splicer) trim(start, end int, comments bool) int {
	for end > start {
		t := strings.TrimSpace(s.lines[end-1])
		if t != "" && (!comments || !strings.HasPrefix(t, "#")) {
			break
		}
		end--
	}
	return end
}

// keepChomping reports whether the block scalar header at column col of
// line ("|+", ">2+") has the keep indicator.
func keepChomping(line string, col int) bool {
	if col-1 >= len(line) {
		return false
	}
	h := line[col-1:]
	if i := strings.IndexAny(h, " \t#\n"); i >= 0 {
		h = h[:i]
	}
	return strings.Contains(h, "+")
}

// scalarKeyColumn is the column of the key of n's last scalar: col when n
// is that scalar (the entry's key is at col), the column of the last key of
// the deepest last mapping, or 0 when the scalar is a sequence item.
func scalarKeyColumn(n *yaml.Node, col int) int {
	for n != nil && (n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode) && len(n.Content) > 0 {
		switch n.Kind {
		case yaml.MappingNode:
			col = n.Content[len(n.Content)-2].Column
		case yaml.SequenceNode:
			col = 0
		}
		n = n.Content[len(n.Content)-1]
	}
	return col
}

// lastScalar returns the last scalar of n, or nil.
func lastScalar(n *yaml.Node) *yaml.Node {
	for n != nil && (n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode) && len(n.Content) > 0 {
		n = n.Content[len(n.Content)-1]
	}
	if n == nil || n.Kind != yaml.ScalarNode {
		return nil
	}
	return n
}

// entryText encodes key: value as a block mapping entry starting at column
// col. Its first line starts at col (the caller keeps the text before it),
// unless whole is set: then every line, the first included, is indented.
func (s *splicer) entryText(k, v *yaml.Node, col int, whole bool) string {
	// Head and foot comments stay where they are in the file, outside the
	// replaced lines (trimEnd).
	key := *k
	key.HeadComment, key.FootComment = "", ""
	val := *v
	val.FootComment = ""
	if val.Kind == yaml.ScalarNode {
		val.HeadComment = ""
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{&key, &val}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if s.compactSeq {
		enc.CompactSeqIndent()
	}
	keepFlowNulls(m, false)
	if err := enc.Encode(m); err != nil {
		s.failed = true
		return ""
	}
	if err := enc.Close(); err != nil {
		s.failed = true
		return ""
	}
	// Every line keeps its ending: the trailing empty lines of a "+"
	// (keep) block scalar are part of its value.
	pad := strings.Repeat(" ", col-1)
	out := splitKeep(buf.String())
	for i := range out {
		if (i > 0 || whole) && out[i] != "\n" {
			out[i] = pad + out[i]
		}
	}
	return strings.Join(out, "")
}

// apply writes the splices.
func (s *splicer) apply() []byte {
	// Last line first, so earlier line numbers hold; of two insertions at
	// one line, the one added last first, so they land in their order.
	for i := range s.edits {
		s.edits[i].seq = i
	}
	sort.Slice(s.edits, func(i, j int) bool {
		if s.edits[i].start != s.edits[j].start {
			return s.edits[i].start > s.edits[j].start
		}
		return s.edits[i].seq > s.edits[j].seq
	})
	lines := append([]string(nil), s.lines...)
	for _, ed := range s.edits {
		if ed.end < ed.start {
			at := ed.start - 1
			ins := splitKeep(ed.text)
			if at > 0 && !strings.HasSuffix(lines[at-1], "\n") {
				lines[at-1] += "\n"
			}
			lines = append(lines[:at], append(ins, lines[at:]...)...)
			continue
		}
		prefix := ""
		if ed.col > 1 {
			prefix = lines[ed.start-1][:ed.col-1]
		}
		var repl []string
		if ed.text != "" {
			repl = splitKeep(prefix + ed.text)
		}
		// The file's last line keeps its missing line ending.
		if ed.end == len(lines) && !strings.HasSuffix(lines[len(lines)-1], "\n") && len(repl) > 0 {
			repl[len(repl)-1] = strings.TrimSuffix(repl[len(repl)-1], "\n")
		}
		lines = append(lines[:ed.start-1], append(repl, lines[ed.end:]...)...)
	}
	return []byte(strings.Join(lines, ""))
}

func splitKeep(s string) []string {
	out := strings.SplitAfter(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// commentLines is the number of lines a head comment takes.
func commentLines(c string) int {
	if c == "" {
		return 0
	}
	return strings.Count(c, "\n") + 1
}

// sameNode reports whether a and b hold the same data and style: the
// original node and its edited copy are equal unless an edit changed it.
func sameNode(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || a.Style != b.Style || a.Anchor != b.Anchor ||
		len(a.Content) != len(b.Content) || a.LineComment != b.LineComment || (a.Alias == nil) != (b.Alias == nil) {
		return false
	}
	for i := range a.Content {
		if !sameNode(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// sameData reports whether out decodes to the data of the edited document
// node (nodeData: duplicate keys and merge keys are data like any other, so
// every document compares). It returns errAliasExpansion for a document
// whose aliases expand past aliasBudget.
func sameData(out []byte, edited *yaml.Node) (bool, error) {
	want, err := (&dataReader{budget: aliasBudget(edited)}).nodeData(edited, 0)
	if err != nil {
		return false, err
	}
	var parsed yaml.Node
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		return false, nil
	}
	have, err := (&dataReader{budget: aliasBudget(&parsed)}).nodeData(&parsed, 0)
	if err != nil {
		return false, nil
	}
	return reflect.DeepEqual(want, have), nil
}

// errAliasExpansion refuses a document whose aliases name aliases of
// aliases, so that its data is exponentially larger than its text (a
// "billion laughs" document: 460 bytes took two minutes to compare, #2078).
var errAliasExpansion = errors.New("the file's YAML aliases expand to far more values than it holds " +
	"(aliases of aliases, nested); kardinal does not edit it")

// aliasBudget is how many nodes nodeData may visit in n: ten times the
// nodes n holds, and at least 10,000. A document without aliases visits
// each node once; one whose aliases repeat whole subtrees, as values files
// do, stays within it.
func aliasBudget(n *yaml.Node) int {
	count := 0
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		count++
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(n)
	return max(10*count, 10_000)
}

// dataReader reads the data of a document (nodeData), visiting at most
// budget nodes.
type dataReader struct{ budget int }

// nodeData is the data of n: scalars decoded with their tags, mappings as
// ordered key/value pairs (duplicates and "<<" kept as written), aliases
// resolved to the data they name.
func (r *dataReader) nodeData(n *yaml.Node, depth int) (interface{}, error) {
	if depth > 64 {
		return nil, fmt.Errorf("nodeData: too deep")
	}
	if r.budget--; r.budget < 0 {
		return nil, errAliasExpansion
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return r.nodeData(n.Content[0], depth+1)
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("nodeData: alias without target")
		}
		return r.nodeData(n.Alias, depth+1)
	case yaml.ScalarNode:
		var v interface{}
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	case yaml.SequenceNode:
		out := make([]interface{}, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := r.nodeData(c, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := make([][2]interface{}, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, err := r.nodeData(n.Content[i], depth+1)
			if err != nil {
				return nil, err
			}
			v, err := r.nodeData(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, [2]interface{}{k, v})
		}
		return out, nil
	}
	return nil, fmt.Errorf("nodeData: node kind %d", n.Kind)
}
