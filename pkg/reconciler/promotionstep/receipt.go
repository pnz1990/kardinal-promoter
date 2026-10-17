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

package promotionstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// LabelReceiptStep names the UID of the PromotionStep whose receipt a
// PromotionReceipt is: a receipt is adopted on AlreadyExists only when it is.
const LabelReceiptStep = "kardinal.io/promotion-step-uid"

// ReceiptConfig turns promotion receipts on (docs/receipts.md): the step
// takes a decision snapshot when it starts, and creates its
// PromotionReceipt when it reaches Verified. The PromotionReceipt
// reconciler signs and stores it; this reconciler makes no network call
// for receipts.
type ReceiptConfig struct {
	// ClusterID identifies the cluster in the receipt's resourceUri: the
	// kube-system Namespace UID.
	ClusterID string
	// ControllerVersion and ChartVersion are recorded in the receipt.
	ControllerVersion string
	ChartVersion      string
}

// Labels of a PromotionReceipt (the step's own).
var receiptLabels = []string{"kardinal.io/pipeline", "kardinal.io/bundle", "kardinal.io/environment"}

// gateOverridden is the start of a gate's status.reason while an override
// is in effect (policygate overrides.go).
const gateOverridden = "OVERRIDDEN by "

// decision is the snapshot of gates, the required gates checkRequiredGates
// just read, for a step leaving Pending. It reads each gate's template from
// the cache; a template it cannot read is recorded without its versions.
func (r *Reconciler) decision(ctx context.Context, ps *v1alpha1.PromotionStep, gates []v1alpha1.PolicyGate,
	pipeline *v1alpha1.Pipeline, bundle *v1alpha1.Bundle) *v1alpha1.PromotionDecision {
	d := &v1alpha1.PromotionDecision{TakenAt: metav1.NewTime(r.now().UTC())}
	if pipeline != nil {
		d.PipelineGeneration = pipeline.Generation
	}
	if bundle != nil {
		d.PipelineSpecHash = bundle.Status.PipelineSpecHash
	}
	all := make([]v1alpha1.DecisionGate, 0, len(gates))
	for i := range gates {
		all = append(all, r.decisionGate(ctx, ps, &gates[i]))
	}
	// The facts and the digest of every gate, before any cut.
	facts := receipt.GateFacts(all)
	d.Facts = &facts
	d.PolicyDigest = receipt.PolicyDigest(d.PipelineGeneration, d.PipelineSpecHash, all)
	d.Gates = all
	if len(d.Gates) > v1alpha1.MaxDecisionGates {
		omit(d, len(d.Gates)-v1alpha1.MaxDecisionGates)
	}
	if !fitDecision(d) {
		return nil
	}
	return d
}

// omit leaves the last n gates of d out, by name.
func omit(d *v1alpha1.PromotionDecision, n int) {
	for _, g := range d.Gates[len(d.Gates)-n:] {
		if len(d.OmittedGateNames) < v1alpha1.MaxDecisionGates {
			d.OmittedGateNames = append(d.OmittedGateNames, g.Name)
		}
	}
	d.Gates = d.Gates[:len(d.Gates)-n]
	d.OmittedGates += n
}

// maxDecisionText bounds a gate's expression, message and reason, and an
// approval's or Verdict's reason, after the second cut.
const maxDecisionText = 1024

// decisionSize is d as JSON, in bytes.
func decisionSize(d *v1alpha1.PromotionDecision) int {
	b, err := json.Marshal(d)
	if err != nil {
		return v1alpha1.MaxDecisionBytes + 1
	}
	return len(b)
}

// fitDecision cuts d to MaxDecisionBytes, in the statement's order: the
// approvals and Verdicts that did not count, then long texts, then gates from
// the end (OmittedGates). It reports whether d fits; one that does not even
// with a single gate is not taken (the step starts without a snapshot).
func fitDecision(d *v1alpha1.PromotionDecision) bool {
	if decisionSize(d) <= v1alpha1.MaxDecisionBytes {
		return true
	}
	d.Truncated = true
	for i := range d.Gates {
		g := &d.Gates[i]
		n := len(g.Approvals) + len(g.Verdicts)
		g.Approvals = slices.DeleteFunc(g.Approvals, func(a v1alpha1.GateApprovalStatus) bool { return !a.Counted })
		g.Verdicts = slices.DeleteFunc(g.Verdicts, func(v v1alpha1.GateVerdictStatus) bool { return !v.Counted })
		g.Truncated = g.Truncated || len(g.Approvals)+len(g.Verdicts) < n
	}
	if decisionSize(d) <= v1alpha1.MaxDecisionBytes {
		return true
	}
	for i := range d.Gates {
		g := &d.Gates[i]
		before := len(g.Expression) + len(g.Message) + len(g.Reason)
		g.Expression, g.Message, g.Reason = cutText(g.Expression), cutText(g.Message), cutText(g.Reason)
		g.Truncated = g.Truncated || len(g.Expression)+len(g.Message)+len(g.Reason) < before
		for j := range g.Approvals {
			g.Approvals[j].Reason = cutText(g.Approvals[j].Reason)
		}
		for j := range g.Verdicts {
			g.Verdicts[j].Reason, g.Verdicts[j].EvidenceURL = cutText(g.Verdicts[j].Reason), cutText(g.Verdicts[j].EvidenceURL)
		}
	}
	for decisionSize(d) > v1alpha1.MaxDecisionBytes && len(d.Gates) > 1 {
		omit(d, 1)
	}
	return decisionSize(d) <= v1alpha1.MaxDecisionBytes
}

