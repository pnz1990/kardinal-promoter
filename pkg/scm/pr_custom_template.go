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
// renders to: a list must not fan out into hundreds of API calls.
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
// is body. Every string is cut to maxTemplateValue characters and the image
// list to maxTemplateImages entries, so what a template reads is bounded
// whatever the Bundle holds.
func NewPRTemplateData(body PRBody) PRTemplateData {
	short := func(s string) string { return tmplsafe.TruncateRunes(s, maxTemplateValue) }
	images := body.Bundle.Images
	if len(images) > maxTemplateImages {
		images = images[:maxTemplateImages]
	}
	cut := make([]v1alpha1.ImageRef, len(images))
	for i, img := range images {
		cut[i] = v1alpha1.ImageRef{Repository: short(img.Repository), Tag: short(img.Tag), Digest: short(img.Digest)}
	}
	d := PRTemplateData{
		Pipeline:    short(body.PipelineName),
		Environment: short(body.Environment),
		Bundle: PRTemplateBundle{
			Name:    short(body.BundleName),
			Type:    short(body.Bundle.Type),
			Version: short(BundleVersion(body.Bundle)),
			Images:  cut,
		},
		IsRollback: body.RollbackOf != "",
	}
	if c := body.Bundle.ConfigRef; c != nil {
		d.Bundle.ConfigCommitSHA = short(c.CommitSHA)
	}
	if p := body.Bundle.Provenance; p != nil {
		d.Bundle.Author, d.Bundle.CommitSHA, d.Bundle.CIRunURL = short(p.Author), short(p.CommitSHA), short(p.CIRunURL)
	}
	if d.IsRollback {
		d.Rollback = PRTemplateRollback{Of: short(body.RollbackOf), From: short(body.RollbackFrom),
			By: short(body.RolledBackBy), Restores: short(body.RestoredVersion)}
	}
	return d
}

// The template data is bounded: at most maxTemplateImages images, and each
// string value at most maxTemplateValue characters.
const (
	maxTemplateImages = 20
	maxTemplateValue  = 1024
)

// multiLineValue names a data value with a line break. A list template
// renders one entry per line, so such a value (an author "alice\nbob")
// would make two entries; the list is refused instead. Every string of the
// data is checked.
func multiLineValue(d PRTemplateData) string {
	values := []struct{ name, v string }{
		{".Pipeline", d.Pipeline}, {".Environment", d.Environment},
		{".Bundle.Name", d.Bundle.Name}, {".Bundle.Type", d.Bundle.Type}, {".Bundle.Version", d.Bundle.Version},
		{".Bundle.ConfigCommitSHA", d.Bundle.ConfigCommitSHA},
		{".Bundle.Author", d.Bundle.Author}, {".Bundle.CommitSHA", d.Bundle.CommitSHA}, {".Bundle.CIRunURL", d.Bundle.CIRunURL},
		{".Rollback.Of", d.Rollback.Of}, {".Rollback.From", d.Rollback.From},
		{".Rollback.By", d.Rollback.By}, {".Rollback.Restores", d.Rollback.Restores},
		{".PR.URL", d.PR.URL}, {".PR.Title", d.PR.Title},
	}
	for _, x := range values {
		if strings.ContainsAny(x.v, "\r\n") {
			return x.name
		}
	}
	for _, img := range d.Bundle.Images {
		if strings.ContainsAny(img.Repository+img.Tag+img.Digest, "\r\n") {
			return ".Bundle.Images"
		}
	}
	return ""
}

// imageList is one line per image of d: <repository>:<tag>, with
// @<digest> when the image has one. The template language has no loop, so
// this is how a template lists the images.
func imageList(d PRTemplateData) string {
	lines := make([]string, 0, len(d.Bundle.Images))
	for _, img := range d.Bundle.Images {
		ref := img.Repository
		if img.Tag != "" {
			ref += ":" + img.Tag
		}
		if img.Digest != "" {
			ref += "@" + img.Digest
		}
		lines = append(lines, ref)
	}
	return strings.Join(lines, "\n")
}

// prTemplateFuncs returns the functions of a pr template. The evidence
// functions return the sections of the default body for body, rendered once
// before the template (their size is known before a call). The text helpers
// are tmplsafe.StringFuncs.
// renderPRBodyFn and renderSectionFn render the evidence for the template
// functions; a test counts the calls through them.
var (
	renderPRBodyFn  = RenderPRBody
	renderSectionFn = renderSection
)

func prTemplateFuncs(body PRBody, data PRTemplateData) tmplsafe.FuncMap {
	funcs := tmplsafe.StringFuncs()
	funcs["imageList"] = tmplsafe.Lazy(func() (string, error) { return imageList(data), nil })
	// The evidence sections render on first use, once per RenderPR or
	// RenderMergeOptions, whatever the number of templates.
	funcs["evidence"] = tmplsafe.Lazy(func() (string, error) { return renderPRBodyFn(body) })
	for fn, section := range map[string]string{
		"heading": "heading", "rollbackNotice": "rollbackNotice", "provenanceTable": "provenance",
		"gatesTable": "gates", "upstreamTable": "upstream",
	} {
		funcs[fn] = tmplsafe.Lazy(func() (string, error) {
			text, err := renderSectionFn(section, body)
			return strings.Trim(text, "\n"), err
		})
	}
	// mdcell at most doubles its input (| becomes \|).
	funcs["mdcell"] = tmplsafe.Func{Fn: mdCellReplacer.Replace, Size: func(a []interface{}) (int, error) {
		s, _ := a[0].(string)
		return 2 * len(s), nil
	}}
	return funcs
}

