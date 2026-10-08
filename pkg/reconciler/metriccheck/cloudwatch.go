// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// defaultCloudWatchPeriod is the point granularity, in seconds, when
// spec.cloudWatch.period is unset.
const defaultCloudWatchPeriod = 60

// CloudWatchProvider runs a GetMetricData expression (metric math, SEARCH or
// a Metrics Insights SELECT) through the CloudWatch Query API (POST
// Action=GetMetricData, signed with SigV4). The expression must return one
// result; the value is its latest point.
//
// Credentials come from spec.cloudWatch's Secret refs. Without them the
// controller's own AWS identity (the SDK default chain: environment, IRSA web
// identity, EKS Pod Identity, instance profile) is used only when
// AmbientCredentials is true; otherwise the check fails. Any user who can
// create a MetricCheck could otherwise read CloudWatch with the controller's
// identity.
//
// The CloudWatch request goes through the egress guard like every other
// provider; the SDK's credential providers do not (they call fixed AWS and
// link-local credential endpoints, never a user-supplied URL).
type CloudWatchProvider struct {
	// HTTPClient is used for the CloudWatch calls; nil means the
	// egress-guarded default client.
	HTTPClient *http.Client
	// AmbientCredentials allows the controller's own AWS identity for
	// MetricChecks that name no credential Secret.
	AmbientCredentials bool

	ambientOnce sync.Once
	ambient     aws.CredentialsProvider
	ambientErr  error
}

// cloudWatchResponse is the part of the GetMetricData response that is read.
type cloudWatchResponse struct {
	Results []struct {
		ID         string   `xml:"Id"`
		StatusCode string   `xml:"StatusCode"`
		Timestamps []string `xml:"Timestamps>member"`
		Values     []string `xml:"Values>member"`
	} `xml:"GetMetricDataResult>MetricDataResults>member"`
	Messages []struct {
		Code  string `xml:"Code"`
		Value string `xml:"Value"`
	} `xml:"GetMetricDataResult>Messages>member"`
}

// cloudWatchError is the Query API error envelope.
type cloudWatchError struct {
	Error struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// Evaluate implements Backend.
func (p *CloudWatchProvider) Evaluate(ctx context.Context, q Query) (Value, error) {
	cw := q.Spec.CloudWatch
	if cw == nil {
		return Value{}, errors.New("cloudwatch: spec.cloudWatch is required")
	}
	address := cw.Endpoint
	if address == "" {
		address = "https://monitoring." + cw.Region + ".amazonaws.com"
	}
	endpoint, err := baseURL(address, "cloudwatch endpoint")
	if err != nil {
		return Value{}, err
	}
	creds, err := p.credentials(ctx, q)
	if err != nil {
		return Value{}, err
	}

	period := int32(defaultCloudWatchPeriod)
	if cw.Period > 0 {
		period = cw.Period
	}
	form := url.Values{}
	form.Set("Action", "GetMetricData")
	form.Set("Version", "2010-08-01")
	form.Set("StartTime", q.Now.Add(-parseWindow(cw.Window)).UTC().Format(time.RFC3339))
	form.Set("EndTime", q.Now.UTC().Format(time.RFC3339))
	form.Set("ScanBy", "TimestampDescending")
	form.Set("MaxDatapoints", "1000")
	form.Set("MetricDataQueries.member.1.Id", "kardinal")
	form.Set("MetricDataQueries.member.1.Expression", q.Spec.Query)
	form.Set("MetricDataQueries.member.1.Period", strconv.Itoa(int(period)))
	form.Set("MetricDataQueries.member.1.ReturnData", "true")
	payload := form.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(payload))
	if err != nil {
		return Value{}, errors.New("build cloudwatch request: invalid URL")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sum := sha256.Sum256([]byte(payload))
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "monitoring", cw.Region, q.Now); err != nil {
		return Value{}, fmt.Errorf("sign cloudwatch request: %w", err)
	}

	status, body, err := do(p.HTTPClient, req, "cloudwatch")
	if err != nil {
		return Value{}, err
	}
	if status != http.StatusOK {
		var e cloudWatchError
		if xml.Unmarshal(body, &e) == nil && e.Error.Code != "" {
			return Value{}, fmt.Errorf("cloudwatch returned HTTP %d: %s: %s", status, e.Error.Code, truncate(e.Error.Message))
		}
		return Value{}, fmt.Errorf("cloudwatch returned HTTP %d", status)
	}
	var resp cloudWatchResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return Value{}, errors.New("decode cloudwatch response: not a GetMetricData response")
	}
	return cloudWatchValue(resp)
}

