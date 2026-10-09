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

// Package subscription implements the SubscriptionReconciler.
//
// The Subscription CRD enables CI-less artifact discovery by polling OCI registries
// and Git repositories on a configurable interval. When a new digest or commit is
// detected, the reconciler creates a Bundle CRD to trigger the promotion pipeline.
//
// Architecture (Graph-first compliance):
//
//	This is an Owned node (Q2 in the Graph-first question stack):
//	  - It writes only to its own CRD status (status.phase, status.lastSeenDigest, etc.)
//	  - It creates Bundle CRDs in its own namespace. The Bundles carry labels, not
//	    owner references: deleting a Subscription leaves its Bundles (and their
//	    promotions) in place.
//	  - time.Now() is only used inside status writes (no bare time calls in logic)
//	  - No cross-CRD status mutations
//	  - No exec.Command or in-memory state between reconcile iterations
package subscription

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

const (
	// maxBundleNameLen keeps Bundle names usable as label values
	// (kardinal.io/bundle on PromotionSteps and PolicyGates).
	maxBundleNameLen = 63
	// defaultInterval is the polling interval when spec is empty, zero or invalid.
	defaultInterval = 5 * time.Minute
	// minInterval is the shortest polling interval: a smaller spec interval is
	// raised to it, so a misconfiguration cannot poll the registry in a hot loop.
	minInterval = 30 * time.Second
	// errorRequeueInterval is how long to wait before retrying after a watch error.
	errorRequeueInterval = 1 * time.Minute
)

// Reconciler watches artifact sources and creates Bundle CRDs when new artifacts are detected.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client
	// WatcherFn constructs a Watcher for the given Subscription.
	// Overridable for testing.
	WatcherFn func(*kardinalv1alpha1.Subscription) (source.Watcher, error)
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile processes a single Subscription object.
//
// State machine:
//  1. Not found → skip (deleted).
//  2. Create watcher via WatcherFn.
//  3. Call watcher.Watch(lastSeenDigest).
//  4. If error → write phase=Error + message, requeue after 1m.
//  5. If Changed=false → write phase=Watching, requeue after interval.
//  6. If Changed=true → create Bundle CRD (image or config), update status, requeue.
//
// A Subscription deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, subscriptionsResource, r.reconcile)
}

// subscriptionsResource is the resource objectgone matches a NotFound against.
var subscriptionsResource = kardinalv1alpha1.GroupVersion.WithResource("subscriptions").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("subscription", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var sub kardinalv1alpha1.Subscription
	if err := r.Get(ctx, req.NamespacedName, &sub); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get subscription: %w", err)
	}

	now := r.now()

	// A Subscription only creates Bundles in its own namespace. spec.namespace
	// used to redirect Bundles into any namespace, which let a user who can
	// create a Subscription start promotions of an image they choose in another
	// team's Pipeline.
	if sub.Spec.Namespace != "" && sub.Spec.Namespace != sub.Namespace {
		return r.writeError(ctx, &sub, now, fmt.Sprintf(
			"spec.namespace %q is not allowed: a Subscription creates Bundles only in its own namespace %q; "+
				"remove spec.namespace or create the Subscription in %q",
			sub.Spec.Namespace, sub.Namespace, sub.Spec.Namespace))
	}

	// Create the watcher for this subscription type.
	watcher, err := r.WatcherFn(&sub)
	if err != nil {
		return r.writeError(ctx, &sub, now, fmt.Sprintf("create watcher: %s", err))
	}

	// Poll the artifact source.
	result, watchErr := watcher.Watch(ctx, sub.Status.LastSeenDigest)
	if watchErr != nil {
		return r.writeError(ctx, &sub, now, watchErr.Error())
	}

	interval := r.parseInterval(&sub)

	// No change — update lastCheckedAt and requeue. The watchers report the
	// first poll (empty lastSeenDigest) as "no change", so the digest must be
	// recorded here: it is the baseline the next poll compares against.
	if !result.Changed {
		log.Debug().Str("digest", result.Digest).Msg("no change detected")
		return ctrl.Result{RequeueAfter: interval}, r.patchStatus(ctx, &sub, func(s *kardinalv1alpha1.SubscriptionStatus) {
			s.Phase = "Watching"
			s.LastCheckedAt = now.UTC().Format(time.RFC3339)
			if result.Digest != "" {
				s.LastSeenDigest = result.Digest
			}
			s.Message = ""
		})
	}

	// New artifact detected — create a Bundle.
	bundleName, createErr := r.createBundle(ctx, &sub, result, now)
	if createErr != nil {
		return r.writeError(ctx, &sub, now, fmt.Sprintf("create bundle: %s", createErr))
	}

	log.Info().Str("bundle", bundleName).Str("digest", result.Digest).Msg("created bundle for new artifact")

	return ctrl.Result{RequeueAfter: interval}, r.patchStatus(ctx, &sub, func(s *kardinalv1alpha1.SubscriptionStatus) {
		s.Phase = "Watching"
		s.LastCheckedAt = now.UTC().Format(time.RFC3339)
		s.LastSeenDigest = result.Digest
		s.LastBundleCreated = bundleName
		s.Message = ""
	})
}

