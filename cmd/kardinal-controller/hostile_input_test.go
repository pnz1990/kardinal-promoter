// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// hostileTokens authenticates the callers' bearer tokens.
type hostileTokens map[string]authv1.UserInfo

func (h hostileTokens) Review(_ context.Context, token string) (*authv1.TokenReviewStatus, error) {
	u, ok := h[token]
	if !ok {
		return &authv1.TokenReviewStatus{}, nil
	}
	return &authv1.TokenReviewStatus{Authenticated: true, User: u}, nil
}

// The callers: an agent (also bounded by its AgentIdentity scope) and a
// person (bounded by RBAC alone), both with team-a's RBAC.
var hostileCallers = map[string]string{"agent-token": scenario.ActorIdentities["agent"].Username,
	"person-token": scenario.ActorIdentities["human"].Username}

// teamAccess grants everything in namespace team-a and nothing else: the
// agent's RBAC.
type teamAccess struct{}

func (teamAccess) Allowed(_ context.Context, _ authv1.UserInfo, a authzv1.ResourceAttributes) (bool, string, error) {
	if a.Namespace == "team-a" {
		return true, "", nil
	}
	return false, "only team-a", nil
}

// apiWorld is mcpWorld's cluster for the controller's HTTP surfaces: the UI
// API (the whole handler: TokenReview, RBAC, the body limit, CORS, the
// security headers) and the Bundle API (TokenReview), each writing as the
// controller and stamping for the agent caller, whose RBAC is team-a's.
func apiWorld(t *testing.T) (*scenario.World, http.Handler, http.Handler) {
	t.Helper()
	c := scenario.Combo{Update: "kustomize", Health: "deployment", Gate: "cel", Topology: "linear", Delivery: "direct",
		SCM: scenario.SCMGitHub, Outcome: "success", Actor: "human"}
	w := scenario.NewWorld(t, scenario.Options{SCM: c.SCM, PolicyNamespaces: []string{scenario.PolicyNS}, Actors: true})
	p := scenario.BuildPlan(c, "team-a", w.Now(), false)
	require.NoError(t, p.Setup(w))
	w.MustApply(scenario.AgentFixture("team-a")...)
	w.MustApply(p.Pipeline)
	other := p.Pipeline.DeepCopy()
	other.Namespace, other.ResourceVersion, other.UID = "team-b", "", ""
	w.MustApply(other)
	w.Round()
	b := scenario.ImageBundle("team-a", scenario.PipelineName, "1.1.0")
	scenario.ShipAs(t, w, "human", b)
	require.True(t, w.RunUntil(scenario.MaxRounds, func() bool { return w.BundlePhase("team-a")[b.Name] == "Verified" }))
	agent := scenario.ActorIdentities["agent"]
	human := scenario.ActorIdentities["human"]
	tokens := hostileTokens{"agent-token": {Username: agent.Username, Groups: agent.Groups},
		"person-token": {Username: human.Username, Groups: human.Groups}}
	actors := &actorWriter{source: &actor.Source{Reader: w.Client, Name: scenario.Release}}
	kube := w.ClientAs(scenario.ControllerInfo())
	ui := newUIHandler(kube, nil, nil, uiAuthConfig{tokens: tokens, access: teamAccess{}, accessCheck: teamAccess{},
		actors: actors, freezeControls: true}, "", nil, zerolog.Nop())
	bapi := newBundleAPIServer(kube, "static-token-not-used", "team-a")
	bapi.limiter = newTokenRateLimiter(1 << 30)
	bapi.actors = actors
	bapi.enableTokenReview(tokens, teamAccess{})
	return w, ui, bapi.Handler()
}

// apiValid is a valid value of each path, query and body field, by name.
var apiValid = map[string]any{
	"namespace": "team-a", "pipeline": "app", "environment": "prod", "bundle": "app-1-1-0", "gate": "cel-gate",
	"step": "app-app-1-1-0-prod", "reason": "scenario", "comment": "scenario", "decision": "approve", "image": "ghcr.io/acme/app:9.9.9",
	"author": "ci", "commitSHA": "abcdef1", "actors": "agents", "minutes": 10, "name": "kardinal-freeze-agents",
	"expression": "true", "stage": "", "expiresInMinutes": 10, "toBundle": "", "holdReason": "", "holdExpiresIn": "",
	"type": "image", "images": []any{map[string]any{"repository": "ghcr.io/acme/app", "tag": "9.9.9"}},
}

