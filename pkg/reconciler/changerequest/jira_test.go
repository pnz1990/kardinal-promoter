// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
)

// The fixtures in testdata/jira are Jira REST API responses (create issue,
// get issue, JQL search) in the shape of the Jira Cloud platform REST API
// reference, written from it, not recorded (spec 23 section 5.4).
// {{CORRELATION}} becomes the correlation label, {{BASE}} the server URL.

const jiraToken = "ops@example.com:atl-token-123"

type jiraServer struct {
	*httptest.Server
	// reporter is the accountId of the search fixture's reporter.
	reporter string
	mu       sync.Mutex
	reqs     []jiraRequest
	answer   func(w http.ResponseWriter, r *http.Request) bool
}

type jiraRequest struct {
	Method, Path, RawQuery, Auth string
	Body                         map[string]interface{}
}

func newJiraServer(t *testing.T, corr string) *jiraServer {
	t.Helper()
	s := &jiraServer{reporter: "5b10a2844c20165700ede21g"}
	fix := func(name string) []byte {
		raw, err := os.ReadFile("testdata/jira/" + name)
		require.NoError(t, err)
		return []byte(strings.NewReplacer("{{CORRELATION}}", corr, "{{BASE}}", s.URL, "{{REPORTER}}", s.reporter).Replace(string(raw)))
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := jiraRequest{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &rec.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		answer := s.answer
		s.mu.Unlock()
		if answer != nil && answer(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issue"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(fix("created.json"))
		case r.Method == http.MethodPost && (r.URL.Path == "/rest/api/3/search/jql" || r.URL.Path == "/rest/api/2/search"):
			if strings.Contains(string(raw), corr) {
				_, _ = w.Write(fix("search.json"))
			} else {
				_, _ = w.Write(fix("empty.json"))
			}
		case r.URL.Path == "/rest/api/3/myself" || r.URL.Path == "/rest/api/2/myself":
			_, _ = w.Write(fix("myself.json"))
		case r.URL.Path == "/rest/api/3/issue/10042" || r.URL.Path == "/rest/api/3/issue/CHG-42" || r.URL.Path == "/rest/api/2/issue/CHG-42":
			_, _ = w.Write(fix("issue.json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist or you do not have permission to see it."],"errors":{}}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jiraServer) requests() []jiraRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]jiraRequest(nil), s.reqs...)
}

func jiraRequestFor(url string, mut ...func(*v1alpha1.JiraChangeRequest)) changerequest.Request {
	jc := &v1alpha1.JiraChangeRequest{BaseURL: url, APIVersion: "3", Project: "CHG", IssueType: "Change",
		ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "jira", Key: "token"}, Scheme: "basic"},
		Template: map[string]string{"summary": "Promote app v1 to prod", "description": "kardinal promotes app v1",
			"labels": "forged", "project": "EVIL"}}
	for _, m := range mut {
		m(jc)
	}
	return changerequest.Request{Spec: &v1alpha1.ChangeRequestSpec{Provider: "jira", Mode: "open", Jira: jc}, CorrelationID: corrID,
		Secret: func(_ context.Context, ref v1alpha1.SecretKeyRef) (string, error) {
			if ref.Name != "jira" {
				return "", errors.New("wrong secret")
			}
			return jiraToken, nil
		}}
}

