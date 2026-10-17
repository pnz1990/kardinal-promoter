// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package urlsecret

import "strings"

// CRDRules are the CEL expressions of the Bundle CRD's ciRunURL rules
// (api/v1alpha1/bundle_types.go), as this package decides: user info, a
// credential parameter (by suffix, with "-" or "_" allowed between the
// words of one, or by exact name), and a percent-encoded name, which CEL
// cannot decode. TestCRDRules pins the CRD to them.
func CRDRules() []string {
	var suffixes []string
	for _, s := range Suffixes {
		suffixes = append(suffixes, separated(s))
	}
	var exact []string
	for _, e := range Exact {
		exact = append(exact, separated(e))
	}
	return []string{
		`!self.matches('^[A-Za-z][A-Za-z0-9+.-]*://[^/]*@')`,
		`!self.matches('(?i)[?&;#][^=&;#]*(` + strings.Join(suffixes, "|") + `)[-_]*=')`,
		`!self.matches('(?i)[?&;#][-_]*(` + strings.Join(exact, "|") + `)[-_]*=')`,
		`!self.matches('[?&;#][^=&;#]*%[0-9A-Fa-f]{2}[^=&;#]*=')`,
	}
}

// separated is word as a CEL (RE2) pattern that allows "-" or "_" between
// its letters, as normalize strips them.
func separated(word string) string {
	var b strings.Builder
	for i, r := range word {
		if i > 0 {
			b.WriteString("[-_]*")
		}
		b.WriteRune(r)
	}
	return b.String()
}
