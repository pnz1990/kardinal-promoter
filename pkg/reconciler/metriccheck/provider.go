// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Value is what a provider's query returned: a number, or for provider web a
// text that may or may not parse as one.
type Value struct {
	// Number is the numeric value; valid when Numeric is true.
	Number float64
	// Numeric reports whether the value is a number.
	Numeric bool
	// Text is the value as text: the number formatted with %g, or the string
	// or boolean JSONPath selected.
	Text string
}

// NumberValue returns a numeric Value.
func NumberValue(v float64) Value {
	return Value{Number: v, Numeric: true, Text: strconv.FormatFloat(v, 'g', -1, 64)}
}

// Query is one evaluation of a MetricCheck.
type Query struct {
	// Spec is the MetricCheck spec.
	Spec *kardinalv1alpha1.MetricCheckSpec
	// Secret returns the value of a Secret key in the MetricCheck namespace.
	Secret func(ctx context.Context, ref kardinalv1alpha1.SecretKeyRef) (string, error)
	// Now is the evaluation time; providers that query a time range end it here.
	Now time.Time
}

// Backend evaluates the query of one provider kind.
//
// Errors end up in status.reason, which anyone who can read the MetricCheck
// can see: they must never contain a credential, a URL (it may carry one) or
// a raw response body.
type Backend interface {
	Evaluate(ctx context.Context, q Query) (Value, error)
}

// DefaultBackends returns the egress-guarded backends for every provider.
// ambientAWS lets cloudwatch MetricChecks without credential Secret refs use
// the controller's own AWS identity.
func DefaultBackends(ambientAWS bool) map[string]Backend {
	return map[string]Backend{
		"prometheus": NewPrometheusProvider(),
		"datadog":    &DatadogProvider{},
		"cloudwatch": &CloudWatchProvider{AmbientCredentials: ambientAWS},
		"newrelic":   &NewRelicProvider{},
		"web":        &WebProvider{},
	}
}

// labelReferenceable must be "true" on every Secret a MetricCheck names
// (program-wide rule, shared with NotificationHook and Subscription): the
// Secret's owner opts in to having it sent to a URL that whoever can create
// a MetricCheck chooses. Without it the controller sends nothing.
const labelReferenceable = "kardinal.io/referenceable"

// ReasonSecretNotReferenceable starts the error for a Secret without
// labelReferenceable.
const ReasonSecretNotReferenceable = "SecretNotReferenceable"

// secretReader returns a Query.Secret that reads Secrets in ns with c.
// The error names the Secret and key, never the value. A Secret without the
// kardinal.io/referenceable: "true" label is refused.
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
		// Surrounding whitespace is dropped: a key made from a file often ends
		// in a newline, which is not valid in a header.
		v := strings.TrimSpace(string(s.Data[ref.Key]))
		if v == "" {
			return "", fmt.Errorf("secret %q has no key %q", ref.Name, ref.Key)
		}
		return v, nil
	}
}

// baseURL parses a user-supplied base URL. The error never repeats it.
func baseURL(raw, what string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("parse %s: must be an http or https URL with a host", what)
	}
	return u, nil
}

// do sends req with hc and returns the response status and up to
// maxResponseBytes of the body. The error never contains the URL.
func do(hc *http.Client, req *http.Request, what string) (int, []byte, error) {
	return doLimit(hc, req, what, maxResponseBytes)
}

// doLimit is do with a body limit of limit bytes. A longer body is an error,
// not a truncated document.
func doLimit(hc *http.Client, req *http.Request, what string, limit int64) (int, []byte, error) {
	if hc == nil {
		hc = defaultHTTPClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			if uerr.Timeout() {
				return 0, nil, fmt.Errorf("%s %s timed out", what, req.Method)
			}
			return 0, nil, fmt.Errorf("%s %s: %w", what, req.Method, uerr.Err)
		}
		return 0, nil, fmt.Errorf("%s %s: %w", what, req.Method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s: read response: %w", what, err)
	}
	if int64(len(body)) > limit {
		return resp.StatusCode, nil, fmt.Errorf("%s: response larger than %d bytes", what, limit)
	}
	return resp.StatusCode, body, nil
}

// jsonNumber converts a decoded JSON value to a float: a number, or a string
// holding one.
func jsonNumber(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// parseWindow parses a window duration, defaulting to 5m.
func parseWindow(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return defaultWindow
}

// defaultWindow is how far back datadog and cloudwatch queries look.
const defaultWindow = 5 * time.Minute
