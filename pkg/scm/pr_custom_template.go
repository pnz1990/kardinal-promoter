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

package scm

import (
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tmplsafe"
)

// This file renders the templates of an environment's pr config
// (v1alpha1.PRConfig): the PR title and body, the label, reviewer and
// assignee lists, and the merge commit message. Every template gets the same
// data, PRTemplateData, and the same functions; the evidence sections of the
// default body are functions too, so a custom body can keep them.

// maxPRTitle is the longest title kardinal sends, in characters. GitHub
// refuses titles over 256 characters, and GitLab's limit is 255.
const maxPRTitle = 255

// maxPRLabel is the longest label kardinal sends, in characters: GitHub's
// limit, the lowest of the providers that have labels.
const maxPRLabel = 50

// maxPRListEntries bounds how many labels, reviewers or assignees one list
// renders to: a template with a range over a long list must not fan out
// into hundreds of API calls.
const maxPRListEntries = 50

// PRTemplateData is the data of every pr template.
type PRTemplateData struct {
	// Pipeline is the Pipeline name.
	Pipeline string
	// Environment is the target environment name.
	Environment string
	// Bundle describes the Bundle being promoted.
	Bundle PRTemplateBundle
	// IsRollback is true for the PR of a rollback Bundle.
	IsRollback bool
	// Rollback describes the rollback; its fields are empty for a promotion.
	Rollback PRTemplateRollback
	// PR is the PR kardinal opened. Only the merge commit message has it;
	// the other templates render before the PR exists and see zero values.
	PR PRTemplatePR
}

// PRTemplateBundle is the Bundle in PRTemplateData. Every field is a value,
// so a template never dereferences a nil pointer.
type PRTemplateBundle struct {
	// Name is the Bundle resource name.
	Name string
	// Type is image, config or mixed.
	Type string
	// Version is the version the Bundle deploys (BundleVersion): the tag of
	// a one-image Bundle, "<image>:<tag>, ..." for several, "config <sha>"
	// for a config Bundle.
	Version string
	// Images are the Bundle's images.
	Images []v1alpha1.ImageRef
	// ConfigCommitSHA is the config commit of a config or mixed Bundle.
	ConfigCommitSHA string
	// Author, CommitSHA and CIRunURL are the Bundle's provenance, empty when
	// it has none.
	Author    string
	CommitSHA string
	CIRunURL  string
}

// PRTemplateRollback describes a rollback in PRTemplateData.
type PRTemplateRollback struct {
	// Of is the Bundle whose state the rollback restores.
	Of string
	// From is the Bundle the rollback replaces, when recorded.
	From string
	// By is who asked for the rollback, when recorded.
	By string
	// Restores is the version the rollback deploys.
	Restores string
}

// PRTemplatePR is the opened PR in PRTemplateData.
type PRTemplatePR struct {
	Number int
	URL    string
	Title  string
}

// NewPRTemplateData returns the template data of the PR whose default body
// is body.
func NewPRTemplateData(body PRBody) PRTemplateData {
	d := PRTemplateData{
		Pipeline:    body.PipelineName,
		Environment: body.Environment,
		Bundle: PRTemplateBundle{
			Name:    body.BundleName,
			Type:    body.Bundle.Type,
			Version: BundleVersion(body.Bundle),
			Images:  body.Bundle.Images,
		},
		IsRollback: body.RollbackOf != "",
	}
	if c := body.Bundle.ConfigRef; c != nil {
		d.Bundle.ConfigCommitSHA = c.CommitSHA
	}
	if p := body.Bundle.Provenance; p != nil {
		d.Bundle.Author, d.Bundle.CommitSHA, d.Bundle.CIRunURL = p.Author, p.CommitSHA, p.CIRunURL
	}
	if d.IsRollback {
		d.Rollback = PRTemplateRollback{Of: body.RollbackOf, From: body.RollbackFrom, By: body.RolledBackBy, Restores: body.RestoredVersion}
	}
	return d
}

