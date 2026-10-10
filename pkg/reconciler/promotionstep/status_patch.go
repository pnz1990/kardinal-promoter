// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// statusPatchTries bounds how often patchStatusLocked writes a status over
// changes others made to the step's spec or metadata.
const statusPatchTries = 3

// patchStatusLocked writes ps's status as a merge patch against base,
// locked on the resourceVersion base was read at. A Conflict has two causes:
//
//   - another reconcile wrote the step's status since base was read (a stale
//     cache, or a newer reconcile that already moved on): the write is
//     refused, and the caller's Conflict handling applies, as before;
//   - only the spec or metadata changed (kro applying the template or a
//     mirror patch, a relabel, the turn queue's annotation): the status this
//     reconcile computed is still the one to write, so it is written over the
//     fresh copy. Dropping it lost the record of work already done, such as
//     the commit git-push had just pushed (#1664).
//
// The status is compared with the API server's copy (APIReader), not the
// cache. On success ps holds the stored object.
func (r *Reconciler) patchStatusLocked(ctx context.Context, base, ps *v1alpha1.PromotionStep) error {
	err := r.Status().Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	for try := 1; try < statusPatchTries && apierrors.IsConflict(err); try++ {
		fresh, gerr := r.readStep(ctx, client.ObjectKeyFromObject(ps))
		if gerr != nil {
			return gerr
		}
		if fresh == nil {
			return apierrors.NewNotFound(v1alpha1.GroupVersion.WithResource("promotionsteps").GroupResource(), ps.Name)
		}
		if !equality.Semantic.DeepEqual(fresh.Status, base.Status) {
			return err // another reconcile wrote the status
		}
		want := fresh.DeepCopy()
		ps.Status.DeepCopyInto(&want.Status)
		if err = r.Status().Patch(ctx, want, client.MergeFromWithOptions(fresh, client.MergeFromWithOptimisticLock{})); err == nil {
			want.DeepCopyInto(ps)
		}
	}
	return err
}

// cacheBehind reports whether the API server stores a newer status for ps
// than the copy this reconcile read, which comes from the informer cache.
// The step engine checks it before running any step: its git work has
// effects outside the cluster that a refused status patch cannot undo. A
// reconcile requeued right after its own write (the Pending → Promoting
// transition, then the AuditEvent outbox flush) can read the cache before
// the last write arrives; it then pushed the commit, its HealthChecking
// patch was refused, and the next reconcile ran the step list again and
// found nothing to commit, so the step recorded no commit to wait for
// (#1664). A newer spec or metadata alone is not a reason to wait:
// patchStatusLocked writes over it.
func (r *Reconciler) cacheBehind(ctx context.Context, ps *v1alpha1.PromotionStep) (bool, error) {
	fresh, err := r.readStep(ctx, client.ObjectKeyFromObject(ps))
	if err != nil || fresh == nil {
		return false, err
	}
	if fresh.ResourceVersion == ps.ResourceVersion {
		return false, nil
	}
	return !equality.Semantic.DeepEqual(fresh.Status, ps.Status), nil
}
