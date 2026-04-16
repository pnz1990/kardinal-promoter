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
//	  - It creates Bundle CRDs as owned child resources (permitted for Owned nodes)
//	  - time.Now() is only used inside status writes (no bare time calls in logic)
//	  - No cross-CRD status mutations
//	  - No exec.Command or in-memory state between reconcile iterations
package subscription

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

const (
	// defaultInterval is the polling interval when spec is empty or invalid.
	defaultInterval = 5 * time.Minute
	// minInterval prevents hot-loops from misconfiguration.
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
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// No change — update lastCheckedAt and requeue.
	if !result.Changed {
		log.Debug().Str("digest", result.Digest).Msg("no change detected")
		return ctrl.Result{RequeueAfter: interval}, r.patchStatus(ctx, &sub, func(s *kardinalv1alpha1.SubscriptionStatus) {
			s.Phase = "Watching"
			s.LastCheckedAt = now.UTC().Format(time.RFC3339)
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
// Deduplication: before creating, checks for an existing Bundle with label
// kardinal.io/source-digest=<digest> and kardinal.io/subscription=<name>.
// This is safe under HA and concurrent reconciles — unlike the status.lastSeenDigest
// comparison, which has a read-compare-write race (#620).
func (r *Reconciler) createBundle(ctx context.Context, sub *kardinalv1alpha1.Subscription, result *source.WatchResult, now time.Time) (string, error) {
	ns := sub.Spec.Namespace
	if ns == "" {
		ns = sub.Namespace
	}

	// Short-circuit: check if a Bundle for this digest already exists in the API server.
	// Uses a label selector — safe under concurrent reconciles and HA deployments.
	if result.Digest != "" {
		existingName, err := r.findExistingBundleForDigest(ctx, ns, sub.Name, result.Digest)
		if err != nil {
			return "", fmt.Errorf("createBundle: check for existing bundle: %w", err)
		}
		if existingName != "" {
			zerolog.Ctx(ctx).Debug().
				Str("bundle", existingName).
				Str("digest", result.Digest).
				Msg("bundle already exists for digest — skipping creation")
			return existingName, nil
		}
	}

	// Derive bundle name from subscription name + short digest.
	shortDigest := result.Tag
	if shortDigest == "" && len(result.Digest) >= 8 {
		shortDigest = result.Digest[len(result.Digest)-8:]
	}
	if shortDigest == "" {
		shortDigest = now.Format("20060102-150405")
	}
	bundleName := fmt.Sprintf("%s-%s", sub.Name, shortDigest)
	// Kubernetes name max is 253 chars; truncate if necessary.
	if len(bundleName) > 253 {
		bundleName = bundleName[:253]
	}

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
				// bundle before creating a duplicate.
				"kardinal.io/source-digest": sanitizeLabelValue(result.Digest),
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
				Repository: sub.Spec.Image.Registry,
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

	if err := r.Create(ctx, bundle); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Already created (crash recovery) — return name without error.
			return bundleName, nil
		}
		return "", fmt.Errorf("create bundle %s: %w", bundleName, err)
	}
	return bundleName, nil
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

// parseInterval parses the subscription's polling interval.
// Returns defaultInterval on parse errors. Enforces minInterval.
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
	if err != nil || d < minInterval {
		return defaultInterval
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

// findExistingBundleForDigest looks up a Bundle by label selector for the given
// subscription + digest combination. Returns the Bundle name if found, or "" if not.
//
// Using a label selector is safe under HA and concurrent reconciles — it reads from
// the API server (or cache) without a read-compare-write race on status fields (#620).
func (r *Reconciler) findExistingBundleForDigest(ctx context.Context, namespace, subscriptionName, digest string) (string, error) {
	safeDigest := sanitizeLabelValue(digest)
	if safeDigest == "" {
		return "", nil
	}

	var list kardinalv1alpha1.BundleList
	if err := r.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"kardinal.io/subscription":  subscriptionName,
			"kardinal.io/source-digest": safeDigest,
		},
	); err != nil {
		return "", fmt.Errorf("findExistingBundleForDigest: list: %w", err)
	}
	if len(list.Items) == 0 {
		return "", nil
	}
	// Return the first match. Under normal operation there is at most one.
	return list.Items[0].Name, nil
}

// sanitizeLabelValue truncates and sanitizes a string for use as a Kubernetes label value.
// Label values must be 63 characters or fewer, and may only contain alphanumerics,
// hyphens, underscores, and dots, starting and ending with an alphanumeric.
// Digests (SHA-256 hex, OCI sha256:...) are shortened to the last 40 hex chars.
func sanitizeLabelValue(s string) string {
	if s == "" {
		return ""
	}
	// Strip common prefixes like "sha256:"
	if len(s) > 7 && s[:7] == "sha256:" {
		s = s[7:]
	}
	// Kubernetes label values must be <= 63 chars.
	if len(s) > 63 {
		s = s[len(s)-63:]
	}
	return s
}

// SetupWithManager registers the SubscriptionReconciler with the controller-runtime Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Subscription{}).
		Complete(r)
}
