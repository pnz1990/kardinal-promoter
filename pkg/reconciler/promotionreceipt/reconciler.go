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

// Package promotionreceipt signs and stores PromotionReceipts
// (docs/receipts.md). The PromotionStep reconciler creates a receipt at
// Verified; this reconciler signs its statement once, writes it to the
// configured sinks (the CR always, an OCI referrer and Rekor when
// configured), and comments on the promotion's PR once.
//
// Graph-first: this is the only place receipts reach the network, and it
// writes only the receipt's own status. No gate reads a receipt and no
// promotion waits for one: a failing sink is a condition and a metric
// (kardinal_receipts_failed_total), retried with backoff.
package promotionreceipt

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// Condition types and sinks of the failure metric.
const (
	ConditionSigned      = "Signed"
	ConditionStored      = "Stored"
	ConditionPRCommented = "PRCommented"

	metricSigning   = "signing"
	metricOCI       = "oci"
	metricPRComment = "pr-comment"
)

// Retry backoff: doubling from retryBase, at most retryMax. A PR comment is
// given up after maxCommentAttempts.
const (
	retryBase          = 30 * time.Second
	retryMax           = time.Hour
	maxCommentAttempts = 10
	// sinkTimeout bounds one signature or one sink write.
	sinkTimeout = 2 * time.Minute
)

// OCISink pushes receipts as referrers of their images.
type OCISink struct {
	// Registry pushes; its Keychain is set per reconcile from SecretName.
	Registry receipt.Registry
	// Repository, when set, receives every referrer instead of the
	// image's own repository (as cosign's COSIGN_REPOSITORY).
	Repository string
	// AllowedRepositories, when set and Repository is not, are the
	// repositories (or path prefixes: ghcr.io/acme/) the controller pushes
	// referrers to. An image's repository is tenant input, and the push
	// credential is the admin's: without a bound, a tenant could make kardinal
	// write into a repository the tenant cannot. Another image gets no
	// referrer: the receipt's Stored condition is False, RepositoryNotAllowed.
	AllowedRepositories []string
	// SecretNamespace and SecretName name a kubernetes.io/dockerconfigjson
	// Secret with push credentials; empty is anonymous.
	SecretNamespace, SecretName string
}

