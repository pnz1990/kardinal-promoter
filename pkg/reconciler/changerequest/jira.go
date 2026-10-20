// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Jira is provider jira (docs/design/23-change-requests.md section 5.2):
// issues through the REST API (version 3 on Jira Cloud, 2 on Data Center).
// The correlation id goes into a label, as CorrelationKey (a label has no
// spaces), and is searched for with JQL. Jira has no approval model outside
// Jira Service Management: teams model approval as a workflow status, so the
// state follows the status (approvedStatuses, or the status category) and
// the resolution (cancelledResolutions).
type Jira struct {
	// Client is the HTTP client; nil means newHTTPClient(ProviderOptions{}).
	Client *http.Client
}

// jiraKeyRE is an issue key a link may name; anything else would be part of
// a path.
var jiraKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}-[0-9]{1,18}$`)

// jiraIDRE is an issue id.
var jiraIDRE = regexp.MustCompile(`^[0-9]{1,18}$`)

// jiraFieldRE is a field name link.requireField may name (labels,
// customfield_10010, ...).
var jiraFieldRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,79}$`)

// jiraDefaultCancelled is cancelledResolutions when unset.
var jiraDefaultCancelled = []string{"Won't Do"}

// jiraIssue is an issue as the REST API answers it.
type jiraIssue struct {
	ID     string                     `json:"id"`
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

func (j *Jira) client() *http.Client {
	if j.Client != nil {
		return j.Client
	}
	return newHTTPClient(ProviderOptions{})
}

// conf is the instance's Jira settings, base URL, API path prefix and the
// Authorization header.
func (j *Jira) conf(ctx context.Context, req Request) (*kardinalv1alpha1.JiraChangeRequest, *url.URL, string, string, error) {
	jc := req.Spec.Jira
	if jc == nil {
		return nil, nil, "", "", errors.New("jira: spec.jira is not set")
	}
	base, err := url.Parse(strings.TrimSuffix(jc.BaseURL, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, nil, "", "", errors.New("jira: baseURL must be https://<host>[/<path>]")
	}
	if !jiraProjectRE.MatchString(jc.Project) {
		return nil, nil, "", "", errors.New("jira: spec.jira.project is needed: kardinal opens, searches and links issues only in configured projects")
	}
	auth, err := authHeader(ctx, req, jc.ChangeRequestAuth)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("jira: %w", err)
	}
	return jc, base, "rest/api/" + jiraVersion(jc), auth, nil
}

// jiraProjectRE is a project key.
var jiraProjectRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// jiraProjects are the projects issues are searched and linked in: Project
// and LinkProjects.
func jiraProjects(jc *kardinalv1alpha1.JiraChangeRequest) []string {
	out := []string{jc.Project}
	for _, p := range jc.LinkProjects {
		if jiraProjectRE.MatchString(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// jiraProjectOf is the project key of an issue key (CHG-42 is CHG).
func jiraProjectOf(key string) string {
	p, _, _ := strings.Cut(key, "-")
	return p
}

func jiraVersion(jc *kardinalv1alpha1.JiraChangeRequest) string {
	if jc.APIVersion == "2" {
		return "2"
	}
	return "3"
}

// jiraFields are the fields read from an issue.
func jiraFields(spec *kardinalv1alpha1.ChangeRequestSpec) []string {
	f := []string{"status", "resolution", "labels", "project", "created"}
	if spec.Link != nil && spec.Link.RequireField != nil && jiraFieldRE.MatchString(spec.Link.RequireField.Name) &&
		!slices.Contains(f, spec.Link.RequireField.Name) {
		f = append(f, spec.Link.RequireField.Name)
	}
	return f
}

// Create opens an issue in the project, of the issue type, with the
// template's fields and the correlation label. The template cannot set the
// project, the issue type or the labels. On API version 3 a description is
// sent as one Atlassian Document Format paragraph.
func (j *Jira) Create(ctx context.Context, req Request) (Reading, error) {
	jc, base, api, auth, err := j.conf(ctx, req)
	if err != nil {
		return Reading{}, err
	}
	fields := map[string]interface{}{}
	for k, v := range jc.Template {
		if k == "description" && jiraVersion(jc) == "3" {
			fields[k] = adfParagraph(v)
			continue
		}
		fields[k] = v
	}
	issueType := jc.IssueType
	if issueType == "" {
		issueType = "Task"
	}
	fields["project"] = map[string]string{"key": jc.Project}
	fields["issuetype"] = map[string]string{"name": issueType}
	fields["labels"] = []string{CorrelationKey(req.CorrelationID)}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := call(ctx, j.client(), auth, http.MethodPost, base, api+"/issue", nil,
		map[string]interface{}{"fields": fields}, &out, "jira: create issue"); err != nil {
		return Reading{}, err
	}
	if !jiraKeyRE.MatchString(out.Key) || !jiraIDRE.MatchString(out.ID) {
		return Reading{}, errors.New("jira: the created issue has no key or id")
	}
	// The create answer has no status: a new issue waits in its workflow's
	// first status. The next poll reads it.
	return Reading{Ticket: kardinalv1alpha1.ChangeRequestTicket{Key: out.Key, ID: out.ID, URL: base.String() + "/browse/" + out.Key},
		State: kardinalv1alpha1.ChangeRequestPending, CorrelationID: req.CorrelationID}, nil
}

// adfParagraph is text as an Atlassian Document Format document.
func adfParagraph(text string) map[string]interface{} {
	return map[string]interface{}{"type": "doc", "version": 1, "content": []interface{}{
		map[string]interface{}{"type": "paragraph", "content": []interface{}{
			map[string]interface{}{"type": "text", "text": text}}}}}
}

// Find returns the oldest issue with the correlation label.
func (j *Jira) Find(ctx context.Context, req Request) (Reading, bool, error) {
	jc, base, api, auth, err := j.conf(ctx, req)
	if err != nil {
		return Reading{}, false, err
	}
	path := api + "/search"
	if jiraVersion(jc) == "3" {
		path = api + "/search/jql"
	}
	// creator = currentUser(): an issue someone else created with the label
	// (planted, then moved to approved by its planter) is not adopted. The
	// creator, unlike the reporter, is set by Jira and cannot be edited:
	// Modify Reporter and Service Management's "raise on behalf of" set the
	// reporter to any account.
	body := map[string]interface{}{
		"jql": fmt.Sprintf("project = %q AND labels = %q AND creator = currentUser() ORDER BY created ASC", jc.Project,
			CorrelationKey(req.CorrelationID)),
		"maxResults": 1,
		"fields":     append(jiraFields(req.Spec), "creator"),
	}
	var out struct {
		Issues []jiraIssue `json:"issues"`
	}
	if err := call(ctx, j.client(), auth, http.MethodPost, base, path, nil, body, &out, "jira: search issues"); err != nil {
		return Reading{}, false, err
	}
	if len(out.Issues) == 0 {
		return Reading{}, false, nil
	}
	// The answer is checked too, against the account the credential is:
	// a site that ignored the clause must not hand over a planted issue.
	var me jiraUser
	if err := call(ctx, j.client(), auth, http.MethodGet, base, api+"/myself", nil, nil, &me, "jira: read the current user"); err != nil {
		return Reading{}, false, err
	}
	var creator jiraUser
	_ = json.Unmarshal(out.Issues[0].Fields["creator"], &creator)
	if !me.same(creator) {
		return Reading{}, false, nil
	}
	r, err := j.reading(base, jc, req, out.Issues[0])
	if err == nil && r.CorrelationID != req.CorrelationID {
		// A site that ignored the label clause answers with another of
		// kardinal's issues: another promotion's, never this one's. Creating
		// one now could duplicate this promotion's, which the search did not
		// show.
		return Reading{}, false, fmt.Errorf("jira: search issues: the answer, %s, does not carry this promotion's label: %w",
			r.Ticket.Key, ErrIgnoredFilter)
	}
	return r, err == nil, err
}

// jiraUser is a Jira user: accountId on Cloud, name or key on Data Center.
type jiraUser struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

// same reports whether u and o are one known user.
func (u jiraUser) same(o jiraUser) bool {
	switch {
	case u.AccountID != "":
		return u.AccountID == o.AccountID
	case u.Key != "":
		return u.Key == o.Key
	case u.Name != "":
		return u.Name == o.Name
	}
	return false
}

// Get reads the issue by id when known, else by its key.
func (j *Jira) Get(ctx context.Context, req Request, t kardinalv1alpha1.ChangeRequestTicket) (Reading, error) {
	jc, base, api, auth, err := j.conf(ctx, req)
	if err != nil {
		return Reading{}, err
	}
	ref := t.ID
	if !jiraIDRE.MatchString(ref) {
		if !jiraKeyRE.MatchString(t.Key) {
			return Reading{}, fmt.Errorf("jira: %q is not an issue key: %w", cut(t.Key, 64), ErrNotFound)
		}
		if !slices.Contains(jiraProjects(jc), jiraProjectOf(t.Key)) {
			return Reading{}, fmt.Errorf("jira: issue %s is not in project %s: %w", t.Key,
				strings.Join(jiraProjects(jc), ", "), ErrNotFound)
		}
		ref = t.Key
	}
	var out jiraIssue
	q := url.Values{"fields": {strings.Join(jiraFields(req.Spec), ",")}}
	if err := call(ctx, j.client(), auth, http.MethodGet, base, api+"/issue/"+ref, q, nil, &out, "jira: read issue"); err != nil {
		return Reading{}, err
	}
	return j.reading(base, jc, req, out)
}

// reading is a Reading of issue.
func (j *Jira) reading(base *url.URL, jc *kardinalv1alpha1.JiraChangeRequest, req Request, issue jiraIssue) (Reading, error) {
	if !jiraKeyRE.MatchString(issue.Key) {
		return Reading{}, errors.New("jira: the issue has no key")
	}
	// The issue's own project, not only its key: an id or a moved issue
	// could name another project's issue.
	var project struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(issue.Fields["project"], &project)
	if project.Key == "" {
		project.Key = jiraProjectOf(issue.Key)
	}
	if !slices.Contains(jiraProjects(jc), project.Key) {
		return Reading{}, fmt.Errorf("jira: issue %s is in project %s, not %s: %w", issue.Key, cut(project.Key, 64),
			strings.Join(jiraProjects(jc), ", "), ErrNotFound)
	}
	var status struct {
		Name     string `json:"name"`
		Category struct {
			Key string `json:"key"`
		} `json:"statusCategory"`
	}
	_ = json.Unmarshal(issue.Fields["status"], &status)
	var resolution struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(issue.Fields["resolution"], &resolution)
	var labels []string
	_ = json.Unmarshal(issue.Fields["labels"], &labels)
	corr := ""
	if slices.Contains(labels, CorrelationKey(req.CorrelationID)) {
		corr = req.CorrelationID
	}
	raw := status.Name
	if resolution.Name != "" {
		raw += " / " + resolution.Name
	}
	r := Reading{
		Ticket:        kardinalv1alpha1.ChangeRequestTicket{Key: issue.Key, ID: issue.ID, URL: base.String() + "/browse/" + issue.Key},
		State:         JiraState(jc, status.Name, status.Category.Key, resolution.Name),
		ProviderState: raw,
		CorrelationID: corr,
	}
	var created string
	if json.Unmarshal(issue.Fields["created"], &created) == nil {
		if t, err := time.Parse("2006-01-02T15:04:05.000-0700", created); err == nil {
			r.Created = t
		}
	}
	if req.Spec.Link != nil && req.Spec.Link.RequireField != nil {
		name := req.Spec.Link.RequireField.Name
		r.Fields = map[string]string{name: jiraFieldText(issue.Fields[name], req.Spec.Link.RequireField.Value)}
	}
	return r, nil
}

// jiraFieldText is a field's value as text: a string or number itself; an
// option, user or object its value, name, key or accountId; a list the
// element equal to want, or its first element.
func jiraFieldText(raw json.RawMessage, want string) string {
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var text func(interface{}) string
	text = func(v interface{}) string {
		switch t := v.(type) {
		case string:
			return t
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(t)
		case map[string]interface{}:
			for _, k := range []string{"value", "name", "key", "accountId"} {
				if s, ok := t[k].(string); ok {
					return s
				}
			}
		case []interface{}:
			for _, e := range t {
				if s := text(e); s == want {
					return s
				}
			}
			if len(t) > 0 {
				return text(t[0])
			}
		}
		return ""
	}
	return text(v)
}

// JiraState maps an issue's status and resolution to the normalised state
// (spec 23 section 5.2), in this order:
//
//	status in rejectedStatuses                          rejected
//	resolution in cancelledResolutions (Won't Do)       cancelled
//	approvedStatuses set: status in it, and unresolved
//	  or resolved in approvedResolutions                approved
//	approvedStatuses unset: status category is
//	  approvedStatusCategory (done) and resolved in
//	  approvedResolutions                               approved
//	resolved otherwise (Duplicate, Declined, ...)       closed: never approved
//	otherwise (an unresolved done issue too)            pending
//
// Only what is configured approves: without approvedStatuses, the category
// approves only with a listed resolution, since many workflows move an
// issue to a done status (Declined, Rejected) without setting one.
func JiraState(jc *kardinalv1alpha1.JiraChangeRequest, status, category, resolution string) string {
	cancelled := jc.CancelledResolutions
	if len(cancelled) == 0 {
		cancelled = jiraDefaultCancelled
	}
	approvedCategory := jc.ApprovedStatusCategory
	if approvedCategory == "" {
		approvedCategory = "done"
	}
	resolvedApproved := resolution != "" && slices.Contains(jc.ApprovedResolutions, resolution)
	switch {
	case status != "" && slices.Contains(jc.RejectedStatuses, status):
		return kardinalv1alpha1.ChangeRequestRejected
	case resolution != "" && slices.Contains(cancelled, resolution):
		return kardinalv1alpha1.ChangeRequestCancelled
	case len(jc.ApprovedStatuses) > 0 && slices.Contains(jc.ApprovedStatuses, status) && (resolution == "" || resolvedApproved):
		return kardinalv1alpha1.ChangeRequestApproved
	case len(jc.ApprovedStatuses) == 0 && category == approvedCategory && resolvedApproved:
		return kardinalv1alpha1.ChangeRequestApproved
	case resolution != "":
		return kardinalv1alpha1.ChangeRequestClosed
	}
	return kardinalv1alpha1.ChangeRequestPending
}
