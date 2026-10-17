// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
)

// TestFakeAgreesWithServiceNowProvider runs kardinal's real ServiceNow
// provider against the fake, so the live tests exercise the same wire
// format: a create is found by its correlation key and read by sys_id and
// by number; the control port moves it to approved with approvers; a wrong
// password is refused; an injected 429 carries its Retry-After.
func TestFakeAgreesWithServiceNowProvider(t *testing.T) {
	s := &server{user: "u", password: "p"}
	api := httptest.NewTLSServer(http.HandlerFunc(s.api))
	t.Cleanup(api.Close)
	ctl := httptest.NewServer(http.HandlerFunc(s.control))
	t.Cleanup(ctl.Close)
	sn := &changerequest.ServiceNow{Client: api.Client()}
	secret := "u:p"
	req := changerequest.Request{CorrelationID: "kardinal:ns/app-v1/prod/uid",
		Secret: func(context.Context, kardinalv1alpha1.SecretKeyRef) (string, error) { return secret, nil },
		Spec: &kardinalv1alpha1.ChangeRequestSpec{Provider: "servicenow", ServiceNow: &kardinalv1alpha1.ServiceNowChangeRequest{
			InstanceURL: api.URL, Template: map[string]string{"short_description": "Promote app v1"}}}}
	ctx := context.Background()

	// A change mallory planted with this promotion's correlation key, and
	// approved: never adopted.
	resp, err := http.Post(ctl.URL+"/_control/servicenow", "application/json", bytes.NewBufferString(
		`{"correlation_id":"`+changerequest.CorrelationKey(req.CorrelationID)+`","sys_created_by":"mallory","state":"-2","approval":"approved"}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	_, found, err := sn.Find(ctx, req)
	require.NoError(t, err)
	assert.False(t, found, "the planted change is not adopted")
	created, err := sn.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "CHG0000002", created.Ticket.Key, "a new change, not the planted one")
	assert.Equal(t, "pending", created.State)
	assert.Equal(t, "u", s.tickets[1].CreatedBy)
	assert.Equal(t, "New / Not Yet Requested", created.ProviderState)
	assert.Equal(t, req.CorrelationID, created.CorrelationID)
	assert.WithinDuration(t, s.tickets[1].Created, created.Created, time.Second, "sys_created_on is read")
	assert.Equal(t, "Promote app v1", s.tickets[1].Fields["short_description"])
	assert.Equal(t, "normal", s.tickets[1].Fields["type"])

	again, found, err := sn.Find(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, created.Ticket, again.Ticket)

	put := func(path, body string) {
		r, err := http.NewRequest(http.MethodPut, ctl.URL+path, bytes.NewBufferString(body))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Less(t, resp.StatusCode, 300, path)
	}
	put("/_control/servicenow/CHG0000002", `{"state":"-2","approval":"approved","approvers":[{"name":"Jane Smith","state":"approved"}]}`)
	got, err := sn.Get(ctx, req, created.Ticket)
	require.NoError(t, err)
	assert.Equal(t, "approved", got.State)
	assert.Equal(t, "Scheduled / Approved", got.ProviderState)
	require.Len(t, got.Approvers, 1)
	assert.Equal(t, "Jane Smith", got.Approvers[0].Name)
	byNumber, err := sn.Get(ctx, req, kardinalv1alpha1.ChangeRequestTicket{Key: "CHG0000002"})
	require.NoError(t, err)
	assert.Equal(t, "approved", byNumber.State)
	_, err = sn.Get(ctx, req, kardinalv1alpha1.ChangeRequestTicket{Key: "CHG0000099"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)

	put("/_control/fail", `{"status":429,"retryAfter":"7","count":1}`)
	_, err = sn.Get(ctx, req, created.Ticket)
	var re *changerequest.RetryError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, 429, re.Status)
	assert.Equal(t, "7s", re.After.String())

	secret = "u:wrong"
	_, err = sn.Get(ctx, req, created.Ticket)
	assert.ErrorIs(t, err, changerequest.ErrCredentials)

	resp, err = http.Get(ctl.URL + "/_control/requests")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var recs []record
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&recs))
	require.NotEmpty(t, recs)
	assert.Equal(t, "wrong user or password", recs[len(recs)-1].Auth)
	for _, rec := range recs {
		assert.NotContains(t, rec.Query+rec.Body, "u:p", "no credential is recorded")
	}
}

// TestFakeAgreesWithJiraProvider runs kardinal's real Jira provider against
// the fake, on API versions 3 (basic) and 2 (bearer): a create is found by
// its label and read by id and key; the control port moves it to Done with
// resolution Won't Do (cancelled) and then to Approved (approved).
func TestFakeAgreesWithJiraProvider(t *testing.T) {
	for _, version := range []string{"3", "2"} {
		t.Run("v"+version, func(t *testing.T) {
			s := &server{user: "u", password: "p"}
			api := httptest.NewTLSServer(http.HandlerFunc(s.api))
			t.Cleanup(api.Close)
			ctl := httptest.NewServer(http.HandlerFunc(s.control))
			t.Cleanup(ctl.Close)
			j := &changerequest.Jira{Client: api.Client()}
			scheme, secret := "basic", "u:p"
			if version == "2" {
				scheme, secret = "bearer", "p"
			}
			req := changerequest.Request{CorrelationID: "kardinal:ns/app-v1/prod/uid",
				Secret: func(context.Context, kardinalv1alpha1.SecretKeyRef) (string, error) { return secret, nil },
				Spec: &kardinalv1alpha1.ChangeRequestSpec{Provider: "jira", Jira: &kardinalv1alpha1.JiraChangeRequest{
					BaseURL: api.URL, APIVersion: version, Project: "CHG", ApprovedResolutions: []string{"Done"}, ChangeRequestAuth: kardinalv1alpha1.ChangeRequestAuth{Scheme: scheme},
					Template: map[string]string{"summary": "Promote app v1", "description": "text"}}}}
			ctx := context.Background()
			// Another promotion's issue, with another label.
			resp, err := http.Post(ctl.URL+"/_control/jira", "application/json",
				bytes.NewBufferString(`{"project":"OPS","labels":["kardinal-0123456789abcdef0123456789abcdef"]}`))
			require.NoError(t, err)
			_ = resp.Body.Close()
			// An issue mallory planted with this promotion's label, approved,
			// with kardinal's account as its reporter (Modify Reporter).
			resp, err = http.Post(ctl.URL+"/_control/jira", "application/json", bytes.NewBufferString(
				`{"project":"CHG","creator":"mallory","reporter":"u","status":"Approved","category":"done","labels":["`+
					changerequest.CorrelationKey(req.CorrelationID)+`"]}`))
			require.NoError(t, err)
			_ = resp.Body.Close()
			_, found, err := j.Find(ctx, req)
			require.NoError(t, err)
			assert.False(t, found, "neither another label nor a planted issue is this promotion's")
			created, err := j.Create(ctx, req)
			require.NoError(t, err)
			assert.Equal(t, "CHG-3", created.Ticket.Key)
			again, found, err := j.Find(ctx, req)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, created.Ticket.Key, again.Ticket.Key)
			assert.Equal(t, "pending", again.State)
			assert.Equal(t, req.CorrelationID, again.CorrelationID)
			assert.Equal(t, "Promote app v1", s.issues[2].Fields["summary"])
			assert.False(t, again.Created.IsZero(), "created is read")

			put := func(body string) {
				r, err := http.NewRequest(http.MethodPut, ctl.URL+"/_control/jira/CHG-3", bytes.NewBufferString(body))
				require.NoError(t, err)
				resp, err := http.DefaultClient.Do(r)
				require.NoError(t, err)
				_ = resp.Body.Close()
				require.Less(t, resp.StatusCode, 300)
			}
			put(`{"status":"Done","category":"done","resolution":"Won't Do"}`)
			got, err := j.Get(ctx, req, created.Ticket)
			require.NoError(t, err)
			assert.Equal(t, "cancelled", got.State)
			assert.Equal(t, "Done / Won't Do", got.ProviderState)
			put(`{"status":"Done","category":"done","resolution":"Done"}`)
			got, err = j.Get(ctx, req, kardinalv1alpha1.ChangeRequestTicket{Key: "CHG-3"})
			require.NoError(t, err)
			assert.Equal(t, "approved", got.State)
			_, err = j.Get(ctx, req, kardinalv1alpha1.ChangeRequestTicket{Key: "CHG-9"})
			assert.ErrorIs(t, err, changerequest.ErrNotFound)
			secret = "u:wrong"
			if version == "2" {
				secret = "wrong"
			}
			_, err = j.Get(ctx, req, created.Ticket)
			assert.ErrorIs(t, err, changerequest.ErrCredentials)
		})
	}
}
