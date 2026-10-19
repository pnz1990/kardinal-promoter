// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ServiceNow is provider servicenow (docs/design/23-change-requests.md
// section 5.1): change_request records through the Table API
// (/api/now/table/change_request), read with sysparm_display_value=all so
// every field comes as {value, display_value}. The correlation id goes into
// the standard task field correlation_id, as CorrelationKey (the field holds
// 100 characters), with correlation_display "kardinal".
type ServiceNow struct {
	// Client is the HTTP client; nil means newHTTPClient(ProviderOptions{}).
	Client *http.Client
}

const (
	snTable     = "api/now/table/change_request"
	snApprovals = "api/now/table/sysapproval_approver"
	// snFields are the fields read from a change_request.
	snFields = "sys_id,number,state,approval,correlation_id,sys_created_on,sys_created_by"
)

// snKeyRE is a change number a link may name; anything else would be part
// of an encoded query.
var snKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// snNumberRE is a change number as ServiceNow writes one (a prefix of
// capitals, then digits: CHG0031337). The number a reading records is shown
// in the PR body and the receipt, so anything else fails the reading
// closed.
var snNumberRE = regexp.MustCompile(`^[A-Z]{1,16}[0-9]{1,32}$`)

// snFieldRE is a field name link.requireField may name.
var snFieldRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

// snClosedStates are the default Change Management states that close a
// change: Closed (3) and Review (0).
var snClosedStates = map[string]bool{"3": true, "0": true}

// snRecord is a record read with sysparm_display_value=all.
type snRecord map[string]interface{}

// field returns a field's value and display value. A plain string (a field
// the instance returns without display_value) is both.
func (r snRecord) field(name string) (string, string) {
	switch v := r[name].(type) {
	case string:
		return v, v
	case map[string]interface{}:
		val, _ := v["value"].(string)
		disp, _ := v["display_value"].(string)
		return val, disp
	}
	return "", ""
}

func (s *ServiceNow) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return newHTTPClient(ProviderOptions{})
}

// conf is the instance's ServiceNow settings, its base URL and the
// Authorization header.
func (s *ServiceNow) conf(ctx context.Context, req Request) (*kardinalv1alpha1.ServiceNowChangeRequest, *url.URL, string, error) {
	sn := req.Spec.ServiceNow
	if sn == nil {
		return nil, nil, "", errors.New("servicenow: spec.servicenow is not set")
	}
	base, err := url.Parse(strings.TrimSuffix(sn.InstanceURL, "/"))
	// A path, query or fragment would be copied into every ticket URL (the
	// PR body, the receipt): refused, as the CRD does.
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil ||
		base.Path != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, nil, "", errors.New("servicenow: instanceURL must be https://<host>")
	}
	auth, err := authHeader(ctx, req, sn.ChangeRequestAuth)
	if err != nil {
		return nil, nil, "", fmt.Errorf("servicenow: %w", err)
	}
	return sn, base, auth, nil
}

// fields is sysparm_fields: snFields and the field link.requireField names.
func snFieldsFor(spec *kardinalv1alpha1.ChangeRequestSpec) string {
	if spec.Link != nil && spec.Link.RequireField != nil && snFieldRE.MatchString(spec.Link.RequireField.Name) &&
		!slices.Contains(strings.Split(snFields, ","), spec.Link.RequireField.Name) {
		return snFields + "," + spec.Link.RequireField.Name
	}
	return snFields
}

// Create opens a change_request with the template's fields, the change
// model (type) and the correlation key.
func (s *ServiceNow) Create(ctx context.Context, req Request) (Reading, error) {
	sn, base, auth, err := s.conf(ctx, req)
	if err != nil {
		return Reading{}, err
	}
	body := map[string]string{}
	for k, v := range sn.Template {
		body[k] = v
	}
	body["type"] = sn.Type
	if body["type"] == "" {
		body["type"] = "normal"
	}
	body["correlation_id"] = CorrelationKey(req.CorrelationID)
	body["correlation_display"] = "kardinal"
	var out struct {
		Result snRecord `json:"result"`
	}
	q := url.Values{"sysparm_fields": {snFieldsFor(req.Spec)}, "sysparm_display_value": {"all"}}
	if err := call(ctx, s.client(), auth, http.MethodPost, base, snTable, q, body, &out, "servicenow: create change"); err != nil {
		return Reading{}, err
	}
	return s.reading(base, sn, req, out.Result, nil)
}