// pathComponent is the OCI distribution spec's repository path component:
// it rules out ".", ".." and empty components.
var pathComponent = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*$`)

// parseRepository is repo's registry and path components, as a registry
// sees them (docker.io/acme and acme are the same repository); false when a
// component is not a valid one (a "..", ".", an empty one), whatever a
// registry or a proxy in front of it would make of it.
func parseRepository(repo string) (string, []string, bool) {
	r, err := name.NewRepository(repo)
	if err != nil {
		return "", nil, false
	}
	comps := strings.Split(r.RepositoryStr(), "/")
	for _, c := range comps {
		if !pathComponent.MatchString(c) {
			return "", nil, false
		}
	}
	return strings.ToLower(r.RegistryStr()), comps, true
}

// allowed reports whether the sink may push a referrer of an image in repo:
// a fixed Repository is the admin's own; otherwise, with
// AllowedRepositories, repo's parsed registry must be an entry's and its
// path must start with the entry's path components.
func (o *OCISink) allowed(repo string) bool {
	if o.Repository != "" || len(o.AllowedRepositories) == 0 {
		return true
	}
	reg, path, ok := parseRepository(repo)
	if !ok {
		return false
	}
	for _, p := range o.AllowedRepositories {
		ereg, epath, ok := parseAllowed(p)
		if !ok || ereg != reg || len(epath) > len(path) {
			continue
		}
		if slices.Equal(epath, path[:len(epath)]) {
			return true
		}
	}
	return false
}

// parseAllowed is an allowedRepositories entry: a registry and an optional
// path prefix (ghcr.io/acme/, ghcr.io/acme, registry.local:5000).
func parseAllowed(entry string) (string, []string, bool) {
	entry = strings.TrimSuffix(strings.TrimSpace(entry), "/")
	host, path, hasPath := strings.Cut(entry, "/")
	if !hasPath {
		if _, err := name.NewRegistry(host); err != nil {
			return "", nil, false
		}
		return strings.ToLower(host), nil, true
	}
	// A prefix, not a repository: parsed with a last component added, so a
	// single Docker Hub component is not read as library/<name>.
	reg, comps, ok := parseRepository(host + "/" + path + "/x")
	if !ok {
		return "", nil, false
	}
	return reg, comps[:len(comps)-1], true
}

// Reconciler signs and stores PromotionReceipts.
type Reconciler struct {
	ctrl.Client
	// APIReader reads the OCI credentials Secret, which the cache does not
	// hold.
	APIReader ctrl.Reader
	// Signer signs statements.
	Signer *receipt.Signer
	// OCI, when set, pushes every receipt as an OCI referrer of each of its
	// images.
	OCI *OCISink
	// SCM and Providers post the PR comment.
	SCM       scm.SCMProvider
	Providers *scm.Registry
	// NowFn overrides the clock in tests.
	NowFn func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

// Done reports whether rc needs nothing more: Failed, or Stored with its PR
// comment posted (or given up) or no PR to comment on.
func Done(rc *v1alpha1.PromotionReceipt) bool {
	switch rc.Status.Phase {
	case v1alpha1.ReceiptFailed:
		return true
	case v1alpha1.ReceiptStored:
		return rc.Spec.PullRequest == nil || rc.Status.PRComment
	}
	return false
}

// CacheTransform is the controller cache's transform of PromotionReceipts:
// a receipt that is Done is cached without spec.statement and
// status.bundle (up to 256 KiB, and its DSSE payload repeats the statement
// in base64) and without managedFields. Nothing the controller does with a
// done receipt reads them: the reconciler returns before, the UI reads its
// name and phase, retention lists uncached, and kardinal verify-receipt
// reads the API server. A receipt still being signed or stored is cached
// whole.
func CacheTransform(obj interface{}) (interface{}, error) {
	rc, ok := obj.(*v1alpha1.PromotionReceipt)
	if !ok {
		return obj, nil
	}
	rc.ManagedFields = nil
	if Done(rc) {
		rc.Spec.Statement, rc.Status.Bundle = "", ""
	}
	return rc, nil
}

// receiptsResource is the resource objectgone matches a NotFound against.
var receiptsResource = v1alpha1.GroupVersion.WithResource("promotionreceipts").GroupResource()

// Reconcile signs, stores and announces one receipt. It is idempotent: a
// signed receipt is never signed again, a stored sink is not written again,
// and the PR comment is posted once.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrlruntime.Request) (ctrlruntime.Result, error) {
	return objectgone.Reconcile(ctx, req, receiptsResource, r.reconcile)
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrlruntime.Request) (ctrlruntime.Result, error) {
	var rc v1alpha1.PromotionReceipt
	if err := r.Get(ctx, req.NamespacedName, &rc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrlruntime.Result{}, nil
		}
		return ctrlruntime.Result{}, fmt.Errorf("get promotion receipt %s: %w", req.Name, err)
	}
	if Done(&rc) {
		// Nothing left to do, and the cache holds it without its statement
		// and bundle (CacheTransform): return before reading either.
		return ctrlruntime.Result{}, nil
	}
	now := r.now()
	if t := rc.Status.NextRetryAt; t != nil && now.Before(t.Time) {
		return ctrlruntime.Result{RequeueAfter: t.Sub(now)}, nil
	}
	base := rc.DeepCopy()
	switch {
	case rc.Status.Bundle == "":
		return r.sign(ctx, base, &rc, now)
	case r.OCI != nil && !r.ociStored(&rc):
		return r.storeOCI(ctx, base, &rc, now)
	case rc.Status.Phase != v1alpha1.ReceiptStored:
		rc.Status.Phase = v1alpha1.ReceiptStored
		if refused := r.notAllowed(&rc); len(refused) > 0 {
			// Not retried: the allowlist decides the same next time.
			zerolog.Ctx(ctx).Warn().Str("receipt", rc.Name).Strs("repositories", refused).
				Msg("receipt referrers not pushed: repositories outside sinks.oci.allowedRepositories")
			setCondition(&rc, ConditionStored, metav1.ConditionFalse, "RepositoryNotAllowed",
				"stored in "+sinks(&rc)+"; no referrer in "+strings.Join(refused, ", ")+
					": not in the controller's allowed repositories (sinks.oci.allowedRepositories)", now)
		} else {
			setCondition(&rc, ConditionStored, metav1.ConditionTrue, "AllSinks", "stored in "+sinks(&rc), now)
		}
		return ctrlruntime.Result{Requeue: true}, r.patch(ctx, base, &rc)
	case rc.Spec.PullRequest != nil && !rc.Status.PRComment:
		return r.comment(ctx, base, &rc, now)
	}
	return ctrlruntime.Result{}, nil
}

// sign signs the statement once. A statement that does not match its
// digest, or is no receipt, is never signed: the receipt is Failed, since
// someone changed it (the spec is immutable, so only an admin who removed
// the CRD rule could).
func (r *Reconciler) sign(ctx context.Context, base, rc *v1alpha1.PromotionReceipt, now time.Time) (ctrlruntime.Result, error) {
	stmt := []byte(rc.Spec.Statement)
	if receipt.Digest(stmt) != rc.Spec.StatementDigest {
		return r.failed(ctx, base, rc, "StatementMismatch",
			fmt.Sprintf("spec.statement is %s, not spec.statementDigest %s: not signed", receipt.Digest(stmt), rc.Spec.StatementDigest), now)
	}
	if _, err := receipt.Parse(stmt); err != nil {
		return r.failed(ctx, base, rc, "InvalidStatement", err.Error(), now)
	}
	if r.Signer == nil {
		setCondition(rc, ConditionSigned, metav1.ConditionFalse, "NotConfigured", "the controller has no receipt signing configured", now)
		rc.Status.Phase = v1alpha1.ReceiptPending
		return ctrlruntime.Result{}, r.patch(ctx, base, rc)
	}
	sctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	signed, err := r.Signer.Sign(sctx, stmt)
	if err != nil {
		observability.ReceiptsFailedTotal.WithLabelValues(metricSigning).Inc()
		rc.Status.Phase = v1alpha1.ReceiptPending
		setCondition(rc, ConditionSigned, metav1.ConditionFalse, "SignFailed", err.Error(), now)
		return r.retry(ctx, base, rc, now)
	}
	at := metav1.NewTime(now.UTC())
	record := func(rc *v1alpha1.PromotionReceipt) bool {
		if rc.Status.Bundle != "" {
			return false // signed meanwhile: that signature stands
		}
		rc.Status.Bundle, rc.Status.SignedBy, rc.Status.Phase = string(signed.Bundle), &signed.SignedBy, v1alpha1.ReceiptSigned
		rc.Status.Bytes = int64(len(rc.Spec.Statement) + len(signed.Bundle))
		rc.Status.Stored = append(rc.Status.Stored, v1alpha1.ReceiptStorage{Sink: v1alpha1.ReceiptSinkCRD, At: at})
		if signed.LogIndex != nil {
			rc.Status.Stored = append(rc.Status.Stored, v1alpha1.ReceiptStorage{
				Sink: v1alpha1.ReceiptSinkRekor, LogIndex: signed.LogIndex, URL: r.Signer.RekorURL, At: at})
		}
		rc.Status.Attempts, rc.Status.NextRetryAt = 0, nil
		setCondition(rc, ConditionSigned, metav1.ConditionTrue, "Signed", "signed by "+signerText(&signed.SignedBy), now)
		return true
	}
	record(rc)
	zerolog.Ctx(ctx).Info().Str("receipt", rc.Name).Str("mode", signed.SignedBy.Mode).Msg("promotion receipt signed")
	// A conflict re-applies this signature to the stored receipt instead of
	// signing again: a second signature would be a second certificate and,
	// with Rekor, a second public log entry.
	if err := r.patchKeeping(ctx, base, rc, record); err != nil {
		return ctrlruntime.Result{}, err
	}
	observability.ReceiptsTotal.WithLabelValues(signed.SignedBy.Mode).Inc()
	observability.ReceiptBytesTotal.Add(float64(len(rc.Spec.Statement) + len(signed.Bundle)))
	return ctrlruntime.Result{Requeue: true}, nil
}

// ociStored reports whether every image subject of rc has its referrer.
func (r *Reconciler) ociStored(rc *v1alpha1.PromotionReceipt) bool {
	return len(r.pendingImages(rc)) == 0
}

// imageSubjects are the image subjects of rc. A config version has none:
// its subject is a commit.
func imageSubjects(rc *v1alpha1.PromotionReceipt) []receipt.Subject {
	st, err := receipt.Parse([]byte(rc.Spec.Statement))
	if err != nil {
		return nil
	}
	var out []receipt.Subject
	for _, s := range st.Subject {
		if s.Digest["sha256"] != "" && !strings.HasPrefix(s.Name, "kardinal://") {
			out = append(out, s)
		}
	}
	return out
}

// notAllowed are the image repositories of rc the OCI sink may not push to.
func (r *Reconciler) notAllowed(rc *v1alpha1.PromotionReceipt) []string {
	if r.OCI == nil {
		return nil
	}
	var out []string
	for _, s := range imageSubjects(rc) {
		if !r.OCI.allowed(s.Name) {
			out = append(out, s.Name)
		}
	}
	return out
}

// pendingImages are the image subjects of rc without a stored referrer
// that the OCI sink may push.
func (r *Reconciler) pendingImages(rc *v1alpha1.PromotionReceipt) []receipt.Subject {
	var out []receipt.Subject
	for _, s := range imageSubjects(rc) {
		if r.OCI != nil && !r.OCI.allowed(s.Name) {
			continue
		}
		repo := s.Name
		if r.OCI != nil && r.OCI.Repository != "" {
			repo = r.OCI.Repository
		}
		stored := false
		for _, e := range rc.Status.Stored {
			if e.Sink == v1alpha1.ReceiptSinkOCI && e.Subject == s.Name && strings.HasPrefix(e.Ref, repo+"@") {
				stored = true
			}
		}
		if !stored {
			out = append(out, s)
		}
	}
	return out
}

// storeOCI pushes the receipt as a referrer of each image not yet stored.
func (r *Reconciler) storeOCI(ctx context.Context, base, rc *v1alpha1.PromotionReceipt, now time.Time) (ctrlruntime.Result, error) {
	reg := r.OCI.Registry
	if r.OCI.SecretName != "" {
		kc, err := r.keychain(ctx)
		if err != nil {
			return r.sinkFailed(ctx, base, rc, "OCICredentials", err, now)
		}
		reg.Keychain = kc
	}
	// The resourceUri is annotated on each referrer, for verify-receipt
	// --image to skip other environments' receipts before it reads them.
	resource := ""
	if st, err := receipt.Parse([]byte(rc.Spec.Statement)); err == nil {
		resource = st.Predicate.ResourceURI
	}
	for _, s := range r.pendingImages(rc) {
		sctx, cancel := context.WithTimeout(ctx, sinkTimeout)
		repo := s.Name
		if r.OCI.Repository != "" {
			repo = r.OCI.Repository
		}
		ref, err := reg.PushReferrer(sctx, repo, s.Name, "sha256:"+s.Digest["sha256"], resource, []byte(rc.Status.Bundle))
		cancel()
		if err != nil {
			return r.sinkFailed(ctx, base, rc, "OCIPushFailed", err, now)
		}
		rc.Status.Stored = append(rc.Status.Stored, v1alpha1.ReceiptStorage{Sink: v1alpha1.ReceiptSinkOCI, Ref: ref, Subject: s.Name, At: metav1.NewTime(now.UTC())})
	}
	rc.Status.Attempts, rc.Status.NextRetryAt = 0, nil
	return ctrlruntime.Result{Requeue: true}, r.patch(ctx, base, rc)
}

func (r *Reconciler) keychain(ctx context.Context) (authn.Keychain, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	return loadKeychain(ctx, reader, r.OCI.SecretNamespace, r.OCI.SecretName)
}

func (r *Reconciler) sinkFailed(ctx context.Context, base, rc *v1alpha1.PromotionReceipt, reason string, err error,
	now time.Time) (ctrlruntime.Result, error) {
	observability.ReceiptsFailedTotal.WithLabelValues(metricOCI).Inc()
	setCondition(rc, ConditionStored, metav1.ConditionFalse, reason, err.Error(), now)
	return r.retry(ctx, base, rc, now)
}

// comment posts the PR comment once: status.prComment is written after a
// post succeeds. A failing post is retried with backoff, and given up
// after maxCommentAttempts.
func (r *Reconciler) comment(ctx context.Context, base, rc *v1alpha1.PromotionReceipt, now time.Time) (ctrlruntime.Result, error) {
	pr := rc.Spec.PullRequest
	provider, err := r.scmFor(ctx, rc)
	found := false
	if err == nil {
		sctx, cancel := context.WithTimeout(ctx, sinkTimeout)
		// Search before posting: a post whose status write did not land (a
		// crash, a conflict) is not posted again. A provider that cannot
		// search is posted to, and its status write is retried on a conflict.
		if f, ok := provider.(scm.PRCommentFinder); ok {
			found, err = f.HasPRComment(sctx, pr.Repo, pr.Number, CommentMarker(rc))
			if errors.Is(err, scm.ErrCommentSearchUnsupported) {
				err = nil
			}
		}
		if err == nil && !found {
			err = provider.CommentOnPR(sctx, pr.Repo, pr.Number, CommentBody(rc))
		}
		cancel()
	}
	if err != nil {
		observability.ReceiptsFailedTotal.WithLabelValues(metricPRComment).Inc()
		if rc.Status.Attempts+1 >= maxCommentAttempts {
			rc.Status.PRComment = true // given up: the condition says so
			setCondition(rc, ConditionPRCommented, metav1.ConditionFalse, "GaveUp",
				fmt.Sprintf("no comment on PR #%d after %d attempts: %v", pr.Number, maxCommentAttempts, err), now)
			rc.Status.Attempts, rc.Status.NextRetryAt = 0, nil
			return ctrlruntime.Result{}, r.patch(ctx, base, rc)
		}
		setCondition(rc, ConditionPRCommented, metav1.ConditionFalse, "CommentFailed", err.Error(), now)
		return r.retry(ctx, base, rc, now)
	}
	msg := fmt.Sprintf("commented on PR #%d", pr.Number)
	if found {
		msg = fmt.Sprintf("PR #%d already has the receipt's comment", pr.Number)
	}
	commented := func(rc *v1alpha1.PromotionReceipt) bool {
		if rc.Status.PRComment {
			return false
		}
		rc.Status.PRComment, rc.Status.Attempts, rc.Status.NextRetryAt = true, 0, nil
		setCondition(rc, ConditionPRCommented, metav1.ConditionTrue, "Commented", msg, now)
		return true
	}
	commented(rc)
	return ctrlruntime.Result{}, r.patchKeeping(ctx, base, rc, commented)
}

// patchKeeping patches rc's status like patch, and on a conflict re-reads
// the receipt and re-applies apply to it, so what an external call just did
// (a signature, a PR comment) is recorded rather than done again. apply
// returns false when the stored receipt already has it.
func (r *Reconciler) patchKeeping(ctx context.Context, base, rc *v1alpha1.PromotionReceipt,
	apply func(*v1alpha1.PromotionReceipt) bool) error {
	err := r.patch(ctx, base, rc)
	for i := 0; i < 5 && apierrors.IsConflict(err); i++ {
		var cur v1alpha1.PromotionReceipt
		if gerr := r.Get(ctx, ctrl.ObjectKeyFromObject(rc), &cur); gerr != nil {
			if apierrors.IsNotFound(gerr) {
				return nil
			}
			return fmt.Errorf("re-read promotion receipt %s: %w", rc.Name, gerr)
		}
		curBase := cur.DeepCopy()
		if !apply(&cur) {
			return nil
		}
		err = r.patch(ctx, curBase, &cur)
	}
	return err
}

func (r *Reconciler) scmFor(ctx context.Context, rc *v1alpha1.PromotionReceipt) (scm.SCMProvider, error) {
	pr := rc.Spec.PullRequest
	if pr.ScmProvider == nil {
		if r.SCM == nil {
			return nil, fmt.Errorf("no SCM provider to comment on PR #%d", pr.Number)
		}
		return r.SCM, nil
	}
	if r.Providers == nil {
		return nil, fmt.Errorf("%s %s: the controller has no ScmProvider registry", pr.ScmProvider.Kind, pr.ScmProvider.Name)
	}
	res, err := r.Providers.ForIdentity(ctx, rc.Namespace, *pr.ScmProvider, pr.Repo)
	if err != nil {
		return nil, fmt.Errorf("scm provider for PR #%d: %w", pr.Number, err)
	}
	return res.Provider, nil
}

// CommentMarker is the hidden marker of rc's PR comment, which the search
// before posting looks for.
func CommentMarker(rc *v1alpha1.PromotionReceipt) string {
	return "<!-- kardinal-receipt:" + rc.Namespace + "/" + rc.Name + " -->"
}

// CommentBody is the PR comment of a signed receipt.
func CommentBody(rc *v1alpha1.PromotionReceipt) string {
	pipeline, env, bundle := rc.Labels["kardinal.io/pipeline"], rc.Labels["kardinal.io/environment"], rc.Labels["kardinal.io/bundle"]
	return fmt.Sprintf("Promotion receipt: `%s`, signed by %s, stored in %s.\n\nVerify: `kardinal verify-receipt %s %s --bundle %s`\n\n%s",
		rc.Name, signerText(rc.Status.SignedBy), sinks(rc), pipeline, env, bundle, CommentMarker(rc))
}

func signerText(s *v1alpha1.ReceiptSigner) string {
	if s == nil {
		return "nobody"
	}
	switch {
	case s.Mode == v1alpha1.ReceiptSigningKeyless && s.Issuer != "":
		return fmt.Sprintf("%s (issuer %s)", s.Identity, s.Issuer)
	case s.Identity != "":
		return fmt.Sprintf("%s (%s)", s.Identity, s.Mode)
	}
	return s.Mode
}

func sinks(rc *v1alpha1.PromotionReceipt) string {
	var out []string
	for _, s := range rc.Status.Stored {
		switch {
		case s.Ref != "":
			out = append(out, s.Sink+" "+s.Ref)
		case s.LogIndex != nil:
			out = append(out, fmt.Sprintf("%s %s (log index %d)", s.Sink, s.URL, *s.LogIndex))
		default:
			out = append(out, s.Sink)
		}
	}
	return strings.Join(out, ", ")
}

func (r *Reconciler) failed(ctx context.Context, base, rc *v1alpha1.PromotionReceipt, reason, msg string, now time.Time) (ctrlruntime.Result, error) {
	rc.Status.Phase = v1alpha1.ReceiptFailed
	setCondition(rc, ConditionSigned, metav1.ConditionFalse, reason, msg, now)
	zerolog.Ctx(ctx).Error().Str("receipt", rc.Name).Str("reason", reason).Msg(msg)
	return ctrlruntime.Result{}, r.patch(ctx, base, rc)
}

// retry records a failed attempt and when the next runs.
func (r *Reconciler) retry(ctx context.Context, base, rc *v1alpha1.PromotionReceipt, now time.Time) (ctrlruntime.Result, error) {
	rc.Status.Attempts++
	d := backoff(rc.Status.Attempts)
	next := metav1.NewTime(now.Add(d).UTC())
	rc.Status.NextRetryAt = &next
	if err := r.patch(ctx, base, rc); err != nil {
		return ctrlruntime.Result{}, err
	}
	return ctrlruntime.Result{RequeueAfter: d}, nil
}

func backoff(attempts int) time.Duration {
	d := retryBase
	for i := 1; i < attempts && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// patch writes rc's status, optimistic-locked: a reconcile from a stale
// copy gets a conflict and is retried, so a receipt is never signed twice
// over (the CRD also refuses to change status.bundle once set).
func (r *Reconciler) patch(ctx context.Context, base, rc *v1alpha1.PromotionReceipt) error {
	if err := r.Status().Patch(ctx, rc, ctrl.MergeFromWithOptions(base, ctrl.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("patch promotion receipt %s status: %w", rc.Name, err)
	}
	return nil
}

func setCondition(rc *v1alpha1.PromotionReceipt, typ string, status metav1.ConditionStatus, reason, msg string, now time.Time) {
	if len(msg) > 1024 {
		msg = msg[:1024] + "..."
	}
	meta.SetStatusCondition(&rc.Status.Conditions, metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg, ObservedGeneration: rc.Generation,
		LastTransitionTime: metav1.NewTime(now.UTC()),
	})
}

// MaxConcurrentReconciles bounds parallel receipt work.
const MaxConcurrentReconciles = 4

// SetupWithManager registers the reconciler, sharded like every namespaced
// reconciler (pkg/shard).
func (r *Reconciler) SetupWithManager(mgr ctrlruntime.Manager) error {
	b := ctrlruntime.NewControllerManagedBy(mgr).
		For(&v1alpha1.PromotionReceipt{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: MaxConcurrentReconciles})
	return shard.Active().Complete(b, tracing.WrapReconciler("promotionreceipt", r), &v1alpha1.PromotionReceiptList{})
}
