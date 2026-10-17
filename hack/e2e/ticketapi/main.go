// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hack/e2e/ticketapi is the live e2e suites' stand-in for the ticketing
// systems ChangeRequests call (hack/e2e/components/ticketapi.sh): the
// ServiceNow Table API subset kardinal uses, over HTTPS with a certificate
// from the suite's own CA (the controller trusts it through
// changeRequest.caBundle), with basic auth. Tests drive the tickets through
// a plain-HTTP control port reached through the API server's service proxy.
//
// HTTPS (-listen-tls), basic auth -user / -password:
//
//	POST /api/now/table/change_request          create (state New, approval not requested)
//	GET  /api/now/table/change_request          sysparm_query correlation_id=<k>[^...] or number=<n>
//	GET  /api/now/table/change_request/<sys_id>
//	GET  /api/now/table/sysapproval_approver    sysparm_query sysapproval=<sys_id>[^...]
//
// Every answer uses sysparm_display_value=all: each field is
// {"display_value", "value"}.
//
// HTTP control (-listen):
//
//	GET  /_healthz
//	GET  /_control/servicenow                   the tickets, as JSON
//	POST /_control/servicenow                   {"number"?, "correlation_id"?, "fields"?} adds a ticket (one
//	                                            someone else opened); answers it
//	PUT  /_control/servicenow/<number>          {"state"?, "approval"?, "approvers"?, "fields"?} changes it
//	PUT  /_control/fail                         {"status":429,"retryAfter":"5","count":2,"match":"..."}: the next
//	                                            count API requests whose path?query contains match fail so
//	                                            ("count":0 clears)
//	GET  /_control/requests                     the API requests, oldest first (no credentials)
//
// Only the standard library, so it builds without downloading modules.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxRecords = 5000
	maxBody    = 1 << 20
)

// snStateNames and snApprovalNames are the default Change Management
// display names.
var (
	snStateNames = map[string]string{"-5": "New", "-4": "Assess", "-3": "Authorize", "-2": "Scheduled",
		"-1": "Implement", "0": "Review", "3": "Closed", "4": "Canceled"}
	snApprovalNames = map[string]string{"not requested": "Not Yet Requested", "requested": "Requested",
		"approved": "Approved", "rejected": "Rejected"}
)

// ticket is one change_request.
type ticket struct {
	SysID         string            `json:"sys_id"`
	Number        string            `json:"number"`
	State         string            `json:"state"`
	Approval      string            `json:"approval"`
	CorrelationID string            `json:"correlation_id"`
	Fields        map[string]string `json:"fields"`
	Approvers     []approver        `json:"approvers"`
	Created       time.Time         `json:"created"`
}

