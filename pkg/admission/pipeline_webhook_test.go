// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package admission_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/admission"
)

// buildReview creates an AdmissionReview request wrapping the given Pipeline.
func buildReview(t *testing.T, pipeline *kardinalv1alpha1.Pipeline) []byte {
	t.Helper()
	raw, err := json.Marshal(pipeline)
	require.NoError(t, err)
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Request: &admissionv1.AdmissionRequest{
			UID:    "test-uid",
			Object: runtime.RawExtension{Raw: raw},
		},
	}
	body, err := json.Marshal(review)
	require.NoError(t, err)
	return body
}

func TestPipelineWebhookHandler_Admitted_NoDepends(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "no-deps"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}

	body := buildReview(t, pipeline)
	req := httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	log := zerolog.Nop()
	handler := admission.PipelineWebhookHandler(log)
	handler(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp admissionv1.AdmissionReview
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.True(t, resp.Response.Allowed, "expected admission allowed")
	assert.Equal(t, types.UID("test-uid"), resp.Response.UID)
}

func TestPipelineWebhookHandler_Admitted_ExplicitLinearDepends(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "explicit-linear"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat", DependsOn: []string{"test"}},
				{Name: "prod", DependsOn: []string{"uat"}},
			},
		},
	}

	body := buildReview(t, pipeline)
	req := httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	log := zerolog.Nop()
	handler := admission.PipelineWebhookHandler(log)
	handler(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp admissionv1.AdmissionReview
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.True(t, resp.Response.Allowed, "expected admission allowed for valid linear chain")
}

func TestPipelineWebhookHandler_Rejected_DirectCycle(t *testing.T) {
	// prod → uat, uat → prod: direct 2-node cycle
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "direct-cycle"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat", DependsOn: []string{"prod"}},
				{Name: "prod", DependsOn: []string{"uat"}},
			},
		},
	}

	body := buildReview(t, pipeline)
	req := httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	log := zerolog.Nop()
	handler := admission.PipelineWebhookHandler(log)
	handler(w, req)

	require.Equal(t, http.StatusOK, w.Code) // AdmissionReview always returns 200 HTTP

	var resp admissionv1.AdmissionReview
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.False(t, resp.Response.Allowed, "expected admission denied for cycle")
	assert.Contains(t, resp.Response.Result.Message, "circular", "error message should mention cycle")
	assert.Equal(t, types.UID("test-uid"), resp.Response.UID)
}

func TestPipelineWebhookHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/webhook/validate/pipeline", nil)
	w := httptest.NewRecorder()

	log := zerolog.Nop()
	handler := admission.PipelineWebhookHandler(log)
	handler(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// TestPipelineWebhookHandler_Operations covers C07-controller-20 (a, b):
// DELETE (no object) is allowed, and each ordering error is reported as
// itself instead of always as a cycle.
func TestPipelineWebhookHandler_Operations(t *testing.T) {
	pipe := func(envs ...kardinalv1alpha1.EnvironmentSpec) []byte {
		raw, err := json.Marshal(&kardinalv1alpha1.Pipeline{
			TypeMeta:   metav1.TypeMeta{APIVersion: "kardinal.io/v1alpha1", Kind: "Pipeline"},
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
		})
		require.NoError(t, err)
		return raw
	}
	tests := []struct {
		name        string
		op          admissionv1.Operation
		raw         []byte
		wantAllowed bool
		wantMsg     string
		notMsg      string
	}{
		{name: "delete without object is allowed", op: admissionv1.Delete, wantAllowed: true},
		{name: "valid create is allowed", op: admissionv1.Create,
			raw: pipe(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "prod"}), wantAllowed: true},
		{name: "unknown dependsOn is not called a cycle", op: admissionv1.Create,
			raw:     pipe(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"stagin"}}),
			wantMsg: "unknown environment", notMsg: "circular"},
		{name: "no environments is not called a cycle", op: admissionv1.Update, raw: pipe(),
			wantMsg: "no environments", notMsg: "circular"},
		{name: "cycle is reported as a cycle", op: admissionv1.Create,
			raw: pipe(kardinalv1alpha1.EnvironmentSpec{Name: "a", DependsOn: []string{"b"}},
				kardinalv1alpha1.EnvironmentSpec{Name: "b", DependsOn: []string{"a"}}),
			wantMsg: "circular"},
		{name: "create without object is denied", op: admissionv1.Create, wantMsg: "failed to decode Pipeline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := admissionv1.AdmissionReview{
				TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
				Request:  &admissionv1.AdmissionRequest{UID: "u", Operation: tt.op, Object: runtime.RawExtension{Raw: tt.raw}},
			}
			body, err := json.Marshal(review)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			admission.PipelineWebhookHandler(zerolog.Nop())(rec,
				httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, rec.Code)
			var out admissionv1.AdmissionReview
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			require.NotNil(t, out.Response)
			assert.Equal(t, tt.wantAllowed, out.Response.Allowed)
			if tt.wantMsg != "" {
				require.NotNil(t, out.Response.Result)
				assert.Contains(t, out.Response.Result.Message, tt.wantMsg)
			}
			if tt.notMsg != "" {
				assert.NotContains(t, out.Response.Result.Message, tt.notMsg)
			}
		})
	}
}

