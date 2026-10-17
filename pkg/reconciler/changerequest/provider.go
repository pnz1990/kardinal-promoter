// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Request is what a provider call needs: the instance's spec, its Secrets
// and the promotion's correlation id.
type Request struct {
	// Spec is the ChangeRequest instance's spec (placeholders rendered).
	Spec *kardinalv1alpha1.ChangeRequestSpec
	// Secret returns the value of a Secret key in the ChangeRequest's
	// namespace (labelled kardinal.io/referenceable=true).
	Secret func(ctx context.Context, ref kardinalv1alpha1.SecretKeyRef) (string, error)
	// CorrelationID is the promotion's id
	// (kardinal:<namespace>/<bundle>/<environment>/<bundle UID>): Create
	// writes it into the ticket and Find searches by it.
	CorrelationID string
}

// Reading is a ticket as the provider reports it.
type Reading struct {
	// Ticket identifies it.
	Ticket kardinalv1alpha1.ChangeRequestTicket
	// State is the normalised state (pending, approved, rejected,
	// cancelled, closed, unknown).
	State string
	// ProviderState is the raw state, for people.
	ProviderState string
	// Approvers are the ticket's approvers, when the provider exposes them.
	Approvers []kardinalv1alpha1.ChangeRequestApprover
	// CorrelationID is the correlation id the ticket carries, "" for none.
	CorrelationID string
	// Fields are the ticket fields link.requireField may name.
	Fields map[string]string
}

// Provider is one ticketing system. Only the ChangeRequest reconciler calls
// it, inside a Limiter slot, with an egress-guarded HTTP client.
//
// Errors end up in status conditions, which anyone who can read the
// ChangeRequest can see: they must never contain a credential, a URL or a
// raw response body. Wrap ErrCredentials, ErrNotFound and *RetryError so
// the reconciler can tell them apart; egress refusals wrap
// egress.ErrBlockedAddress.
type Provider interface {
	// Create opens a ticket that carries req.CorrelationID.
	Create(ctx context.Context, req Request) (Reading, error)
	// Find returns the ticket that carries req.CorrelationID, if any.
	Find(ctx context.Context, req Request) (Reading, bool, error)
	// Get reads the ticket with key (a ServiceNow number or a Jira issue key).
	Get(ctx context.Context, req Request, key string) (Reading, error)
}

// ErrCredentials is a provider's refusal of the credential (HTTP 401 or 403).
var ErrCredentials = errors.New("the provider refused the credential")

// ErrNotFound is a ticket the provider does not have.
var ErrNotFound = errors.New("ticket not found")

// RetryError is a provider error after which the next request waits, at
// least After when set (HTTP 429 or 503 with Retry-After).
type RetryError struct {
	// Status is the HTTP status, 0 for a transport error.
	Status int
	// After is the provider's Retry-After, 0 when it sent none.
	After time.Duration
	// Err is what went wrong, without a URL or a body.
	Err error
}

func (e *RetryError) Error() string {
	if e.After > 0 {
		return fmt.Sprintf("%v (retry after %s)", e.Err, e.After)
	}
	return e.Err.Error()
}

func (e *RetryError) Unwrap() error { return e.Err }

// labelReferenceable must be "true" on every Secret a ChangeRequest names,
// as for MetricChecks, NotificationHooks and Subscriptions: the Secret's
// owner opts in to having it sent to an address whoever can create a
// ChangeRequest chooses.
const labelReferenceable = "kardinal.io/referenceable"

// ReasonSecretNotReferenceable starts the error for a Secret without
// labelReferenceable.
const ReasonSecretNotReferenceable = "SecretNotReferenceable"

// secretReader returns a Request.Secret that reads Secrets in ns with c, by
// name. The error names the Secret and key, never the value.
func secretReader(c client.Reader, ns string) func(context.Context, kardinalv1alpha1.SecretKeyRef) (string, error) {
	return func(ctx context.Context, ref kardinalv1alpha1.SecretKeyRef) (string, error) {
		var s corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("secret %q not found", ref.Name)
			}
			return "", fmt.Errorf("read secret %q: %w", ref.Name, err)
		}
		if s.Labels[labelReferenceable] != "true" {
			return "", fmt.Errorf("%s: secret %q does not have the label %s: \"true\"",
				ReasonSecretNotReferenceable, ref.Name, labelReferenceable)
		}
		v := strings.TrimSpace(string(s.Data[ref.Key]))
		if v == "" {
			return "", fmt.Errorf("secret %q has no key %q", ref.Name, ref.Key)
		}
		return v, nil
	}
}

// DefaultProviders returns the ticketing systems this controller serves,
// by spec.provider. A ChangeRequest of a provider not listed gets condition
// ProviderError.
func DefaultProviders() map[string]Provider {
	return map[string]Provider{}
}
