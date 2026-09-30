// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package admission_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/admission"
)

// TestBundleWebhookHandler: a Bundle is created only with an empty or
// absolute http(s) provenance.ciRunURL, the bundle API's rule; updates and
// deletes of existing Bundles are allowed whatever their ciRunURL.
func TestBundleWebhookHandler(t *testing.T) {
	bundle := func(prov *kardinalv1alpha1.BundleProvenance) []byte {
		raw, err := json.Marshal(&kardinalv1alpha1.Bundle{
			TypeMeta:   metav1.TypeMeta{APIVersion: "kardinal.io/v1alpha1", Kind: "Bundle"},
			ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
			Spec: kardinalv1alpha1.BundleSpec{
				Type: "image", Pipeline: "app",
				Images:     []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "1"}},
				Provenance: prov,
			},
		})
		require.NoError(t, err)
		return raw
	}
	withURL := func(u string) []byte {
		return bundle(&kardinalv1alpha1.BundleProvenance{CommitSHA: "abc", CIRunURL: u})
	}
	tests := []struct {
		name        string
		op          admissionv1.Operation
		raw         []byte
		wantAllowed bool
		wantMsg     string
	}{
		{name: "create without provenance", op: admissionv1.Create, raw: bundle(nil), wantAllowed: true},
		{name: "create without ciRunURL", op: admissionv1.Create, raw: withURL(""), wantAllowed: true},
		{name: "create with an https ciRunURL", op: admissionv1.Create,
			raw: withURL("https://github.com/o/r/actions/runs/1"), wantAllowed: true},
		{name: "create with a javascript ciRunURL", op: admissionv1.Create, raw: withURL("javascript:alert(1)"),
			wantMsg: "Bundle rejected: provenance.ciRunURL must be an absolute http or https URL"},
		{name: "create with a relative ciRunURL", op: admissionv1.Create, raw: withURL("/runs/1"),
			wantMsg: "Bundle rejected: provenance.ciRunURL must be an absolute http or https URL"},
		{name: "create with whitespace in ciRunURL", op: admissionv1.Create, raw: withURL("https://ci.example.com/1\n| x |"),
			wantMsg: "Bundle rejected: provenance.ciRunURL must not contain whitespace"},
		{name: "create with user info in ciRunURL", op: admissionv1.Create, raw: withURL("https://u:p@ci.example.com/1"),
			wantMsg: "Bundle rejected: provenance.ciRunURL must not contain user info"},
		{name: "create without object", op: admissionv1.Create, wantMsg: "failed to decode Bundle"},
		{name: "update of an existing bundle with an invalid ciRunURL", op: admissionv1.Update,
			raw: withURL("javascript:alert(1)"), wantAllowed: true},
		{name: "delete", op: admissionv1.Delete, wantAllowed: true},
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
			admission.BundleWebhookHandler(zerolog.Nop())(rec,
				httptest.NewRequest(http.MethodPost, "/webhook/validate/bundle", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, rec.Code)
			var out admissionv1.AdmissionReview
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			require.NotNil(t, out.Response)
			assert.Equal(t, types.UID("u"), out.Response.UID)
			assert.Equal(t, tt.wantAllowed, out.Response.Allowed)
			if tt.wantMsg != "" {
				require.NotNil(t, out.Response.Result)
				assert.Equal(t, int32(http.StatusBadRequest), out.Response.Result.Code)
				assert.Contains(t, out.Response.Result.Message, tt.wantMsg)
				assert.NotContains(t, out.Response.Result.Message, "u:p@", "the message must not echo the URL")
			}
		})
	}
}

// TestBundleWebhookHandler_Request: the Bundle webhook shares the Pipeline
// webhook's request handling.
func TestBundleWebhookHandler_Request(t *testing.T) {
	h := admission.BundleWebhookHandler(zerolog.Nop())

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/webhook/validate/bundle", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/webhook/validate/bundle", strings.NewReader(strings.Repeat("x", 1<<20+1))))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/webhook/validate/bundle", strings.NewReader(`{"kind":"AdmissionReview"}`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "nil request")
}
