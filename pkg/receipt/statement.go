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

// Package receipt builds, signs, stores and verifies promotion receipts:
// in-toto statements with a SLSA verification summary predicate whose
// kardinalPromotion extension records one promotion (docs/receipts.md,
// docs/design/21-receipts.md).
//
// Building a statement reads nothing: the caller passes the objects it
// already read. Signing and storing reach the network and run only in the
// PromotionReceipt reconciler; verification runs in the CLI.
package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The statement's fixed values.
const (
	StatementType   = "https://in-toto.io/Statement/v1"
	PredicateVSA    = "https://slsa.dev/verification_summary/v1"
	PayloadType     = "application/vnd.in-toto+json"
	VerifierID      = "https://kardinal.io/controller"
	ResultPassed    = "PASSED"
	slsaVersion     = "1.0"
	digestSHA256    = "sha256"
	digestGitCommit = "gitCommit"
)

// Levels kardinal records in verifiedLevels.
const (
	LevelGatesPassed = "KARDINAL_GATES_PASSED"
	LevelApproved    = "KARDINAL_APPROVED"
	LevelHealthy     = "KARDINAL_HEALTHY"
	LevelOverridden  = "KARDINAL_OVERRIDDEN"
	// LevelVerifiedByAgent: a verify gate counted a passing Verdict.
	LevelVerifiedByAgent = "KARDINAL_VERIFIED_BY_AGENT"
)

// Statement is an in-toto v1 statement with the VSA predicate.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

// Subject is one artifact the statement is about.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Predicate is a SLSA verification summary v1 with the kardinalPromotion
// extension (in-toto allows extension fields; consumers ignore them).
type Predicate struct {
	Verifier           Verifier             `json:"verifier"`
	TimeVerified       string               `json:"timeVerified"`
	ResourceURI        string               `json:"resourceUri"`
	Policy             ResourceDescriptor   `json:"policy"`
	InputAttestations  []ResourceDescriptor `json:"inputAttestations,omitempty"`
	VerificationResult string               `json:"verificationResult"`
	VerifiedLevels     []string             `json:"verifiedLevels"`
	SlsaVersion        string               `json:"slsaVersion"`
	Kardinal           Record               `json:"kardinalPromotion"`
}

// Verifier names the controller that wrote the summary.
type Verifier struct {
	ID      string            `json:"id"`
	Version map[string]string `json:"version,omitempty"`
}

// ResourceDescriptor is a URI and digest.
type ResourceDescriptor struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest,omitempty"`
}

// Record is the kardinalPromotion extension: what kardinal knows about the
// promotion.
type Record struct {
	Receipt  string          `json:"receipt"`
	Pipeline string          `json:"pipeline"`
	Version  Version         `json:"version"`
	Actor    *v1alpha1.Actor `json:"actor,omitempty"`
	// RollbackOf is the Bundle a rollback restores.
	RollbackOf string `json:"rollbackOf,omitempty"`
	// DecisionSnapshot is false when the step started before the controller
	// took decision snapshots: its gates were read at Verified.
	DecisionSnapshot bool   `json:"decisionSnapshot"`
	Gates            []Gate `json:"gates"`
	OmittedGates     int    `json:"omittedGates,omitempty"`
	Checks           Checks `json:"checks"`
	Change           Change `json:"change"`
	Controller       string `json:"controller,omitempty"`
	Chart            string `json:"chart,omitempty"`
	Truncated        bool   `json:"truncated"`
}

// Version is the promoted Bundle.
type Version struct {
	Bundle     string                     `json:"bundle"`
	BundleUID  string                     `json:"bundleUID"`
	Version    string                     `json:"version,omitempty"`
	Type       string                     `json:"type"`
	Images     []string                   `json:"images,omitempty"`
	Config     string                     `json:"config,omitempty"`
	Provenance *v1alpha1.BundleProvenance `json:"provenance,omitempty"`
}

// Gate is one required gate as the step started on it.
type Gate struct {
	Name               string            `json:"name"`
	Template           string            `json:"template,omitempty"`
	TemplateUID        string            `json:"templateUID,omitempty"`
	TemplateGeneration int64             `json:"templateGeneration,omitempty"`
	TemplateDigest     map[string]string `json:"templateDigest,omitempty"`
	Expression         string            `json:"expression,omitempty"`
	Message            string            `json:"message,omitempty"`
	Result             string            `json:"result"`
	Reason             string            `json:"reason,omitempty"`
	EvaluatedAt        string            `json:"evaluatedAt,omitempty"`
	Override           *Override         `json:"override,omitempty"`
	ApprovalRequired   bool              `json:"approvalRequired"`
	Approvals          []Approval        `json:"approvals"`
	ApprovalsOmitted   int               `json:"approvalsOmitted,omitempty"`
	VerifyRequired     bool              `json:"verifyRequired,omitempty"`
	Verdicts           []Verdict         `json:"verdicts,omitempty"`
	VerdictsOmitted    int               `json:"verdictsOmitted,omitempty"`
}

