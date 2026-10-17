// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestLogWaitOutlastsASlowRead (#2172): a log wait shorter than one slow
// pod-log read still finds a line already written. The first read of the
// controller's log takes longer than the wait's 5s (an API server on a
// loaded host); the wait reads again, with at least minLogWait, and finds
// the line instead of timing out on that one read.
func TestLogWaitOutlastsASlowRead(t *testing.T) {
	written := time.Now().UTC()
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods/kp-0/log"):
			if reads.Add(1) == 1 {
				select { // the first read outlasts the caller's 5s wait
				case <-time.After(7 * time.Second):
				case <-r.Context().Done():
				}
				return
			}
			_, _ = fmt.Fprintf(w, "%s {\"level\":\"info\",\"message\":\"webhook received\"}\n", written.Add(time.Second).Format(time.RFC3339Nano))
		case strings.HasSuffix(r.URL.Path, "/namespaces/"+ControllerNamespace+"/pods"):
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList",
				"items": []any{map[string]any{"metadata": map[string]any{"name": "kp-0", "namespace": ControllerNamespace},
					"status": map[string]any{"phase": "Running"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{Kube: kube}
	start := time.Now()
	got := e.WaitControllerLog(t, written, 5*time.Second, "the line", LogMessage("webhook received"))
	assert.Equal(t, "webhook received", got.Message)
	assert.GreaterOrEqual(t, int(reads.Load()), 2, "read again after the slow read")
	assert.Less(t, time.Since(start), minLogWait, "found well within the floor")
}
