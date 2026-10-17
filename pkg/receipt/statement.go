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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	// LevelFreezeExempted: the promotion went on through a freeze under an
	// exemption (kardinal override --freeze). Like KARDINAL_OVERRIDDEN, a
	// verifier usually requires it absent: --require '!FREEZE_EXEMPTED'.
	LevelFreezeExempted = "KARDINAL_FREEZE_EXEMPTED"
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
	// OmittedGateNames name the gates the snapshot left out.
	OmittedGateNames []string `json:"omittedGateNames,omitempty"`
	Checks           Checks   `json:"checks"`
	// ChangeRequests are the step's change request tickets as it started
	// on them: the inputs its gates read as changeRequests.<name>.
	ChangeRequests []ChangeRequest `json:"changeRequests,omitempty"`
	Change         Change          `json:"change"`
	// FreezeExemptions are the freezes the promotion went on through under
	// an exemption: who made it, why, and until when.
	FreezeExemptions []FreezeExemption `json:"freezeExemptions,omitempty"`
	// FreezeExemptionsUnknown: the step went on through a freeze under an
	// exemption, and which one was not recorded (a step let through by
	// v0.12). The receipt claims KARDINAL_FREEZE_EXEMPTED all the same.
	FreezeExemptionsUnknown bool   `json:"freezeExemptionsUnknown,omitempty"`
	Controller              string `json:"controller,omitempty"`
	Chart                   string `json:"chart,omitempty"`
	Truncated               bool   `json:"truncated"`
	// Redacted are the JSON paths of the strings redacted before signing
	// (the userinfo of a URL, the value of a credential parameter such as
	// ?token=): a receipt is signed and may be published for good, so a
	// credential a Bundle carried is never in it.
	Redacted []string `json:"redacted,omitempty"`
}

// FreezeExemption is a freeze the promotion went on through.
type FreezeExemption struct {
	// Freeze is the freezing ChangeWindow; Actors whom it held (agents or
	// everyone).
	Freeze string `json:"freeze"`
	Actors string `json:"actors"`
	// CreatedBy and Actor are who made the exemption, as admission verified
	// them; Reason why; Until when it stopped counting.
	CreatedBy   string          `json:"createdBy,omitempty"`
	Actor       *v1alpha1.Actor `json:"actor,omitempty"`
	Reason      string          `json:"reason"`
	Environment string          `json:"environment,omitempty"`
	Bundle      string          `json:"bundle,omitempty"`
	Until       string          `json:"until"`
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
	// Truncated: the snapshot cut this gate (approvals or Verdicts that did
	// not count, or long texts).
	Truncated       bool      `json:"truncated,omitempty"`
	Verdicts        []Verdict `json:"verdicts,omitempty"`
	VerdictsOmitted int       `json:"verdictsOmitted,omitempty"`
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
	// Fields are the decision's structured values (each cut to 256 bytes
	// on the gate): statements by the approver, not facts.
	Fields map[string]string `json:"fields,omitempty"`
}

// ChangeRequest is one change request ticket of the promotion.
type ChangeRequest struct {
	// Name is the ChangeRequest template, as gates read it.
	Name     string `json:"name"`
	Instance string `json:"instance"`
	Provider string `json:"provider,omitempty"`
	// Ticket is the ticket's key and URL; empty when it had none yet.
	Ticket        string `json:"ticket,omitempty"`
	URL           string `json:"url,omitempty"`
	State         string `json:"state,omitempty"`
	ProviderState string `json:"providerState,omitempty"`
	// Verified: the ticket is this promotion's (opened by kardinal, or a
	// link the reconciler verified).
	Verified         bool     `json:"verified"`
	VerifiedReason   string   `json:"verifiedReason,omitempty"`
	Approvers        []string `json:"approvers,omitempty"`
	ApproversOmitted int      `json:"approversOmitted,omitempty"`
	TimedOut         bool     `json:"timedOut,omitempty"`
	ObservedAt       string   `json:"observedAt,omitempty"`
	ValidUntil       string   `json:"validUntil,omitempty"`
}

