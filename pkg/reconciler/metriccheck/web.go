// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/util/jsonpath"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

const (
	// defaultWebTimeout and maxWebTimeout bound a web request.
	defaultWebTimeout = 10 * time.Second
	maxWebTimeout     = 60 * time.Second
	// maxWebResponseBytes bounds the web response that is read and parsed.
	// JSONPath walks the whole document, so its size bounds the CPU a check
	// can take; metric APIs answer far less.
	maxWebResponseBytes = 64 << 10
	// maxWebConcurrent is how many web checks run at once. Web endpoints are
	// arbitrary, so a slow one must not take every MetricCheck worker: a
	// check that finds no slot is retried shortly (ErrBusy).
	maxWebConcurrent = 2
)

// ErrBusy means the provider has no free slot: the check is retried shortly
// without recording a result.
var ErrBusy = errors.New("provider busy")

// webHTTPClient is egress-guarded like defaultHTTPClient. It has no client
// timeout (the request context carries web.timeoutSeconds) and does not
// follow redirects: a 3xx answer fails the check.
var webHTTPClient = &http.Client{
	Transport: egress.NewTransport(http.ProxyFromEnvironment),
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// WebProvider calls any HTTP API that answers JSON (provider web) and reads
// one value from the response with a kubectl-style JSONPath expression. The
// check fails unless the status is 2xx and the expression selects exactly one
// string, number or boolean. No CEL: the value is compared with the
// MetricCheck threshold like every other provider's.
type WebProvider struct {
	// HTTPClient is used for the calls; nil means the egress-guarded default
	// client. Its Timeout is replaced by web.timeoutSeconds.
	HTTPClient *http.Client

	slotsOnce sync.Once
	slots     chan struct{}
}

// Evaluate implements Backend. At most maxWebConcurrent web checks run at
// once; when every slot is taken it returns ErrBusy at once instead of
// holding the reconcile worker.
func (p *WebProvider) Evaluate(ctx context.Context, q Query) (Value, error) {
	p.slotsOnce.Do(func() { p.slots = make(chan struct{}, maxWebConcurrent) })
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return Value{}, ErrBusy
	}
	w := q.Spec.Web
	if w == nil {
		return Value{}, errors.New("web: spec.web is required")
	}
	if _, err := baseURL(w.URL, "web url"); err != nil {
		return Value{}, err
	}
	if !strings.HasPrefix(w.JSONPath, "{") || !strings.HasSuffix(w.JSONPath, "}") {
		return Value{}, errors.New("web jsonPath: must be one {...} expression, for example {.data.value}")
	}
	// Recursive descent visits every node of the document for each step.
	if strings.Contains(w.JSONPath, "..") {
		return Value{}, errors.New("web jsonPath: recursive descent (..) is not supported")
	}
	jp := jsonpath.New("web")
	if err := jp.Parse(w.JSONPath); err != nil {
		return Value{}, errors.New("web jsonPath: not a valid JSONPath expression")
	}
	method := w.Method
	if method == "" {
		method = http.MethodGet
	}
	timeout := defaultWebTimeout
	if w.TimeoutSeconds > 0 {
		timeout = min(time.Duration(w.TimeoutSeconds)*time.Second, maxWebTimeout)
	}
	// A request never outlives the check's interval.
	timeout = min(timeout, parseInterval(q.Spec.Interval)/2)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body *strings.Reader
	if w.Body != "" {
		body = strings.NewReader(w.Body)
	}
	req, err := newRequest(ctx, method, w.URL, body)
	if err != nil {
		return Value{}, errors.New("build web request: invalid URL")
	}
	req.Header.Set("Accept", "application/json")
	if w.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range w.Headers {
		v := ""
		switch {
		case h.ValueFromSecret != nil:
			if v, err = q.Secret(ctx, *h.ValueFromSecret); err != nil {
				return Value{}, fmt.Errorf("web header %s: %w", h.Name, err)
			}
		case h.Value != nil:
			v = *h.Value
		}
		req.Header.Set(h.Name, v)
	}

	hc := p.HTTPClient
	if hc == nil {
		hc = webHTTPClient
	}
	status, raw, err := doLimit(hc, req, "web", maxWebResponseBytes)
	if err != nil {
		return Value{}, err
	}
	if status < 200 || status > 299 {
		return Value{}, fmt.Errorf("web returned HTTP %d", status)
	}
	var doc interface{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return Value{}, errors.New("decode web response: not JSON")
	}
	return jsonPathValue(jp, doc, w.JSONPath)
}

// newRequest builds a request; body may be nil.
func newRequest(ctx context.Context, method, url string, body *strings.Reader) (*http.Request, error) {
	if body == nil {
		return http.NewRequestWithContext(ctx, method, url, nil)
	}
	return http.NewRequestWithContext(ctx, method, url, body)
}

// jsonPathValue runs jp over doc and returns the single value it selects.
func jsonPathValue(jp *jsonpath.JSONPath, doc interface{}, expr string) (Value, error) {
	// The jsonpath error text quotes the document, which can hold what the
	// endpoint returned to the controller: never copy it into status.
	results, err := jp.FindResults(doc)
	if err != nil {
		return Value{}, fmt.Errorf("web jsonPath %s selected nothing", truncate(expr))
	}
	var found []reflect.Value
	for _, r := range results {
		found = append(found, r...)
	}
	switch n := len(found); {
	case n == 0:
		return Value{}, fmt.Errorf("web jsonPath %s selected nothing", truncate(expr))
	case n > 1:
		return Value{}, fmt.Errorf("web jsonPath %s selected %d values, expected 1", truncate(expr), n)
	}
	v := found[0]
	for v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	if !v.IsValid() || (v.Kind() == reflect.Interface && v.IsNil()) {
		return Value{}, fmt.Errorf("web jsonPath %s selected null", truncate(expr))
	}
	switch t := v.Interface().(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return Value{}, fmt.Errorf("web jsonPath %s: number out of range", truncate(expr))
		}
		return Value{Number: f, Numeric: true, Text: t.String()}, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return Value{Number: f, Numeric: err == nil, Text: truncate(t)}, nil
	case bool:
		return Value{Text: strconv.FormatBool(t)}, nil
	default:
		return Value{}, fmt.Errorf("web jsonPath %s selected an object or a list, expected a string, number or boolean",
			truncate(expr))
	}
}