// Verdict is one Verdict a verify gate counted or refused.
type Verdict struct {
	Name        string `json:"name"`
	Verifier    string `json:"verifier,omitempty"`
	Result      string `json:"result"`
	Counted     bool   `json:"counted"`
	Reason      string `json:"reason,omitempty"`
	EvidenceURL string `json:"evidenceURL,omitempty"`
	Findings    int    `json:"findings,omitempty"`
}

// Override is the override a gate passed by.
type Override struct {
	CreatedBy string          `json:"createdBy,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	ExpiresAt string          `json:"expiresAt,omitempty"`
	Verified  bool            `json:"verified"`
	Actor     *v1alpha1.Actor `json:"actor,omitempty"`
}

// Approval is one approval decision on a gate.
type Approval struct {
	User     string          `json:"user"`
	Actor    *v1alpha1.Actor `json:"actor,omitempty"`
	Decision string          `json:"decision"`
	Counted  bool            `json:"counted"`
	Reason   string          `json:"reason,omitempty"`
}

// Checks are the per-promotion checks at Verified.
type Checks struct {
	Metrics           []Metric     `json:"metrics,omitempty"`
	ImageVerification []ImageCheck `json:"imageVerification,omitempty"`
	Health            *Health      `json:"health,omitempty"`
}

// Metric is one per-promotion MetricCheck result.
type Metric struct {
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	Result     string `json:"result,omitempty"`
	ValidUntil string `json:"validUntil,omitempty"`
}

// ImageCheck is the ImageVerification result of one image.
type ImageCheck struct {
	Image    string `json:"image"`
	Verified bool   `json:"verified"`
	By       string `json:"by,omitempty"`
}

// Health is the health check's result.
type Health struct {
	Adapter string `json:"adapter"`
	Result  string `json:"result"`
	At      string `json:"at,omitempty"`
}

// Change is what changed in the environment.
type Change struct {
	Environment string `json:"environment"`
	PR          *PR    `json:"pr,omitempty"`
	// Commit is the commit pushed to the environment branch, for an
	// environment without a PR.
	Commit string `json:"commit,omitempty"`
}

// PR is the promotion's pull request.
type PR struct {
	URL         string `json:"url,omitempty"`
	Number      int    `json:"number,omitempty"`
	MergeCommit string `json:"mergeCommit,omitempty"`
	MergedAt    string `json:"mergedAt,omitempty"`
}

// Input is everything a statement is built from. The caller has read it.
type Input struct {
	// Name is the PromotionReceipt name.
	Name string
	// ClusterID identifies the cluster: the kube-system Namespace UID.
	ClusterID         string
	ControllerVersion string
	ChartVersion      string
	// Now is timeVerified.
	Now    time.Time
	Bundle *v1alpha1.Bundle
	Step   *v1alpha1.PromotionStep
	// Decision is the step's snapshot; Snapshot is false when it was taken
	// at Verified instead of when the step started.
	Decision *v1alpha1.PromotionDecision
	Snapshot bool
	Metrics  []v1alpha1.MetricCheck
	// ImageVerification is the Bundle's ImageVerification, nil when none.
	ImageVerification *v1alpha1.ImageVerification
	// HealthAdapter is the environment's health.type ("resource" when empty).
	HealthAdapter string
	PR            *PR
	// Commit is the commit pushed for an environment without a PR.
	Commit string
}

// ErrTooLarge is a statement over MaxReceiptStatementBytes after every cut.
var ErrTooLarge = errors.New("receipt statement exceeds the size cap after truncation")

// Name is the PromotionReceipt name of a promotion: the step stepUID of
// bundle in environment. It is <bundle>-<environment> for the eye, cut to
// fit, and a hash of the three, so that no two promotions share one: Bundle
// a-b in c and Bundle a in b-c differ, and so does a second step of the
// same Bundle and environment.
func Name(bundle, environment, stepUID string) string {
	sum := sha256.Sum256([]byte(bundle + "\x00" + environment + "\x00" + stepUID))
	suffix := "-" + hex.EncodeToString(sum[:])[:10]
	n := strings.ToLower(bundle + "-" + environment)
	if limit := 253 - len(suffix); len(n) > limit {
		n = strings.TrimRight(n[:limit], "-.")
	}
	return n + suffix
}

// Digest is sha256:<hex> of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// digestMap is {"sha256": hex} of a sha256:<hex> digest, nil for another form.
func digestMap(d string) map[string]string {
	hexPart, ok := strings.CutPrefix(d, "sha256:")
	if !ok || hexPart == "" {
		return nil
	}
	return map[string]string{digestSHA256: hexPart}
}

// ResourceURI is the receipt's resourceUri: the environment of a Pipeline
// in a cluster.
func ResourceURI(clusterID, namespace, pipeline, env string) string {
	return fmt.Sprintf("kardinal://%s/namespaces/%s/pipelines/%s/environments/%s", clusterID, namespace, pipeline, env)
}

// TemplateDigest is sha256:<hex> of a gate template's spec as JSON. Its
// status is not part of it, so a status write does not change it.
func TemplateDigest(spec v1alpha1.PolicyGateSpec) (string, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal gate spec: %w", err)
	}
	return Digest(b), nil
}

// PolicyDigest is sha256:<hex> of the canonical policy set: the Pipeline
// generation and spec hash, and each gate template (namespace/name, UID,
// generation, spec digest), sorted by template.
func PolicyDigest(pipelineGeneration int64, pipelineSpecHash string, gates []v1alpha1.DecisionGate) string {
	type tmpl struct {
		Template   string `json:"template"`
		UID        string `json:"uid"`
		Generation int64  `json:"generation"`
		Digest     string `json:"digest"`
	}
	set := struct {
		PipelineGeneration int64  `json:"pipelineGeneration"`
		PipelineSpecHash   string `json:"pipelineSpecHash"`
		Templates          []tmpl `json:"templates"`
	}{PipelineGeneration: pipelineGeneration, PipelineSpecHash: pipelineSpecHash, Templates: []tmpl{}}
	seen := map[string]bool{}
	for _, g := range gates {
		if g.Template == "" || seen[g.Template] {
			continue
		}
		seen[g.Template] = true
		set.Templates = append(set.Templates, tmpl{g.Template, g.TemplateUID, g.TemplateGeneration, g.TemplateDigest})
	}
	sort.Slice(set.Templates, func(i, j int) bool { return set.Templates[i].Template < set.Templates[j].Template })
	b, _ := json.Marshal(set) // a struct of strings and ints always marshals
	return Digest(b)
}

// Build returns the statement for in, as JSON, and its digest. The JSON is
// what is signed and stored: verification compares these bytes, it never
// marshals the statement again.
func Build(in Input) ([]byte, string, error) {
	if in.Bundle == nil || in.Step == nil {
		return nil, "", errors.New("receipt input needs the Bundle and the PromotionStep")
	}
	st := statement(in)
	b, err := fit(&st)
	if err != nil {
		return nil, "", err
	}
	return b, Digest(b), nil
}

func statement(in Input) Statement {
	b, ps := in.Bundle, in.Step
	ns := ps.Namespace
	rec := Record{
		Receipt:          in.Name,
		Pipeline:         ps.Spec.PipelineName,
		Version:          version(b),
		Actor:            b.Spec.Actor,
		DecisionSnapshot: in.Snapshot,
		Gates:            []Gate{},
		Checks:           checks(in),
		Change:           Change{Environment: ps.Spec.Environment, PR: in.PR, Commit: in.Commit},
		Controller:       in.ControllerVersion,
		Chart:            in.ChartVersion,
	}
	if b.Spec.Provenance != nil {
		rec.RollbackOf = b.Spec.Provenance.RollbackOf
	}
	policy := ResourceDescriptor{URI: fmt.Sprintf("kardinal://%s/namespaces/%s/pipelines/%s", in.ClusterID, ns, ps.Spec.PipelineName)}
	if d := in.Decision; d != nil {
		policy.URI += fmt.Sprintf("?generation=%d", d.PipelineGeneration)
		policy.Digest = digestMap(d.PolicyDigest)
		rec.OmittedGates, rec.Truncated = d.OmittedGates, d.Truncated
		for i := range d.Gates {
			rec.Gates = append(rec.Gates, gate(&d.Gates[i]))
		}
	}
	versionMap := map[string]string{}
	if in.ControllerVersion != "" {
		versionMap["kardinal-promoter"] = in.ControllerVersion
	}
	return Statement{
		Type:          StatementType,
		Subject:       subjects(in),
		PredicateType: PredicateVSA,
		Predicate: Predicate{
			Verifier:           Verifier{ID: VerifierID, Version: versionMap},
			TimeVerified:       stamp(in.Now),
			ResourceURI:        ResourceURI(in.ClusterID, ns, ps.Spec.PipelineName, ps.Spec.Environment),
			Policy:             policy,
			VerificationResult: ResultPassed,
			VerifiedLevels:     levels(rec),
			SlsaVersion:        slsaVersion,
			Kardinal:           rec,
		},
	}
}

func version(b *v1alpha1.Bundle) Version {
	v := Version{Bundle: b.Name, BundleUID: string(b.UID), Version: b.Status.Version, Type: b.Spec.Type, Provenance: b.Spec.Provenance}
	for _, img := range b.Spec.Images {
		ref := img.Repository
		switch {
		case img.Digest != "":
			ref += "@" + img.Digest
		case img.Tag != "":
			ref += ":" + img.Tag
		}
		v.Images = append(v.Images, ref)
	}
	if c := b.Spec.ConfigRef; c != nil && c.CommitSHA != "" {
		v.Config = c.GitRepo + "@" + c.CommitSHA
	}
	return v
}

// subjects are the image digests and the config commit. A Bundle whose
// images have no digest and no config commit is named by its own spec: the
// statement then says which Bundle, not which bytes, was promoted.
func subjects(in Input) []Subject {
	var out []Subject
	for _, img := range in.Bundle.Spec.Images {
		if d := digestMap(img.Digest); d != nil {
			out = append(out, Subject{Name: img.Repository, Digest: d})
		}
	}
	if c := in.Bundle.Spec.ConfigRef; c != nil && c.CommitSHA != "" {
		out = append(out, Subject{Name: c.GitRepo, Digest: map[string]string{digestGitCommit: c.CommitSHA}})
	}
	if len(out) == 0 {
		spec, _ := json.Marshal(in.Bundle.Spec) // API types always marshal
		out = append(out, Subject{
			Name:   fmt.Sprintf("kardinal://%s/namespaces/%s/bundles/%s", in.ClusterID, in.Bundle.Namespace, in.Bundle.Name),
			Digest: digestMap(Digest(spec)),
		})
	}
	return out
}

func gate(g *v1alpha1.DecisionGate) Gate {
	out := Gate{
		Name: g.Name, Template: g.Template, TemplateUID: g.TemplateUID, TemplateGeneration: g.TemplateGeneration,
		TemplateDigest: digestMap(g.TemplateDigest), Expression: g.Expression, Message: g.Message,
		Result: "fail", Reason: g.Reason, ApprovalRequired: g.ApprovalRequired, Approvals: []Approval{},
	}
	if g.Ready {
		out.Result = "pass"
	}
	if g.EvaluatedAt != nil {
		out.EvaluatedAt = stamp(g.EvaluatedAt.Time)
	}
	if o := g.Override; o != nil {
		out.Override = &Override{CreatedBy: o.CreatedBy, Reason: o.Reason, Verified: o.Verified, Actor: o.Actor}
		if o.ExpiresAt != nil {
			out.Override.ExpiresAt = stamp(o.ExpiresAt.Time)
		}
	}
	for _, a := range g.Approvals {
		out.Approvals = append(out.Approvals, Approval{User: a.User, Actor: a.Actor, Decision: a.Decision, Counted: a.Counted, Reason: a.Reason})
	}
	out.VerifyRequired = g.VerifyRequired
	for _, v := range g.Verdicts {
		out.Verdicts = append(out.Verdicts, Verdict{Name: v.Name, Verifier: v.Verifier, Result: v.Result, Counted: v.Counted,
			Reason: v.Reason, EvidenceURL: v.EvidenceURL, Findings: v.Findings})
	}
	return out
}

func checks(in Input) Checks {
	var c Checks
	for _, m := range in.Metrics {
		name := m.Labels["kardinal.io/metric-template"]
		if name == "" {
			name = m.Name
		}
		mc := Metric{Name: name, Value: m.Status.LastValue, Result: m.Status.Result}
		if m.Status.ValidUntil != nil {
			mc.ValidUntil = stamp(m.Status.ValidUntil.Time)
		}
		c.Metrics = append(c.Metrics, mc)
	}
	sort.Slice(c.Metrics, func(i, j int) bool { return c.Metrics[i].Name < c.Metrics[j].Name })
	if iv := in.ImageVerification; iv != nil {
		for _, r := range iv.Status.Images {
			c.ImageVerification = append(c.ImageVerification, ImageCheck{Image: r.Image, Verified: r.Verified, By: r.Signer})
		}
	}
	for _, s := range in.Step.Status.Steps {
		if s.Name != "health-check" {
			continue
		}
		adapter := in.HealthAdapter
		if adapter == "" {
			adapter = "resource"
		}
		h := &Health{Adapter: adapter, Result: "Unhealthy"}
		if s.State == v1alpha1.StepExecutionCompleted {
			h.Result = "Healthy"
		}
		if s.CompletedAt != nil {
			h.At = stamp(s.CompletedAt.Time)
		}
		c.Health = h
	}
	return c
}

// levels are the verifiedLevels the record supports, computed before any
// cut (a truncated record keeps them). Nothing is claimed that the full
// record does not show.
func levels(rec Record) []string {
	out := []string{}
	passed, approved, overridden, verified := len(rec.Gates) > 0, false, false, false
	for _, g := range rec.Gates {
		if g.Result != "pass" {
			passed = false
		}
		if g.Override != nil {
			overridden = true
		}
		for _, a := range g.Approvals {
			if a.Counted && a.Decision == "approve" {
				approved = true
			}
		}
		for _, v := range g.Verdicts {
			if v.Counted && v.Result == "pass" {
				verified = true
			}
		}
	}
	if passed {
		out = append(out, LevelGatesPassed)
	}
	if approved {
		out = append(out, LevelApproved)
	}
	if verified {
		out = append(out, LevelVerifiedByAgent)
	}
	if rec.Checks.Health != nil && rec.Checks.Health.Result == "Healthy" {
		out = append(out, LevelHealthy)
	}
	if overridden {
		out = append(out, LevelOverridden)
	}
	return out
}

// maxCutField bounds a gate's expression, message and reason after the
// second cut.
const maxCutField = 1024

// fit marshals st, cutting it to MaxReceiptStatementBytes: first the
// approvals and Verdicts that did not count, then long gate texts, then the gates past
// the first and the metrics; each cut is counted and sets truncated. The
// verifiedLevels stay those of the full record.
func fit(st *Statement) ([]byte, error) {
	cuts := []func(*Record){
		func(r *Record) {
			for i := range r.Gates {
				g := &r.Gates[i]
				kept := g.Approvals[:0]
				for _, a := range g.Approvals {
					if a.Counted {
						kept = append(kept, a)
					} else {
						g.ApprovalsOmitted++
					}
				}
				g.Approvals = kept
				counted := g.Verdicts[:0]
				for _, v := range g.Verdicts {
					if v.Counted {
						counted = append(counted, v)
					} else {
						g.VerdictsOmitted++
					}
				}
				g.Verdicts = counted
			}
		},
		func(r *Record) {
			for i := range r.Gates {
				g := &r.Gates[i]
				g.Expression, g.Message, g.Reason = cut(g.Expression), cut(g.Message), cut(g.Reason)
			}
		},
		func(r *Record) {
			if len(r.Gates) > 1 {
				r.OmittedGates += len(r.Gates) - 1
				r.Gates = r.Gates[:1]
			}
			r.Checks.Metrics = nil
		},
	}
	for i := 0; ; i++ {
		b, err := json.Marshal(st)
		if err != nil {
			return nil, fmt.Errorf("marshal receipt statement: %w", err)
		}
		if len(b) <= v1alpha1.MaxReceiptStatementBytes {
			return b, nil
		}
		if i == len(cuts) {
			return nil, ErrTooLarge
		}
		cuts[i](&st.Predicate.Kardinal)
		st.Predicate.Kardinal.Truncated = true
	}
}

func cut(s string) string {
	if len(s) <= maxCutField {
		return s
	}
	return s[:maxCutField] + "..."
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Parse decodes a statement.
func Parse(b []byte) (*Statement, error) {
	var st Statement
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse receipt statement: %w", err)
	}
	if st.Type != StatementType || st.PredicateType != PredicateVSA {
		return nil, fmt.Errorf("not a kardinal receipt: _type %q, predicateType %q", st.Type, st.PredicateType)
	}
	return &st, nil
}