// Checks are the per-promotion checks at Verified.
type Checks struct {
	Metrics           []Metric     `json:"metrics,omitempty"`
	ImageVerification []ImageCheck `json:"imageVerification,omitempty"`
	// SLSA is the version's SLSA build level, from its ImageVerification's
	// attestations; nil when its policy decides none.
	SLSA   *SLSA   `json:"slsa,omitempty"`
	Health *Health `json:"health,omitempty"`
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
	// Adopted is set for a merged PR of the promotion's branch that the
	// step found rather than recorded opening (#2092): kardinal may not
	// have opened it, and it may carry commits kardinal did not push.
	Adopted *AdoptedPR `json:"adopted,omitempty"`
}

// AdoptedPR is what is known of an adopted PR: its head commit, the SCM
// account that opened it, and its commits kardinal did not push (the first
// 20; UnpushedCount is how many there are). SHAs are hex or "(invalid)";
// names are the SCM's free text, without e-mail addresses, at most 128
// bytes.
type AdoptedPR struct {
	HeadSHA       string     `json:"headSHA,omitempty"`
	OpenedBy      string     `json:"openedBy,omitempty"`
	Unpushed      []PRCommit `json:"unpushedCommits,omitempty"`
	UnpushedCount int        `json:"unpushedCount,omitempty"`
}

// PRCommit is one commit of a pull request and its author.
type PRCommit struct {
	SHA    string `json:"sha"`
	Author string `json:"author,omitempty"`
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
	st, err := statement(in)
	if err != nil {
		return nil, "", err
	}
	b, err := fit(&st)
	if err != nil {
		return nil, "", err
	}
	return b, Digest(b), nil
}

func statement(in Input) (Statement, error) {
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
	// A step let through a freeze by v0.12 has the condition and no
	// record: its exemptions are unknown, and the receipt says so and
	// claims KARDINAL_FREEZE_EXEMPTED (a verifier that refuses exempted
	// promotions refuses it: fail closed).
	if c := meta.FindStatusCondition(ps.Status.Conditions, v1alpha1.ConditionFreezeExempted); c != nil &&
		c.Status == metav1.ConditionTrue && len(ps.Status.FreezeExemptions) == 0 {
		rec.FreezeExemptionsUnknown = true
	}
	for _, x := range ps.Status.FreezeExemptions {
		rec.FreezeExemptions = append(rec.FreezeExemptions, FreezeExemption{Freeze: x.Freeze, Actors: x.Actors,
			CreatedBy: x.Exemption.CreatedBy, Actor: x.Exemption.Actor, Reason: x.Exemption.Reason,
			Environment: x.Exemption.Environment, Bundle: x.Exemption.Bundle, Until: stamp(x.Until.Time)})
	}
	policy := ResourceDescriptor{URI: fmt.Sprintf("kardinal://%s/namespaces/%s/pipelines/%s", in.ClusterID, ns, ps.Spec.PipelineName)}
	var facts *v1alpha1.DecisionFacts
	if d := in.Decision; d != nil {
		policy.URI += fmt.Sprintf("?generation=%d", d.PipelineGeneration)
		policy.Digest = digestMap(d.PolicyDigest)
		rec.OmittedGates, rec.Truncated, rec.OmittedGateNames = d.OmittedGates, d.Truncated, d.OmittedGateNames
		facts = d.Facts
		for i := range d.Gates {
			rec.Gates = append(rec.Gates, gate(&d.Gates[i]))
		}
		for i := range d.ChangeRequests {
			rec.ChangeRequests = append(rec.ChangeRequests, changeRequest(&d.ChangeRequests[i]))
		}
	}
	// Every string of the record and every subject name, redacted before
	// signing: the paths changed are recorded in kardinalPromotion.redacted.
	redacted, paths, err := redactRecord(rec)
	if err != nil {
		return Statement{}, err
	}
	rec = redacted
	subj := subjects(in)
	for i := range subj {
		if r := redact(subj[i].Name); r != subj[i].Name {
			subj[i].Name = r
			paths = append(paths, fmt.Sprintf("subject[%d].name", i))
		}
	}
	rec.Redacted = paths
	versionMap := map[string]string{}
	if in.ControllerVersion != "" {
		versionMap["kardinal-promoter"] = in.ControllerVersion
	}
	return Statement{
		Type:          StatementType,
		Subject:       subj,
		PredicateType: PredicateVSA,
		Predicate: Predicate{
			Verifier:           Verifier{ID: VerifierID, Version: versionMap},
			TimeVerified:       stamp(in.Now),
			ResourceURI:        ResourceURI(in.ClusterID, ns, ps.Spec.PipelineName, ps.Spec.Environment),
			Policy:             policy,
			VerificationResult: ResultPassed,
			VerifiedLevels:     append(levels(rec, facts), slsaLevels(rec)...),
			SlsaVersion:        slsaVersion,
			Kardinal:           rec,
		},
	}, nil
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
		out.Approvals = append(out.Approvals, Approval{User: a.User, Actor: a.Actor, Decision: a.Decision, Counted: a.Counted,
			Reason: a.Reason, Fields: a.Fields})
	}
	out.VerifyRequired, out.Truncated = g.VerifyRequired, g.Truncated
	for _, v := range g.Verdicts {
		out.Verdicts = append(out.Verdicts, Verdict{Name: v.Name, Verifier: v.Verifier, Result: v.Result, Counted: v.Counted,
			Reason: v.Reason, EvidenceURL: v.EvidenceURL, Findings: v.Findings})
	}
	return out
}