type approver struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// record is one API request.
type record struct {
	Time   time.Time `json:"time"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Query  string    `json:"query"`
	// Auth is "ok" or why the credential was refused.
	Auth   string `json:"auth"`
	Status int    `json:"status"`
	Body   string `json:"body,omitempty"`
}

type failure struct {
	Status     int    `json:"status"`
	RetryAfter string `json:"retryAfter"`
	Count      int    `json:"count"`
	// Match, when set, limits the failures to requests whose path?query
	// contains it, so parallel tests do not take each other's.
	Match string `json:"match"`
}

type server struct {
	user, password string
	mu             sync.Mutex
	tickets        []*ticket
	records        []record
	fail           failure
}

func main() {
	addr := flag.String("listen", ":8080", "control listen address (HTTP)")
	tlsAddr := flag.String("listen-tls", ":8443", "API listen address (HTTPS)")
	cert := flag.String("tls-cert", "/tls/tls.crt", "certificate")
	key := flag.String("tls-key", "/tls/tls.key", "key")
	s := &server{}
	flag.StringVar(&s.user, "user", "kardinal-e2e", "accepted basic-auth user")
	flag.StringVar(&s.password, "password", "e2e-snow-pass", "accepted basic-auth password")
	flag.Parse()
	go func() {
		srv := &http.Server{Addr: *tlsAddr, Handler: http.HandlerFunc(s.api), ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServeTLS(*cert, *key))
	}()
	srv := &http.Server{Addr: *addr, Handler: http.HandlerFunc(s.control), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("ticket API on %s (HTTPS), control on %s", *tlsAddr, *addr)
	log.Fatal(srv.ListenAndServe())
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// add makes a ticket; the caller holds s.mu.
func (s *server) add(t *ticket) *ticket {
	if t.SysID == "" {
		t.SysID = newID()
	}
	if t.Number == "" {
		t.Number = fmt.Sprintf("CHG%07d", len(s.tickets)+1)
	}
	if t.State == "" {
		t.State = "-5"
	}
	if t.Approval == "" {
		t.Approval = "not requested"
	}
	if t.Fields == nil {
		t.Fields = map[string]string{}
	}
	t.Created = time.Now().UTC()
	s.tickets = append(s.tickets, t)
	return t
}

func (s *server) control(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	path := strings.TrimPrefix(r.URL.Path, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case path == "_healthz":
		w.WriteHeader(http.StatusOK)
	case path == "_control/requests":
		writeJSON(w, http.StatusOK, s.records)
	case path == "_control/fail" && r.Method == http.MethodPut:
		var f failure
		if json.Unmarshal(body, &f) != nil {
			http.Error(w, `PUT {"status":429,"retryAfter":"5","count":1}`, http.StatusBadRequest)
			return
		}
		s.fail = f
		w.WriteHeader(http.StatusNoContent)
	case path == "_control/servicenow" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.tickets)
	case path == "_control/servicenow" && r.Method == http.MethodPost:
		var t ticket
		if json.Unmarshal(body, &t) != nil {
			http.Error(w, "POST a ticket", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, s.add(&t))
	case strings.HasPrefix(path, "_control/servicenow/") && r.Method == http.MethodPut:
		t := s.byNumber(strings.TrimPrefix(path, "_control/servicenow/"))
		if t == nil {
			http.NotFound(w, r)
			return
		}
		var in struct {
			State     *string           `json:"state"`
			Approval  *string           `json:"approval"`
			Approvers []approver        `json:"approvers"`
			Fields    map[string]string `json:"fields"`
		}
		if json.Unmarshal(body, &in) != nil {
			http.Error(w, "PUT a change", http.StatusBadRequest)
			return
		}
		if in.State != nil {
			t.State = *in.State
		}
		if in.Approval != nil {
			t.Approval = *in.Approval
		}
		if in.Approvers != nil {
			t.Approvers = in.Approvers
		}
		for k, v := range in.Fields {
			t.Fields[k] = v
		}
		writeJSON(w, http.StatusOK, t)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) byNumber(n string) *ticket {
	for _, t := range s.tickets {
		if t.Number == n {
			return t
		}
	}
	return nil
}

func (s *server) bySysID(id string) *ticket {
	for _, t := range s.tickets {
		if t.SysID == id {
			return t
		}
	}
	return nil
}

func (s *server) api(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := record{Time: time.Now().UTC(), Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body)}
	status := s.serve(w, r, body, &rec)
	rec.Status = status
	if len(s.records) >= maxRecords {
		s.records = s.records[1:]
	}
	s.records = append(s.records, rec)
}

// serve answers one API request and returns its status; the caller holds s.mu.
func (s *server) serve(w http.ResponseWriter, r *http.Request, body []byte, rec *record) int {
	user, pass, ok := r.BasicAuth()
	switch {
	case !ok:
		rec.Auth = "no basic credentials"
	case user != s.user || pass != s.password:
		rec.Auth = "wrong user or password"
	default:
		rec.Auth = "ok"
	}
	if rec.Auth != "ok" {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": map[string]string{"message": "User Not Authenticated"},
			"status": "failure"})
		return http.StatusUnauthorized
	}
	if s.fail.Count > 0 && strings.Contains(r.URL.Path+"?"+r.URL.RawQuery, s.fail.Match) {
		s.fail.Count--
		if s.fail.RetryAfter != "" {
			w.Header().Set("Retry-After", s.fail.RetryAfter)
		}
		w.WriteHeader(s.fail.Status)
		return s.fail.Status
	}
	q := r.URL.Query()
	fields := q.Get("sysparm_fields")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/now/table/change_request":
		var in map[string]string
		if json.Unmarshal(body, &in) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "failure"})
			return http.StatusBadRequest
		}
		t := &ticket{CorrelationID: in["correlation_id"], Fields: map[string]string{}}
		for k, v := range in {
			if k != "correlation_id" && k != "state" && k != "approval" && k != "number" && k != "sys_id" {
				t.Fields[k] = v
			}
		}
		writeJSON(w, http.StatusCreated, map[string]interface{}{"result": display(s.add(t), fields)})
		return http.StatusCreated
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/now/table/change_request/"):
		t := s.bySysID(strings.TrimPrefix(r.URL.Path, "/api/now/table/change_request/"))
		if t == nil {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": map[string]string{"message": "No Record found"},
				"status": "failure"})
			return http.StatusNotFound
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"result": display(t, fields)})
		return http.StatusOK
	case r.Method == http.MethodGet && r.URL.Path == "/api/now/table/change_request":
		cond := strings.SplitN(q.Get("sysparm_query"), "^", 2)[0]
		name, value, _ := strings.Cut(cond, "=")
		var out []map[string]interface{}
		list := append([]*ticket(nil), s.tickets...)
		sort.SliceStable(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
		for _, t := range list {
			if (name == "correlation_id" && t.CorrelationID == value) || (name == "number" && t.Number == value) {
				out = append(out, display(t, fields))
			}
		}
		if q.Get("sysparm_limit") == "1" && len(out) > 1 {
			out = out[:1]
		}
		if out == nil {
			out = []map[string]interface{}{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"result": out})
		return http.StatusOK
	case r.Method == http.MethodGet && r.URL.Path == "/api/now/table/sysapproval_approver":
		cond := strings.SplitN(q.Get("sysparm_query"), "^", 2)[0]
		out := []map[string]interface{}{}
		if t := s.bySysID(strings.TrimPrefix(cond, "sysapproval=")); t != nil {
			for _, a := range t.Approvers {
				out = append(out, map[string]interface{}{
					"approver":       map[string]string{"display_value": a.Name, "value": newID()},
					"state":          map[string]string{"display_value": a.State, "value": a.State},
					"sys_updated_on": map[string]string{"display_value": "", "value": time.Now().UTC().Format("2006-01-02 15:04:05")},
				})
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"result": out})
		return http.StatusOK
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"status": "failure"})
	return http.StatusNotFound
}

// display is t as the Table API answers with sysparm_display_value=all,
// limited to fields when set.
func display(t *ticket, fields string) map[string]interface{} {
	all := map[string]interface{}{
		"sys_id":         dv(t.SysID, t.SysID),
		"number":         dv(t.Number, t.Number),
		"state":          dv(t.State, snStateNames[t.State]),
		"approval":       dv(t.Approval, snApprovalNames[t.Approval]),
		"correlation_id": dv(t.CorrelationID, t.CorrelationID),
		"sys_created_on": dv(t.Created.UTC().Format("2006-01-02 15:04:05"), t.Created.UTC().Format("2006-01-02 15:04:05")),
	}
	for k, v := range t.Fields {
		if _, set := all[k]; !set {
			all[k] = dv(v, v)
		}
	}
	if fields == "" {
		return all
	}
	out := map[string]interface{}{}
	for _, f := range strings.Split(fields, ",") {
		if v, ok := all[f]; ok {
			out[f] = v
		} else {
			out[f] = dv("", "")
		}
	}
	return out
}

func dv(value, display string) map[string]string {
	return map[string]string{"value": value, "display_value": display}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
