// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Environment variables hack/e2e/components/ticketapi.sh sets.
const (
	// EnvTicketAPIURL is the fake ticketing API's in-cluster HTTPS base URL.
	EnvTicketAPIURL = "KARDINAL_E2E_TICKETAPI_URL"
	// EnvSnowCredentials is the user:password the fake ServiceNow accepts.
	EnvSnowCredentials = "KARDINAL_E2E_SNOW_CREDENTIALS"
)

// ticketAPIProxy reaches the fake ticketing API's control port through the
// API server.
const ticketAPIProxy = "/api/v1/namespaces/ticket-api/services/http:ticketapi:8080/proxy"

// TicketAPIURL is the fake ticketing API's in-cluster HTTPS base URL.
func TicketAPIURL(t *testing.T) string {
	t.Helper()
	return mustEnv(t, EnvTicketAPIURL)
}

// SnowCredentials is the user:password the fake ServiceNow accepts.
func SnowCredentials(t *testing.T) string {
	t.Helper()
	return mustEnv(t, EnvSnowCredentials)
}

func mustEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; the core suite runs hack/e2e/components/ticketapi.sh", name)
	}
	return v
}

// FakeSnowTicket is a change_request of the fake ServiceNow.
type FakeSnowTicket struct {
	SysID         string            `json:"sys_id"`
	Number        string            `json:"number"`
	State         string            `json:"state"`
	Approval      string            `json:"approval"`
	CorrelationID string            `json:"correlation_id"`
	CreatedBy     string            `json:"sys_created_by"`
	Fields        map[string]string `json:"fields"`
}

// FakeTicketRequest is one request the fake ticketing API got.
type FakeTicketRequest struct {
	Time   time.Time `json:"time"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Query  string    `json:"query"`
	Auth   string    `json:"auth"`
	// User is the basic-auth user the request carried, accepted or not.
	User   string `json:"user"`
	Status int    `json:"status"`
	// Body is the request body (a create carries the correlation key).
	Body string `json:"body"`
}

func (e *Env) ticketControl(ctx context.Context, method string, body interface{}, out interface{}, path ...string) error {
	rc := e.Kube.CoreV1().RESTClient()
	req := rc.Get()
	switch method {
	case "PUT":
		req = rc.Put()
	case "POST":
		req = rc.Post()
	}
	req = req.AbsPath(append([]string{ticketAPIProxy, "_control"}, path...)...)
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req = req.SetHeader("Content-Type", "application/json").Body(raw)
	}
	raw, err := req.DoRaw(ctx)
	if err != nil {
		return fmt.Errorf("fake ticketing API %s %s: %w", method, strings.Join(path, "/"), err)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// AddFakeTicketUser makes the fake ticketing API accept user with password
// too, and returns the "user:password" credential a ChangeRequest's Secret
// holds. Request records name the user (FakeTicketRequest.User), so a test
// sees whose credential each request carried.
func (e *Env) AddFakeTicketUser(t *testing.T, user, password string) string {
	t.Helper()
	if err := e.ticketControl(context.Background(), "PUT", map[string]string{"password": password}, nil, "users", user); err != nil {
		t.Fatalf("add the fake ticketing API user %s: %v", user, err)
	}
	return user + ":" + password
}

// FakeSnowTickets returns the fake ServiceNow's change_requests.
func (e *Env) FakeSnowTickets(ctx context.Context) ([]FakeSnowTicket, error) {
	var out []FakeSnowTicket
	return out, e.ticketControl(ctx, "GET", nil, &out, "servicenow")
}

// SetFakeSnowTicket changes the fake ServiceNow change number: its state,
// approval and fields where set.
func (e *Env) SetFakeSnowTicket(t *testing.T, number string, change map[string]interface{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.ticketControl(ctx, "PUT", change, nil, "servicenow", number); err != nil {
		t.Fatal(err)
	}
}

// AddFakeSnowTicket adds a change_request someone else opened (no
// correlation id unless given) and returns it.
func (e *Env) AddFakeSnowTicket(t *testing.T, ticket FakeSnowTicket) FakeSnowTicket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out FakeSnowTicket
	if err := e.ticketControl(ctx, "POST", ticket, &out, "servicenow"); err != nil {
		t.Fatal(err)
	}
	return out
}

// FailFakeTicketAPI makes the next count API requests whose path?query
// contains match answer status, with Retry-After retryAfter when set.
func (e *Env) FailFakeTicketAPI(t *testing.T, match string, status, count int, retryAfter string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.ticketControl(ctx, "PUT", map[string]interface{}{"status": status, "count": count, "retryAfter": retryAfter, "match": match},
		nil, "fail"); err != nil {
		t.Fatal(err)
	}
}

// FakeTicketRequests returns the requests the fake ticketing API got whose
// query or path contains match, oldest first.
func (e *Env) FakeTicketRequests(ctx context.Context, match string) ([]FakeTicketRequest, error) {
	var all []FakeTicketRequest
	if err := e.ticketControl(ctx, "GET", nil, &all, "requests"); err != nil {
		return nil, err
	}
	var out []FakeTicketRequest
	for _, r := range all {
		if strings.Contains(r.Path+"?"+r.Query, match) {
			out = append(out, r)
		}
	}
	return out, nil
}

// FakeJiraIssue is an issue of the fake Jira.
type FakeJiraIssue struct {
	ID         string                 `json:"id"`
	Key        string                 `json:"key"`
	Project    string                 `json:"project"`
	Status     string                 `json:"status"`
	Category   string                 `json:"category"`
	Resolution string                 `json:"resolution"`
	Labels     []string               `json:"labels"`
	Fields     map[string]interface{} `json:"fields"`
}

// FakeJiraIssues returns the fake Jira's issues.
func (e *Env) FakeJiraIssues(ctx context.Context) ([]FakeJiraIssue, error) {
	var out []FakeJiraIssue
	return out, e.ticketControl(ctx, "GET", nil, &out, "jira")
}

// SetFakeJiraIssue changes the fake Jira issue key: its status, status
// category, resolution and fields where set.
func (e *Env) SetFakeJiraIssue(t *testing.T, key string, change map[string]interface{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.ticketControl(ctx, "PUT", change, nil, "jira", key); err != nil {
		t.Fatal(err)
	}
}
