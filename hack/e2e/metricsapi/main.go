// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hack/e2e/metricsapi is the live e2e suites' stand-in for the metrics APIs
// MetricChecks query (hack/e2e/components/metrics-api.sh): Datadog, New
// Relic NerdGraph, CloudWatch GetMetricData and generic web (JSON) endpoints.
// It checks credentials the way the real services do (API keys, a SigV4
// signature) and answers with values a test sets, so the live tests run the
// real MetricCheck reconciler and gate path against it.
//
//	GET  /datadog/api/v1/query          DD-API-KEY, DD-APPLICATION-KEY
//	POST /newrelic/graphql              API-Key; variables.nrql is the query
//	POST /cloudwatch/                   SigV4 (service monitoring); the
//	                                    MetricDataQueries.member.1.Expression
//	                                    is the query
//	ANY  /web/<path>                    Authorization; answers the JSON
//	                                    document set for <path>
//
//	PUT  /_values/<provider>  {"query":"...","value":1.5} sets the value a
//	                          query returns (datadog, newrelic, cloudwatch);
//	                          "value": null removes it (the query returns no
//	                          data)
//	PUT  /_web/<path>         the body is the JSON document /web/<path> answers
//	GET  /_records/<provider> the provider's requests, oldest first, as JSON
//	GET  /_healthz            200
//
// Only the standard library, so it builds without downloading modules.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRecords = 2000
	maxBody    = 1 << 20
)

// creds are the credentials the fake services accept.
type creds struct {
	ddAPIKey, ddAppKey string
	nrAPIKey           string
	awsKeyID, awsKey   string
	webAuth            string
}

// record is one request as a fake service got it.
type record struct {
	Time   time.Time `json:"time"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	// Query is the metric query the request carried.
	Query string `json:"query"`
	// Auth is "ok" when the credentials were accepted, otherwise why not.
	Auth   string `json:"auth"`
	Status int    `json:"status"`
	// Header is the request headers that are not credentials.
	Header map[string]string `json:"header,omitempty"`
	Body   string            `json:"body,omitempty"`
}

type server struct {
	creds   creds
	mu      sync.Mutex
	values  map[string]map[string]float64 // provider → query → value
	docs    map[string][]byte             // web path → JSON document
	records map[string][]record
}

func main() {
	addr := flag.String("listen", ":8080", "listen address")
	c := creds{}
	flag.StringVar(&c.ddAPIKey, "datadog-api-key", "e2e-dd-api-key", "accepted DD-API-KEY")
	flag.StringVar(&c.ddAppKey, "datadog-app-key", "e2e-dd-app-key", "accepted DD-APPLICATION-KEY")
	flag.StringVar(&c.nrAPIKey, "newrelic-api-key", "e2e-nr-api-key", "accepted API-Key")
	flag.StringVar(&c.awsKeyID, "aws-access-key-id", "AKIDE2EKARDINAL", "accepted AWS access key ID")
	flag.StringVar(&c.awsKey, "aws-secret-access-key", "e2e-aws-secret", "secret key SigV4 signatures are checked with")
	flag.StringVar(&c.webAuth, "web-authorization", "Bearer e2e-web-token", "accepted web Authorization header")
	flag.Parse()
	s := newServer(c)
	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("metrics API listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

func newServer(c creds) *server {
	return &server{creds: c, values: map[string]map[string]float64{}, docs: map[string][]byte{},
		records: map[string][]record{}}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}
	switch parts[0] {
	case "_healthz":
		w.WriteHeader(http.StatusOK)
	case "_values":
		s.setValue(w, r, rest, body)
	case "_web":
		s.setDoc(w, r, rest, body)
	case "_records":
		s.mu.Lock()
		recs := append([]record{}, s.records[rest]...)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, recs)
	case "datadog":
		s.datadog(w, r)
	case "newrelic":
		s.newRelic(w, r, body)
	case "cloudwatch":
		s.cloudWatch(w, r, body)
	case "web":
		s.web(w, r, rest, body)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) setValue(w http.ResponseWriter, r *http.Request, provider string, body []byte) {
	var in struct {
		Query string   `json:"query"`
		Value *float64 `json:"value"`
	}
	if r.Method != http.MethodPut || json.Unmarshal(body, &in) != nil || in.Query == "" {
		http.Error(w, `PUT {"query":"...","value":1}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[provider] == nil {
		s.values[provider] = map[string]float64{}
	}
	if in.Value == nil {
		delete(s.values[provider], in.Query)
	} else {
		s.values[provider][in.Query] = *in.Value
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) setDoc(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if r.Method != http.MethodPut || !json.Valid(body) {
		http.Error(w, "PUT a JSON document", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.docs[path] = body
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// value returns the value set for a query.
func (s *server) value(provider, query string) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[provider][query]
	return v, ok
}

// record keeps a request; credential headers are left out.
func (s *server) record(provider string, r *http.Request, query, auth string, status int, body []byte) {
	h := map[string]string{}
	for k := range r.Header {
		switch strings.ToLower(k) {
		case "authorization", "dd-api-key", "dd-application-key", "api-key", "x-amz-security-token":
			continue
		}
		h[k] = r.Header.Get(k)
	}
	rec := record{Time: time.Now().UTC(), Method: r.Method, Path: r.URL.Path, Query: query, Auth: auth,
		Status: status, Header: h, Body: string(body)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[provider] = append(s.records[provider], rec)
	if n := len(s.records[provider]); n > maxRecords {
		s.records[provider] = s.records[provider][n-maxRecords:]
	}
}

func (s *server) datadog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("query")
	if r.URL.Path != "/datadog/api/v1/query" || r.Method != http.MethodGet {
		s.record("datadog", r, query, "", http.StatusNotFound, nil)
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"errors": []string{"Not found"}})
		return
	}
	if r.Header.Get("DD-API-KEY") != s.creds.ddAPIKey || r.Header.Get("DD-APPLICATION-KEY") != s.creds.ddAppKey {
		s.record("datadog", r, query, "bad API or application key", http.StatusForbidden, nil)
		writeJSON(w, http.StatusForbidden, map[string]interface{}{"errors": []string{"Forbidden"}})
		return
	}
	from, err1 := strconv.ParseInt(q.Get("from"), 10, 64)
	to, err2 := strconv.ParseInt(q.Get("to"), 10, 64)
	if err1 != nil || err2 != nil || from >= to {
		s.record("datadog", r, query, "ok", http.StatusBadRequest, nil)
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"errors": []string{"bad from/to"}})
		return
	}
	s.record("datadog", r, query, "ok", http.StatusOK, nil)
	series := []interface{}{}
	if v, ok := s.value("datadog", query); ok {
		series = append(series, map[string]interface{}{
			"metric": query, "pointlist": [][]interface{}{{from * 1000, v}, {to * 1000, v}, {to * 1000, nil}},
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "query": query, "series": series})
}

