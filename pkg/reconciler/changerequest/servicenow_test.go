// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
)

// The fixtures in testdata/servicenow are Table API responses read with
// sysparm_display_value=all, in the shape of the ServiceNow REST API
// reference. They are written from the vendor documentation, not recorded
// from an instance; a record in the reference's documented GET shape is read in
// published_fixtures_test.go (spec 23 section 5.4: the recording from a Personal
// Developer Instance is an open contract item). {{CORRELATION}} becomes the
// request's correlation key.

const snSecret = "kardinal:s3cr3t-pass"

// snServer is an httptest TLS server answering with the fixtures, recording
// each request.
type snServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []snRequest
	// answer, when set, answers instead of the fixtures.
	answer func(w http.ResponseWriter, r *http.Request) bool
}

type snRequest struct {
	Method, Path string
	Query        map[string]string
	Auth         string
	Body         map[string]string
}

func fixture(t *testing.T, name, corr string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/servicenow/" + name)
	require.NoError(t, err)
	return []byte(strings.ReplaceAll(string(raw), "{{CORRELATION}}", corr))
}

func newSNServer(t *testing.T, corr string) *snServer {
	t.Helper()
	s := &snServer{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := snRequest{Method: r.Method, Path: r.URL.Path, Query: map[string]string{}, Auth: r.Header.Get("Authorization")}
		for k := range r.URL.Query() {
			rec.Query[k] = r.URL.Query().Get(k)
		}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &rec.Body)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		answer := s.answer
		s.mu.Unlock()
		if answer != nil && answer(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("sysparm_query")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/now/table/change_request":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(fixture(t, "change.json", corr))
		case r.URL.Path == "/api/now/table/change_request/9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3":
			_, _ = w.Write(fixture(t, "change.json", corr))
		case r.URL.Path == "/api/now/table/change_request" && (strings.HasPrefix(q, "correlation_id="+corr+"^") || q == "number=CHG0031337"):
			_, _ = w.Write(fixture(t, "changes.json", corr))
		case r.URL.Path == "/api/now/table/change_request":
			_, _ = w.Write(fixture(t, "empty.json", corr))
		case r.URL.Path == "/api/now/table/sysapproval_approver":
			_, _ = w.Write(fixture(t, "approvers.json", corr))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(fixture(t, "error.json", corr))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *snServer) requests() []snRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]snRequest(nil), s.reqs...)
}

func snRequestFor(url string, mut ...func(*v1alpha1.ChangeRequestSpec)) changerequest.Request {
	spec := &v1alpha1.ChangeRequestSpec{Provider: "servicenow", Mode: "open", ServiceNow: &v1alpha1.ServiceNowChangeRequest{
		InstanceURL: url, Type: "normal",
		ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "snow", Key: "token"}, Scheme: "basic"},
		Template: map[string]string{"short_description": "Promote app v1 to prod", "assignment_group": "payments-cab",
			"correlation_id": "forged-by-template", "type": "emergency"}}}
	for _, m := range mut {
		m(spec)
	}
	return changerequest.Request{Spec: spec, CorrelationID: corrID,
		Secret: func(_ context.Context, ref v1alpha1.SecretKeyRef) (string, error) {
			if ref.Name != "snow" || ref.Key != "token" {
				return "", errors.New("wrong secret")
			}
			return snSecret, nil
		}}
}

