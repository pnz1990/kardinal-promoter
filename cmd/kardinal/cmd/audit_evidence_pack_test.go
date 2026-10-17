// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// pager makes the fake client page as the API server does: it honours
// Limit and Continue (an offset here), which the fake client ignores, and
// records the largest page it returned. forbidden lists answer 403.
type pager struct {
	maxPage   int
	lists     int
	forbidden map[string]bool
}

func (pg *pager) funcs() interceptor.Funcs {
	return interceptor.Funcs{List: func(ctx context.Context, c sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
		lo := (&sigs_client.ListOptions{}).ApplyOptions(opts)
		gvk, err := c.GroupVersionKindFor(list)
		if err != nil {
			return err
		}
		kind := strings.TrimSuffix(gvk.Kind, "List")
		if pg.forbidden[kind] {
			return apierrors.NewForbidden(schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(kind) + "s"}, "", fmt.Errorf("no"))
		}
		limit, cont := lo.Limit, lo.Continue
		lo.Limit, lo.Continue = 0, ""
		if err := c.List(ctx, list, lo); err != nil {
			return err
		}
		pg.lists++
		if limit == 0 {
			return nil
		}
		items, err := apimeta.ExtractList(list)
		if err != nil {
			return err
		}
		from := 0
		if cont != "" {
			from, _ = strconv.Atoi(cont)
		}
		to := min(from+int(limit), len(items))
		if err := apimeta.SetList(list, items[from:to]); err != nil {
			return err
		}
		next := ""
		if to < len(items) {
			next = strconv.Itoa(to)
		}
		list.(metav1.ListInterface).SetContinue(next)
		pg.maxPage = max(pg.maxPage, to-from)
		return nil
	}}
}

func packScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// packWorld is a cluster with records in and out of the period
// [2026-07-01, 2026-10-01), in two namespaces.
func packWorld(t *testing.T, events int) []sigs_client.Object {
	t.Helper()
	at := func(s string) metav1.Time {
		tm, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return metav1.NewTime(tm)
	}
	var objs []sigs_client.Object
	bundle := func(ns, name, pipeline, created string) {
		objs = append(objs, &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: at(created)},
			Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline},
		})
	}
	bundle("pay", "app-v1", "app", "2026-06-15T10:00:00Z") // before the period
	bundle("pay", "app-v2", "app", "2026-07-15T10:00:00Z")
	bundle("pay", "web-v1", "web", "2026-08-01T10:00:00Z")
	bundle("ops", "app-v9", "app", "2026-09-01T10:00:00Z")
	for _, a := range []struct{ ns, bundle, env string }{{"pay", "app-v1", "prod"}, {"pay", "app-v2", "prod"}, {"pay", "app-v2", "uat"}, {"ops", "app-v9", "prod"}} {
		objs = append(objs, &v1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: a.bundle + "-" + a.env, CreationTimestamp: at("2026-09-02T00:00:00Z")},
			Spec:       v1alpha1.ApprovalSpec{Bundle: a.bundle, Environment: a.env, User: "alice", Decision: "approve"},
		})
		objs = append(objs, &v1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "gate-" + a.bundle + "-" + a.env, CreationTimestamp: at("2026-09-02T00:00:00Z"),
				Labels: map[string]string{"kardinal.io/bundle": a.bundle, "kardinal.io/environment": a.env, "kardinal.io/gate-template": "gate"}},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true"},
		})
		objs = append(objs, &v1alpha1.Verdict{
			ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "v-" + a.bundle + "-" + a.env, CreationTimestamp: at("2026-09-02T00:00:00Z")},
			Spec:       v1alpha1.VerdictSpec{Pipeline: "app", Bundle: a.bundle, Environment: a.env, Check: "smoke", Result: "pass", By: "ci"},
		})
	}
	objs = append(objs,
		&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "pay", Name: "gate", CreationTimestamp: at("2026-01-01T00:00:00Z")},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true"}},
		&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "pay", Name: "freeze-app", CreationTimestamp: at("2026-01-01T00:00:00Z")},
			Spec: v1alpha1.PolicyGateSpec{Expression: "false", Generated: true}},
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "pay", Name: "app", CreationTimestamp: at("2026-01-01T00:00:00Z")}},
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "pay", Name: "web", CreationTimestamp: at("2026-01-01T00:00:00Z")}},
		&v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "holidays", CreationTimestamp: at("2026-01-01T00:00:00Z")},
			Spec: v1alpha1.ChangeWindowSpec{Type: "blackout"}},
		&v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", CreationTimestamp: at("2026-01-01T00:00:00Z")}},
		&admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter-approvals",
			Labels: map[string]string{chartNameLabel: "kardinal-promoter"}}},
		&admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter-approvals",
			Labels: map[string]string{chartNameLabel: "kardinal-promoter"}}},
		&admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "someone-elses"}},
	)
	start := at("2026-06-20T00:00:00Z").Time
	for i := 0; i < events; i++ {
		ns, pl, env := "pay", "app", "prod"
		switch i % 4 {
		case 1:
			pl, env = "web", "uat"
		case 2:
			env = "uat"
		}
		if i%5 == 4 {
			ns = "ops"
		}
		objs = append(objs, &v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: fmt.Sprintf("ev-%04d", i)},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(start.Add(time.Duration(i) * 6 * time.Hour)),
				PipelineName: pl, Environment: env, Action: "PromotionSucceeded", Outcome: "Success"},
		})
	}
	return objs
}