func cutText(s string) string {
	if len(s) <= maxDecisionText {
		return s
	}
	return strings.ToValidUTF8(s[:maxDecisionText-3], "") + "..."
}

func (r *Reconciler) decisionGate(ctx context.Context, ps *v1alpha1.PromotionStep, g *v1alpha1.PolicyGate) v1alpha1.DecisionGate {
	out := v1alpha1.DecisionGate{
		Name: g.Name, Expression: g.Spec.Expression, Message: g.Spec.Message,
		Ready: g.Status.Ready, Reason: g.Status.Reason, EvaluatedAt: g.Status.LastEvaluatedAt,
		ApprovalRequired: g.Spec.Approval != nil, Approvals: g.Status.Approvals,
	}
	if len(out.Approvals) > 101 {
		out.Approvals = out.Approvals[:101]
	}
	out.VerifyRequired, out.Verdicts = g.Spec.Verify != nil, g.Status.Verdicts
	if len(out.Verdicts) > 101 {
		out.Verdicts = out.Verdicts[:101]
	}
	// Copies without what the statement never reads: an approval's comment
	// and when it was first seen, a Verdict's summary.
	out.Approvals = slices.Clone(out.Approvals)
	for i := range out.Approvals {
		out.Approvals[i].Comment, out.Approvals[i].FirstSeenAt = "", nil
	}
	out.Verdicts = slices.Clone(out.Verdicts)
	for i := range out.Verdicts {
		out.Verdicts[i].Summary = ""
	}
	if strings.HasPrefix(g.Status.Reason, gateOverridden) {
		out.Override = activeOverride(g, ps.Spec.Environment)
	}
	name, ns := g.Labels["kardinal.io/gate-template"], g.Labels[graph.LabelGateTemplateNamespace]
	if name == "" {
		return out
	}
	if ns == "" {
		ns = g.Namespace
	}
	out.Template = ns + "/" + name
	// The template the instance was translated from, as the Graph builder
	// stamped it: the instance's expression is that generation's, even when
	// the template was edited since (#1360).
	if gen, err := strconv.ParseInt(g.Annotations[graph.AnnotationGateTemplateGeneration], 10, 64); err == nil &&
		g.Annotations[graph.AnnotationGateTemplateDigest] != "" {
		out.TemplateUID, out.TemplateGeneration = g.Annotations[graph.AnnotationGateTemplateUID], gen
		out.TemplateDigest = g.Annotations[graph.AnnotationGateTemplateDigest]
		return out
	}
	// An instance built before the stamp: the template as it is now.
	var tmpl v1alpha1.PolicyGate
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &tmpl); err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Str("template", out.Template).Msg("receipt: gate template not read; recorded without its versions")
		return out
	}
	out.TemplateUID, out.TemplateGeneration = string(tmpl.UID), tmpl.Generation
	out.TemplateDigest = graph.TemplateDigest(tmpl)
	return out
}

// activeOverride is the override an overridden gate passed by: the first
// spec.overrides entry for env (or every stage) that had not expired when
// the gate was last evaluated. The gate's reason says one is in effect.
func activeOverride(g *v1alpha1.PolicyGate, env string) *v1alpha1.DecisionOverride {
	for i := range g.Spec.Overrides {
		o := &g.Spec.Overrides[i]
		if o.Stage != "" && o.Stage != env {
			continue
		}
		if g.Status.LastEvaluatedAt != nil && !o.ExpiresAt.After(g.Status.LastEvaluatedAt.Time) {
			continue
		}
		exp := o.ExpiresAt
		return &v1alpha1.DecisionOverride{CreatedBy: o.CreatedBy, Reason: o.Reason, ExpiresAt: &exp,
			Verified: policygate.OverrideVerified(g, o), Actor: o.Actor}
	}
	return &v1alpha1.DecisionOverride{Reason: g.Status.Reason}
}