// TestServiceNow_CreateFindGet (spec 23 §5.1): a create posts the
// template's fields with the change model and the correlation key (the
// template cannot set either), with basic auth from the Secret; a reading
// has the number, sys_id, link, normalised and raw state, and maps the key
// back to the correlation id; Find searches by the key, oldest first; Get
// reads by sys_id, or by number for a link, and reads the approvers.
//
// Covers CHANGE-REQUEST-06.
func TestServiceNow_CreateFindGet(t *testing.T) {
	key := changerequest.CorrelationKey(corrID)
	srv := newSNServer(t, key)
	sn := &changerequest.ServiceNow{Client: srv.Client()}
	req := snRequestFor(srv.URL)
	ctx := context.Background()

	r, err := sn.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ChangeRequestTicket{Key: "CHG0031337", ID: "9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3",
		URL: srv.URL + "/nav_to.do?uri=change_request.do%3Fsys_id%3D9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3"}, r.Ticket)
	assert.Equal(t, "pending", r.State)
	assert.Equal(t, "New / Not Yet Requested", r.ProviderState)
	assert.Equal(t, corrID, r.CorrelationID, "the key maps back to the promotion's id")
	got := srv.requests()[0]
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/api/now/table/change_request", got.Path)
	assert.Equal(t, "all", got.Query["sysparm_display_value"])
	assert.Equal(t, "sys_id,number,state,approval,correlation_id,sys_created_on,sys_created_by", got.Query["sysparm_fields"])
	assert.Equal(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), r.Created, "sys_created_on is read as UTC")
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(snSecret)), got.Auth)
	assert.Equal(t, map[string]string{"short_description": "Promote app v1 to prod", "assignment_group": "payments-cab",
		"correlation_id": key, "correlation_display": "kardinal", "type": "normal"}, got.Body,
		"the template cannot set the correlation key or the change model")
	assert.LessOrEqual(t, len(key), 100, "fits ServiceNow's correlation_id")

	found, ok, err := sn.Find(ctx, req)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "approved", found.State)
	assert.Equal(t, "Scheduled / Approved", found.ProviderState)
	got = srv.requests()[1]
	assert.Equal(t, "correlation_id="+key+"^sys_created_by=kardinal^ORDERBYsys_created_on", got.Query["sysparm_query"],
		"only a change the integration user created is adopted")
	assert.Equal(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), found.Created, "Find reads the creation time too")
	assert.Equal(t, "1", got.Query["sysparm_limit"])

	other := snRequestFor(srv.URL)
	other.CorrelationID = "kardinal:checkout/app-v1/prod/uid-2"
	_, ok, err = sn.Find(ctx, other)
	require.NoError(t, err)
	assert.False(t, ok, "another promotion's key finds nothing")

	byID, err := sn.Get(ctx, req, r.Ticket)
	require.NoError(t, err)
	assert.Equal(t, "/api/now/table/change_request/9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3", srv.requests()[3].Path)
	assert.Equal(t, "sysapproval=9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3^ORDERBYsys_created_on", srv.requests()[4].Query["sysparm_query"])
	require.Len(t, byID.Approvers, 2)
	assert.Equal(t, "Jane Smith", byID.Approvers[0].Name)
	assert.Equal(t, "approved", byID.Approvers[0].Decision)
	require.NotNil(t, byID.Approvers[0].At)
	assert.Equal(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), byID.Approvers[0].At.UTC(), "the value is UTC")
	assert.Nil(t, byID.Approvers[1].At)

	byNumber, err := sn.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "CHG0031337"})
	require.NoError(t, err)
	assert.Equal(t, "approved", byNumber.State)
	assert.Equal(t, "number=CHG0031337", srv.requests()[5].Query["sysparm_query"])

	_, err = sn.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "CHG0000404"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	n := len(srv.requests())
	_, err = sn.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "CHG1^ORnumber!=x"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	assert.Len(t, srv.requests(), n, "a key that is not a change number is never put into a query")
}

// TestServiceNow_RequireField: link.requireField's field is read, and a
// reference field matches by its display value too.
func TestServiceNow_RequireField(t *testing.T) {
	srv := newSNServer(t, "kardinal-other")
	sn := &changerequest.ServiceNow{Client: srv.Client()}
	for value, want := range map[string]string{"checkout": "checkout", "b0c4030ac0a800090152e7a4564ca36c": "b0c4030ac0a800090152e7a4564ca36c",
		"payments": "b0c4030ac0a800090152e7a4564ca36c"} {
		req := snRequestFor(srv.URL, func(s *v1alpha1.ChangeRequestSpec) {
			s.Mode, s.Link = "link", &v1alpha1.ChangeRequestLink{From: "approvalField", Name: "ticketID",
				RequireField: &v1alpha1.ChangeRequestFieldMatch{Name: "cmdb_ci", Value: value}}
		})
		r, err := sn.Get(context.Background(), req, v1alpha1.ChangeRequestTicket{Key: "CHG0031337"})
		require.NoError(t, err)
		assert.Equal(t, want, r.Fields["cmdb_ci"], value)
		assert.Equal(t, "kardinal-other", r.CorrelationID, "another promotion's key is not mapped to this id")
		assert.Contains(t, srv.requests()[len(srv.requests())-2].Query["sysparm_fields"], ",cmdb_ci")
	}
}

