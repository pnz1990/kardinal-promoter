// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hack/e2e/testagent is the reference in-cluster verifier the live e2e suites
// run as a StepTemplate (hack/e2e/components/test-agent.sh,
// docs/agentic/verify-with-agents.md). It does what an AgentRun's agent must:
// read the run's context (KARDINAL_CONTEXT), test the environment, and write
// exactly one Verdict with its own ServiceAccount token.
//
// Its test: GET every Service of the target namespace (first port, path
// PROBE_PATH, default /) and expect a status below 500; and, when
// PROBE_FORBIDDEN is set, expect that URL to be unreachable (the AgentRun's
// NetworkPolicy blocks it). A Service that fails, or a forbidden URL that
// answers, is a high finding and the verdict is fail; otherwise pass.
// PROBE_RESULTS ("inconclusive,pass") overrides the result per attempt, for
// tests of the retry rule. PROBE_EXTRA set makes the agent first try a
// second, unbound Verdict, which admission must refuse (one vote per run),
// and report what happened as a finding. PROBE_DELAY (a duration) waits before probing:
// kindnet enforces a NetworkPolicy for a new Pod only about a second after
// the Pod gets its IP.
//
// Only the standard library, so it builds without downloading modules.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// runContext is the part of the AgentRun context the agent reads.
type runContext struct {
	TargetNamespace string                 `json:"targetNamespace"`
	Attempt         int                    `json:"attempt"`
	Verdict         map[string]interface{} `json:"verdict"`
}

type finding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

func main() {
	start := time.Now()
	ctxPath := os.Getenv("KARDINAL_CONTEXT")
	if ctxPath == "" {
		log.Fatal("KARDINAL_CONTEXT is not set: not run by an AgentRun")
	}
	raw, err := os.ReadFile(ctxPath)
	if err != nil {
		log.Fatalf("read context: %v", err)
	}
	var rc runContext
	if err := json.Unmarshal(raw, &rc); err != nil {
		log.Fatalf("parse context: %v", err)
	}
	api, err := newAPI()
	if err != nil {
		log.Fatalf("API client: %v", err)
	}
	if d, err := time.ParseDuration(os.Getenv("PROBE_DELAY")); err == nil && d > 0 {
		time.Sleep(d)
	}
	result, summary, findings := probe(api, rc)
	if r := forced(os.Getenv("PROBE_RESULTS"), rc.Attempt); r != "" {
		result, summary = r, fmt.Sprintf("%s (PROBE_RESULTS, attempt %d); %s", r, rc.Attempt, summary)
	}
	v := rc.Verdict
	spec, _ := v["spec"].(map[string]interface{})
	if spec == nil {
		log.Fatal("context has no verdict.spec")
	}
	spec["result"] = result
	spec["summary"] = truncate(summary, 1024)
	spec["findings"] = findings
	spec["startedAt"] = start.UTC().Format(time.RFC3339)
	spec["durationSeconds"] = int64(time.Since(start).Seconds())
	md, _ := v["metadata"].(map[string]interface{})
	ns, _ := md["namespace"].(string)
	if os.Getenv("PROBE_EXTRA") != "" {
		f := tryExtraVote(api, v, ns)
		spec["findings"] = append(spec["findings"].([]finding), f)
		if f.Severity == "high" {
			spec["result"], spec["summary"] = "fail", "a second vote was admitted; "+summary
		}
	}
	body, err := json.Marshal(v)
	if err != nil {
		log.Fatalf("marshal verdict: %v", err)
	}
	status, out, err := api.do(http.MethodPost, "/apis/kardinal.io/v1alpha1/namespaces/"+ns+"/verdicts", body)
	if err != nil || status >= 300 {
		log.Fatalf("write verdict: status %d: %v %s", status, err, out)
	}
	log.Printf("verdict %s: %s: %s", md["name"], result, summary)
}