// readPack extracts a pack into name -> content, in tar order.
func readPack(t *testing.T, b []byte) (map[string][]byte, []string) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	var order []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if h.Typeflag == tar.TypeDir {
			continue
		}
		c, err := io.ReadAll(tr)
		require.NoError(t, err)
		name := strings.TrimPrefix(h.Name, packDir)
		files[name] = c
		order = append(order, name)
	}
	return files, order
}

func packLines(b []byte) []map[string]interface{} {
	var out []map[string]interface{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]interface{}
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func names(recs []map[string]interface{}) []string {
	var out []string
	for _, r := range recs {
		if md, ok := r["metadata"].(map[string]interface{}); ok {
			ns, _ := md["namespace"].(string)
			out = append(out, fmt.Sprint(ns, "/", md["name"]))
		} else {
			out = append(out, fmt.Sprint(r["namespace"], "/", r["name"]))
		}
	}
	return out
}

func buildPack(t *testing.T, objs []sigs_client.Object, pg *pager, opts evidencePackOptions) ([]byte, string, error) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(packScheme(t)).WithObjects(objs...).WithInterceptorFuncs(pg.funcs()).Build()
	out := filepath.Join(t.TempDir(), "pack.tar.gz")
	opts.TempDir = t.TempDir()
	var stderr bytes.Buffer
	err := writeEvidencePack(context.Background(), c, opts, out, io.Discard, &stderr)
	if err != nil {
		_, statErr := os.Stat(out)
		assert.True(t, os.IsNotExist(statErr), "a failed export leaves no pack")
		return nil, stderr.String(), err
	}
	b, rerr := os.ReadFile(out)
	require.NoError(t, rerr)
	return b, stderr.String(), nil
}

var packPeriodOpts = evidencePackOptions{
	Since:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	Until:      time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	ExportedBy: "auditor@example.com",
}