// prTemplateFuncs returns the functions of a pr template. The evidence
// functions render the sections of the default body for body.
func prTemplateFuncs(body PRBody) template.FuncMap {
	section := func(name string) func() (string, error) {
		return func() (string, error) {
			s, err := renderSection(name, body)
			return strings.Trim(s, "\n"), err
		}
	}
	return template.FuncMap{
		// The default body, whole.
		"evidence": func() (string, error) { return RenderPRBody(body) },
		// Its sections.
		"heading":         section("heading"),
		"rollbackNotice":  section("rollbackNotice"),
		"provenanceTable": section("provenance"),
		"gatesTable":      section("gates"),
		"upstreamTable":   section("upstream"),
		// Text helpers.
		"mdcell":     mdCellReplacer.Replace,
		"join":       func(sep string, items []string) string { return strings.Join(items, sep) },
		"lower":      strings.ToLower,
		"upper":      strings.ToUpper,
		"trimSpace":  strings.TrimSpace,
		"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
		"replace":    func(old, repl, s string) string { return strings.ReplaceAll(s, old, repl) },
		"contains":   func(substr, s string) bool { return strings.Contains(s, substr) },
		"hasPrefix":  func(prefix, s string) bool { return strings.HasPrefix(s, prefix) },
		"truncate":   func(n int, s string) string { return truncateRunes(s, n) },
		"default": func(def, s string) string {
			if s == "" {
				return def
			}
			return s
		},
	}
}

// truncateRunes returns the first n characters of s.
func truncateRunes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// The limits of the pr templates (pkg/tmplsafe): a body may be as long as
// GitHub takes (65536 characters), and the other fields far less.
var (
	prBodyLimits  = tmplsafe.Limits{MaxOutput: 64 << 10, MaxFuncInput: 64 << 10, MaxFuncOutput: 64 << 10, MaxRangeDepth: 2}
	prShortLimits = tmplsafe.Limits{MaxOutput: 4 << 10, MaxFuncInput: 64 << 10, MaxFuncOutput: 64 << 10, MaxRangeDepth: 2}
	prMergeLimits = tmplsafe.Limits{MaxOutput: 16 << 10, MaxFuncInput: 64 << 10, MaxFuncOutput: 64 << 10, MaxRangeDepth: 2}
)

// renderPRTemplate parses and executes one pr template in the tmplsafe
// sandbox: a template written in a Pipeline runs in the controller, so it
// may not declare variables, recurse, loop over a number or grow past lim.
// A template is parsed per render, with its functions bound to body:
// parsing is cheap, and it keeps the shared state of a render out of the
// template.
func renderPRTemplate(field, text string, lim tmplsafe.Limits, body PRBody, data PRTemplateData) (string, error) {
	t, err := tmplsafe.Parse(field, text, prTemplateFuncs(body), lim)
	if err != nil {
		return "", fmt.Errorf("pr.%s: %w", field, err)
	}
	out, err := t.Execute(data)
	if err != nil {
		return "", fmt.Errorf("pr.%s: %w", field, err)
	}
	return out, nil
}

// RenderedPR is an environment's pr config rendered for one PR.
type RenderedPR struct {
	Title         string
	Body          string
	Labels        []string
	Reviewers     []string
	TeamReviewers []string
	Assignees     []string
}

// RenderPR renders the title, body and lists of cfg for the PR whose default
// title is defaultTitle and default body is body's. A nil cfg, or an empty
// template, keeps the default.
func RenderPR(cfg *v1alpha1.PRConfig, defaultTitle string, body PRBody) (RenderedPR, error) {
	out := RenderedPR{Title: defaultTitle}
	if cfg == nil || cfg.BodyTemplate == "" {
		b, err := RenderPRBody(body)
		if err != nil {
			return RenderedPR{}, err
		}
		out.Body = b
	}
	if cfg == nil {
		return out, nil
	}
	data := NewPRTemplateData(body)
	if cfg.TitleTemplate != "" {
		title, err := renderPRTemplate("titleTemplate", cfg.TitleTemplate, prShortLimits, body, data)
		if err != nil {
			return RenderedPR{}, err
		}
		title = strings.Join(strings.Fields(title), " ")
		if title == "" {
			return RenderedPR{}, errors.New("pr.titleTemplate: rendered an empty title")
		}
		out.Title = truncateRunes(title, maxPRTitle)
	}
	if cfg.BodyTemplate != "" {
		b, err := renderPRTemplate("bodyTemplate", cfg.BodyTemplate, prBodyLimits, body, data)
		if err != nil {
			return RenderedPR{}, err
		}
		// The marker names kardinal's PRs, as in the default body.
		out.Body = "<!-- kardinal-promoter auto-generated PR -->\n" + b
	}
	var err error
	if out.Labels, err = renderPRList("labels", cfg.Labels, body, data); err != nil {
		return RenderedPR{}, err
	}
	for _, l := range out.Labels {
		if utf8.RuneCountInString(l) > maxPRLabel {
			return RenderedPR{}, fmt.Errorf("pr.labels: label %q is longer than %d characters", l, maxPRLabel)
		}
		if strings.Contains(l, ",") {
			// GitLab takes labels as a comma-separated list.
			return RenderedPR{}, fmt.Errorf("pr.labels: label %q has a comma", l)
		}
	}
	if out.Reviewers, err = renderPRList("reviewers", cfg.Reviewers, body, data); err != nil {
		return RenderedPR{}, err
	}
	if out.TeamReviewers, err = renderPRList("teamReviewers", cfg.TeamReviewers, body, data); err != nil {
		return RenderedPR{}, err
	}
	if out.Assignees, err = renderPRList("assignees", cfg.Assignees, body, data); err != nil {
		return RenderedPR{}, err
	}
	return out, nil
}

