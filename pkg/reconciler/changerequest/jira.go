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
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, nil, "", "", errors.New("jira: baseURL must be https://<host>[/<path>]")
	}
	auth, err := authHeader(ctx, req, jc.ChangeRequestAuth)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("jira: %w", err)
	}
	return jc, base, "rest/api/" + jiraVersion(jc), auth, nil
}

func jiraVersion(jc *kardinalv1alpha1.JiraChangeRequest) string {
	if jc.APIVersion == "2" {
		return "2"
	}
	return "3"
}

// jiraFields are the fields read from an issue.
func jiraFields(spec *kardinalv1alpha1.ChangeRequestSpec) []string {
	f := []string{"status", "resolution", "labels"}
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
	if jc.Project == "" {
		return Reading{}, errors.New("jira: spec.jira.project is needed to open an issue")
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
	body := map[string]interface{}{
		"jql":        fmt.Sprintf("labels = %q ORDER BY created ASC", CorrelationKey(req.CorrelationID)),
		"maxResults": 1,
		"fields":     jiraFields(req.Spec),
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
	r, err := j.reading(base, jc, req, out.Issues[0])
	return r, err == nil, err
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
//	status in approvedStatuses, or, without them, the
//	status category is approvedStatusCategory (done)    approved
//	otherwise                                           pending
//
// A rejected or cancelled issue is often in the done category too, so they
// come first.
func JiraState(jc *kardinalv1alpha1.JiraChangeRequest, status, category, resolution string) string {
	cancelled := jc.CancelledResolutions
	if len(cancelled) == 0 {
		cancelled = jiraDefaultCancelled
	}
	approvedCategory := jc.ApprovedStatusCategory
	if approvedCategory == "" {
		approvedCategory = "done"
	}
	switch {
	case status != "" && slices.Contains(jc.RejectedStatuses, status):
		return kardinalv1alpha1.ChangeRequestRejected
	case resolution != "" && slices.Contains(cancelled, resolution):
		return kardinalv1alpha1.ChangeRequestCancelled
	case len(jc.ApprovedStatuses) > 0 && slices.Contains(jc.ApprovedStatuses, status):
		return kardinalv1alpha1.ChangeRequestApproved
	case len(jc.ApprovedStatuses) == 0 && category == approvedCategory:
		return kardinalv1alpha1.ChangeRequestApproved
	}
	return kardinalv1alpha1.ChangeRequestPending
}