// createBundle creates a Bundle CRD from the WatchResult.
// Returns the created Bundle's name on success.
//
// Deduplication: when the Subscription's newest Bundle (label
// kardinal.io/subscription=<name>) is already for the digest
// (kardinal.io/source-digest), it is returned and nothing is created. That is
// the Bundle a reconcile that read a stale status.lastSeenDigest, or crashed
// before writing it, created for this change (#620). A digest that comes back
// after another one (a moving tag pushed back to an earlier image) gets a new
// Bundle; its name ends in -<n>, one more than the highest generation of the
// Subscription's Bundles for the digest, so a retry of the same change picks
// the same name and hits AlreadyExists.
func (r *Reconciler) createBundle(ctx context.Context, sub *kardinalv1alpha1.Subscription, result *source.WatchResult, now time.Time) (string, error) {
	ns := sub.Namespace

	generation := 0
	if result.Digest != "" {
		newest, highest, err := r.bundlesForDigest(ctx, ns, sub.Name, result.Digest)
		if err != nil {
			return "", fmt.Errorf("createBundle: check for existing bundle: %w", err)
		}
		if newest != "" {
			zerolog.Ctx(ctx).Debug().
				Str("bundle", newest).
				Str("digest", result.Digest).
				Msg("the newest bundle is for this digest — skipping creation")
			return newest, nil
		}
		generation = highest + 1
	}

	bundleName := bundleNameFor(sub.Name, result, now, generation)

	bundleType := "image"
	if sub.Spec.Type == kardinalv1alpha1.SubscriptionTypeGit {
		bundleType = "config"
	}

	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bundleName,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/pipeline":     sub.Spec.Pipeline,
				"kardinal.io/subscription": sub.Name,
				// source-digest enables idempotent dedup by label selector (#620):
				// any reconcile (including concurrent HA replicas) can find this
				// bundle before creating a duplicate. The value is the first 63
				// characters of the digest (E2E-R13).
				sourceDigestLabelKey: sourceDigestLabel(result.Digest),
			},
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     bundleType,
			Pipeline: sub.Spec.Pipeline,
		},
	}

	// Populate artifact-specific fields.
	if sub.Spec.Type == kardinalv1alpha1.SubscriptionTypeImage && sub.Spec.Image != nil {
		bundle.Spec.Images = []kardinalv1alpha1.ImageRef{
			{
				// The watcher accepts an explicit scheme
				// ("http://registry.registry.svc.cluster.local:5000/app");
				// an image reference does not carry one.
				Repository: strings.TrimPrefix(strings.TrimPrefix(sub.Spec.Image.Registry, "https://"), "http://"),
				Tag:        result.Tag,
				Digest:     result.Digest,
			},
		}
		bundle.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{
			CommitSHA: result.Digest,
		}
	} else if sub.Spec.Type == kardinalv1alpha1.SubscriptionTypeGit && sub.Spec.Git != nil {
		bundle.Spec.ConfigRef = &kardinalv1alpha1.ConfigRef{
			GitRepo:   sub.Spec.Git.RepoURL,
			CommitSHA: result.Digest,
		}
		bundle.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{
			CommitSHA: result.Digest,
		}
	}
	lifecycle.StampCreatedAt(bundle, now) // sub-second creation order for supersession

	if err := r.Create(ctx, bundle); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("create bundle %s: %w", bundleName, err)
		}
		// Crash recovery returns the existing Bundle, but only when it is for the
		// same artifact: a name collision must not swallow a new digest.
		var existing kardinalv1alpha1.Bundle
		if getErr := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: bundleName}, &existing); getErr != nil {
			return "", fmt.Errorf("get existing bundle %s: %w", bundleName, getErr)
		}
		if !sourceDigestMatches(existing.Labels[sourceDigestLabelKey], result.Digest) ||
			existing.Labels["kardinal.io/subscription"] != sub.Name {
			return "", fmt.Errorf("bundle %s already exists for another artifact or owner; not creating a Bundle for digest %s",
				bundleName, result.Digest)
		}
		return bundleName, nil
	}
	return bundleName, nil
}

