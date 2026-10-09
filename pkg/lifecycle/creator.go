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
// is already set). The chart's bundle-creator admission policy lets only
// this release's controller ServiceAccount, and the usernames listed in
// admission.controllerUsernames, name a creator other than themselves. A
// controller running as another identity (a second controller instance) is
// refused; the Bundle is then created without the annotation, which is
// always admitted: it has no verified creator, so an approval gate with
// excludeAuthor holds it. who empty creates b as it is.
func CreateBundleAs(ctx context.Context, c client.Client, b *v1alpha1.Bundle, who string) error {
	StampCreatedBy(b, who)
	err := c.Create(ctx, b)
	if err == nil || !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), AnnotationCreatedBy) {
		return err
	}
	zerolog.Ctx(ctx).Warn().Err(err).Str("bundle", b.Name+b.GenerateName).Str("createdBy", who).
		Msg("this controller may not name the Bundle's creator (admission.controllerUsernames); creating it without one")
	delete(b.Annotations, AnnotationCreatedBy)
	b.ResourceVersion = ""
	if err := c.Create(ctx, b); err != nil {
		return fmt.Errorf("create bundle without a creator: %w", err)
	}
	return nil
}
