// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
)

// The published fixtures (#2140) are not recordings of a real instance, and
// hold no credential:
//
//   - testdata/jira/published: the example responses and schema property
//     names of the Jira Cloud platform REST API OpenAPI document
//     (https://developer.atlassian.com/cloud/jira/platform/swagger-v3.v3.json,
//     version 1001.0.0-SNAPSHOT-3a6fd1304745f31b038791cc665cc6593c57d4d9,
//     sha256 7034cb64290ff38e158291d81789342f7fd09469d088b9c62bc35d07d3d7feed,
//     read 2026-10-17), extracted unchanged. The document states its licence
//     in its own info.license field: {"name": "Apache 2.0", "url":
//     "http://www.apache.org/licenses/LICENSE-2.0.html"}.
//   - testdata/servicenow/published/table-get-shape-200.json: hand-written
//     by kardinal, with its own values, in the shape the Table API reference
//     documents for GET /now/table/{tableName} without display values
//     (https://www.servicenow.com/docs/bundle/yokohama-api-reference/page/integrate/inbound-rest/concept/c_TableAPI.html,
//     read 2026-10-17): a "result" list of records whose fields are plain
//     strings and whose reference fields are {"link", "value"} objects.
//     ServiceNow's own example is not copied: its documentation is not
//     openly licensed.
//
// The other hand-written fixtures (testdata/jira, testdata/servicenow)
// carry what these do not (a status, labels, a creator, display values);
// the tests below check that they use the vendor's names, and that the
// providers read the vendor's own examples and documented shapes, extra
// fields and all, failing closed where one lacks what kardinal needs.

func vendorFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return raw
}

// TestJira_VendorExamples: the create example (201) gives the key, id and
// browse URL; the create error example (400) fails without echoing its
// body; the get-issue example, verbose and without a status, reads as
// pending (never approved) in the issue's own project; the search example,
// whose issue names no creator, is not adopted although /myself answers.
func TestJira_VendorExamples(t *testing.T) {
	var srv *httptest.Server
	routes := map[string]struct {
		code int
		file string
	}{}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rt.code)
		_, _ = w.Write(vendorFixture(t, "jira/published/"+rt.file))
	}))
	t.Cleanup(srv.Close)
	j := &changerequest.Jira{Client: srv.Client()}
	ctx := context.Background()
	req := jiraRequestFor(srv.URL, func(jc *v1alpha1.JiraChangeRequest) { jc.Project = "ED"; jc.LinkProjects = []string{"EX"} })

	routes["POST /rest/api/3/issue"] = struct {
		code int
		file string
	}{201, "v3-create-issue-201.json"}
	r, err := j.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ChangeRequestTicket{Key: "ED-24", ID: "10000", URL: srv.URL + "/browse/ED-24"}, r.Ticket)
	assert.Equal(t, v1alpha1.ChangeRequestPending, r.State)

	routes["POST /rest/api/3/issue"] = struct {
		code int
		file string
	}{400, "v3-create-issue-400.json"}
	_, err = j.Create(ctx, req)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "priority", "the error body is not echoed")

	routes["GET /rest/api/3/issue/10002"] = struct {
		code int
		file string
	}{200, "v3-get-issue-200.json"}
	r, err = j.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "ED-1", ID: "10002"})
	require.NoError(t, err)
	assert.Equal(t, "ED-1", r.Ticket.Key)
	assert.Equal(t, v1alpha1.ChangeRequestPending, r.State, "no status: pending, not approved")
	assert.Empty(t, r.CorrelationID, "no correlation label")
	r, err = j.Get(ctx, jiraRequestFor(srv.URL, func(jc *v1alpha1.JiraChangeRequest) { jc.Project = "ED" }),
		v1alpha1.ChangeRequestTicket{Key: "ED-1", ID: "10002"})
	require.Error(t, err, "the example issue is in project EX: not ED's")
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	_ = r

	routes["POST /rest/api/3/search/jql"] = struct {
		code int
		file string
	}{200, "v3-search-jql-200.json"}
	routes["GET /rest/api/3/myself"] = struct {
		code int
		file string
	}{200, "v3-myself-200.json"}
	_, found, err := j.Find(ctx, req)
	require.NoError(t, err)
	assert.False(t, found, "an issue without a creator is never adopted")
}

