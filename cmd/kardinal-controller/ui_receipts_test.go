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

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestUIAPI_StepReceipt: with receipts on, a Verified step names its
// PromotionReceipt and whether it is signed, for the stage drawer; with
// receipts off the API reads none.
//
// Covers RECEIPT-PR-02.
func TestUIAPI_StepReceipt(t *testing.T) {
	steps := []*v1alpha1.PromotionStep{
		{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod", Namespace: "default"},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "prod"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified", Receipt: "app-v1-prod"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-test", Namespace: "default"},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "test"},
			Status: v1alpha1.PromotionStepStatus{State: "Promoting"}},
	}
	rc := &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod", Namespace: "default"},
		Status: v1alpha1.PromotionReceiptStatus{Phase: v1alpha1.ReceiptStored}}
	get := func(receipts bool) map[string]*uiReceipt {
		c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(steps[0], steps[1], rc).Build()
		srv := newUIAPIServer(c, zerolog.Nop())
		srv.receipts = receipts
		mux := http.NewServeMux()
		srv.RegisterRoutes(mux)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/ui/bundles/app-v1/steps", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var resp []uiStepResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		out := map[string]*uiReceipt{}
		for _, s := range resp {
			out[s.Environment] = s.Receipt
		}
		return out
	}
	on := get(true)
	assert.Equal(t, &uiReceipt{Name: "app-v1-prod", Phase: "Stored", Verified: true}, on["prod"])
	assert.Nil(t, on["test"], "not Verified: no receipt yet")
	assert.Nil(t, get(false)["prod"], "receipts off")
}
