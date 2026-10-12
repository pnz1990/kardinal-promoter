// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// UnimplementedFields returns one message per reserved or unsupported
// Pipeline field that is set, or nil: the PromotionStep reconciler fails a
// health.resource.kind other than Deployment in that environment, and the API
// server rejects autoRollback (CRD CEL), which is also listed so a file
// checked offline gets the same answer.
//
// A git.secretRef in another namespace is refused on purpose, not
// unimplemented: see ValidateSecretRef.
//
// The Pipeline reconciler (Ready=False, reason NotImplemented) and "kardinal
// validate" both call it, so the Pipeline status and the CLI agree with what
// a Bundle does.
func UnimplementedFields(p *kardinalv1alpha1.Pipeline) []string {
	var msgs []string
	for i := range p.Spec.Environments {
		e := &p.Spec.Environments[i]
		if e.AutoRollback != nil {
			msgs = append(msgs, fmt.Sprintf("environment %q: environments[].autoRollback is not implemented; "+
				"remove it (automatic rollback is configured with onHealthFailure, see docs/rollback.md)", e.Name))
		}
		// As the PromotionStep reconciler checks it: only the resource adapter
		// reads health.resource, and it fails the step before any git change.
		if res := e.Health.Resource; res != nil && res.Kind != "" && res.Kind != "Deployment" &&
			health.EffectiveType(*e) == health.DefaultType {
			msgs = append(msgs, fmt.Sprintf("environment %q: health.resource.kind %q is not supported: "+
				"only Deployment is checked", e.Name, res.Kind))
		}
	}
	return msgs
}