// apiOp is one operation of the API's OpenAPI document.
type apiOp struct {
	method, path string
	params       map[string]string // name -> in (path, query)
	body         map[string]string // property -> type
}

// apiOps reads the operations from the OpenAPI document both surfaces
// serve.
func apiOps(t *testing.T) []apiOp {
	t.Helper()
	raw, err := os.ReadFile("openapi.json")
	require.NoError(t, err)
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type any `json:"type"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	var ops []apiOp
	for path, methods := range doc.Paths {
		for method, rawOp := range methods {
			var op struct {
				Parameters []struct {
					Name string `json:"name"`
					In   string `json:"in"`
				} `json:"parameters"`
				RequestBody *struct {
					Content map[string]struct {
						Schema struct {
							Ref string `json:"$ref"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
			}
			if json.Unmarshal(rawOp, &op) != nil || !slices.Contains([]string{"get", "post"}, method) {
				continue
			}
			o := apiOp{method: strings.ToUpper(method), path: path, params: map[string]string{}, body: map[string]string{}}
			for _, p := range op.Parameters {
				if p.Name != "" {
					o.params[p.Name] = p.In
				}
			}
			if op.RequestBody != nil {
				ref := op.RequestBody.Content["application/json"].Schema.Ref
				for name, prop := range doc.Components.Schemas[ref[strings.LastIndex(ref, "/")+1:]].Properties {
					o.body[name] = fmt.Sprint(prop.Type)
				}
			}
			ops = append(ops, o)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].method+ops[i].path < ops[j].method+ops[j].path })
	return ops
}

// request builds op's request with the valid values, and field (a path or
// query parameter, or a body property) set to bad.
func (o apiOp) request(token, field string, bad *string) *http.Request {
	path := o.path
	q := url.Values{}
	for name, in := range o.params {
		v := fmt.Sprint(apiValid[name])
		if name == field && bad != nil {
			v = *bad
		}
		switch in {
		case "path":
			path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(v))
		case "query":
			q.Set(name, v)
		}
	}
	var body string
	if o.method == http.MethodPost {
		m := map[string]any{}
		for name, typ := range o.body {
			if v, ok := apiValid[name]; ok && (typ == "string" || name == "images" || name == "minutes" || name == "expiresInMinutes") {
				m[name] = v
			}
		}
		if bad != nil {
			if _, ok := o.body[field]; ok {
				m[field] = *bad
			}
		}
		raw, _ := json.Marshal(m)
		body = string(raw)
	}
	target := path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	r := httptest.NewRequest(o.method, target, strings.NewReader(body))
	r.Host = "localhost"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// TestAPI_HostileInputs: every operation of the UI API and the Bundle API,
