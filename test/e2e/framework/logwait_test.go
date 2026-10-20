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
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// fakeLogServer is an API server with one controller pod whose log has
// lines, and whose first log read takes slow.
func fakeLogServer(t *testing.T, slow time.Duration, lines ...string) (*Env, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods/kp-0/log"):
			if reads.Add(1) == 1 && slow > 0 {
				select {
				case <-time.After(slow):
				case <-r.Context().Done():
				}
				return
			}
			_, _ = fmt.Fprint(w, strings.Join(lines, ""))
		case strings.HasSuffix(r.URL.Path, "/namespaces/"+ControllerNamespace+"/pods"):
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList",
				"items": []any{map[string]any{"metadata": map[string]any{"name": "kp-0", "namespace": ControllerNamespace},
					"status": map[string]any{"phase": "Running"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)
	return &Env{Kube: kube}, &reads
}

func logLine(at time.Time, msg string) string {
	return fmt.Sprintf("%s {\"level\":\"info\",\"message\":%q}\n", at.UTC().Format(time.RFC3339Nano), msg)
}

// TestLogWaitOutlastsASlowRead (#2172): a log wait whose deadline is shorter
// than one slow pod-log read still finds a line logged in time: the read is
// retried past the deadline, and the deadline applies to the line's own
// timestamp.
func TestLogWaitOutlastsASlowRead(t *testing.T) {
	since := time.Now().UTC()
	e, reads := fakeLogServer(t, 7*time.Second, logLine(since.Add(time.Second), "webhook received"))
	got := e.WaitControllerLog(t, since, 5*time.Second, "the line", LogMessage("webhook received"))
	assert.Equal(t, "webhook received", got.Message)
	assert.GreaterOrEqual(t, int(reads.Load()), 2, "read again after the slow read")
}

// TestLogWaitDeadlineIsTheLines: a matching line logged after the deadline
// fails the wait (core_test's "starts cancelling at once" stays a real
// assertion), and so do later lines that match nothing.
func TestLogWaitDeadlineIsTheLines(t *testing.T) {
	since := time.Now().UTC()
	deadline := since.Add(2 * time.Second)
	e, _ := fakeLogServer(t, 0, logLine(since.Add(time.Second), "other"), logLine(since.Add(3*time.Second), "webhook received"))
	_, err := e.ControllerLog(since).waitFor(deadline, 20*time.Second, LogMessage("webhook received"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after the deadline")

	e, _ = fakeLogServer(t, 0, logLine(since.Add(3*time.Second), "other"))
	_, err = e.ControllerLog(since).waitFor(deadline, 20*time.Second, LogMessage("webhook received"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "match nothing")

	e, _ = fakeLogServer(t, 0, logLine(since.Add(time.Second), "webhook received"))
	got, err := e.ControllerLog(since).waitFor(deadline, 20*time.Second, LogMessage("webhook received"))
	require.NoError(t, err)
	assert.Equal(t, "webhook received", got.Message)
}