// TestJira_CreateFindGet (spec 23 §5.2): a create sends the project, issue
// type and the correlation label (the template cannot set them), a v3
// description as an ADF paragraph, with basic auth (email:API token); the
// reading is pending with the browse link; Find searches the label with JQL
// on /search/jql (v3) or /search (v2), oldest first; Get reads by id, or by
// key for a link, and maps the label back to the correlation id.
//
// Covers CHANGE-REQUEST-08.
func TestJira_CreateFindGet(t *testing.T) {
	key := changerequest.CorrelationKey(corrID)
	srv := newJiraServer(t, key)
	j := &changerequest.Jira{Client: srv.Client()}
	req := jiraRequestFor(srv.URL)
	ctx := context.Background()

	r, err := j.Create(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ChangeRequestTicket{Key: "CHG-42", ID: "10042", URL: srv.URL + "/browse/CHG-42"}, r.Ticket)
	assert.Equal(t, "pending", r.State)
	assert.Equal(t, corrID, r.CorrelationID)
	got := srv.requests()[0]
	assert.Equal(t, "/rest/api/3/issue", got.Path)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraToken)), got.Auth)
	fields := got.Body["fields"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"key": "CHG"}, fields["project"])
	assert.Equal(t, map[string]interface{}{"name": "Change"}, fields["issuetype"])
	assert.Equal(t, []interface{}{key}, fields["labels"], "the template cannot set the labels")
	assert.Equal(t, "Promote app v1 to prod", fields["summary"])
	assert.Equal(t, map[string]interface{}{"type": "doc", "version": float64(1), "content": []interface{}{
		map[string]interface{}{"type": "paragraph", "content": []interface{}{map[string]interface{}{"type": "text", "text": "kardinal promotes app v1"}}}}},
		fields["description"], "v3 takes an ADF description")

	found, ok, err := j.Find(ctx, req)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "approved", found.State, "Approved is in the done category")
	assert.Equal(t, corrID, found.CorrelationID)
	got = srv.requests()[1]
	assert.Equal(t, "/rest/api/3/search/jql", got.Path)
	assert.Equal(t, `project = "CHG" AND labels = "`+key+`" AND reporter = currentUser() ORDER BY created ASC`, got.Body["jql"],
		"searched only in the project, only issues the integration account reported")
	assert.Equal(t, "/rest/api/3/myself", srv.requests()[2].Path, "the answer is checked against the current user")
	assert.Equal(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), found.Created.UTC(), "Find reads the creation time")
	assert.Equal(t, float64(1), got.Body["maxResults"])

	other := jiraRequestFor(srv.URL)
	other.CorrelationID = "kardinal:checkout/app-v1/prod/uid-2"
	_, ok, err = j.Find(ctx, other)
	require.NoError(t, err)
	assert.False(t, ok)

	byID, err := j.Get(ctx, req, r.Ticket)
	require.NoError(t, err)
	assert.Equal(t, "/rest/api/3/issue/10042", srv.requests()[4].Path)
	assert.Equal(t, "fields=status%2Cresolution%2Clabels%2Cproject%2Ccreated", srv.requests()[4].RawQuery)
	assert.Equal(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), byID.Created.UTC(), "created is read")
	assert.Equal(t, "pending", byID.State, "Awaiting approval is in progress")
	assert.Equal(t, "Awaiting approval", byID.ProviderState)
	assert.Equal(t, corrID, byID.CorrelationID)

	byKey, err := j.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "CHG-42"})
	require.NoError(t, err)
	assert.Equal(t, "CHG-42", byKey.Ticket.Key)
	assert.Equal(t, "/rest/api/3/issue/CHG-42", srv.requests()[5].Path)

	_, err = j.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "CHG-404"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	n := len(srv.requests())
	_, err = j.Get(ctx, req, v1alpha1.ChangeRequestTicket{Key: "../../myself"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	assert.Len(t, srv.requests(), n, "a key that is not an issue key is never put into a path")
}

// TestJira_DataCenter: API version 2 searches /rest/api/2/search, sends the
// description as text and a PAT as bearer.
func TestJira_DataCenter(t *testing.T) {
	key := changerequest.CorrelationKey(corrID)
	srv := newJiraServer(t, key)
	j := &changerequest.Jira{Client: srv.Client()}
	req := jiraRequestFor(srv.URL, func(jc *v1alpha1.JiraChangeRequest) { jc.APIVersion, jc.Scheme = "2", "bearer" })
	_, err := j.Create(context.Background(), req)
	require.NoError(t, err)
	got := srv.requests()[0]
	assert.Equal(t, "/rest/api/2/issue", got.Path)
	assert.Equal(t, "Bearer "+jiraToken, got.Auth)
	assert.Equal(t, "kardinal promotes app v1", got.Body["fields"].(map[string]interface{})["description"])
	_, ok, err := j.Find(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "/rest/api/2/search", srv.requests()[1].Path)

	n := len(srv.requests())
	noProject := jiraRequestFor(srv.URL, func(jc *v1alpha1.JiraChangeRequest) { jc.Project = "" })
	_, err = j.Create(context.Background(), noProject)
	assert.ErrorContains(t, err, "spec.jira.project is needed")
	_, _, err = j.Find(context.Background(), noProject)
	assert.ErrorContains(t, err, "spec.jira.project is needed")
	_, err = j.Get(context.Background(), noProject, v1alpha1.ChangeRequestTicket{Key: "CHG-42"})
	assert.ErrorContains(t, err, "spec.jira.project is needed")
	assert.Len(t, srv.requests(), n, "no project, no request")
}

// TestJira_ProjectScope (#2131 QA): a linked key of another project is
// refused before any request; an issue whose own project is not configured
// (read by id, or moved) is refused after it; LinkProjects admit more.
func TestJira_ProjectScope(t *testing.T) {
	srv := newJiraServer(t, "k")
	j := &changerequest.Jira{Client: srv.Client()}
	req := jiraRequestFor(srv.URL)
	n := len(srv.requests())
	_, err := j.Get(context.Background(), req, v1alpha1.ChangeRequestTicket{Key: "OPS-42"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	assert.ErrorContains(t, err, "is not in project CHG")
	assert.Len(t, srv.requests(), n)

	ops := jiraRequestFor(srv.URL, func(jc *v1alpha1.JiraChangeRequest) { jc.Project = "OPS" })
	_, err = j.Get(context.Background(), ops, v1alpha1.ChangeRequestTicket{Key: "CHG-42", ID: "10042"})
	assert.ErrorIs(t, err, changerequest.ErrNotFound)
	assert.ErrorContains(t, err, "is in project CHG, not OPS")

	both := jiraRequestFor(srv.URL, func(jc *v1alpha1.JiraChangeRequest) { jc.Project, jc.LinkProjects = "OPS", []string{"CHG"} })
	r, err := j.Get(context.Background(), both, v1alpha1.ChangeRequestTicket{Key: "CHG-42"})
	require.NoError(t, err)
	assert.Equal(t, "CHG-42", r.Ticket.Key)
}

// TestJira_RequireField: link.requireField reads an option's value, a
// list's matching element, or a plain field.
func TestJira_RequireField(t *testing.T) {
	srv := newJiraServer(t, "kardinal-other")
	j := &changerequest.Jira{Client: srv.Client()}
	for _, tc := range []struct{ field, value, want string }{
		{"customfield_10010", "checkout", "checkout"},
		{"components", "checkout", "checkout"},
		{"components", "billing", "payroll"},
		{"labels", "release", "release"},
	} {
		req := jiraRequestFor(srv.URL)
		req.Spec.Mode, req.Spec.Link = "link", &v1alpha1.ChangeRequestLink{From: "bundleLabel", Name: "change",
			RequireField: &v1alpha1.ChangeRequestFieldMatch{Name: tc.field, Value: tc.value}}
		r, err := j.Get(context.Background(), req, v1alpha1.ChangeRequestTicket{Key: "CHG-42"})
		require.NoError(t, err)
		assert.Equal(t, tc.want, r.Fields[tc.field], tc.field)
		assert.Empty(t, r.CorrelationID, "another promotion's label")
		assert.Contains(t, srv.requests()[len(srv.requests())-1].RawQuery, "%2C"+tc.field)
	}
}

func TestJiraState(t *testing.T) {
	def := &v1alpha1.JiraChangeRequest{}
	done := &v1alpha1.JiraChangeRequest{ApprovedResolutions: []string{"Done"}}
	named := &v1alpha1.JiraChangeRequest{ApprovedStatuses: []string{"Approved"}, RejectedStatuses: []string{"Declined"},
		CancelledResolutions: []string{"Duplicate"}, ApprovedStatusCategory: "indeterminate", ApprovedResolutions: []string{"Done"}}
	for _, tc := range []struct {
		jc                           *v1alpha1.JiraChangeRequest
		status, category, resolution string
		want                         string
	}{
		{def, "To Do", "new", "", "pending"},
		{def, "Awaiting approval", "indeterminate", "", "pending"},
		{def, "Approved", "done", "", "approved"},                // unresolved, in the approved category
		{def, "Done", "done", "Done", "closed"},                  // no resolution configured: only an explicit set counts
		{def, "Done", "done", "Duplicate", "closed"},             // never approved
		{def, "Done", "done", "Declined", "closed"},              // never approved
		{def, "Done", "done", "Rejected", "closed"},              // never approved
		{def, "Done", "done", "Something new", "closed"},         // an unknown resolution is not an approval
		{def, "Done", "done", "Won't Do", "cancelled"},           // the default cancelled resolution
		{done, "Done", "done", "Done", "approved"},               // explicitly configured
		{done, "Done", "done", "Duplicate", "closed"},            // not in the configured set
		{done, "In Progress", "indeterminate", "Done", "closed"}, // a resolution outside an approved status
		{named, "Approved", "done", "", "approved"},
		{named, "Approved", "done", "Done", "approved"},
		{named, "Approved", "done", "Declined", "closed"},
		{named, "Done", "done", "", "pending"},               // approvedStatuses set: the category does not count
		{named, "In Review", "indeterminate", "", "pending"}, // nor the configured category
		{named, "Declined", "done", "", "rejected"},
		{named, "Approved", "done", "Duplicate", "cancelled"},
		{&v1alpha1.JiraChangeRequest{ApprovedStatusCategory: "indeterminate"}, "In Review", "indeterminate", "", "approved"},
	} {
		assert.Equal(t, tc.want, changerequest.JiraState(tc.jc, tc.status, tc.category, tc.resolution),
			"%s/%s/%s %+v", tc.status, tc.category, tc.resolution, tc.jc)
	}
}

// TestJira_Errors: Jira's 401, 403, 404 and 429 map as ServiceNow's do; a
// created issue without a key is an error; a non-https base URL is refused
// before any request.
//
// Covers CHANGE-REQUEST-08.
func TestJira_Errors(t *testing.T) {
	srv := newJiraServer(t, "k")
	j := &changerequest.Jira{Client: srv.Client()}
	for status, check := range map[int]func(error) bool{
		401: func(err error) bool { return errors.Is(err, changerequest.ErrCredentials) },
		403: func(err error) bool { return errors.Is(err, changerequest.ErrCredentials) },
		404: func(err error) bool { return errors.Is(err, changerequest.ErrNotFound) },
		429: func(err error) bool {
			var re *changerequest.RetryError
			return errors.As(err, &re) && re.After.Seconds() == 12
		},
	} {
		srv.answer = func(w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errorMessages":["atl-token-123"]}`))
			return true
		}
		_, err := j.Get(context.Background(), jiraRequestFor(srv.URL), v1alpha1.ChangeRequestTicket{Key: "CHG-42"})
		require.Error(t, err)
		assert.True(t, check(err), "%d: %v", status, err)
		assert.NotContains(t, err.Error(), "atl-token")
		assert.NotContains(t, err.Error(), srv.URL)
	}
	srv.answer = func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1"}`))
		return true
	}
	_, err := j.Create(context.Background(), jiraRequestFor(srv.URL))
	assert.ErrorContains(t, err, "has no key or id")
	n := len(srv.requests())
	for _, u := range []string{strings.Replace(srv.URL, "https://", "http://", 1), srv.URL + "?x=1", strings.Replace(srv.URL, "https://", "https://a:b@", 1)} {
		_, err = j.Get(context.Background(), jiraRequestFor(u), v1alpha1.ChangeRequestTicket{Key: "CHG-42"})
		assert.ErrorContains(t, err, "baseURL must be https://", u)
	}
	assert.Len(t, srv.requests(), n)
}

// TestJira_PrePlanting (coordinator decision on #2131): Find adopts an
// issue only when the integration account reported it: the JQL says
// reporter = currentUser(), and an answer whose reporter is another account
// (a site that ignored the clause, handing over an issue mallory planted
// with the label and approved) is not adopted.
func TestJira_PrePlanting(t *testing.T) {
	key := changerequest.CorrelationKey(corrID)
	srv := newJiraServer(t, key)
	srv.reporter = "mallory-account"
	j := &changerequest.Jira{Client: srv.Client()}
	_, found, err := j.Find(context.Background(), jiraRequestFor(srv.URL))
	require.NoError(t, err)
	assert.False(t, found, "an issue another account reported is never adopted")
	srv.reporter = "5b10a2844c20165700ede21g"
	_, found, err = j.Find(context.Background(), jiraRequestFor(srv.URL))
	require.NoError(t, err)
	assert.True(t, found)
}