// every path parameter, query parameter and string body field, every value
// of scenario.HostileInputs, and whole bodies that are malformed, too
// large, too deep, of the wrong types or with a field given twice, sent as
// an agent and as a person, whose RBAC is namespace team-a's, against the
// scenario World. No
// request gets a 5xx; no response is HTML; whatever a request writes is in
// team-a, and a Bundle, Approval or Verdict it creates names its caller.
// Covers API-HOSTILE-01.
func TestAPI_HostileInputs(t *testing.T) {
	w, ui, bundles := apiWorld(t)
	ops := apiOps(t)
	require.NotEmpty(t, ops)
	served := 0
	statuses := map[int]int{}
	var agent string // the caller of the request send makes
	snap := w.Snapshot()
	send := func(op apiOp, r *http.Request, what string) {
		t.Helper()
		h := ui
		if strings.HasPrefix(op.path, "/api/v1/bundles") {
			h = bundles
		}
		before, writes := snap, w.Writes()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		served++
		statuses[rec.Code]++
		assert.Less(t, rec.Code, 500, "%s: %d %.200s", what, rec.Code, rec.Body.String())
		if rec.Code/100 == 3 {
			// The mux's redirect to a cleaned path ("..", "//"): it stays in
			// the API.
			assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/api/"), "%s: redirected to %s", what, rec.Header().Get("Location"))
		} else {
			assert.NotContains(t, rec.Header().Get("Content-Type"), "html", "%s: an HTML answer", what)
		}
		var changed []string
		if w.Writes() != writes {
			snap = w.Snapshot()
			changed = scenario.Changed(before, snap)
		}
		assert.Empty(t, w.LongStrings(changed, 4096), "%s: stored text past 4 KiB", what)
		for _, k := range changed {
			parts := strings.SplitN(k, "/", 3)
			if parts[0] == "ChangeWindow" {
				// A freeze of the caller's own namespace, in its name.
				var cw v1alpha1.ChangeWindow
				if w.Store().Get(context.Background(), sigs_client.ObjectKey{Name: parts[2]}, &cw) == nil && assert.NotNil(t, cw.Spec.Freeze, "%s wrote ChangeWindow %s", what, parts[2]) {
					assert.Equal(t, []string{"team-a"}, cw.Spec.Freeze.Namespaces, "%s: a freeze beyond the caller's namespace", what)
					assert.Equal(t, agent, cw.Spec.Freeze.CreatedBy, "%s: a freeze in another's name", what)
				}
				continue
			}
			assert.Equal(t, "team-a", parts[1], "%s wrote %s: outside the caller's namespace", what, k)
			checkAPIStamp(t, w, what, parts, before[k] == "", agent)
		}
	}
	for _, token := range []string{"agent-token", "person-token"} {
		agent = hostileCallers[token]
		for _, op := range ops {
			if op.path == "/api/v1/openapi.json" || strings.HasPrefix(op.path, "/webhook/") {
				continue
			}
			fields := []string{}
			for name := range op.params {
				fields = append(fields, name)
			}
			for name, typ := range op.body {
				if typ == "string" {
					fields = append(fields, name)
				}
			}
			sort.Strings(fields)
			send(op, op.request(token, "", nil), op.method+" "+op.path+" (valid)")
			for _, f := range fields {
				for _, h := range scenario.HostileInputs() {
					v := h.Value
					send(op, op.request(token, f, &v), fmt.Sprintf("%s %s %s=%s(%.40q)", op.method, op.path, f, h.Kind, h.Value))
				}
			}
			if op.method != http.MethodPost {
				continue
			}
			for kind, body := range map[string]string{
				"malformed":  `{"namespace": "team-a", "pipeline":`,
				"too large":  `{"reason": "` + strings.Repeat("a", 2<<20) + `"}`,
				"too deep":   strings.Repeat("[", 100000) + strings.Repeat("]", 100000),
				"wrong type": `{"namespace": 5, "pipeline": ["app"], "environment": {"name": "prod"}, "images": "x"}`,
				"twice":      `{"namespace": "team-a", "pipeline": "app", "environment": "prod", "namespace": "team-b"}`,
				"not json":   "namespace=team-a&pipeline=app",
			} {
				r := op.request(token, "", nil)
				r.Body = httptestBody(body)
				r.ContentLength = int64(len(body))
				send(op, r, fmt.Sprintf("%s %s body %s", op.method, op.path, kind))
			}
		}
	}
	t.Logf("%d requests over %d operations; statuses %v", served, len(ops), statuses)
}

func httptestBody(s string) *readCloser { return &readCloser{strings.NewReader(s)} }

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

// checkAPIStamp checks a Bundle, Approval or Verdict a request created
// names the agent, as the UI and the Bundle API stamp their callers, and
// that a Bundle it rejected names the agent as who rejected it.
func checkAPIStamp(t *testing.T, w *scenario.World, what string, parts []string, created bool, agent string) {
	t.Helper()
	key := sigs_client.ObjectKey{Namespace: parts[1], Name: parts[2]}
	ctx := context.Background()
	var a *v1alpha1.Actor
	switch parts[0] {
	case "Bundle":
		var b v1alpha1.Bundle
		if w.Store().Get(ctx, key, &b) != nil {
			return
		}
		if !created {
			if b.Spec.Rejected != nil {
				assert.Equal(t, agent, b.Spec.Rejected.By, "%s: %s rejected in another's name", what, parts[2])
			}
			return
		}
		a = b.Spec.Actor
	case "Approval":
		var o v1alpha1.Approval
		if !created || w.Store().Get(ctx, key, &o) != nil {
			return
		}
		a = o.Spec.Actor
	default:
		return
	}
	if assert.NotNil(t, a, "%s: %s has no actor stamp", what, parts[2]) {
		assert.Equal(t, agent, a.Principal, "%s: %s names another actor", what, parts[2])
	}
}
