// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The OpenAPI spec (openapi.json, served at /api/v1/openapi.json and copied
// to docs/reference/openapi.json) is generated here from the route table
// below and the Go request and response types, with the field comments of
// their source as descriptions. Regenerate after changing either:
//
//	KARDINAL_UPDATE_OPENAPI=1 go test ./cmd/kardinal-controller -run TestOpenAPISpecIsUpToDate
//
// TestOpenAPIRoutesAreServed checks the table against the real handlers.

// apiServer is which listener serves a route.
type apiServer string

const (
	serverUI     apiServer = "ui"     // --ui-listen-address, :8082
	serverBundle apiServer = "bundle" // --webhook-bind-address, :8083
)

type apiParam struct {
	name, in, desc string
	required       bool
}

type apiRoute struct {
	method, path string
	server       apiServer
	id, summary  string
	desc         string
	tag          string
	params       []apiParam
	request      interface{}
	status       int
	response     interface{}
	errors       []int
	security     string // "", "uiAuth", "bundleToken", "none"
}

var nsQuery = apiParam{name: "namespace", in: "query",
	desc: "Namespace to look in. Empty finds the object in any namespace the caller may read; an ambiguous name is refused."}

// apiRoutes is every REST endpoint of the controller.
var apiRoutes = []apiRoute{
	{method: "GET", path: "/api/v1/ui/pipelines", server: serverUI, tag: "UI", id: "listPipelines",
		summary: "List Pipelines with their health, active Bundle and DORA metrics",
		status:  200, response: []uiPipelineResponse{}, errors: []int{401, 403, 500}, security: "uiAuth"},
	{method: "GET", path: "/api/v1/ui/pipelines/{pipeline}/bundles", server: serverUI, tag: "UI", id: "listPipelineBundles",
		summary: "List the Bundles of a Pipeline, newest first",
		params: []apiParam{{name: "pipeline", in: "path", required: true, desc: "Pipeline name."},
			{name: "namespace", in: "query", desc: "Namespace of the Pipeline. Empty lists the Pipeline's Bundles in every namespace the caller may read."}},
		status: 200, response: []uiBundleResponse{}, errors: []int{401, 403, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/bundles", server: serverUI, tag: "UI", id: "createBundleFromUI",
		summary: "Create an image Bundle (the UI's Create Bundle dialog)",
		desc:    "The requester (UI auth identity) is recorded in the kardinal.io/requested-by annotation.",
		request: uiCreateBundleRequest{}, status: 201, response: uiCreateBundleResponse{}, errors: []int{400, 401, 403, 404, 409, 500}, security: "uiAuth"},
	{method: "GET", path: "/api/v1/ui/bundles/{bundle}/graph", server: serverUI, tag: "UI", id: "getBundleGraph",
		summary: "The promotion DAG of a Bundle: its PromotionSteps and PolicyGates",
		params:  []apiParam{{name: "bundle", in: "path", required: true, desc: "Bundle name."}, nsQuery},
		status:  200, response: uiGraphResponse{}, errors: []int{401, 403, 404, 409, 500}, security: "uiAuth"},
	{method: "GET", path: "/api/v1/ui/bundles/{bundle}/steps", server: serverUI, tag: "UI", id: "listBundleSteps",
		summary: "The PromotionSteps of a Bundle, with per-step timings",
		params:  []apiParam{{name: "bundle", in: "path", required: true, desc: "Bundle name."}, nsQuery},
		status:  200, response: []uiStepResponse{}, errors: []int{401, 403, 404, 409, 500}, security: "uiAuth"},
	{method: "GET", path: "/api/v1/ui/gates", server: serverUI, tag: "UI", id: "listGates",
		summary: "List PolicyGates: templates and the instances of every Bundle",
		status:  200, response: []uiGateResponse{}, errors: []int{401, 403, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/gates/{gate}/approve", server: serverUI, tag: "UI", id: "overrideGate",
		summary: "Override a PolicyGate for a while (kardinal override)",
		desc:    "Adds a time-limited entry to spec.overrides with the requester as createdBy. The gate's namespace comes from the body.",
		params:  []apiParam{{name: "gate", in: "path", required: true, desc: "PolicyGate name."}},
		request: uiGateOverrideRequest{}, status: 200, response: uiMessageResponse{}, errors: []int{400, 401, 403, 404, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/gates/{namespace}/{gate}/approve", server: serverUI, tag: "UI", id: "overrideGateInNamespace",
		summary: "Override a PolicyGate in a namespace for a while",
		params: []apiParam{{name: "namespace", in: "path", required: true, desc: "PolicyGate namespace."},
			{name: "gate", in: "path", required: true, desc: "PolicyGate name."}},
		request: uiGateOverrideRequest{}, status: 200, response: uiMessageResponse{}, errors: []int{400, 401, 403, 404, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/promote", server: serverUI, tag: "UI", id: "promote",
		summary: "Promote the Bundle Verified upstream into an environment (kardinal promote)",
		request: uiPromoteRequest{}, status: 201, response: uiPromoteResponse{}, errors: []int{400, 401, 403, 404, 409, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/rollback", server: serverUI, tag: "UI", id: "rollback",
		summary: "Roll an environment back to an earlier Verified Bundle (kardinal rollback)",
		request: uiRollbackRequest{}, status: 201, response: uiRollbackResponse{}, errors: []int{400, 401, 403, 404, 409, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/pause", server: serverUI, tag: "UI", id: "pausePipeline",
		summary: "Pause a Pipeline (kardinal pause)",
		request: uiPipelineActionRequest{}, status: 200, response: uiMessageResponse{}, errors: []int{400, 401, 403, 404, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/resume", server: serverUI, tag: "UI", id: "resumePipeline",
		summary: "Resume a paused Pipeline (kardinal resume)",
		request: uiPipelineActionRequest{}, status: 200, response: uiMessageResponse{}, errors: []int{400, 401, 403, 404, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/ui/validate-cel", server: serverUI, tag: "UI", id: "validateCEL",
		summary: "Compile a PolicyGate CEL expression",
		request: uiValidateCELRequest{}, status: 200, response: uiValidateCELResponse{}, errors: []int{400, 401}, security: "uiAuth"},
	{method: "GET", path: "/api/v1/ui/steps/{namespace}/{step}/events", server: serverUI, tag: "UI", id: "listStepEvents",
		summary: "The Kubernetes Events of a PromotionStep, newest first",
		params: []apiParam{{name: "namespace", in: "path", required: true, desc: "PromotionStep namespace."},
			{name: "step", in: "path", required: true, desc: "PromotionStep name."}},
		status: 200, response: []uiEventResponse{}, errors: []int{400, 401, 403, 404, 500}, security: "uiAuth"},
	{method: "POST", path: "/api/v1/bundles", server: serverBundle, tag: "Bundle API", id: "createBundle",
		summary: "Create a Bundle from CI",
		desc: "Requires the Bundle API token (bundleAPI.tokenSecretRef). The same validation as kardinal create bundle; " +
			"at most 60 requests a minute.",
		request: bundleCreateRequest{}, status: 201, response: bundleCreateResponse{}, errors: []int{400, 401, 403, 404, 405, 409, 429, 500},
		security: "bundleToken"},
	{method: "GET", path: "/webhook/scm/health", server: serverBundle, tag: "SCM webhook", id: "webhookHealth",
		summary: "Whether SCM webhooks are configured, and how many were processed",
		status:  200, response: webhookHealthResponse{}, security: "none"},
	{method: "GET", path: openAPIPath, server: serverUI, tag: "Meta", id: "getOpenAPI",
		summary: "This OpenAPI description (also on the Bundle API listener)", status: 200, response: "openapi", security: "none"},
}

// schemaNames are the component names of the Go types.
var schemaNames = map[string]string{
	"uiPipelineResponse": "Pipeline", "uiEnvironmentNode": "EnvironmentNode", "uiBundleResponse": "Bundle",
	"uiBundleEnvStatus": "BundleEnvironment", "uiGraphResponse": "Graph", "uiGraphNode": "GraphNode",
	"uiGraphEdge": "GraphEdge", "uiStepResponse": "PromotionStep", "uiStepStatus": "StepStatus",
	"uiCondition": "Condition", "uiGateResponse": "PolicyGate", "uiGateOverride": "PolicyGateOverride",
	"uiEventResponse": "Event", "uiPromoteRequest": "PromoteRequest", "uiPromoteResponse": "PromoteResponse",
	"uiRollbackRequest": "RollbackRequest", "uiRollbackResponse": "RollbackResponse",
	"uiPipelineActionRequest": "PipelineActionRequest", "uiMessageResponse": "MessageResponse",
	"uiValidateCELRequest": "ValidateCELRequest", "uiValidateCELResponse": "ValidateCELResponse",
	"uiGateOverrideRequest": "GateOverrideRequest", "uiCreateBundleRequest": "UICreateBundleRequest",
	"uiCreateBundleResponse": "UICreateBundleResponse", "bundleCreateRequest": "CreateBundleRequest",
	"bundleCreateResponse": "CreateBundleResponse", "webhookHealthResponse": "WebhookHealth",
}

// fieldDocs maps "Type.Field" to the field's doc comment, from the source of
// this package and api/v1alpha1; typeDocs maps a type to its doc comment.
func loadDocs(t *testing.T) (fieldDocs, typeDocs map[string]string) {
	t.Helper()
	fieldDocs, typeDocs = map[string]string{}, map[string]string{}
	for _, dir := range []string{".", filepath.Join("..", "..", "api", "v1alpha1")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		fset := token.NewFileSet()
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(filepath.Base(f), "zz_") {
				continue
			}
			file, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
			require.NoError(t, err)
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts := spec.(*ast.TypeSpec)
					doc := ts.Doc
					if doc == nil && len(gd.Specs) == 1 {
						doc = gd.Doc
					}
					typeDocs[ts.Name.Name] = cleanDoc(doc.Text())
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, fl := range st.Fields.List {
						text := fl.Doc.Text()
						if text == "" {
							text = fl.Comment.Text()
						}
						for _, n := range fl.Names {
							fieldDocs[ts.Name.Name+"."+n.Name] = cleanDoc(text)
						}
					}
				}
			}
		}
	}
	return fieldDocs, typeDocs
}

var (
	issueRef  = regexp.MustCompile(`\s*\((#\d+[^)]*|[A-Z][0-9]{2}[a-z-]*-\d+)\)`)
	issueHead = regexp.MustCompile(`^#\d+:\s*`)
)

// cleanDoc keeps the prose of a comment: no kubebuilder markers, no issue
// references, one paragraph per blank line.
func cleanDoc(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "+") {
			continue
		}
		lines = append(lines, l)
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	out = issueHead.ReplaceAllString(out, "")
	out = issueRef.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "\n\n", "\x00")
	out = strings.ReplaceAll(out, "\n", " ")
	return strings.ReplaceAll(out, "\x00", "\n\n")
}

type specBuilder struct {
	fieldDocs, typeDocs map[string]string
	schemas             map[string]interface{}
}

var metaTime = reflect.TypeOf(metav1.Time{})

func (b *specBuilder) schema(t reflect.Type) map[string]interface{} {
	if t == metaTime {
		// A zero metav1.Time is encoded as null, even with omitempty.
		return map[string]interface{}{"type": []string{"string", "null"}, "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Ptr:
		return b.schema(t.Elem())
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	case reflect.Int, reflect.Int32:
		return map[string]interface{}{"type": "integer", "format": "int32"}
	case reflect.Int64:
		return map[string]interface{}{"type": "integer", "format": "int64"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}
	case reflect.Slice:
		// A nil slice is encoded as null.
		return map[string]interface{}{"type": []string{"array", "null"}, "items": b.schema(t.Elem())}
	case reflect.Map:
		return map[string]interface{}{"type": []string{"object", "null"}, "additionalProperties": b.schema(t.Elem())}
	case reflect.Struct:
		name := schemaNames[t.Name()]
		if name == "" {
			name = t.Name()
		}
		if _, done := b.schemas[name]; !done {
			b.schemas[name] = nil // reserve against recursion
			b.schemas[name] = b.object(t)
		}
		return map[string]interface{}{"$ref": "#/components/schemas/" + name}
	}
	panic("openapi: unsupported type " + t.String())
}

func (b *specBuilder) object(t reflect.Type) map[string]interface{} {
	props := map[string]interface{}{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		s := b.schema(f.Type)
		if doc := b.fieldDocs[t.Name()+"."+f.Name]; doc != "" {
			if _, ref := s["$ref"]; ref {
				s = map[string]interface{}{"allOf": []interface{}{s}, "description": doc}
			} else {
				s["description"] = doc
			}
		}
		props[name] = s
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}
	o := map[string]interface{}{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		o["required"] = required
	}
	if doc := b.typeDocs[t.Name()]; doc != "" {
		o["description"] = doc
	}
	return o
}

var statusText = map[int]string{
	400: "The request is invalid; the body says why.",
	401: "No valid credentials (Www-Authenticate: Bearer).",
	403: "The caller may not do this (TokenReview mode: Kubernetes RBAC denied it), or the namespace is not watched.",
	404: "The object does not exist.",
	405: "Method not allowed.",
	409: "Conflict: an ambiguous name, an object that already exists, or nothing to promote or roll back to.",
	429: "Rate limit exceeded.",
	500: "Internal error.",
}

func buildOpenAPI(t *testing.T) []byte {
	t.Helper()
	fd, td := loadDocs(t)
	b := &specBuilder{fieldDocs: fd, typeDocs: td, schemas: map[string]interface{}{}}
	paths := map[string]map[string]interface{}{}
	for _, r := range apiRoutes {
		op := map[string]interface{}{"operationId": r.id, "summary": r.summary, "tags": []string{r.tag}}
		if r.desc != "" {
			op["description"] = r.desc
		}
		if r.server == serverBundle {
			op["servers"] = []interface{}{map[string]interface{}{
				"url":         "http://{controller}:8083",
				"description": "The webhook and Bundle API listener (--webhook-bind-address).",
				"variables":   map[string]interface{}{"controller": map[string]interface{}{"default": "kardinal-promoter.kardinal-system.svc"}},
			}}
		}
		var params []interface{}
		for _, p := range r.params {
			params = append(params, map[string]interface{}{"name": p.name, "in": p.in, "required": p.required,
				"description": p.desc, "schema": map[string]interface{}{"type": "string"}})
		}
		if params != nil {
			op["parameters"] = params
		}
		if r.request != nil {
			op["requestBody"] = map[string]interface{}{"required": true, "content": map[string]interface{}{
				"application/json": map[string]interface{}{"schema": b.schema(reflect.TypeOf(r.request))}}}
		}
		var okSchema map[string]interface{}
		if r.response == "openapi" {
			okSchema = map[string]interface{}{"type": "object", "description": "An OpenAPI 3.1 document."}
		} else {
			okSchema = b.schema(reflect.TypeOf(r.response))
		}
		responses := map[string]interface{}{
			itoa(r.status): map[string]interface{}{"description": "OK", "content": map[string]interface{}{
				"application/json": map[string]interface{}{"schema": okSchema}}},
		}
		for _, code := range r.errors {
			responses[itoa(code)] = map[string]interface{}{"$ref": "#/components/responses/Error" + itoa(code)}
		}
		op["responses"] = responses
		switch r.security {
		case "uiAuth":
			op["security"] = []interface{}{map[string]interface{}{"uiToken": []string{}}, map[string]interface{}{}}
		case "bundleToken":
			op["security"] = []interface{}{map[string]interface{}{"bundleToken": []string{}}}
		default:
			op["security"] = []interface{}{}
		}
		if paths[r.path] == nil {
			paths[r.path] = map[string]interface{}{}
		}
		paths[r.path][strings.ToLower(r.method)] = op
	}
	errResponses := map[string]interface{}{}
	for code, text := range statusText {
		errResponses["Error"+itoa(code)] = map[string]interface{}{"description": text, "content": map[string]interface{}{
			"text/plain": map[string]interface{}{"schema": map[string]interface{}{"type": "string"}}}}
	}
	doc := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":   "kardinal-promoter REST API",
			"version": "v1",
			"description": "The UI API (/api/v1/ui/*, on the UI listener), the Bundle API for CI (POST /api/v1/bundles) and " +
				"the SCM webhook health endpoint (on the webhook listener). Authentication and RBAC: " +
				"https://pnz1990.github.io/kardinal-promoter/reference/rest-api/",
			"license": map[string]interface{}{"name": "Apache-2.0", "identifier": "Apache-2.0"},
		},
		"servers": []interface{}{map[string]interface{}{
			"url":         "http://{controller}:8082",
			"description": "The UI listener (--ui-listen-address). https:// when controller TLS is set.",
			"variables":   map[string]interface{}{"controller": map[string]interface{}{"default": "kardinal-promoter.kardinal-system.svc"}},
		}},
		"paths": paths,
		"components": map[string]interface{}{
			"schemas":   b.schemas,
			"responses": errResponses,
			"securitySchemes": map[string]interface{}{
				"uiToken": map[string]interface{}{"type": "http", "scheme": "bearer",
					"description": "The static UI token (ui.auth.tokenSecretRef) or, with ui.auth.tokenReview, a Kubernetes token " +
						"(kubectl create token) whose user Kubernetes RBAC authorizes for each request. With neither mode set, " +
						"the UI API takes no credentials and answers only loopback peers (kubectl port-forward)."},
				"bundleToken": map[string]interface{}{"type": "http", "scheme": "bearer",
					"description": "The Bundle API token (bundleAPI.tokenSecretRef, --bundle-api-token)."},
			},
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(doc))
	return buf.Bytes()
}

func itoa(i int) string { return strconv.Itoa(i) }

// TestOpenAPISpecIsUpToDate fails when openapi.json or
// docs/reference/openapi.json differs from what the route table and the Go
// types generate. KARDINAL_UPDATE_OPENAPI=1 rewrites both.
func TestOpenAPISpecIsUpToDate(t *testing.T) {
	want := buildOpenAPI(t)
	files := []string{"openapi.json", filepath.Join("..", "..", "docs", "reference", "openapi.json")}
	if os.Getenv("KARDINAL_UPDATE_OPENAPI") == "1" {
		for _, f := range files {
			require.NoError(t, os.WriteFile(f, want, 0o644))
		}
		return
	}
	for _, f := range files {
		got, err := os.ReadFile(f)
		require.NoError(t, err)
		require.Equal(t, string(want), string(got),
			"%s is stale: run KARDINAL_UPDATE_OPENAPI=1 go test ./cmd/kardinal-controller -run TestOpenAPISpecIsUpToDate", f)
	}
	require.Equal(t, string(want), string(openAPISpec), "the embedded spec is the generated one")
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(want, &doc), "valid JSON")
}

// TestOpenAPIRoutesAreServed sends every documented request to the real
// handlers: none is answered by the mux's own 404 or by 405, and every
// path the code registers is documented.
func TestOpenAPIRoutesAreServed(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	ui := http.NewServeMux()
	newUIAPIServer(c, zerolog.Nop()).RegisterRoutes(ui)
	bundle := http.NewServeMux()
	bundle.HandleFunc("/api/v1/bundles", newBundleAPIServer(c, "token", "default").Handler())
	bundle.HandleFunc("/webhook/scm/health", newWebhookServerWithConfig(nil, c, zerolog.Nop(), false).HealthHandler())
	bundle.HandleFunc(openAPIPath, handleOpenAPI)

	fill := strings.NewReplacer("{pipeline}", "p", "{bundle}", "b", "{gate}", "g", "{namespace}", "default", "{step}", "s")
	for _, r := range apiRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			mux := ui
			if r.server == serverBundle {
				mux = bundle
			}
			req := httptest.NewRequest(r.method, fill.Replace(r.path), strings.NewReader("{}"))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			require.NotEqual(t, http.StatusMethodNotAllowed, w.Code, w.Body.String())
			require.NotEqual(t, "404 page not found\n", w.Body.String(), "no handler for the documented path")
		})
	}

	documented := map[string]bool{}
	for _, r := range apiRoutes {
		documented[r.path] = true
	}
	pattern := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	for _, f := range []string{"ui_api.go", "main.go"} {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
			p := m[1]
			if p == "/webhook/scm" {
				continue // provider payloads, documented in docs/scm-providers.md
			}
			found := false
			for d := range documented {
				if d == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(d, p)) {
					found = true
				}
			}
			require.True(t, found, "%s registers %s, which apiRoutes does not document", f, p)
		}
	}
}

// TestRESTAPIDocsListEveryOperation: the endpoint table of
// docs/reference/rest-api.md lists exactly the documented operations.
func TestRESTAPIDocsListEveryOperation(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "rest-api.md"))
	require.NoError(t, err)
	row := regexp.MustCompile("(?m)^\\| `(GET|POST)` \\| `([^`]+)` \\|")
	var got, want []string
	for _, m := range row.FindAllStringSubmatch(string(doc), -1) {
		got = append(got, m[1]+" "+m[2])
	}
	for _, r := range apiRoutes {
		want = append(want, r.method+" "+r.path)
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}
