// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IdentityPolicyCheck reports whether the chart's gate-overrides
// ValidatingAdmissionPolicy is in force: its binding exists, binds the policy
// of the same name with the Deny action, and the policy exists. Only then is
// an override's createdBy checked by the API server, so only then does the
// controller record an override as verified (#1503). The answer is read
// straight from the API server and kept for TTL.
type IdentityPolicyCheck struct {
	// Reader reads the binding and the policy (mgr.GetAPIReader(): the
	// controller may only get these two objects by name).
	Reader client.Reader
	// Name is the binding's and the policy's name (--override-identity-policy).
	Name string
	// TTL is how long an answer is kept; zero is 30s.
	TTL time.Duration
	// NowFn is the clock; nil is time.Now (tests inject one).
	NowFn func() time.Time

	mu      sync.Mutex
	checked time.Time
	active  bool
}

// Active reports whether the policy is bound.
func (c *IdentityPolicyCheck) Active(ctx context.Context) bool {
	if c == nil || c.Name == "" || c.Reader == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := time.Now
	if c.NowFn != nil {
		now = c.NowFn
	}
	if !c.checked.IsZero() && now().Sub(c.checked) < ttl {
		return c.active
	}
	c.active = c.read(ctx)
	c.checked = now()
	return c.active
}

func (c *IdentityPolicyCheck) read(ctx context.Context) bool {
	log := zerolog.Ctx(ctx)
	var binding admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if err := c.Reader.Get(ctx, client.ObjectKey{Name: c.Name}, &binding); err != nil {
		log.Warn().Err(err).Str("binding", c.Name).
			Msg("override identity policy binding not readable: new overrides are recorded unverified")
		return false
	}
	if binding.Spec.PolicyName != c.Name ||
		!slices.Contains(binding.Spec.ValidationActions, admissionregistrationv1.Deny) {
		log.Warn().Str("binding", c.Name).Str("policy", binding.Spec.PolicyName).
			Msg("override identity policy binding does not deny with the policy: new overrides are recorded unverified")
		return false
	}
	var policy admissionregistrationv1.ValidatingAdmissionPolicy
	if err := c.Reader.Get(ctx, client.ObjectKey{Name: c.Name}, &policy); err != nil {
		log.Warn().Err(err).Str("policy", c.Name).
			Msg("override identity policy not readable: new overrides are recorded unverified")
		return false
	}
	return true
}

// identityPolicyActive reports whether override createdBy is checked now.
func (r *Reconciler) identityPolicyActive(ctx context.Context) bool {
	return r.IdentityPolicy != nil && r.IdentityPolicy.Active(ctx)
}