// TestEvidencePack (spec 22 sections 4.1 to 4.6, #2050): the pack holds the
// period's records and no others, the versions bring their Approvals, gate
// instances and Verdicts, the manifest's and SHA256SUMS' hashes match the
// files, the List calls page (memory bounded by one page), and two exports
// of the same cluster are the same bytes.
//
// Covers EVIDENCE-PACK-01.
func TestEvidencePack(t *testing.T) {
	objs := packWorld(t, 1200)
	pg := &pager{}
	b, stderr, err := buildPack(t, objs, pg, packPeriodOpts)
	require.NoError(t, err)
	assert.Equal(t, auditExportPage, pg.maxPage, "every kind is read in pages of %d", auditExportPage)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}  .*pack\.tar\.gz \(14 files, \d+ records\)\n$`, stderr)
	sum := sha256.Sum256(b)
	assert.Contains(t, stderr, hex.EncodeToString(sum[:]), "the printed digest is the pack's")

	files, order := readPack(t, b)
	assert.Equal(t, []string{"manifest.json", "SHA256SUMS", "admission.yaml", "agentidentities.ndjson", "approvals.ndjson",
		"auditevents.ndjson", "auditevents.ocsf.ndjson", "bundles.ndjson", "changewindows.ndjson", "controls.json",
		"pipelines.ndjson", "policygate-templates.ndjson", "policygates.ndjson", "verdicts.ndjson"}, order)

	var m packManifest
	require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
	assert.Equal(t, packFormat, m.Format)
	assert.Equal(t, "auditor@example.com", m.ExportedBy)
	assert.Equal(t, packFilters{Namespace: "*"}, m.Filters)
	for _, f := range m.Files {
		s := sha256.Sum256(files[f.Name])
		assert.Equal(t, hex.EncodeToString(s[:]), f.SHA256, f.Name)
		assert.Equal(t, int64(len(files[f.Name])), f.Bytes, f.Name)
	}
	// SHA256SUMS is sha256sum -c input for every other file.
	sums := strings.Split(strings.TrimSpace(string(files["SHA256SUMS"])), "\n")
	assert.Len(t, sums, len(files)-1)
	for _, l := range sums {
		sum, name, ok := strings.Cut(l, "  ")
		require.True(t, ok, l)
		s := sha256.Sum256(files[name])
		assert.Equal(t, hex.EncodeToString(s[:]), sum, name)
	}

	// The period: events from 2026-07-01 (included) to 2026-10-01 (excluded).
	evs := packLines(files["auditevents.ndjson"])
	assert.Len(t, evs, 368, "92 days of events every 6 hours")
	for _, e := range evs {
		ts := e["spec"].(map[string]interface{})["timestamp"].(string)
		assert.True(t, ts >= "2026-07-01" && ts < "2026-10-01", ts)
	}
	assert.Len(t, packLines(files["auditevents.ocsf.ndjson"]), 368)
	assert.Equal(t, []string{"ops/app-v9", "pay/app-v2", "pay/web-v1"}, names(packLines(files["bundles.ndjson"])), "versions created in the period")
	assert.Equal(t, []string{"ops/app-v9-prod", "pay/app-v2-prod", "pay/app-v2-uat"}, names(packLines(files["approvals.ndjson"])),
		"the Approvals of the period's versions, not app-v1's")
	assert.Equal(t, []string{"ops/gate-app-v9-prod", "pay/gate-app-v2-prod", "pay/gate-app-v2-uat"}, names(packLines(files["policygates.ndjson"])))
	assert.Equal(t, []string{"ops/v-app-v9-prod", "pay/v-app-v2-prod", "pay/v-app-v2-uat"}, names(packLines(files["verdicts.ndjson"])))
	assert.Equal(t, []string{"pay/gate"}, names(packLines(files["policygate-templates.ndjson"])), "templates, not instances or generated gates")
	assert.Equal(t, []string{"pay/app", "pay/web"}, names(packLines(files["pipelines.ndjson"])))
	assert.Equal(t, []string{"/holidays"}, names(packLines(files["changewindows.ndjson"])))
	assert.Equal(t, []string{"/fixer"}, names(packLines(files["agentidentities.ndjson"])))
	bundle := packLines(files["bundles.ndjson"])[0]
	assert.Equal(t, "Bundle", bundle["kind"])
	admission := string(files["admission.yaml"])
	assert.Contains(t, admission, "kind: ValidatingAdmissionPolicy\n")
	assert.Contains(t, admission, "kind: ValidatingAdmissionPolicyBinding\n")
	assert.NotContains(t, admission, "someone-elses", "only the chart's policies")
	var controls struct {
		Frameworks []struct {
			ID string `json:"id"`
		} `json:"frameworks"`
	}
	require.NoError(t, json.Unmarshal(files["controls.json"], &controls))
	assert.NotEmpty(t, controls.Frameworks)

	// Coverage: the oldest of each kind the cluster holds, whatever the period.
	assert.Equal(t, time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), m.Coverage.Oldest["AuditEvent"])
	assert.Equal(t, time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC), m.Coverage.Oldest["Bundle"])
	assert.Empty(t, m.Coverage.Missing)
	assert.Empty(t, m.Coverage.Forbidden)
	assert.Empty(t, m.Coverage.Warnings, "the cluster holds AuditEvents from before the period")

	again, _, err := buildPack(t, objs, &pager{}, packPeriodOpts)
	require.NoError(t, err)
	assert.Equal(t, b, again, "a frozen cluster gives the same pack")
}

// TestEvidencePack_Filters: -n, --pipeline and --environment narrow the
// records; the procedure and policy kinds are filtered by namespace and
// pipeline only.
func TestEvidencePack_Filters(t *testing.T) {
	opts := packPeriodOpts
	opts.Namespace, opts.Pipeline, opts.Environment = "pay", "app", "prod"
	b, _, err := buildPack(t, packWorld(t, 400), &pager{}, opts)
	require.NoError(t, err)
	files, _ := readPack(t, b)
	for _, e := range packLines(files["auditevents.ndjson"]) {
		assert.Equal(t, "pay", e["namespace"])
		spec := e["spec"].(map[string]interface{})
		assert.Equal(t, "app", spec["pipelineName"])
		assert.Equal(t, "prod", spec["environment"])
	}
	assert.Equal(t, []string{"pay/app-v2"}, names(packLines(files["bundles.ndjson"])))
	assert.Equal(t, []string{"pay/app-v2-prod"}, names(packLines(files["approvals.ndjson"])))
	assert.Equal(t, []string{"pay/gate-app-v2-prod"}, names(packLines(files["policygates.ndjson"])))
	assert.Equal(t, []string{"pay/app"}, names(packLines(files["pipelines.ndjson"])))
	var m packManifest
	require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
	assert.Equal(t, packFilters{Namespace: "pay", Pipeline: "app", Environment: "prod"}, m.Filters)
}

// TestEvidencePack_Coverage: a kind the exporter may not read is listed in
// coverage.forbidden and its file left out, not written empty; a kind the
// cluster does not serve (an older chart) is listed in coverage.missing; a
// period starting before the oldest AuditEvent warns, and one ending before
// it fails with the export advice and writes nothing.
func TestEvidencePack_Coverage(t *testing.T) {
	t.Run("forbidden", func(t *testing.T) {
		b, stderr, err := buildPack(t, packWorld(t, 400), &pager{forbidden: map[string]bool{"Verdict": true, "ValidatingAdmissionPolicy": true}}, packPeriodOpts)
		require.NoError(t, err)
		files, _ := readPack(t, b)
		var m packManifest
		require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
		assert.Equal(t, []string{"ValidatingAdmissionPolicy", "Verdict"}, m.Coverage.Forbidden)
		assert.NotContains(t, files, "verdicts.ndjson")
		assert.NotContains(t, files, "admission.yaml")
		assert.Contains(t, stderr, "(12 files,")
		assert.Contains(t, stderr, "warning: you may not read some kinds in every namespace (-A)")
	})
	t.Run("an export that fails part way leaves no pack", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(packScheme(t)).WithObjects(packWorld(t, 100)...).
			WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
				if _, ok := list.(*v1alpha1.VerdictList); ok {
					return apierrors.NewServiceUnavailable("etcd is down")
				}
				return c.List(ctx, list, opts...)
			}}).Build()
		out := filepath.Join(t.TempDir(), "pack.tar.gz")
		opts := packPeriodOpts
		opts.TempDir = t.TempDir()
		var stdout bytes.Buffer
		err := writeEvidencePack(context.Background(), c, opts, out, &stdout, io.Discard)
		require.ErrorContains(t, err, "list Verdict")
		entries, rerr := os.ReadDir(filepath.Dir(out))
		require.NoError(t, rerr)
		assert.Empty(t, entries, "no pack and no partial file")
		require.Error(t, writeEvidencePack(context.Background(), c, opts, "", &stdout, io.Discard))
		assert.Zero(t, stdout.Len(), "nothing reaches standard output before every kind is read")
	})
	t.Run("missing", func(t *testing.T) {
		s := packScheme(t)
		older := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(older))
		metav1.AddToGroupVersion(older, v1alpha1.GroupVersion)
		for gvk, typ := range s.AllKnownTypes() {
			if gvk.Group == v1alpha1.GroupVersion.Group && !strings.HasPrefix(gvk.Kind, "Verdict") && !strings.HasPrefix(gvk.Kind, "AgentIdentity") {
				older.AddKnownTypeWithName(gvk, reflectNew(typ))
			}
		}
		var objs []sigs_client.Object
		for _, o := range packWorld(t, 100) {
			switch o.(type) {
			case *v1alpha1.Verdict, *v1alpha1.AgentIdentity:
				continue
			}
			objs = append(objs, o)
		}
		c := fake.NewClientBuilder().WithScheme(older).WithObjects(objs...).Build()
		opts := packPeriodOpts
		opts.TempDir = t.TempDir()
		var out bytes.Buffer
		require.NoError(t, writeEvidencePack(context.Background(), c, opts, "", &out, io.Discard))
		files, _ := readPack(t, out.Bytes())
		var m packManifest
		require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
		assert.Equal(t, []string{"AgentIdentity", "Verdict"}, m.Coverage.Missing)
	})
	t.Run("retention gap warns", func(t *testing.T) {
		opts := packPeriodOpts
		opts.Since = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		b, stderr, err := buildPack(t, packWorld(t, 400), &pager{}, opts)
		require.NoError(t, err)
		assert.Contains(t, stderr, "warning: the oldest AuditEvent the cluster holds is from 2026-06-20T00:00:00Z, after the period's start (2026-06-01T00:00:00Z)")
		files, _ := readPack(t, b)
		var m packManifest
		require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
		require.Len(t, m.Coverage.Warnings, 1)
	})
	t.Run("a period before the oldest AuditEvent fails", func(t *testing.T) {
		opts := packPeriodOpts
		opts.Since, opts.Until = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
		_, _, err := buildPack(t, packWorld(t, 400), &pager{}, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "before the oldest AuditEvent the cluster holds (2026-06-20T00:00:00Z)")
		assert.Contains(t, err.Error(), "Export continuously into your SIEM with kardinal audit export")
	})
}

// TestParsePackTime: a date, an RFC 3339 time, or a duration before now.
func TestParsePackTime(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 30, 45, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-07-01":                time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		"2026-07-01T09:00:00+02:00": time.Date(2026, 7, 1, 7, 0, 0, 0, time.UTC),
		"30d":                       now.Add(-30 * 24 * time.Hour),
		"12h":                       now.Add(-12 * time.Hour),
	} {
		got, err := parsePackTime(in, now)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := parsePackTime("last quarter", now)
	assert.ErrorContains(t, err, `"last quarter" is not a date`)
}

// reflectNew is a new object of a scheme's registered type.
func reflectNew(typ reflect.Type) runtime.Object {
	return reflect.New(typ).Interface().(runtime.Object)
}

// TestEvidencePackRecord: a record has its kind and no managedFields, and
// the object read is not changed.
func TestEvidencePackRecord(t *testing.T) {
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "v1", ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}}}
	r := record(b, "Bundle")
	assert.Nil(t, r.GetManagedFields())
	assert.Equal(t, v1alpha1.GroupVersion.WithKind("Bundle"), r.GetObjectKind().GroupVersionKind())
	assert.Len(t, b.ManagedFields, 1, "the listed object is not changed")
}