// credentials returns static credentials from the Secret refs, or the
// controller's own when allowed.
func (p *CloudWatchProvider) credentials(ctx context.Context, q Query) (aws.Credentials, error) {
	cw := q.Spec.CloudWatch
	if cw.AccessKeyIDSecretRef != nil && cw.SecretAccessKeySecretRef != nil {
		id, err := q.Secret(ctx, *cw.AccessKeyIDSecretRef)
		if err != nil {
			return aws.Credentials{}, fmt.Errorf("cloudwatch access key ID: %w", err)
		}
		secret, err := q.Secret(ctx, *cw.SecretAccessKeySecretRef)
		if err != nil {
			return aws.Credentials{}, fmt.Errorf("cloudwatch secret access key: %w", err)
		}
		var token string
		if cw.SessionTokenSecretRef != nil {
			if token, err = q.Secret(ctx, *cw.SessionTokenSecretRef); err != nil {
				return aws.Credentials{}, fmt.Errorf("cloudwatch session token: %w", err)
			}
		}
		return credentials.NewStaticCredentialsProvider(id, secret, token).Retrieve(ctx)
	}
	if !p.AmbientCredentials {
		return aws.Credentials{}, errors.New("cloudwatch credentials: set cloudWatch.accessKeyIDSecretRef and " +
			"secretAccessKeySecretRef (the controller's own AWS identity is not enabled: " +
			"--metriccheck-cloudwatch-ambient-credentials)")
	}
	p.ambientOnce.Do(func() {
		cfg, err := config.LoadDefaultConfig(context.Background())
		if err != nil {
			p.ambientErr = err
			return
		}
		p.ambient = aws.NewCredentialsCache(cfg.Credentials)
	})
	if p.ambientErr != nil {
		return aws.Credentials{}, fmt.Errorf("cloudwatch controller credentials: %w", p.ambientErr)
	}
	if p.ambient == nil {
		return aws.Credentials{}, errors.New("cloudwatch controller credentials: none found")
	}
	c, err := p.ambient.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, errors.New("cloudwatch controller credentials: none could be retrieved")
	}
	return c, nil
}

// cloudWatchValue returns the latest point of the single result.
func cloudWatchValue(resp cloudWatchResponse) (Value, error) {
	for _, m := range resp.Messages {
		if m.Code != "" {
			return Value{}, fmt.Errorf("cloudwatch query error: %s: %s", m.Code, truncate(m.Value))
		}
	}
	switch n := len(resp.Results); {
	case n == 0:
		return Value{}, errors.New("cloudwatch query returned no results")
	case n > 1:
		return Value{}, fmt.Errorf("cloudwatch query returned %d results, expected 1", n)
	}
	r := resp.Results[0]
	if len(r.Values) == 0 {
		return Value{}, errors.New("cloudwatch query returned no points")
	}
	if len(r.Timestamps) != len(r.Values) {
		return Value{}, errors.New("decode cloudwatch response: timestamps and values differ in length")
	}
	// ScanBy=TimestampDescending puts the latest point first; pick the latest
	// timestamp anyway, in case an endpoint ignores ScanBy.
	latest, at := 0, time.Time{}
	for i, ts := range r.Timestamps {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
		if err != nil {
			return Value{}, errors.New("decode cloudwatch response: bad timestamp")
		}
		if i == 0 || t.After(at) {
			latest, at = i, t
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(r.Values[latest]), 64)
	if err != nil {
		return Value{}, errors.New("decode cloudwatch response: bad value")
	}
	return NumberValue(v), nil
}
