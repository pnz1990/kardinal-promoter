// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package source

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	mmsemver "github.com/Masterminds/semver/v3"
)

// TagFilters select the tags (image tags or chart versions) a watcher
// considers. They apply in order: Allow, Include, Exclude, Ignore,
// SemverConstraint. The zero value keeps every tag.
type TagFilters struct {
	// Include is a regular expression tags must match (spec tagFilter).
	Include string
	// Exclude is a regular expression that drops the tags it matches.
	Exclude string
	// SemverConstraint keeps the semantic versions that satisfy it
	// (Masterminds syntax) and drops every other tag.
	SemverConstraint string
	// Allow, when not empty, keeps only these tags.
	Allow []string
	// Ignore drops these tags.
	Ignore []string
}

// onlyInclude reports whether Include is the only filter set: the error for
// "nothing matches" then keeps the wording of the tagFilter-only releases.
func (f TagFilters) onlyInclude() bool {
	return f.Exclude == "" && f.SemverConstraint == "" && len(f.Allow) == 0 && len(f.Ignore) == 0
}

// describe lists the filters that are set, for an error message.
func (f TagFilters) describe() string {
	var parts []string
	if len(f.Allow) > 0 {
		parts = append(parts, fmt.Sprintf("allowTags %q", f.Allow))
	}
	if f.Include != "" {
		parts = append(parts, fmt.Sprintf("tagFilter %q", f.Include))
	}
	if f.Exclude != "" {
		parts = append(parts, fmt.Sprintf("excludeTagFilter %q", f.Exclude))
	}
	if len(f.Ignore) > 0 {
		parts = append(parts, fmt.Sprintf("ignoreTags %q", f.Ignore))
	}
	if f.SemverConstraint != "" {
		parts = append(parts, fmt.Sprintf("semverConstraint %q", f.SemverConstraint))
	}
	if len(parts) == 0 {
		return "no filters"
	}
	return strings.Join(parts, ", ")
}

// compiledFilters is TagFilters with its expressions parsed.
type compiledFilters struct {
	include, exclude *regexp.Regexp
	constraint       *mmsemver.Constraints
	allow, ignore    map[string]bool
}

// compile parses the regular expressions and the constraint. what names the
// spec fields' owner in errors ("tagFilter", "excludeTagFilter").
func (f TagFilters) compile() (*compiledFilters, error) {
	c := &compiledFilters{}
	var err error
	if f.Include != "" {
		if c.include, err = regexp.Compile(f.Include); err != nil {
			return nil, fmt.Errorf("invalid tagFilter regex %q: %w", f.Include, err)
		}
	}
	if f.Exclude != "" {
		if c.exclude, err = regexp.Compile(f.Exclude); err != nil {
			return nil, fmt.Errorf("invalid excludeTagFilter regex %q: %w", f.Exclude, err)
		}
	}
	if f.SemverConstraint != "" {
		if c.constraint, err = mmsemver.NewConstraint(f.SemverConstraint); err != nil {
			return nil, fmt.Errorf("invalid semverConstraint %q: %w", f.SemverConstraint, err)
		}
	}
	if len(f.Allow) > 0 {
		c.allow = make(map[string]bool, len(f.Allow))
		for _, t := range f.Allow {
			c.allow[t] = true
		}
	}
	if len(f.Ignore) > 0 {
		c.ignore = make(map[string]bool, len(f.Ignore))
		for _, t := range f.Ignore {
			c.ignore[t] = true
		}
	}
	return c, nil
}

// apply returns the tags that pass every filter, in their listed order.
func (c *compiledFilters) apply(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if c.keep(t) {
			out = append(out, t)
		}
	}
	return out
}

func (c *compiledFilters) keep(tag string) bool {
	if c.allow != nil && !c.allow[tag] {
		return false
	}
	if c.include != nil && !c.include.MatchString(tag) {
		return false
	}
	if c.exclude != nil && c.exclude.MatchString(tag) {
		return false
	}
	if c.ignore[tag] {
		return false
	}
	if c.constraint != nil {
		v, ok := parseSemverTag(tag)
		if !ok || !c.constraint.Check(v) {
			return false
		}
	}
	return true
}

// parseSemverTag parses a full semantic version tag (optional "v" prefix).
// Partial versions such as "1.2" are not semantic version tags.
func parseSemverTag(tag string) (*mmsemver.Version, bool) {
	if !semverTag.MatchString(tag) {
		return nil, false
	}
	v, err := mmsemver.NewVersion(tag)
	if err != nil {
		return nil, false
	}
	return v, true
}

// semverOnly returns the tags that are semantic versions.
func semverOnly(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if _, ok := parseSemverTag(t); ok {
			out = append(out, t)
		}
	}
	return out
}

// maxLexical returns the tag that sorts last (byte order).
func maxLexical(tags []string) string {
	sorted := append([]string(nil), tags...)
	sort.Strings(sorted)
	return sorted[len(sorted)-1]
}
