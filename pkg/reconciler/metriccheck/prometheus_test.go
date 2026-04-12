// Copyright 2026 The kardinal-promoter Authors.
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

package metriccheck_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

// prometheusResponse builds a Prometheus API JSON response for a vector result.
func prometheusVectorResponse(value float64) string {
	return fmt.Sprintf(`{
	  "status": "success",
	  "data": {
	    "resultType": "vector",
	    "result": [{"metric": {}, "value": [1609459200, "%g"]}]
	  }
	}`, value)
}

// prometheusScalarResponse builds a Prometheus API JSON response for a scalar result.
func prometheusScalarResponse(value float64) string {
	return fmt.Sprintf(`{
	  "status": "success",
	  "data": {
	    "resultType": "scalar",
	    "result": [1609459200, "%g"]
	  }
	}`, value)
}

// TestNewPrometheusProvider verifies the constructor returns a non-nil provider.
func TestNewPrometheusProvider(t *testing.T) {
	p := metriccheck.NewPrometheusProvider()
	require.NotNil(t, p)
	require.NotNil(t, p.HTTPClient)
}

// TestQueryScalar_VectorResult verifies that a single-series vector result is parsed correctly.
func TestQueryScalar_VectorResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/query", r.URL.Path)
		assert.Equal(t, "error_rate", r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, prometheusVectorResponse(0.01))
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	val, err := p.QueryScalar(context.Background(), srv.URL, "error_rate")
	require.NoError(t, err)
	assert.InDelta(t, 0.01, val, 1e-9)
}

// TestQueryScalar_ScalarResult verifies that a scalar resultType is parsed correctly.
func TestQueryScalar_ScalarResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, prometheusScalarResponse(42.5))
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	val, err := p.QueryScalar(context.Background(), srv.URL, "some_query")
	require.NoError(t, err)
	assert.InDelta(t, 42.5, val, 1e-9)
}

// TestQueryScalar_HTTPError verifies that a non-200 HTTP response returns an error.
func TestQueryScalar_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	_, err := p.QueryScalar(context.Background(), srv.URL, "bad_query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

// TestQueryScalar_PrometheusError verifies that a Prometheus error status is surfaced.
func TestQueryScalar_PrometheusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"error","error":"query timeout"}`)
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	_, err := p.QueryScalar(context.Background(), srv.URL, "slow_query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query timeout")
}

// TestQueryScalar_InvalidURL verifies that an unparseable URL returns an error.
func TestQueryScalar_InvalidURL(t *testing.T) {
	p := metriccheck.NewPrometheusProvider()
	_, err := p.QueryScalar(context.Background(), "://bad-url", "query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// TestQueryScalar_ContextCancelled verifies that a cancelled context returns an error.
func TestQueryScalar_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before the request is made

	p := metriccheck.NewPrometheusProvider()
	// Use localhost with an invalid port to ensure connection failure with a cancelled context.
	_, err := p.QueryScalar(ctx, "http://127.0.0.1:1", "query")
	require.Error(t, err)
}

// TestExtractScalar_EmptyVector verifies that an empty vector result returns an error.
func TestQueryScalar_EmptyVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	_, err := p.QueryScalar(context.Background(), srv.URL, "no_data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty vector")
}

// TestQueryScalar_MultipleSeriesError verifies that multiple series returns an error.
func TestQueryScalar_MultipleSeriesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
		  "status": "success",
		  "data": {
		    "resultType": "vector",
		    "result": [
		      {"metric": {"job": "a"}, "value": [1, "1.0"]},
		      {"metric": {"job": "b"}, "value": [1, "2.0"]}
		    ]
		  }
		}`)
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	_, err := p.QueryScalar(context.Background(), srv.URL, "multi_series")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 series")
}

// TestQueryScalar_UnsupportedResultType verifies that an unknown result type returns an error.
func TestQueryScalar_UnsupportedResultType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer srv.Close()

	p := metriccheck.NewPrometheusProvider()
	_, err := p.QueryScalar(context.Background(), srv.URL, "matrix_query")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}