// bundleNameFor returns "<subscription>-<tag>-<digest[:8]>", made DNS-safe and
// at most maxBundleNameLen characters. The digest suffix keeps a re-pushed
// mutable tag ("latest", "v1") from colliding with the previous Bundle. The
// tag part is omitted when it is already a prefix of the digest (git short SHA).
// A generation above 1 (a digest that came back) is appended as -<generation>.
func bundleNameFor(subName string, result *source.WatchResult, now time.Time, generation int) string {
	digest := shortDigest(result.Digest)
	tag := dnsSlug(result.Tag)
	if tag != "" && digest != "" && strings.HasPrefix(digest, tag) {
		tag = ""
	}

	suffix := digest
	if suffix == "" {
		suffix = now.UTC().Format("20060102-150405")
	} else if generation > 1 {
		suffix += "-" + strconv.Itoa(generation)
	}
	prefix := subName
	if tag != "" {
		prefix += "-" + tag
	}
	if max := maxBundleNameLen - len(suffix) - 1; len(prefix) > max {
		prefix = strings.TrimRight(prefix[:max], "-.")
	}
	return prefix + "-" + suffix
}

// shortDigest returns the first 8 characters of the DNS-safe digest without its
// algorithm prefix: the digest part of a Bundle name.
func shortDigest(digest string) string {
	if _, hexPart, ok := strings.Cut(digest, ":"); ok {
		digest = hexPart
	}
	digest = dnsSlug(digest)
	if len(digest) > 8 {
		digest = digest[:8]
	}
	return digest
}

// bundleGeneration returns n for a Bundle name that ends in -<digest[:8]>-<n>
// and 1 for any other name of a Bundle for digest (the first one, or a name an
// older release gave it).
func bundleGeneration(name, digest string) int {
	_, after, ok := strings.Cut(name, "-"+shortDigest(digest)+"-")
	if !ok {
		return 1
	}
	n, err := strconv.Atoi(after)
	if err != nil || n < 2 {
		return 1
	}
	return n
}

// dnsSlug lowercases s and replaces every character that is not a lowercase
// letter or digit with "-", collapsing runs and trimming the ends.
func dnsSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// writeError patches status to phase=Error and returns a requeue result.
func (r *Reconciler) writeError(ctx context.Context, sub *kardinalv1alpha1.Subscription, now time.Time, msg string) (ctrl.Result, error) {
	zerolog.Ctx(ctx).Warn().Str("subscription", sub.Name).Str("error", msg).Msg("subscription watch error")
	patchErr := r.patchStatus(ctx, sub, func(s *kardinalv1alpha1.SubscriptionStatus) {
		s.Phase = "Error"
		s.LastCheckedAt = now.UTC().Format(time.RFC3339)
		s.Message = msg
	})
	return ctrl.Result{RequeueAfter: errorRequeueInterval}, patchErr
}

// patchStatus applies a mutating function to the subscription's status.
func (r *Reconciler) patchStatus(ctx context.Context, sub *kardinalv1alpha1.Subscription, fn func(*kardinalv1alpha1.SubscriptionStatus)) error {
	patch := client.MergeFrom(sub.DeepCopy())
	fn(&sub.Status)
	if err := r.Status().Patch(ctx, sub, patch); err != nil {
		return fmt.Errorf("status patch: %w", err)
	}
	return nil
}

