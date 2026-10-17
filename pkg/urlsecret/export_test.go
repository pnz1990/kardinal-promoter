// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package urlsecret

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// CRDRules are the CEL expressions of the Bundle CRD's ciRunURL rules
// (api/v1alpha1/bundle_types.go), as this package decides. A rule that
// reads each parameter name (findAll, then a check per name) costs more
// than a CRD rule may on a 2048-character field, so the rules are cheap
// regular expressions over the URL normalized once (lower case, no "-" or
// "_": a parameter's value cannot hold its delimiter, so names are
// normalized as normalize does): user info; a name ending in a suffix
// other than "key", or an exact name (an array index after it allowed); a
// name ending in "key" once every Harmless name, with its delimiter, is
// replaced by a neutral one; and a percent-encoded name, which CEL cannot
// decode. TestCRDRules pins the CRD to them.
func CRDRules() []string {
	norm := `self.lowerAscii().replace('-', '').replace('_', '')`
	index := `(\\[[^=&;#]*\\])*=`
	var suffixes, stems []string
	for _, s := range Suffixes {
		if s != "key" {
			suffixes = append(suffixes, s)
		}
	}
	for _, h := range Harmless {
		stems = append(stems, strings.TrimSuffix(h, "key"))
	}
	return []string{
		`!self.matches('^[A-Za-z][A-Za-z0-9+.-]*://[^/]*@')`,
		`!` + norm + `.matches('[?&;#][^=&;#[]*(` + strings.Join(suffixes, "|") + `)` + index + `')`,
		`!` + norm + `.matches('[?&;#](` + strings.Join(Exact, "|") + `)` + index + `')`,
		`!` + norm + `.matches('[?&;#]` + notOneOf(stems) + `key` + index + `')`,
		`!self.matches('[?&;#][^=&;#]*%[0-9A-Fa-f]{2}[^=&;#]*=')`,
	}
}

// nameChar is a character of a parameter name.
const nameChar = `=&;#[`

// notOneOf is an RE2 expression (no lookaround) for every name, possibly
// empty, that is not one of words: their complement, from their trie. At
// each node a name may end (unless a word ends there), go on with a
// character no word does (then anything), or follow a word.
func notOneOf(words []string) string {
	type node struct {
		end      bool
		children map[byte]*node
	}
	root := &node{children: map[byte]*node{}}
	for _, w := range words {
		n := root
		for i := 0; i < len(w); i++ {
			c, ok := n.children[w[i]]
			if !ok {
				c = &node{children: map[byte]*node{}}
				n.children[w[i]] = c
			}
			n = c
		}
		n.end = true
	}
	var walk func(n *node) string
	walk = func(n *node) string {
		var keys []byte
		for k := range n.children {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		var alts []string
		if !n.end {
			alts = append(alts, "")
		}
		alts = append(alts, "[^"+nameChar+string(keys)+"][^"+nameChar+"]*")
		for _, k := range keys {
			alts = append(alts, string(k)+walk(n.children[k]))
		}
		return "(" + strings.Join(alts, "|") + ")"
	}
	return walk(root)
}

// TestNotOneOf: the complement matches every name but the words.
func TestNotOneOf(t *testing.T) {
	re := regexp.MustCompile(`^` + notOneOf([]string{"mon", "hot", "hoc", "primary"}) + `$`)
	for name, want := range map[string]bool{
		"": true, "api": true, "mo": true, "monx": true, "ho": true, "hox": true, "prim": true, "primaryx": true,
		"mon": false, "hot": false, "hoc": false, "primary": false,
	} {
		if re.MatchString(name) != want {
			t.Errorf("%q: matched %v, want %v", name, !want, want)
		}
	}
}