// The limits of the pr templates (pkg/tmplsafe): a body may be as long as
// GitHub takes (65536 characters), and the other fields far less.
var (
	prBodyLimits  = prLimits(64 << 10)
	prShortLimits = prLimits(4 << 10)
	prMergeLimits = prLimits(16 << 10)
)

// prLimits are tmplsafe.DefaultLimits with output at most maxOutput bytes.
func prLimits(maxOutput int) tmplsafe.Limits {
	l := tmplsafe.DefaultLimits
	l.MaxOutput = maxOutput
	l.RangeHint = "use the functions that list the data (provenanceTable, gatesTable, upstreamTable, imageList)"
	return l
}

// renderPRTemplate parses and executes one pr template in the tmplsafe
// sandbox: a template written in a Pipeline runs in the controller, so it
// may not declare variables, recurse, loop or grow past lim.
// A template is parsed per render, with its functions bound to body:
// parsing is cheap, and it keeps the shared state of a render out of the
// template.
func renderPRTemplate(field, text string, lim tmplsafe.Limits, funcs tmplsafe.FuncMap, data PRTemplateData) (string, error) {
	t, err := tmplsafe.Parse(field, text, funcs, lim)
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
	funcs := prTemplateFuncs(body, data)
	if cfg.TitleTemplate != "" {
		title, err := renderPRTemplate("titleTemplate", cfg.TitleTemplate, prShortLimits, funcs, data)
		if err != nil {
			return RenderedPR{}, err
		}
		title = strings.Join(strings.Fields(title), " ")
		if title == "" {
			return RenderedPR{}, errors.New("pr.titleTemplate: rendered an empty title")
		}
		out.Title = tmplsafe.TruncateRunes(title, maxPRTitle)
	}
	if cfg.BodyTemplate != "" {
		b, err := renderPRTemplate("bodyTemplate", cfg.BodyTemplate, prBodyLimits, funcs, data)
		if err != nil {
			return RenderedPR{}, err
		}
		// The marker names kardinal's PRs, as in the default body.
		out.Body = "<!-- kardinal-promoter auto-generated PR -->\n" + b
	}
	var err error
	if out.Labels, err = renderPRList(funcs, "labels", cfg.Labels, body, data); err != nil {
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
	if out.Reviewers, err = renderPRList(funcs, "reviewers", cfg.Reviewers, body, data); err != nil {
		return RenderedPR{}, err
	}
	if out.TeamReviewers, err = renderPRList(funcs, "teamReviewers", cfg.TeamReviewers, body, data); err != nil {
		return RenderedPR{}, err
	}
	if out.Assignees, err = renderPRList(funcs, "assignees", cfg.Assignees, body, data); err != nil {
		return RenderedPR{}, err
	}
	return out, nil
}

// renderPRList renders each template of a list. Each line of a template's
// output is one entry; blank lines and repeats are dropped, so a template
// that renders nothing (an empty .Bundle.Author) adds nothing.
func renderPRList(funcs tmplsafe.FuncMap, field string, templates []string, body PRBody, data PRTemplateData) ([]string, error) {
	if len(templates) == 0 {
		return nil, nil
	}
	if v := multiLineValue(data); v != "" {
		return nil, fmt.Errorf("pr.%s: %s has a line break, which would split into several entries", field, v)
	}
	var out []string
	seen := map[string]bool{}
	for i, text := range templates {
		s, err := renderPRTemplate(fmt.Sprintf("%s[%d]", field, i), text, prShortLimits, funcs, data)
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
	Method string `json:"method,omitempty"`
	// AllowImmediate lets kardinal merge a PR that has nothing pending
	// (pr.merge.allowImmediate).
	AllowImmediate bool `json:"allowImmediate,omitempty"`
	// CommitTitle and CommitBody are the merge commit message. Empty leaves
	// the SCM's default.
	CommitTitle string `json:"commitTitle,omitempty"`
	CommitBody  string `json:"commitBody,omitempty"`
}

// RenderMergeOptions renders the merge options of m for the PR whose default
// body is body. pr is the opened PR.
func RenderMergeOptions(m *v1alpha1.PRMergeConfig, body PRBody, pr PRTemplatePR) (MergeOptions, error) {
	opts := MergeOptions{Method: m.Method, AllowImmediate: m.AllowImmediate}
	if opts.Method == "" {
		opts.Method = MergeMethodMerge
	}
	if m.CommitMessageTemplate == "" {
		return opts, nil
	}
	data := NewPRTemplateData(body)
	data.PR = pr
	msg, err := renderPRTemplate("merge.commitMessageTemplate", m.CommitMessageTemplate, prMergeLimits, prTemplateFuncs(body, data), data)
	if err != nil {
		return MergeOptions{}, err
	}
	title, rest, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	opts.CommitTitle = tmplsafe.TruncateRunes(strings.TrimSpace(title), maxPRTitle)
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