func (s *server) newRelic(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		Query     string `json:"query"`
		Variables struct {
			AccountID int64  `json:"accountId"`
			NRQL      string `json:"nrql"`
		} `json:"variables"`
	}
	_ = json.Unmarshal(body, &in)
	query := in.Variables.NRQL
	if r.Header.Get("API-Key") != s.creds.nrAPIKey {
		s.record("newrelic", r, query, "bad API key", http.StatusUnauthorized, body)
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"errors": []map[string]string{{"message": "Invalid API key"}}})
		return
	}
	if r.Method != http.MethodPost || !strings.Contains(in.Query, "nrql(query: $nrql)") || in.Variables.AccountID <= 0 {
		s.record("newrelic", r, query, "ok", http.StatusOK, body)
		writeJSON(w, http.StatusOK, map[string]interface{}{"errors": []map[string]string{{"message": "malformed NerdGraph request"}}})
		return
	}
	s.record("newrelic", r, query, "ok", http.StatusOK, body)
	results := []interface{}{}
	if v, ok := s.value("newrelic", query); ok {
		results = append(results, map[string]interface{}{"value": v})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{"actor": map[string]interface{}{
		"account": map[string]interface{}{"nrql": map[string]interface{}{"results": results}}}}})
}

func (s *server) cloudWatch(w http.ResponseWriter, r *http.Request, body []byte) {
	form, _ := url.ParseQuery(string(body))
	query := form.Get("MetricDataQueries.member.1.Expression")
	if why := verifySigV4(r, body, s.creds.awsKeyID, s.creds.awsKey, "monitoring"); why != "" {
		s.record("cloudwatch", r, query, why, http.StatusForbidden, nil)
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `<ErrorResponse><Error><Type>Sender</Type><Code>SignatureDoesNotMatch</Code>`+
			`<Message>%s</Message></Error></ErrorResponse>`, xmlEscape(why))
		return
	}
	if form.Get("Action") != "GetMetricData" || form.Get("Version") != "2010-08-01" || query == "" {
		s.record("cloudwatch", r, query, "ok", http.StatusBadRequest, nil)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `<ErrorResponse><Error><Code>InvalidAction</Code><Message>expected GetMetricData</Message></Error></ErrorResponse>`)
		return
	}
	s.record("cloudwatch", r, query, "ok", http.StatusOK, nil)
	id := form.Get("MetricDataQueries.member.1.Id")
	points := ""
	if v, ok := s.value("cloudwatch", query); ok {
		end := form.Get("EndTime")
		points = fmt.Sprintf(`<Timestamps><member>%s</member></Timestamps><Values><member>%s</member></Values>`,
			xmlEscape(end), strconv.FormatFloat(v, 'g', -1, 64))
	} else {
		points = `<Timestamps/><Values/>`
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = fmt.Fprintf(w, `<GetMetricDataResponse xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/">`+
		`<GetMetricDataResult><MetricDataResults><member><Id>%s</Id><Label>%s</Label><StatusCode>Complete</StatusCode>%s</member>`+
		`</MetricDataResults><Messages/></GetMetricDataResult></GetMetricDataResponse>`, xmlEscape(id), xmlEscape(id), points)
}