func TestServiceNowState(t *testing.T) {
	custom := map[string]string{"-3": "approved", "Implement": "closed", "9": "bogus"}
	for _, tc := range []struct {
		state, name, approval string
		custom                map[string]string
		want                  string
	}{
		{"-2", "Scheduled", "approved", nil, "approved"},
		{"-1", "Implement", "approved", nil, "approved"},
		{"-2", "Scheduled", "requested", nil, "pending"},     // scheduled, but nobody approved it
		{"-1", "Implement", "not requested", nil, "pending"}, // implementing, never approved
		{"-3", "Authorize", "approved", nil, "pending"},      // approved but not yet scheduled
		{"-3", "Authorize", "requested", nil, "pending"},
		{"-5", "New", "not requested", nil, "pending"},
		{"-4", "Assess", "rejected", nil, "rejected"},
		{"4", "Canceled", "rejected", nil, "rejected"},
		{"4", "Canceled", "approved", nil, "cancelled"},
		{"3", "Closed", "approved", nil, "closed"},
		{"0", "Review", "approved", nil, "closed"},
		{"-3", "Authorize", "approved", custom, "approved"}, // by raw value, approval approved
		{"-3", "Authorize", "requested", custom, "pending"}, // a map cannot approve what the approval did not
		{"-3", "Authorize", "not requested", custom, "pending"},
		{"-3", "Authorize", "rejected", custom, "rejected"}, // rejection always wins over the map
		{"-1", "Implement", "approved", custom, "closed"},   // by display name
		{"-1", "Implement", "rejected", custom, "rejected"},
		{"9", "Custom", "approved", custom, "unknown"},      // a mapping to no state
		{"-2", "Scheduled", "approved", custom, "approved"}, // not in the map: the default
		{"-2", "Scheduled", "rejected", map[string]string{"-2": "approved", "Scheduled": "approved"}, "rejected"},
	} {
		assert.Equal(t, tc.want, changerequest.ServiceNowState(tc.state, tc.name, tc.approval, tc.custom),
			"%s/%s/%s %v", tc.state, tc.name, tc.approval, tc.custom)
	}
}

// TestServiceNow_Errors: 401 and 403 are a refused credential, 404 not
// found, 429 and 5xx a retry with the server's Retry-After; other answers
// are errors. No error carries the URL, the credential or the response
// body; no redirect is followed.
//
// Covers CHANGE-REQUEST-06.
func TestServiceNow_Errors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		check  func(t *testing.T, err error)
	}{
		{"401", 401, nil, `{"error":{"detail":"secret-in-body"}}`, func(t *testing.T, err error) { assert.ErrorIs(t, err, changerequest.ErrCredentials) }},
		{"403", 403, nil, "", func(t *testing.T, err error) { assert.ErrorIs(t, err, changerequest.ErrCredentials) }},
		{"404", 404, nil, "", func(t *testing.T, err error) { assert.ErrorIs(t, err, changerequest.ErrNotFound) }},
		{"429 seconds", 429, map[string]string{"Retry-After": "30"}, "", func(t *testing.T, err error) {
			var re *changerequest.RetryError
			require.ErrorAs(t, err, &re)
			assert.Equal(t, 429, re.Status)
			assert.Equal(t, 30*time.Second, re.After)
		}},
		{"429 capped", 429, map[string]string{"Retry-After": "86400"}, "", func(t *testing.T, err error) {
			var re *changerequest.RetryError
			require.ErrorAs(t, err, &re)
			assert.Equal(t, time.Hour, re.After)
		}},
		{"503 date", 503, map[string]string{"Retry-After": time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)}, "", func(t *testing.T, err error) {
			var re *changerequest.RetryError
			require.ErrorAs(t, err, &re)
			assert.InDelta(t, 2*time.Minute, re.After, float64(5*time.Second))
		}},
		{"500", 500, nil, "secret-in-body", func(t *testing.T, err error) {
			var re *changerequest.RetryError
			require.ErrorAs(t, err, &re)
			assert.Zero(t, re.After)
		}},
		{"400", 400, nil, "secret-in-body", func(t *testing.T, err error) {
			var re *changerequest.RetryError
			assert.False(t, errors.As(err, &re))
			assert.Contains(t, err.Error(), "HTTP 400")
		}},
		{"redirect", 302, map[string]string{"Location": "https://evil.example/steal"}, "", func(t *testing.T, err error) {
			assert.Contains(t, err.Error(), "HTTP 302")
		}},
		{"not JSON", 201, nil, "<html>secret-in-body</html>", func(t *testing.T, err error) {
			assert.Contains(t, err.Error(), "not the JSON expected")
		}},
		{"too large", 201, nil, `{"result":"` + strings.Repeat("x", 1<<20) + `"}`, func(t *testing.T, err error) {
			assert.Contains(t, err.Error(), "larger than")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSNServer(t, "k")
			srv.answer = func(w http.ResponseWriter, _ *http.Request) bool {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
				return true
			}
			_, err := (&changerequest.ServiceNow{Client: srv.Client()}).Create(context.Background(), snRequestFor(srv.URL))
			require.Error(t, err)
			tc.check(t, err)
			for _, leak := range []string{srv.URL, "127.0.0.1", "s3cr3t", "secret-in-body", "evil.example"} {
				assert.NotContains(t, err.Error(), leak)
			}
			assert.Len(t, srv.requests(), 1, "one request, no redirect followed")
		})
	}
}