// renderPRList renders each template of a list. Each line of a template's
// output is one entry; blank lines and repeats are dropped, so a template
// that renders nothing (an empty .Bundle.Author) adds nothing.
func renderPRList(field string, templates []string, body PRBody, data PRTemplateData) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for i, text := range templates {
		s, err := renderPRTemplate(fmt.Sprintf("%s[%d]", field, i), text, prShortLimits, body, data)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
	}
	if len(out) > maxPRListEntries {
		return nil, fmt.Errorf("pr.%s: renders %d entries, more than %d", field, len(out), maxPRListEntries)
	}
	return out, nil
}

// MergeOptions is how the SCM merges a PR kardinal enabled auto-merge on.
type MergeOptions struct {
	// Method is merge, squash or rebase. Empty means merge.
	Method string
	// CommitTitle and CommitBody are the merge commit message. Empty leaves
	// the SCM's default.
	CommitTitle string
	CommitBody  string
}

// RenderMergeOptions renders the merge options of m for the PR whose default
// body is body. pr is the opened PR.
func RenderMergeOptions(m *v1alpha1.PRMergeConfig, body PRBody, pr PRTemplatePR) (MergeOptions, error) {
	opts := MergeOptions{Method: m.Method}
	if opts.Method == "" {
		opts.Method = MergeMethodMerge
	}
	if m.CommitMessageTemplate == "" {
		return opts, nil
	}
	data := NewPRTemplateData(body)
	data.PR = pr
	msg, err := renderPRTemplate("merge.commitMessageTemplate", m.CommitMessageTemplate, prMergeLimits, body, data)
	if err != nil {
		return MergeOptions{}, err
	}
	title, rest, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	opts.CommitTitle = truncateRunes(strings.TrimSpace(title), maxPRTitle)
	opts.CommitBody = strings.TrimSpace(rest)
	if opts.CommitTitle == "" {
		return MergeOptions{}, errors.New("pr.merge.commitMessageTemplate: rendered an empty message")
	}
	return opts, nil
}

// The merge methods of pr.merge.method.
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// ValidatePRConfig checks that every template of cfg parses and renders, for
// a promotion and for a rollback, with sample data. The Pipeline reconciler
// and "kardinal validate" report its error; a template that only fails on
// real data (a label too long for one Bundle) fails the step that renders
// it. A nil cfg is valid.
func ValidatePRConfig(cfg *v1alpha1.PRConfig) error {
	if cfg == nil {
		return nil
	}
	at := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	promotion := PRBody{
		PipelineName: "pipeline", Environment: "env", BundleName: "bundle",
		Bundle: v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: "registry/app", Tag: "1.0.0"}},
			Provenance: &v1alpha1.BundleProvenance{Author: "author", CommitSHA: "0123456789abcdef", CIRunURL: "https://ci.example/run/1"}},
		GateResults:          []v1alpha1.GateResult{{GateName: "gate", Result: "Pass", EvaluatedAt: at}},
		UpstreamEnvironments: []PRBodyUpstreamEnv{{Name: "test", HealthCheckedAt: &at, Elapsed: "5m"}},
	}
	rollback := promotion
	rollback.RollbackOf, rollback.RollbackFrom, rollback.RolledBackBy, rollback.RestoredVersion = "previous", "current", "user", "0.9.0"
	for _, body := range []PRBody{promotion, rollback} {
		if _, err := RenderPR(cfg, "title", body); err != nil {
			return err
		}
		if cfg.Merge != nil {
			if _, err := RenderMergeOptions(cfg.Merge, body, PRTemplatePR{Number: 1, URL: "https://scm.example/pr/1", Title: "title"}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidatePipelinePR runs ValidatePRConfig on the pr config of every
// environment of p.
func ValidatePipelinePR(p *v1alpha1.Pipeline) error {
	for _, env := range p.Spec.Environments {
		if err := ValidatePRConfig(env.PR); err != nil {
			return fmt.Errorf("environment %q: %w", env.Name, err)
		}
	}
	return nil
}