// pendingReceipt is the receipt of ps at Verified, for the outbox. It reads
// the Bundle, the Pipeline, the gates when the step has no snapshot (it
// started before receipts were enabled), the step's per-promotion
// MetricChecks and the Bundle's ImageVerification, all from the cache. An
// error is retried with the transition.
func (r *Reconciler) pendingReceipt(ctx context.Context, ps *v1alpha1.PromotionStep) (*v1alpha1.PendingReceipt, error) {
	bundle, err := r.loadBundle(ctx, ps)
	if err != nil {
		return nil, err
	}
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return nil, err
	}
	in := receipt.Input{
		Name: receipt.Name(ps.Spec.BundleName, ps.Spec.Environment, string(ps.UID)), ClusterID: r.Receipts.ClusterID,
		ControllerVersion: r.Receipts.ControllerVersion, ChartVersion: r.Receipts.ChartVersion,
		Now: r.now(), Bundle: bundle, Step: ps, Decision: ps.Status.Decision, Snapshot: ps.Status.Decision != nil,
		HealthAdapter: findEnv(pipeline, ps.Spec.Environment).Health.Type,
	}
	if in.Decision == nil {
		gates, err := r.readGates(ctx, ps)
		if err != nil {
			return nil, err
		}
		in.Decision = r.decision(ctx, ps, gates, pipeline, bundle)
	}
	var metrics v1alpha1.MetricCheckList
	if err := r.List(ctx, &metrics, client.InNamespace(ps.Namespace), client.MatchingLabels{
		"kardinal.io/bundle": ps.Spec.BundleName, "kardinal.io/environment": ps.Spec.Environment,
	}); err != nil {
		return nil, fmt.Errorf("list metric checks: %w", err)
	}
	in.Metrics = metrics.Items
	var ivs v1alpha1.ImageVerificationList
	if err := r.List(ctx, &ivs, client.InNamespace(ps.Namespace), client.MatchingLabels{
		"kardinal.io/pipeline": ps.Spec.PipelineName, "kardinal.io/bundle": ps.Spec.BundleName,
	}); err != nil {
		return nil, fmt.Errorf("list image verifications: %w", err)
	}
	// The newest Verified one: after a policy edit there are two, and list
	// order is not creation order (as the attestation context reads them).
	for i := range ivs.Items {
		iv := &ivs.Items[i]
		if iv.Status.Phase != v1alpha1.ImageVerificationVerified {
			continue
		}
		if cur := in.ImageVerification; cur == nil || cur.CreationTimestamp.Before(&iv.CreationTimestamp) ||
			(cur.CreationTimestamp.Equal(&iv.CreationTimestamp) && cur.Name < iv.Name) {
			in.ImageVerification = iv
		}
	}
	pr := receiptPR(ps)
	if pr != nil {
		in.PR = &receipt.PR{URL: pr.URL, Number: pr.Number, MergeCommit: ps.Status.Outputs["mergeCommitSHA"]}
		if s := stepNamed(ps, "wait-for-merge"); s != nil && s.CompletedAt != nil {
			in.PR.MergedAt = s.CompletedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
	} else {
		in.Commit = ps.Status.Outputs["commitSHA"]
	}
	stmt, digest, err := receipt.Build(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errReceiptBuild, err)
	}
	return &v1alpha1.PendingReceipt{Name: in.Name, Spec: v1alpha1.PromotionReceiptSpec{
		Statement: string(stmt), StatementDigest: digest, PullRequest: pr,
	}}, nil
}

// receiptSinkBuild is the kardinal_receipts_failed_total sink of a receipt
// that could not be built.
const receiptSinkBuild = "build"

// errReceiptBuild marks a receipt.Build error: the same inputs give the same
// error, so retrying cannot help.
var errReceiptBuild = errors.New("build receipt")

// receiptNeverBuilds reports whether err from pendingReceipt is
// deterministic: the statement cannot be built from these inputs (over the
// size cap after every cut), or the Bundle or Pipeline it is built from no
// longer exists. Other errors (a cache read) may succeed on a retry.
func receiptNeverBuilds(err error) bool {
	return errors.Is(err, errReceiptBuild) || apierrors.IsNotFound(err)
}

// receiptErrorText is err for status.receiptError, within its 1024 bytes.
func receiptErrorText(err error) string {
	s := err.Error()
	if len(s) > 1024 {
		s = strings.ToValidUTF8(s[:1021], "") + "..."
	}
	return s
}

