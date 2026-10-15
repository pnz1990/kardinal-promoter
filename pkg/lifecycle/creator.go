// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// CreateBundleAs creates b with kardinal.io/created-by set to who (unless it
// is already set), and with the actor stamp b.Spec.Actor the caller set. The
// chart's bundle-creator and actor admission policies let only this
// release's controller ServiceAccount, and the usernames listed in
// admission.controllerUsernames, name a creator other than themselves. A
// controller running as another identity (a second controller instance) is
// refused; the Bundle is then created without the annotation and the stamp,
// which is always admitted: it has no verified creator, so an approval gate
// with excludeAuthor holds it. who empty creates b as it is.
func CreateBundleAs(ctx context.Context, c client.Client, b *v1alpha1.Bundle, who string) error {
	StampCreatedBy(b, who)
	err := c.Create(ctx, b)
	if err == nil || !apierrors.IsForbidden(err) || !refusedCreator(err) {
		return err
	}
	zerolog.Ctx(ctx).Warn().Err(err).Str("bundle", b.Name+b.GenerateName).Str("createdBy", who).
		Msg("this controller may not name the Bundle's creator (admission.controllerUsernames); creating it without one")
	delete(b.Annotations, AnnotationCreatedBy)
	b.Spec.Actor = nil
	b.ResourceVersion = ""
	if err := c.Create(ctx, b); err != nil {
		return fmt.Errorf("create bundle without a creator: %w", err)
	}
	return nil
}

// refusedCreator reports whether err is the bundle-creator or actor policy
// refusing the creator this controller named.
func refusedCreator(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, AnnotationCreatedBy) || strings.Contains(msg, "actor must be")
}