// tryExtraVote tries to write a second Verdict, not bound to the run, and
// says whether admission refused it.
func tryExtraVote(api *apiClient, v map[string]interface{}, ns string) finding {
	extra, _ := json.Marshal(v)
	var cp map[string]interface{}
	_ = json.Unmarshal(extra, &cp)
	md := cp["metadata"].(map[string]interface{})
	md["name"] = fmt.Sprint(md["name"]) + "-extra"
	spec := cp["spec"].(map[string]interface{})
	delete(spec, "run")
	spec["result"], spec["summary"] = "pass", "a second vote"
	body, _ := json.Marshal(cp)
	status, out, err := api.do(http.MethodPost, "/apis/kardinal.io/v1alpha1/namespaces/"+ns+"/verdicts", body)
	if err == nil && status == http.StatusForbidden {
		return finding{Severity: "info", Title: "second vote refused"}
	}
	return finding{Severity: "high", Title: "second vote admitted", Detail: fmt.Sprintf("status %d %v %s", status, err, truncate(string(out), 200))}
}

// forced is the result PROBE_RESULTS names for attempt (1-based), or "".
func forced(spec string, attempt int) string {
	if spec == "" || attempt < 1 {
		return ""
	}
	parts := strings.Split(spec, ",")
	if attempt > len(parts) {
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return strings.TrimSpace(parts[attempt-1])
}

// probe tests the target namespace's Services and the forbidden URL.
func probe(api *apiClient, rc runContext) (string, string, []finding) {
	var findings []finding
	status, out, err := api.do(http.MethodGet, "/api/v1/namespaces/"+rc.TargetNamespace+"/services", nil)
	if err != nil || status != http.StatusOK {
		return "inconclusive", fmt.Sprintf("cannot list the Services of %s: status %d %v", rc.TargetNamespace, status, err), nil
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Spec     struct {
				Ports []struct{ Port int } `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return "inconclusive", "cannot read the Service list: " + err.Error(), nil
	}
	path := os.Getenv("PROBE_PATH")
	if path == "" {
		path = "/"
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	ok, failed := 0, 0
	for _, s := range list.Items {
		if len(s.Spec.Ports) == 0 {
			continue
		}
		url := fmt.Sprintf("http://%s.%s.svc:%d%s", s.Metadata.Name, rc.TargetNamespace, s.Spec.Ports[0].Port, path)
		resp, err := hc.Get(url)
		if err != nil {
			failed++
			findings = append(findings, finding{Severity: "high", Title: "unreachable: " + s.Metadata.Name, Detail: err.Error()})
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			failed++
			findings = append(findings, finding{Severity: "high", Title: fmt.Sprintf("%s answers %d", s.Metadata.Name, resp.StatusCode)})
			continue
		}
		ok++
	}
	if ok+failed == 0 {
		return "inconclusive", "no Service to test in " + rc.TargetNamespace, findings
	}
	summary := fmt.Sprintf("%d of %d Services in %s answer", ok, ok+failed, rc.TargetNamespace)
	if forbidden := os.Getenv("PROBE_FORBIDDEN"); forbidden != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, forbidden, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			failed++
			findings = append(findings, finding{Severity: "high", Title: "egress not blocked: " + forbidden,
				Detail: "status " + strconv.Itoa(resp.StatusCode)})
		} else {
			findings = append(findings, finding{Severity: "info", Title: "egress blocked: " + forbidden})
		}
		summary += "; egress outside the bounds blocked: " + strconv.FormatBool(err != nil)
	}
	if failed > 0 {
		return "fail", summary, findings
	}
	return "pass", summary, findings
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// apiClient calls the Kubernetes API with the Pod's ServiceAccount token.
type apiClient struct {
	base  string
	token string
	http  *http.Client
}

func newAPI() (*apiClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, fmt.Errorf("not in a cluster (no KUBERNETES_SERVICE_HOST)")
	}
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read ca: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &apiClient{base: "https://" + host + ":" + port, token: strings.TrimSpace(string(token)),
		http: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}}, nil
}

func (a *apiClient) do(method, path string, body []byte) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, a.base+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, err
}