// readGates reads the step's required gates, for a step without a
// snapshot. A gate that no longer exists is skipped.
func (r *Reconciler) readGates(ctx context.Context, ps *v1alpha1.PromotionStep) ([]v1alpha1.PolicyGate, error) {
	var out []v1alpha1.PolicyGate
	for _, name := range ps.Spec.RequiredGates {
		var g v1alpha1.PolicyGate
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ps.Namespace}, &g)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("get policy gate %s: %w", name, err)
		}
		out = append(out, g)
	}
	return out, nil
}

// receiptPR is the step's PR, nil for a step that opened none.
func receiptPR(ps *v1alpha1.PromotionStep) *v1alpha1.ReceiptPullRequest {
	url := ps.Status.Outputs["prURL"]
	if url == "" {
		url = ps.Status.PRURL
	}
	if url == "" {
		return nil
	}
	num, err := strconv.Atoi(ps.Status.Outputs["prNumber"])
	if err != nil {
		num = extractPRNumber(url)
	}
	repo := extractRepo(url)
	if num <= 0 || repo == "" {
		return nil
	}
	return &v1alpha1.ReceiptPullRequest{Repo: repo, Number: num, URL: url, ScmProvider: ps.Spec.ScmProvider}
}

func stepNamed(ps *v1alpha1.PromotionStep, name string) *v1alpha1.StepStatus {
	for i := range ps.Status.Steps {
		if ps.Status.Steps[i].Name == name {
			return &ps.Status.Steps[i]
		}
	}
	return nil
}

// flushReceipt creates the PromotionReceipt in ps's outbox
// (status.pendingReceipt), then clears the outbox and the decision snapshot
// and records status.receipt. A receipt that already exists counts as
// written (a retry after a crash): its name derives from the Bundle and
// the environment.
func (r *Reconciler) flushReceipt(ctx context.Context, ps *v1alpha1.PromotionStep) error {
	p := ps.Status.PendingReceipt
	if p == nil {
		return nil
	}
	labels := map[string]string{}
	for _, k := range receiptLabels {
		if v := ps.Labels[k]; v != "" {
			labels[k] = v
		}
	}
	labels["kardinal.io/pipeline"] = ps.Spec.PipelineName
	labels["kardinal.io/bundle"] = ps.Spec.BundleName
	labels["kardinal.io/environment"] = ps.Spec.Environment
	labels[LabelReceiptStep] = string(ps.UID)
	rc := &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: ps.Namespace, Labels: labels},
		Spec:       p.Spec,
	}
	name, receiptErr := p.Name, ""
	if err := r.Create(ctx, rc); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			zerolog.Ctx(ctx).Error().Err(err).Str("receipt", p.Name).
				Msg("failed to create PromotionReceipt; kept in status.pendingReceipt to retry")
			return fmt.Errorf("create promotion receipt %s: %w", p.Name, err)
		}
		// A retry after a crash finds the one it created: the same step and
		// statement. Anything else is not this promotion's receipt.
		var have v1alpha1.PromotionReceipt
		if err := r.Get(ctx, client.ObjectKeyFromObject(rc), &have); err != nil {
			return fmt.Errorf("read promotion receipt %s: %w", p.Name, err)
		}
		if have.Labels[LabelReceiptStep] != string(ps.UID) || have.Spec.StatementDigest != p.Spec.StatementDigest {
			zerolog.Ctx(ctx).Error().Str("receipt", p.Name).Msg("a PromotionReceipt of another promotion has this step's name: no receipt")
			name, receiptErr = "", fmt.Sprintf("PromotionReceipt %s exists and is not this promotion's (step %s, statement %s): no receipt",
				p.Name, have.Labels[LabelReceiptStep], have.Spec.StatementDigest)
		}
	}
	patch := client.MergeFromWithOptions(ps.DeepCopy(), client.MergeFromWithOptimisticLock{})
	decision := ps.Status.Decision
	ps.Status.PendingReceipt, ps.Status.Decision, ps.Status.Receipt = nil, nil, name
	if receiptErr != "" {
		ps.Status.ReceiptError = receiptErr
	}
	if err := r.Status().Patch(ctx, ps, patch); err != nil && !apierrors.IsNotFound(err) {
		// The receipt exists: the next flush finds it (AlreadyExists).
		ps.Status.PendingReceipt, ps.Status.Decision, ps.Status.Receipt = p, decision, ""
		return fmt.Errorf("clear the receipt outbox: %w", err)
	}
	return nil
}
