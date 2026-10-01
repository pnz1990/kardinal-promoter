// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hack/e2e/receiver is the live e2e suites' webhook receiver
// (hack/e2e/components/webhook-receiver.sh). It records every request it gets
// and answers as a test tells it to, so tests can assert what the controller
// sent and make deliveries fail.
//
// Requests are grouped in buckets, the first path segment; each test uses its
// own (its namespace name):
//
//	ANY  /<bucket>/...      recorded; answered 200 "ok" unless a mode is set
//	GET  /_records/<bucket> the bucket's records, oldest first, as JSON
//	POST /_mode/<bucket>    {"status":503,"times":2,"location":"...",
//	                        "retryAfter":"600"}: answer the next times
//	                        requests (0: all) with status, with a Location or
//	                        Retry-After header when set; {"status":0} resets
//	GET  /_healthz          200
//
// Only the standard library, so it builds without downloading modules.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxRecords = 1000
	maxBody    = 1 << 20
)

// record is one request as the receiver got it.
type record struct {
	Time    time.Time           `json:"time"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Status is what the receiver answered.
	Status int `json:"status"`
}

// mode makes the receiver answer a bucket's requests with Status.
type mode struct {
	Status   int    `json:"status"`
	Times    int    `json:"times"`
	Location string `json:"location,omitempty"`
	// RetryAfter is the Retry-After header value (seconds or an HTTP date).
	RetryAfter string `json:"retryAfter,omitempty"`
}

type receiver struct {
	mu      sync.Mutex
	records map[string][]record
	modes   map[string]*mode
}

func main() {
	addr := flag.String("listen", ":8080", "listen address")
	flag.Parse()
	r := &receiver{records: map[string][]record{}, modes: map[string]*mode{}}
	srv := &http.Server{Addr: *addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("receiver listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/", 3)
	switch {
	case parts[0] == "_healthz":
		w.WriteHeader(http.StatusOK)
	case parts[0] == "_records" && len(parts) > 1 && req.Method == http.MethodGet:
		r.mu.Lock()
		recs := append([]record{}, r.records[parts[1]]...)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recs)
	case parts[0] == "_mode" && len(parts) > 1 && req.Method == http.MethodPost:
		var m mode
		if err := json.NewDecoder(io.LimitReader(req.Body, maxBody)).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		if m.Status == 0 {
			delete(r.modes, parts[1])
		} else {
			r.modes[parts[1]] = &m
		}
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case parts[0] == "" || strings.HasPrefix(parts[0], "_"):
		http.NotFound(w, req)
	default:
		r.record(w, req, parts[0])
	}
}

// record stores the request in its bucket and answers it.
func (r *receiver) record(w http.ResponseWriter, req *http.Request, bucket string) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))
	status, location, retryAfter := http.StatusOK, "", ""
	r.mu.Lock()
	if m := r.modes[bucket]; m != nil {
		status, location, retryAfter = m.Status, m.Location, m.RetryAfter
		if m.Times > 0 {
			m.Times--
			if m.Times == 0 {
				delete(r.modes, bucket)
			}
		}
	}
	recs := r.records[bucket]
	recs = append(recs, record{
		Time: time.Now().UTC(), Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
		Headers: req.Header.Clone(), Body: string(body), Status: status,
	})
	if len(recs) > maxRecords {
		recs = recs[len(recs)-maxRecords:]
	}
	r.records[bucket] = recs
	r.mu.Unlock()
	if location != "" {
		w.Header().Set("Location", location)
	}
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, http.StatusText(status))
}
