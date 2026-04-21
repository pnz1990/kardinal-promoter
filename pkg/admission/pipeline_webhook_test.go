// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