// parseInterval parses the subscription's polling interval. It returns
// defaultInterval when the interval is empty, zero or does not parse, and
// raises a positive interval below minInterval to minInterval.
func (r *Reconciler) parseInterval(sub *kardinalv1alpha1.Subscription) time.Duration {
	var raw string
	switch sub.Spec.Type {
	case kardinalv1alpha1.SubscriptionTypeImage:
		if sub.Spec.Image != nil {
			raw = sub.Spec.Image.Interval
		}
	case kardinalv1alpha1.SubscriptionTypeGit:
		if sub.Spec.Git != nil {
			raw = sub.Spec.Git.Interval
		}
	}
	if raw == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	if d < minInterval {
		return minInterval
	}
	return d
}

// now returns the current time, using NowFn if set.
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}

// bundlesForDigest lists the Subscription's Bundles (label
// kardinal.io/subscription). It returns the name of the newest one
// (lifecycle.CompareCreation) when its kardinal.io/source-digest label is for
// digest, and otherwise the highest bundleGeneration of the Bundles for digest,
// 0 when there is none.
//
// The list reads Bundles, not status fields, so it has no read-compare-write
// race with another reconcile (#620). The label matches in the current form
// and the form older controllers wrote (legacySourceDigestLabel), so a Bundle
// created before an upgrade is still found and not duplicated.
func (r *Reconciler) bundlesForDigest(ctx context.Context, namespace, subscriptionName, digest string) (string, int, error) {
	var list kardinalv1alpha1.BundleList
	if err := r.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{"kardinal.io/subscription": subscriptionName},
	); err != nil {
		return "", 0, fmt.Errorf("list bundles of subscription %s: %w", subscriptionName, err)
	}
	var newest *kardinalv1alpha1.Bundle
	highest := 0
	for i := range list.Items {
		b := &list.Items[i]
		if newest == nil || lifecycle.CompareCreation(b, newest) > 0 {
			newest = b
		}
		if sourceDigestMatches(b.Labels[sourceDigestLabelKey], digest) {
			highest = max(highest, bundleGeneration(b.Name, digest))
		}
	}
	if newest != nil && sourceDigestMatches(newest.Labels[sourceDigestLabelKey], digest) {
		return newest.Name, 0, nil
	}
	return "", highest, nil
}

// sourceDigestLabelKey is the Bundle label that records the artifact digest a
// Subscription created the Bundle for.
const sourceDigestLabelKey = "kardinal.io/source-digest"

// sourceDigestLabel turns a digest into the kardinal.io/source-digest label value.
// Label values must be 63 characters or fewer, and may only contain alphanumerics,
// hyphens, underscores, and dots, starting and ending with an alphanumeric.
// The algorithm prefix ("sha256:") is stripped, other invalid characters are
// dropped, and the first 63 characters are kept (a sha256 hex digest is 64), so
// the label is a prefix of the digest and matches its short forms (E2E-R13).
func sourceDigestLabel(digest string) string {
	s := digestLabelChars(digest)
	if len(s) > 63 {
		s = s[:63]
	}
	return strings.Trim(s, "-_.")
}

// legacySourceDigestLabel is the label value controllers before the E2E-R13
// fix wrote: the last 63 characters, which dropped the first hex character of
// a sha256 digest. Dedup still matches it so an upgrade does not create a
// second Bundle for a digest a Bundle already exists for.
func legacySourceDigestLabel(digest string) string {
	s := digestLabelChars(digest)
	if len(s) > 63 {
		s = s[len(s)-63:]
	}
	return strings.Trim(s, "-_.")
}

// sourceDigestMatches reports whether a kardinal.io/source-digest label value
// identifies digest, in the current or the legacy form.
func sourceDigestMatches(value, digest string) bool {
	return value == sourceDigestLabel(digest) || value == legacySourceDigestLabel(digest)
}

// digestLabelChars strips the algorithm prefix from digest and drops every
// character a label value cannot hold.
func digestLabelChars(digest string) string {
	if _, hexPart, ok := strings.Cut(digest, ":"); ok {
		digest = hexPart
	}
	return strings.Map(func(c rune) rune {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			return c
		}
		return -1
	}, digest)
}

// SetupWithManager registers the SubscriptionReconciler with the controller-runtime Manager.
// Its own status writes do not re-trigger it (eventfilter.SpecOrAnnotationChanged):
// every poll writes lastCheckedAt, and each re-trigger was an extra registry
// or git poll (C04-gates-36). Polling is driven by RequeueAfter; a spec edit or
// an annotation change polls at once.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Subscription{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged))
	return shard.Active().Complete(b, r, &kardinalv1alpha1.SubscriptionList{})
}