// TestPipelineWebhookHandler_OversizedBody covers the admission half of
// C07-controller-25: a body over 1 MB is rejected with 413, not truncated.
func TestPipelineWebhookHandler_OversizedBody(t *testing.T) {
	body := append(buildReview(t, &kardinalv1alpha1.Pipeline{}), bytes.Repeat([]byte(" "), 1<<20)...)
	rec := httptest.NewRecorder()
	admission.PipelineWebhookHandler(zerolog.Nop())(rec,
		httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(body)))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// E2E-R14: the optional webhook admits a Pipeline that sets a reserved,
// unimplemented field (the CRD accepts it) but warns with the same messages
// as "kardinal validate" and the Pipeline's Ready=False/NotImplemented.
func TestPipelineWebhookHandler_WarnsOnUnimplementedFields(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "reserved"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo", Layout: "branch"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test", Regions: []string{"us-east-1", "eu-west-1"}},
			},
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(buildReview(t, pipeline)))
	w := httptest.NewRecorder()
	admission.PipelineWebhookHandler(zerolog.Nop())(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp admissionv1.AdmissionReview
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.True(t, resp.Response.Allowed)
	require.Len(t, resp.Response.Warnings, 2)
	assert.Contains(t, resp.Response.Warnings[0], "spec.git.layout: branch is not implemented")
	assert.Contains(t, resp.Response.Warnings[1], "regions fan-out is not implemented")
}

// A git.secretRef in another namespace is refused on purpose (C03-promotionstep-18),
// so the webhook rejects it like any other invalid Pipeline instead of warning
// that it is "not implemented". The request namespace is used when the object
// does not carry one.
func TestPipelineWebhookHandler_DeniesCrossNamespaceSecretRef(t *testing.T) {
	tests := []struct {
		name        string
		objectNS    string
		requestNS   string
		secretNS    string
		wantAllowed bool
		wantMsg     string
	}{
		{name: "another namespace", objectNS: "team-a", requestNS: "team-a", secretNS: "kardinal-system",
			wantMsg: `git.secretRef.namespace "kardinal-system" is not allowed: the Secret must be in the Pipeline's namespace "team-a"`},
		{name: "namespace taken from the request", requestNS: "team-a", secretNS: "team-b",
			wantMsg: `git.secretRef.namespace "team-b" is not allowed`},
		{name: "the Pipeline's namespace", requestNS: "team-a", secretNS: "team-a", wantAllowed: true},
		{name: "no namespace", objectNS: "team-a", requestNS: "team-a", wantAllowed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: tc.objectNS},
				Spec: kardinalv1alpha1.PipelineSpec{
					Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/repo",
						SecretRef: &kardinalv1alpha1.SecretRef{Name: "github-token", Namespace: tc.secretNS}},
					Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
				},
			}
			raw, err := json.Marshal(pipeline)
			require.NoError(t, err)
			body, err := json.Marshal(admissionv1.AdmissionReview{
				TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
				Request: &admissionv1.AdmissionRequest{UID: "test-uid", Namespace: tc.requestNS,
					Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: raw}},
			})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			admission.PipelineWebhookHandler(zerolog.Nop())(w,
				httptest.NewRequest(http.MethodPost, "/webhook/validate/pipeline", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, w.Code)

			var resp admissionv1.AdmissionReview
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			assert.Equal(t, tc.wantAllowed, resp.Response.Allowed)
			assert.Empty(t, resp.Response.Warnings)
			if !tc.wantAllowed {
				require.NotNil(t, resp.Response.Result)
				assert.Contains(t, resp.Response.Result.Message, tc.wantMsg)
			}
		})
	}
}
