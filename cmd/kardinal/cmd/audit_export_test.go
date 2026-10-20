// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func exportFixture(t *testing.T, now time.Time) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	ev := func(ns, name, pipeline, action, outcome string, age time.Duration, a *v1alpha1.Actor, req *v1alpha1.AuditRequest) *v1alpha1.AuditEvent {
		return &v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name),
				Labels: map[string]string{"kardinal.io/pipeline": pipeline}},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(now.Add(-age)), BundleName: "b1", PipelineName: pipeline,
				Environment: "prod", Action: action, Outcome: outcome, Message: name, Actor: a, Request: req},
		}
	}
	agent := &v1alpha1.Actor{Kind: "agent", Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice",
		Asserted: &v1alpha1.AssertedActor{Model: "m1"}}
	human := &v1alpha1.Actor{Kind: "human", Principal: "bob", Via: "ui"}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(
		ev("team-a", "approved", "checkout", "ApprovalRecorded", "Success", time.Hour, agent, nil),
		ev("team-a", "paused", "checkout", "APIWrite", "Success", 2*time.Hour, human,
			&v1alpha1.AuditRequest{Route: "/api/v1/ui/pause", Method: "POST"}),
		ev("team-a", "started", "checkout", "PromotionStarted", "Pending", 3*time.Hour, nil, nil),
		ev("team-a", "old", "checkout", "PromotionStarted", "Success", 72*time.Hour, nil, nil),
		ev("team-a", "other", "billing", "PromotionFailed", "Failure", time.Hour, nil, nil),
		ev("team-b", "elsewhere", "checkout", "PromotionStarted", "Success", time.Hour, nil, nil),
	).Build()
}

func lines(t *testing.T, out *bytes.Buffer) []map[string]interface{} {
	t.Helper()
	var recs []map[string]interface{}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		recs = append(recs, m)
	}
	return recs
}

// TestAuditExport_JSON: one line per AuditEvent in the window and the
// namespace (all with ""), filtered by pipeline, with the actor.
func TestAuditExport_JSON(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := exportFixture(t, now)
	for _, tc := range []struct {
		name, ns, pipeline string
		want               []string
	}{
		{name: "namespace", ns: "team-a", want: []string{"approved", "other", "paused", "started"}},
		{name: "pipeline", ns: "team-a", pipeline: "checkout", want: []string{"approved", "paused", "started"}},
		{name: "all namespaces", ns: "", pipeline: "checkout", want: []string{"approved", "elsewhere", "paused", "started"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			n, err := auditExportFn(context.Background(), &out, c, tc.ns, tc.pipeline, "json", now.Add(-24*time.Hour))
			require.NoError(t, err)
			recs := lines(t, &out)
			assert.Equal(t, len(tc.want), n)
			var names []string
			for _, r := range recs {
				names = append(names, r["name"].(string))
			}
			assert.ElementsMatch(t, tc.want, names)
		})
	}
	var out bytes.Buffer
	_, err := auditExportFn(context.Background(), &out, c, "team-a", "checkout", "json", now.Add(-90*time.Minute))
	require.NoError(t, err)
	recs := lines(t, &out)
	require.Len(t, recs, 1)
	assert.Equal(t, "uid-approved", recs[0]["uid"])
	spec := recs[0]["spec"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"kind": "agent", "principal": "system:serviceaccount:ci:fixer", "agent": "fixer",
		"sponsor": "alice", "asserted": map[string]interface{}{"model": "m1"}}, spec["actor"])
}

// TestAuditExport_OCSF: the OCSF 1.3.0 API Activity fields, the actor
// mapping (principal in actor.user, agent in actor.app_name), and the
// kardinal fields OCSF has no place for under unmapped.kardinal.
func TestAuditExport_OCSF(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := exportFixture(t, now)
	var out bytes.Buffer
	_, err := auditExportFn(context.Background(), &out, c, "team-a", "checkout", "ocsf", now.Add(-24*time.Hour))
	require.NoError(t, err)
	by := map[string]map[string]interface{}{}
	for _, r := range lines(t, &out) {
		by[r["message"].(string)] = r
	}
	require.Len(t, by, 3)

	approved := by["approved"]
	for k, v := range map[string]interface{}{"class_uid": 6003.0, "category_uid": 6.0, "activity_id": 1.0, "type_uid": 600301.0,
		"status_id": 1.0, "severity_id": 1.0, "time": float64(now.Add(-time.Hour).UnixMilli())} {
		assert.Equal(t, v, approved[k], k)
	}
	assert.Equal(t, map[string]interface{}{"app_name": "fixer",
		"user": map[string]interface{}{"name": "system:serviceaccount:ci:fixer", "type_id": 99.0, "type": "Agent"}}, approved["actor"])
	md := approved["metadata"].(map[string]interface{})
	assert.Equal(t, "1.3.0", md["version"])
	assert.Equal(t, "kardinal-promoter", md["product"].(map[string]interface{})["name"])
	assert.Equal(t, "ApprovalRecorded", approved["api"].(map[string]interface{})["operation"])
	assert.Len(t, approved["resources"], 3)
	unm := approved["unmapped"].(map[string]interface{})["kardinal"].(map[string]interface{})
	assert.Equal(t, "alice", unm["actor"].(map[string]interface{})["sponsor"])
	assert.Equal(t, map[string]interface{}{"model": "m1"}, unm["actor"].(map[string]interface{})["asserted"])

	paused := by["paused"]
	assert.Equal(t, 3.0, paused["activity_id"], "a pause is an update")
	assert.Equal(t, map[string]interface{}{"http_method": "POST", "url": map[string]interface{}{"path": "/api/v1/ui/pause"}},
		paused["http_request"])
	assert.Equal(t, "User", paused["actor"].(map[string]interface{})["user"].(map[string]interface{})["type"])

	started := by["started"]
	assert.Equal(t, 99.0, started["activity_id"])
	assert.Equal(t, 99.0, started["status_id"], "Pending is Other")
	assert.Equal(t, "Unknown", started["actor"].(map[string]interface{})["user"].(map[string]interface{})["type"],
		"a record without an actor")
	_, ok := started["http_request"]
	assert.False(t, ok)
}

func TestAuditExport_Flags(t *testing.T) {
	keepStyle(t)
	cmd := newAuditExportCmd()
	cmd.SetArgs([]string{"--format", "csv"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	assert.ErrorContains(t, cmd.Execute(), "--format is json or ocsf")
	cmd = newAuditExportCmd()
	cmd.SetArgs([]string{"--since", "-1h"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	assert.ErrorContains(t, cmd.Execute(), "invalid --since")
}