func (s *server) web(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if r.Header.Get("Authorization") != s.creds.webAuth {
		s.record("web", r, r.URL.RequestURI(), "bad Authorization", http.StatusUnauthorized, body)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	s.mu.Lock()
	doc, ok := s.docs[path]
	s.mu.Unlock()
	if !ok {
		s.record("web", r, r.URL.RequestURI(), "ok", http.StatusNotFound, body)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no document for " + path})
		return
	}
	s.record("web", r, r.URL.RequestURI(), "ok", http.StatusOK, body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}

// verifySigV4 checks an AWS Signature Version 4 Authorization header the way
// AWS does for a request without a query string. It returns "" when the
// signature is valid, otherwise why not.
func verifySigV4(r *http.Request, body []byte, keyID, secret, service string) string {
	auth := r.Header.Get("Authorization")
	const algo = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(auth, algo) {
		return "no SigV4 Authorization header"
	}
	fields := map[string]string{}
	for _, f := range strings.Split(strings.TrimPrefix(auth, algo), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
		fields[k] = v
	}
	scope := strings.Split(fields["Credential"], "/")
	if len(scope) != 5 || scope[4] != "aws4_request" || scope[3] != service {
		return "malformed credential scope"
	}
	if scope[0] != keyID {
		return "unknown access key ID"
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if len(amzDate) != 16 || amzDate[:8] != scope[1] {
		return "X-Amz-Date does not match the credential scope"
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	if !sort.StringsAreSorted(signed) {
		return "SignedHeaders not sorted"
	}
	var canonHeaders strings.Builder
	for _, h := range signed {
		var v string
		switch h {
		case "host":
			v = r.Host
		case "content-length":
			v = strconv.FormatInt(r.ContentLength, 10)
		default:
			v = strings.Join(strings.Fields(r.Header.Get(h)), " ")
		}
		canonHeaders.WriteString(h + ":" + v + "\n")
	}
	payload := sha256.Sum256(body)
	canonical := strings.Join([]string{r.Method, escapePath(r.URL.EscapedPath()), r.URL.RawQuery,
		canonHeaders.String(), fields["SignedHeaders"], hex.EncodeToString(payload[:])}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, strings.Join(scope[1:], "/"),
		hex.EncodeToString(sum[:])}, "\n")
	k := hmacSHA256([]byte("AWS4"+secret), scope[1])
	k = hmacSHA256(k, scope[2])
	k = hmacSHA256(k, scope[3])
	k = hmacSHA256(k, "aws4_request")
	want := hex.EncodeToString(hmacSHA256(k, stringToSign))
	if !hmac.Equal([]byte(want), []byte(fields["Signature"])) {
		return "signature does not match"
	}
	return ""
}

// escapePath escapes an already escaped path once more, as SigV4 does for
// every service but S3.
func escapePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