// TestServiceNow_AuthAndApprovers: bearer sends the token; a basic
// credential without a colon is refused before any request; a refused
// sysapproval_approver read leaves the reading without approvers.
func TestServiceNow_AuthAndApprovers(t *testing.T) {
	srv := newSNServer(t, "k")
	sn := &changerequest.ServiceNow{Client: srv.Client()}
	bearer := snRequestFor(srv.URL, func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.Scheme = "bearer" })
	_, err := sn.Create(context.Background(), bearer)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+snSecret, srv.requests()[0].Auth)

	noColon := snRequestFor(srv.URL)
	noColon.Secret = func(context.Context, v1alpha1.SecretKeyRef) (string, error) { return "just-a-token", nil }
	_, err = sn.Create(context.Background(), noColon)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold user:password")
	assert.NotContains(t, err.Error(), "just-a-token")
	assert.Len(t, srv.requests(), 1)

	srv.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/now/table/sysapproval_approver" {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	r, err := sn.Get(context.Background(), snRequestFor(srv.URL), v1alpha1.ChangeRequestTicket{Key: "CHG0031337",
		ID: "9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3"})
	require.NoError(t, err)
	assert.Nil(t, r.Approvers)
	assert.Equal(t, "pending", r.State)
}

// TestProviders_EgressGuard: the controller serves servicenow and jira, and
// their client refuses a loopback instance (the egress guard), which the
// reconciler reports as Refused.
func TestProviders_EgressGuard(t *testing.T) {
	providers := changerequest.DefaultProviders(changerequest.ProviderOptions{})
	assert.Len(t, providers, 2)
	sn := newSNServer(t, "k")
	_, err := providers["servicenow"].Create(context.Background(), snRequestFor(sn.URL))
	require.Error(t, err)
	assert.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Empty(t, sn.requests())
	jira := newJiraServer(t, "k")
	require.Contains(t, providers, "jira")
	_, err = providers["jira"].Create(context.Background(), jiraRequestFor(jira.URL))
	require.Error(t, err)
	assert.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Empty(t, jira.requests())
}

// TestLoadRootCAs: the CA file adds to the system roots; a file without a
// certificate is an error.
func TestLoadRootCAs(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	pemCert := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(srv.Certificate().Raw) + "\n-----END CERTIFICATE-----\n"
	pool, err := changerequest.LoadRootCAs([]byte(pemCert))
	require.NoError(t, err)
	_, err = srv.Certificate().Verify(x509VerifyOptions(pool))
	require.NoError(t, err)
	_, err = changerequest.LoadRootCAs([]byte("not a pem"))
	assert.Error(t, err)
}

func x509VerifyOptions(p *x509.CertPool) x509.VerifyOptions { return x509.VerifyOptions{Roots: p} }

// TestServiceNow_InstanceURL: an instance that is not https://<host> (a path,
// query or fragment would reach every ticket URL), or
// carries userinfo, is refused before any request (the CRD refuses them
// too; the provider does not rely on it).
func TestServiceNow_InstanceURL(t *testing.T) {
	srv := newSNServer(t, "k")
	sn := &changerequest.ServiceNow{Client: srv.Client()}
	for _, u := range []string{strings.Replace(srv.URL, "https://", "http://", 1), strings.Replace(srv.URL, "https://", "https://user:pw@", 1), "https://",
		srv.URL + "?sig=pw", srv.URL + "?", srv.URL + "/#pw", srv.URL + "/x"} {
		_, err := sn.Create(context.Background(), snRequestFor(u))
		require.Error(t, err, u)
		assert.Contains(t, err.Error(), "instanceURL must be https://<host>")
		assert.NotContains(t, err.Error(), "pw")
	}
	assert.Empty(t, srv.requests())
}