// TestJira_FixturesUseVendorNames: the hand-written Jira fixtures and the
// search kardinal sends use only property names of the vendor's schemas
// (CreatedIssue, SearchAndReconcileResults and its IssueBeans, StatusDetails,
// StatusCategory, Resolution, User, SearchAndReconcileRequestBean), so a
// fixture cannot pass by inventing a field Jira does not return.
func TestJira_FixturesUseVendorNames(t *testing.T) {
	var schemas map[string][]string
	require.NoError(t, json.Unmarshal(vendorFixture(t, "jira/published/v3-schema-properties.json"), &schemas))
	keys := func(v interface{}) []string {
		m, ok := v.(map[string]interface{})
		require.True(t, ok, "%v is an object", v)
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	within := func(what string, v interface{}, schema string) {
		t.Helper()
		allowed, ok := schemas[schema]
		require.True(t, ok, schema)
		for _, k := range keys(v) {
			assert.Contains(t, allowed, k, "%s: %q is not a %s property", what, k, schema)
		}
	}
	load := func(name string) map[string]interface{} {
		var v map[string]interface{}
		require.NoError(t, json.Unmarshal(vendorFixture(t, "jira/"+name), &v), name)
		return v
	}
	within("created.json", load("created.json"), "CreatedIssue")
	within("myself.json", load("myself.json"), "User")
	issue := func(what string, v interface{}) {
		t.Helper()
		within(what, v, "IssueBean")
		fields := v.(map[string]interface{})["fields"].(map[string]interface{})
		if st, ok := fields["status"]; ok {
			within(what+" status", st, "StatusDetails")
			if c, ok := st.(map[string]interface{})["statusCategory"]; ok {
				within(what+" statusCategory", c, "StatusCategory")
			}
		}
		if res, ok := fields["resolution"].(map[string]interface{}); ok {
			within(what+" resolution", res, "Resolution")
		}
		for _, u := range []string{"creator", "reporter"} {
			if v, ok := fields[u]; ok {
				within(what+" "+u, v, "User")
			}
		}
	}
	issue("issue.json", load("issue.json"))
	for _, name := range []string{"search.json", "empty.json"} {
		s := load(name)
		within(name, s, "SearchAndReconcileResults")
		for i, is := range s["issues"].([]interface{}) {
			issue(name+" issue "+string(rune('0'+i)), is)
		}
	}

	// The search kardinal sends.
	srv := newJiraServer(t, changerequest.CorrelationKey(corrID))
	_, _, err := (&changerequest.Jira{Client: srv.Client()}).Find(context.Background(), jiraRequestFor(srv.URL))
	require.NoError(t, err)
	var sent bool
	for _, r := range srv.requests() {
		if strings.HasSuffix(r.Path, "/search/jql") {
			within("the search request", r.Body, "SearchAndReconcileRequestBean")
			sent = true
		}
	}
	assert.True(t, sent, "Find searched")
}

// TestServiceNow_VendorExample: a record in the Table API reference's GET
// shape (plain strings, references as {"link", "value"} without display
// values)
// reads: its number, sys_id, creation time, its creator as the integration
// user, a reference field by its value (link.requireField on cmdb_ci), and
// the raw state as the provider state. Without the correlation id, a
// search answering with it is an error (ErrIgnoredFilter): the instance
// ignored the filter, and the change is not this promotion's.
func TestServiceNow_VendorExample(t *testing.T) {
	corr := ""
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/now/table/sysapproval_approver":
			// An approval record in the documented reference shape (no
			// display value), written for this test.
			_, _ = w.Write([]byte(`{"result":[{"approver":{"link":"https://example.service-now.com/api/now/table/sys_user/svc.cab",` +
				`"value":"svc.cab"},"state":"approved","sys_updated_on":"2026-09-30 15:00:00"}]}`))
		case "/api/now/table/change_request/5d1f0c3a9b8e4f7a8c2d6e1b0a9f8e7d":
			var list struct {
				Result []json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(vendorFixture(t, "servicenow/published/table-get-shape-200.json"), &list))
			_, _ = w.Write([]byte(`{"result":` + string(list.Result[0]) + `}`))
		case "/api/now/table/change_request":
			_, _ = w.Write([]byte(strings.Replace(string(vendorFixture(t, "servicenow/published/table-get-shape-200.json")),
				`"correlation_id": ""`, `"correlation_id": "`+corr+`"`, 1)))
		default:
			_, _ = w.Write([]byte(`{"result":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	sn := &changerequest.ServiceNow{Client: srv.Client()}
	req := snRequestFor(srv.URL, func(s *v1alpha1.ChangeRequestSpec) {
		s.ServiceNow.IntegrationUser = "svc.kardinal"
		s.Link = &v1alpha1.ChangeRequestLink{From: "approvalField", Name: "ticketID",
			RequireField: &v1alpha1.ChangeRequestFieldMatch{Name: "cmdb_ci", Value: "8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d"}}
	})
	_, found, err := sn.Find(context.Background(), req)
	require.ErrorIs(t, err, changerequest.ErrIgnoredFilter, "the record has no correlation id: the instance ignored the filter")
	require.False(t, found)

	corr = changerequest.CorrelationKey(corrID)
	r, found, err := sn.Find(context.Background(), req)
	require.NoError(t, err)
	require.True(t, found, "created by the integration user, with the correlation id")
	assert.Equal(t, "CHG0042001", r.Ticket.Key)
	assert.Equal(t, "5d1f0c3a9b8e4f7a8c2d6e1b0a9f8e7d", r.Ticket.ID)
	assert.Equal(t, time.Date(2026, 9, 30, 14, 5, 11, 0, time.UTC), r.Created)
	assert.Equal(t, map[string]string{"cmdb_ci": "8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d"}, r.Fields, "a reference without a display value reads as its value")
	assert.Equal(t, "-4 / requested", r.ProviderState)
	assert.Equal(t, v1alpha1.ChangeRequestPending, r.State)
	assert.Equal(t, corrID, r.CorrelationID)

	got, err := sn.Get(context.Background(), req, r.Ticket)
	require.NoError(t, err)
	require.Len(t, got.Approvers, 1, "an approver reference without a display value")
	assert.Equal(t, "svc.cab", got.Approvers[0].Name)

	other := snRequestFor(srv.URL, func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.IntegrationUser = "someone.else" })
	_, found, err = sn.Find(context.Background(), other)
	require.NoError(t, err)
	assert.False(t, found, "created by another user: not adopted")
}

// TestFind_AnswerMustCarryTheCorrelation: a search answer is adopted only
// when it carries this promotion's correlation (the Jira label, the
// ServiceNow correlation_id): a site that ignored that part of the query
// answers with another of kardinal's own tickets, which is another
// promotion's.
func TestFind_AnswerMustCarryTheCorrelation(t *testing.T) {
	// The site answers with another promotion's issue whatever the JQL says.
	other := changerequest.CorrelationKey("kardinal:checkout/app-v0/prod/uid-0")
	js := newJiraServer(t, other)
	search, err := os.ReadFile("testdata/jira/search.json")
	require.NoError(t, err)
	js.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/search/jql") {
			return false
		}
		_, _ = w.Write([]byte(strings.NewReplacer("{{CORRELATION}}", other, "{{BASE}}", js.URL, "{{CREATOR}}", js.creator).Replace(string(search))))
		return true
	}
	_, found, err := (&changerequest.Jira{Client: js.Client()}).Find(context.Background(), jiraRequestFor(js.URL))
	require.ErrorIs(t, err, changerequest.ErrIgnoredFilter, "jira: another promotion's issue")
	assert.False(t, found)
	js = newJiraServer(t, changerequest.CorrelationKey(corrID))
	_, found, err = (&changerequest.Jira{Client: js.Client()}).Find(context.Background(), jiraRequestFor(js.URL))
	require.NoError(t, err)
	assert.True(t, found, "jira: this promotion's issue")
}