func changeRequest(c *v1alpha1.DecisionChangeRequest) ChangeRequest {
	out := ChangeRequest{Name: c.Name, Instance: c.Instance, Provider: c.Provider, State: c.State, ProviderState: c.ProviderState,
		Verified: c.Verified, VerifiedReason: c.VerifiedReason, ApproversOmitted: c.ApproversOmitted, TimedOut: c.TimedOut}
	if c.Ticket != nil {
		out.Ticket, out.URL = c.Ticket.Key, c.Ticket.URL
	}
	for _, a := range c.Approvers {
		out.Approvers = append(out.Approvers, a.Name)
	}
	if c.ObservedAt != nil {
		out.ObservedAt = stamp(c.ObservedAt.Time)
	}
	if c.ValidUntil != nil {
		out.ValidUntil = stamp(c.ValidUntil.Time)
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
		c.SLSA = slsaCheck(iv)
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

// GateFacts are the level facts of gates: whether every one passed, and
// whether one counted an approval, passed by an override, or counted a
// passing Verdict. The step takes them over all its gates before it cuts
// its snapshot (PromotionDecision.Facts).
func GateFacts(gates []v1alpha1.DecisionGate) v1alpha1.DecisionFacts {
	f := v1alpha1.DecisionFacts{Gates: len(gates), Passed: len(gates) > 0}
	for _, g := range gates {
		f.Passed = f.Passed && g.Ready
		f.Overridden = f.Overridden || g.Override != nil
		for _, a := range g.Approvals {
			f.Approved = f.Approved || (a.Counted && a.Decision == "approve")
		}
		for _, v := range g.Verdicts {
			f.VerifiedByAgent = f.VerifiedByAgent || (v.Counted && v.Result == "pass")
		}
	}
	return f
}

// levels are the verifiedLevels the record supports. The gates' come from
// the snapshot's facts, taken over every gate before any cut, so a gate
// left out of the snapshot or the statement still counts (its override is
// still OVERRIDDEN); a snapshot without facts (an older controller's) has
// them computed from its gates. Nothing is claimed that the full record
// does not show.
func levels(rec Record, facts *v1alpha1.DecisionFacts) []string {
	var passed, approved, overridden, verified bool
	if facts != nil {
		passed, approved, overridden, verified = facts.Passed, facts.Approved, facts.Overridden, facts.VerifiedByAgent
	} else {
		passed = len(rec.Gates) > 0
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
	}
	out := []string{}
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
	if len(rec.FreezeExemptions) > 0 || rec.FreezeExemptionsUnknown {
		out = append(out, LevelFreezeExempted)
	}
	return out
}

// maxCutField bounds a gate's expression, message and reason after the
// second cut.
const maxCutField = 1024

// fit marshals st, cutting it to MaxReceiptStatementBytes: first the
// approvals and Verdicts that did not count, then long gate texts and the
// change requests' approvers, then the gates past
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
			for i := range r.ChangeRequests {
				c := &r.ChangeRequests[i]
				c.ApproversOmitted += len(c.Approvers)
				c.Approvers = nil
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
		b, err := marshalStatement(st)
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

// marshalStatement is st as JSON without HTML escaping: an "&" in a URL
// stays "&", not "\u0026", so a redaction that reads the statement as
// text (scm.RedactText, as the evidence pack applies it) sees each
// parameter on its own and changes nothing a receipt already redacted.
func marshalStatement(st *Statement) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(st); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