// TestServiceNow_PrePlanting (coordinator decision on #2129): Find adopts a
// change only when the integration user created it: the query filters on
// sys_created_by, and a change another user created with this promotion's
// correlation key (planted, then approved by its planter) is not adopted
// even if the instance ignores the filter. The user is the basic
// credential's, or spec.servicenow.integrationUser (needed for bearer).
func TestServiceNow_PrePlanting(t *testing.T) {
	key := changerequest.CorrelationKey(corrID)
	srv := newSNServer(t, key)
	srv.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/now/table/change_request" {
			// An instance that ignores the filter, answering a planted change.
			_, _ = w.Write([]byte(strings.Replace(string(fixture(t, "changes.json", key)),
				`"sys_created_by": {"display_value": "kardinal", "value": "kardinal"}`,
				`"sys_created_by": {"display_value": "mallory", "value": "mallory"}`, 1)))
			return true
		}
		return false
	}
	sn := &changerequest.ServiceNow{Client: srv.Client()}
	_, found, err := sn.Find(context.Background(), snRequestFor(srv.URL))
	require.NoError(t, err)
	assert.False(t, found, "a change mallory created is never adopted")

	srv.answer = nil
	bearer := snRequestFor(srv.URL, func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.Scheme = "bearer" })
	n := len(srv.requests())
	_, _, err = sn.Find(context.Background(), bearer)
	assert.ErrorContains(t, err, "set spec.servicenow.integrationUser")
	assert.Len(t, srv.requests(), n, "no request without a known integration user")
	bearer.Spec.ServiceNow.IntegrationUser = "kardinal"
	_, found, err = sn.Find(context.Background(), bearer)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Contains(t, srv.requests()[len(srv.requests())-1].Query["sysparm_query"], "^sys_created_by=kardinal^")

	// user_name compares case-insensitively in ServiceNow: Kardinal is kardinal.
	srv.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/now/table/change_request" {
			_, _ = w.Write([]byte(strings.Replace(string(fixture(t, "changes.json", key)),
				`"sys_created_by": {"display_value": "kardinal", "value": "kardinal"}`,
				`"sys_created_by": {"display_value": "Kardinal", "value": "Kardinal"}`, 1)))
			return true
		}
		return false
	}
	_, found, err = sn.Find(context.Background(), snRequestFor(srv.URL))
	require.NoError(t, err)
	assert.True(t, found, "the same user in another case")
	srv.answer = nil

	injected := snRequestFor(srv.URL, func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.IntegrationUser = "x^ORsys_created_byISNOTEMPTY" })
	_, _, err = sn.Find(context.Background(), injected)
	assert.ErrorContains(t, err, "not a user_name", "a user that would rewrite the query is refused")
}

// TestServiceNow_NumberChecked (#2170 QA): the change number a reading
// records reaches the PR body and the receipt, so a number that is not
// capitals then digits (markup, a mention, a link) fails the reading closed,
// on create, search and read alike.
func TestServiceNow_NumberChecked(t *testing.T) {
	for _, bad := range []string{`<img src=x>`, `@org/team`, `CHG1](https://evil)`, `chg0031337`, `XQZ`, `CHG 1`} {
		t.Run(bad, func(t *testing.T) {
			srv := newSNServer(t, "k")
			srv.answer = func(w http.ResponseWriter, r *http.Request) bool {
				body := strings.ReplaceAll(string(fixture(t, "change.json", "k")), `"CHG0031337"`, strconv.Quote(bad))
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
				}
				_, _ = w.Write([]byte(body))
				return true
			}
			sn := &changerequest.ServiceNow{Client: srv.Client()}
			_, err := sn.Create(context.Background(), snRequestFor(srv.URL))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "the change number is not capitals then digits")
			assert.NotContains(t, err.Error(), bad, "the number is not echoed")
			_, err = sn.Get(context.Background(), snRequestFor(srv.URL), v1alpha1.ChangeRequestTicket{Key: "CHG0031337", ID: "9f1c4b1e2f3a40d5a6b7c8d9e0f1a2b3"})
			require.Error(t, err)
		})
	}
}
