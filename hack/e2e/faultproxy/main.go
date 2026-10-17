// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command faultproxy is a reverse proxy the receipts-keyless suite puts in
// front of each sigstore service (components/sigstore.sh): the controller's
// Fulcio, Rekor and timestamp authority URLs point at it, and a test sets
// how each route answers: pass (proxy), down (the connection is closed
// before any answer), slow (proxy after a delay) or status (answer that
// status, such as 503 or 429), and reads how many requests each route
// answered, by status. Standard library only.
//
//	faultproxy -admin :9000 -route fulcio=:8081=http://fulcio.fulcio-system.svc.cluster.local ...
//
// Admin API: GET /routes (every route's mode and counts), POST
// /set?route=R&mode=pass|down|slow|status[&delay=30s][&code=503], POST
// /reset?route=R (zero the counts).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// route is one proxied upstream and how it answers now.
type route struct {
	Name   string         `json:"name"`
	Target string         `json:"target"`
	Mode   string         `json:"mode"`
	Delay  string         `json:"delay,omitempty"`
	Code   int            `json:"code,omitempty"`
	Counts map[string]int `json:"counts"` // by status ("200", "503", "down")

	mu    sync.Mutex
	delay time.Duration
	proxy *httputil.ReverseProxy
}

func (r *route) count(key string) {
	r.mu.Lock()
	r.Counts[key]++
	r.mu.Unlock()
}

// statusRecorder records the status the proxy answered.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (r *route) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	mode, delay, code := r.Mode, r.delay, r.Code
	r.mu.Unlock()
	switch mode {
	case "down":
		r.count("down")
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	case "status":
		r.count(strconv.Itoa(code))
		http.Error(w, fmt.Sprintf("faultproxy: %s answers %d", r.Name, code), code)
		return
	case "slow":
		select {
		case <-time.After(delay):
		case <-req.Context().Done():
			r.count("canceled")
			return
		}
	}
	rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
	r.proxy.ServeHTTP(rec, req)
	r.count(strconv.Itoa(rec.code))
}

func newRoute(spec string) (*route, string, error) {
	parts := strings.SplitN(spec, "=", 3)
	if len(parts) != 3 {
		return nil, "", fmt.Errorf("route %q: want name=listen=target", spec)
	}
	u, err := url.Parse(parts[2])
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("route %q: bad target", spec)
	}
	// The upstream routes by Host (Knative): send the target's.
	p := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(u)
		pr.Out.Host = u.Host
	}}
	return &route{Name: parts[0], Target: parts[2], Mode: "pass", Counts: map[string]int{}, proxy: p}, parts[1], nil
}

type routes []*route

func (rs routes) find(name string) *route {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	return nil
}

func (rs routes) admin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /routes", func(w http.ResponseWriter, _ *http.Request) {
		out := make([]map[string]interface{}, 0, len(rs))
		for _, r := range rs {
			r.mu.Lock()
			counts := map[string]int{}
			for k, v := range r.Counts {
				counts[k] = v
			}
			out = append(out, map[string]interface{}{"name": r.Name, "target": r.Target, "mode": r.Mode,
				"delay": r.Delay, "code": r.Code, "counts": counts})
			r.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /set", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		r := rs.find(q.Get("route"))
		if r == nil {
			http.Error(w, "no such route", http.StatusNotFound)
			return
		}
		mode := q.Get("mode")
		var delay time.Duration
		var code int
		switch mode {
		case "pass", "down":
		case "slow":
			d, err := time.ParseDuration(q.Get("delay"))
			if err != nil {
				http.Error(w, "slow needs delay", http.StatusBadRequest)
				return
			}
			delay = d
		case "status":
			c, err := strconv.Atoi(q.Get("code"))
			if err != nil || c < 400 || c > 599 {
				http.Error(w, "status needs a code in 400-599", http.StatusBadRequest)
				return
			}
			code = c
		default:
			http.Error(w, "mode is pass, down, slow or status", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.Mode, r.delay, r.Code, r.Delay = mode, delay, code, ""
		if delay > 0 {
			r.Delay = delay.String()
		}
		r.mu.Unlock()
		log.Printf("route %s: %s %s %d", r.Name, mode, delay, code)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, req *http.Request) {
		r := rs.find(req.URL.Query().Get("route"))
		if r == nil {
			http.Error(w, "no such route", http.StatusNotFound)
			return
		}
		r.mu.Lock()
		r.Counts = map[string]int{}
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

type specs []string

func (s *specs) String() string     { return strings.Join(*s, ",") }
func (s *specs) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	adminAddr := flag.String("admin", ":9000", "admin API address")
	var rs specs
	flag.Var(&rs, "route", "name=listen=target, repeated")
	flag.Parse()
	var all routes
	for _, spec := range rs {
		r, listen, err := newRoute(spec)
		if err != nil {
			log.Fatal(err)
		}
		all = append(all, r)
		srv := &http.Server{Addr: listen, Handler: r, ReadHeaderTimeout: 10 * time.Second}
		go func() { log.Fatal(srv.ListenAndServe()) }()
		log.Printf("route %s: %s -> %s", r.Name, listen, r.Target)
	}
	srv := &http.Server{Addr: *adminAddr, Handler: all.admin(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