// snUserRE is a ServiceNow user_name kardinal puts into a query.
var snUserRE = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,100}$`)

// integrationUser is the user_name the credential acts as:
// spec.servicenow.integrationUser, else, for scheme basic, the credential's
// user.
func integrationUser(ctx context.Context, req Request, sn *kardinalv1alpha1.ServiceNowChangeRequest) (string, error) {
	user := sn.IntegrationUser
	if user == "" && sn.Scheme != "bearer" && req.Secret != nil {
		v, err := req.Secret(ctx, sn.CredentialsSecretRef)
		if err != nil {
			return "", fmt.Errorf("servicenow: credentials: %w", err)
		}
		user, _, _ = strings.Cut(v, ":")
	}
	if !snUserRE.MatchString(user) {
		return "", errors.New("servicenow: the integration user is unknown or not a user_name: set spec.servicenow.integrationUser")
	}
	return user, nil
}

// Find returns the change_request whose correlation_id is the correlation
// key and that the integration user created (sys_created_by), the oldest
// when there are several. A change someone else created with the key is
// never adopted: anyone who may create changes could plant one and approve
// it.
func (s *ServiceNow) Find(ctx context.Context, req Request) (Reading, bool, error) {
	sn, base, auth, err := s.conf(ctx, req)
	if err != nil {
		return Reading{}, false, err
	}
	user, err := integrationUser(ctx, req, sn)
	if err != nil {
		return Reading{}, false, err
	}
	rec, ok, err := s.query(ctx, base, auth,
		"correlation_id="+CorrelationKey(req.CorrelationID)+"^sys_created_by="+user+"^ORDERBYsys_created_on",
		snFieldsFor(req.Spec), "servicenow: find change")
	if err != nil || !ok {
		return Reading{}, false, err
	}
	// user_name compares case-insensitively in ServiceNow.
	if by, _ := rec.field("sys_created_by"); !strings.EqualFold(by, user) {
		return Reading{}, false, nil // the instance ignored the filter: not ours
	}
	r, err := s.reading(base, sn, req, rec, nil)
	return r, err == nil, err
}

// Get reads the change by sys_id when known, else by number, and its
// approvers (best effort: an instance that refuses sysapproval_approver
// gives none).
func (s *ServiceNow) Get(ctx context.Context, req Request, t kardinalv1alpha1.ChangeRequestTicket) (Reading, error) {
	sn, base, auth, err := s.conf(ctx, req)
	if err != nil {
		return Reading{}, err
	}
	fields := snFieldsFor(req.Spec)
	var rec snRecord
	if t.ID != "" && snSysIDRE.MatchString(t.ID) {
		var out struct {
			Result snRecord `json:"result"`
		}
		q := url.Values{"sysparm_fields": {fields}, "sysparm_display_value": {"all"}}
		if err := call(ctx, s.client(), auth, http.MethodGet, base, snTable+"/"+t.ID, q, nil, &out, "servicenow: read change"); err != nil {
			return Reading{}, err
		}
		rec = out.Result
	} else {
		if !snKeyRE.MatchString(t.Key) {
			return Reading{}, fmt.Errorf("servicenow: %q is not a change number: %w", cut(t.Key, 64), ErrNotFound)
		}
		var ok bool
		rec, ok, err = s.query(ctx, base, auth, "number="+t.Key, fields, "servicenow: read change")
		if err != nil {
			return Reading{}, err
		}
		if !ok {
			return Reading{}, fmt.Errorf("servicenow: change %s: %w", t.Key, ErrNotFound)
		}
	}
	sysID, _ := rec.field("sys_id")
	return s.reading(base, sn, req, rec, s.approvers(ctx, base, auth, sysID))
}

// snSysIDRE is a sys_id.
var snSysIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// query returns the first change_request of an encoded query.
func (s *ServiceNow) query(ctx context.Context, base *url.URL, auth, query, fields, what string) (snRecord, bool, error) {
	var out struct {
		Result []snRecord `json:"result"`
	}
	q := url.Values{"sysparm_query": {query}, "sysparm_fields": {fields}, "sysparm_display_value": {"all"},
		"sysparm_limit": {"1"}}
	if err := call(ctx, s.client(), auth, http.MethodGet, base, snTable, q, nil, &out, what); err != nil {
		return nil, false, err
	}
	if len(out.Result) == 0 {
		return nil, false, nil
	}
	return out.Result[0], true, nil
}

// approvers reads the change's approval records; nil when they cannot be
// read.
func (s *ServiceNow) approvers(ctx context.Context, base *url.URL, auth, sysID string) []kardinalv1alpha1.ChangeRequestApprover {
	if !snSysIDRE.MatchString(sysID) {
		return nil
	}
	var out struct {
		Result []snRecord `json:"result"`
	}
	q := url.Values{"sysparm_query": {"sysapproval=" + sysID + "^ORDERBYsys_created_on"},
		"sysparm_fields": {"approver,state,sys_updated_on"}, "sysparm_display_value": {"all"}, "sysparm_limit": {"32"}}
	if err := call(ctx, s.client(), auth, http.MethodGet, base, snApprovals, q, nil, &out, "servicenow: read approvers"); err != nil {
		return nil
	}
	var list []kardinalv1alpha1.ChangeRequestApprover
	for _, r := range out.Result {
		_, name := r.field("approver")
		state, _ := r.field("state")
		if name == "" {
			continue
		}
		a := kardinalv1alpha1.ChangeRequestApprover{Name: cut(name, 256), Decision: cut(state, 64)}
		if at, _ := r.field("sys_updated_on"); at != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", at, time.UTC); err == nil {
				mt := metav1.NewTime(t)
				a.At = &mt
			}
		}
		list = append(list, a)
	}
	return list
}

// reading is a Reading of rec.
func (s *ServiceNow) reading(base *url.URL, sn *kardinalv1alpha1.ServiceNowChangeRequest, req Request, rec snRecord,
	approvers []kardinalv1alpha1.ChangeRequestApprover) (Reading, error) {
	sysID, _ := rec.field("sys_id")
	number, _ := rec.field("number")
	if number == "" || sysID == "" {
		return Reading{}, errors.New("servicenow: the change has no number or sys_id")
	}
	if !snNumberRE.MatchString(number) {
		return Reading{}, errors.New("servicenow: the change number is not capitals then digits (CHG0031337); the change is not read")
	}
	state, stateName := rec.field("state")
	approval, approvalName := rec.field("approval")
	corr, _ := rec.field("correlation_id")
	if corr == CorrelationKey(req.CorrelationID) {
		corr = req.CorrelationID
	}
	r := Reading{
		Ticket: kardinalv1alpha1.ChangeRequestTicket{Key: number, ID: sysID,
			URL: base.String() + "/nav_to.do?uri=" + url.QueryEscape("change_request.do?sys_id="+sysID)},
		State:         ServiceNowState(state, stateName, approval, sn.States),
		ProviderState: strings.TrimSpace(orRaw(stateName, state) + " / " + orRaw(approvalName, approval)),
		Approvers:     approvers,
		CorrelationID: corr,
	}
	if created, _ := rec.field("sys_created_on"); created != "" {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", created, time.UTC); err == nil {
			r.Created = t
		}
	}
	if req.Spec.Link != nil && req.Spec.Link.RequireField != nil {
		name := req.Spec.Link.RequireField.Name
		v, d := rec.field(name)
		r.Fields = map[string]string{name: v}
		if d != v && req.Spec.Link.RequireField.Value == d {
			r.Fields[name] = d // a reference field matches by its display value too
		}
	}
	return r, nil
}

// ServiceNowState maps a change's state and approval to the normalised
// state (spec 23 section 5.1):
//
//	approval rejected                              rejected, always: no states map overrides it
//	the template's states map (raw value or display name), where a mapping
//	to approved counts only with approval approved (else pending)
//	state Canceled (4)                             cancelled
//	state Closed (3) or Review (0)                 closed
//	approval approved, state Scheduled (-2) or Implement (-1)   approved
//	otherwise                                      pending
func ServiceNowState(state, stateName, approval string, custom map[string]string) string {
	if approval == "rejected" {
		return kardinalv1alpha1.ChangeRequestRejected
	}
	for _, k := range []string{state, stateName} {
		if v, ok := custom[k]; ok && k != "" {
			v = normalState(v)
			if v == kardinalv1alpha1.ChangeRequestApproved && approval != "approved" {
				return kardinalv1alpha1.ChangeRequestPending
			}
			return v
		}
	}
	switch {
	case state == "4":
		return kardinalv1alpha1.ChangeRequestCancelled
	case snClosedStates[state]:
		return kardinalv1alpha1.ChangeRequestClosed
	case approval == "approved" && (state == "-2" || state == "-1"):
		return kardinalv1alpha1.ChangeRequestApproved
	}
	return kardinalv1alpha1.ChangeRequestPending
}

func orRaw(display, raw string) string {
	if display != "" {
		return display
	}
	return raw
}
