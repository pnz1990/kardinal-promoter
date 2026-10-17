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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
	assert.Regexp(t, `^sha256:[0-9a-f]{64}  .*pack\.tar\.gz \(15 files, \d+ records\)\n$`, stderr)
	sum := sha256.Sum256(b)
	assert.Contains(t, stderr, hex.EncodeToString(sum[:]), "the printed digest is the pack's")

	files, order := readPack(t, b)
	assert.Equal(t, []string{"manifest.json", "SHA256SUMS", "admission.yaml", "agentidentities.ndjson", "approvals.ndjson",
		"auditevents.ndjson", "auditevents.ocsf.ndjson", "bundles.ndjson", "changewindows.ndjson", "controls.json",
		"pipelines.ndjson", "policygate-templates.ndjson", "policygates.ndjson", "receipts.ndjson", "verdicts.ndjson"}, order)

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
		assert.Contains(t, stderr, "(13 files,")
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
	r := (&evidence{}).record(b, "Bundle")
	assert.Nil(t, r.GetManagedFields())
	assert.Equal(t, v1alpha1.GroupVersion.WithKind("Bundle"), r.GetObjectKind().GroupVersionKind())
	assert.Len(t, b.ManagedFields, 1, "the listed object is not changed")
}

// redactProbe is the credential probe of #2127 (QA-2): secrets in a
// Pipeline's git URL, kubectl's last-applied annotation, a hook Job's env,
// command, args and probe, a version's CI run URL and an AuditEvent
// message.
type redactProbe struct {
	objs    []sigs_client.Object
	secrets []string
	command []string
	args    []string
	probe   []string
}

func newRedactProbe(t *testing.T) redactProbe {
	t.Helper()
	const (
		token  = "ghp_SECRETTOKEN123"
		apiKey = "sk-live-APIKEY456"
		param  = "QUERYSECRET789"
	)
	p := redactProbe{
		secrets: []string{token, apiKey, param, "FLAGTOKEN1", "FLAGKEY2", "FLAGPASSWORD3", "BEARERTOKEN4abc", "GHTOKEN5", "EXPORTKEY6",
			"PASSPHRASE7", "PWD8", "AUTH9", "BEARERFLAG10", "PAT11", "PROBEPW12", "USERPASS13", "XAUTH14", "PRIV15", "DBPASS16"},
		command: []string{"sh", "-c", "GITHUB_TOKEN=GHTOKEN5 curl --token=FLAGTOKEN1 -H 'X: 1' --api-key 'FLAGKEY2' https://example.com; " +
			"export API_KEY=EXPORTKEY6; DB_PASS=DBPASS16 run --path /etc --author bob"},
		args: []string{"--password", "FLAGPASSWORD3", "--verbose", "--name", "smoke", "-H", "Authorization: Bearer BEARERTOKEN4abc",
			"--passphrase", "PASSPHRASE7", "--pwd=PWD8", "--auth", "AUTH9", "--bearer", "BEARERFLAG10", "--github-pat", "PAT11",
			"-u", "admin:USERPASS13", "-H", "X-Auth-Token: XAUTH14", "-H", "PRIVATE-TOKEN: PRIV15"},
		probe: []string{"check", "--db-password=PROBEPW12"},
	}
	url := "https://x-access-token:" + token + "@github.com/org/gitops.git"
	list := func(l []string) string { b, _ := json.Marshal(l); return string(b) }
	p.objs = packWorld(t, 100)
	for _, o := range p.objs {
		switch v := o.(type) {
		case *v1alpha1.Pipeline:
			if v.Name != "app" {
				continue
			}
			v.Spec.Git = v1alpha1.PipelineGit{URL: url}
			v.Annotations = map[string]string{lastAppliedAnnotation: `{"spec":{"git":{"url":"` + url + `"}}}`, "team": "payments"}
			v.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "prod", Hooks: []v1alpha1.HookSpec{{Name: "smoke", Phase: "post",
				Job: runtime.RawExtension{Raw: []byte(`{"template":{"spec":{"containers":[{"name":"t","image":"curl",` +
					`"command":` + list(p.command) + `,"args":` + list(p.args) + `,` +
					`"livenessProbe":{"exec":{"command":` + list(p.probe) + `}},` +
					`"env":[{"name":"API_KEY","value":"` + apiKey + `"},{"name":"FROM_SECRET","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]}]}}}`)}}}}}
		case *v1alpha1.Bundle:
			if v.Name == "app-v2" {
				v.Spec.Provenance = &v1alpha1.BundleProvenance{CIRunURL: "https://ci.example.com/run/1?token=" + param}
			}
		case *v1alpha1.AuditEvent:
			if v.Name == "ev-0050" {
				v.Spec.Message = "pushed to " + url
			}
		}
	}
	return p
}

// export builds the probe's pack and returns its files, the app Pipeline's
// hook container and the manifest.
func (p redactProbe) export(t *testing.T, includeCommands bool) (map[string][]byte, map[string]interface{}, map[string]interface{}, packManifest) {
	t.Helper()
	opts := packPeriodOpts
	opts.IncludeCommands = includeCommands
	b, _, err := buildPack(t, p.objs, &pager{}, opts)
	require.NoError(t, err)
	files, _ := readPack(t, b)
	var app map[string]interface{}
	for _, pl := range packLines(files["pipelines.ndjson"]) {
		if pl["metadata"].(map[string]interface{})["name"] == "app" {
			app = pl
		}
	}
	require.NotNil(t, app)
	hook := app["spec"].(map[string]interface{})["environments"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0]
	container := hook.(map[string]interface{})["job"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0]
	var m packManifest
	require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
	return files, app, container.(map[string]interface{}), m
}

// TestEvidencePack_Redacts (#2127): the pack is handed to an auditor, so no
// credential leaves the cluster with it. By default container commands and
// arguments (probes included) are exported as their SHA-256 and length
// only, so no secret in them is exported whatever its form; the git URL
// keeps no userinfo, the last-applied annotation is gone, literal env
// values are <redacted> with their names kept, and the manifest says what
// it did.
//
// Covers EVIDENCE-PACK-REDACT-01.
func TestEvidencePack_Redacts(t *testing.T) {
	p := newRedactProbe(t)
	files, app, container, m := p.export(t, false)
	for name, content := range files {
		for _, secret := range p.secrets {
			assert.NotContains(t, string(content), secret, "%s holds a credential", name)
		}
		if name != "manifest.json" { // its redaction rules name the annotation
			assert.NotContains(t, string(content), lastAppliedAnnotation, "%s keeps the last-applied annotation", name)
		}
	}
	assert.Equal(t, "https://github.com/org/gitops.git", app["spec"].(map[string]interface{})["git"].(map[string]interface{})["url"],
		"the URL is kept without its userinfo")
	assert.Equal(t, map[string]interface{}{"team": "payments"}, app["metadata"].(map[string]interface{})["annotations"], "other annotations stay")
	assert.Equal(t, []interface{}{
		map[string]interface{}{"name": "API_KEY", "value": redactedValue},
		map[string]interface{}{"name": "FROM_SECRET", "valueFrom": map[string]interface{}{"secretKeyRef": map[string]interface{}{"name": "s", "key": "k"}}},
	}, container["env"], "the variable's name stays, its value goes; a reference to a Secret is kept")
	digest := func(l []string) map[string]interface{} {
		d := commandDigest(l)
		return map[string]interface{}{"sha256": d["sha256"], "length": float64(len(l))}
	}
	assert.Equal(t, digest(p.command), container["command"], "the command is its digest")
	assert.Equal(t, digest(p.args), container["args"])
	assert.Equal(t, digest(p.probe), container["livenessProbe"].(map[string]interface{})["exec"].(map[string]interface{})["command"], "a probe's command too")
	// The digest is the one printf '%s\0' ARG... | sha256sum gives.
	sum := sha256.Sum256([]byte(strings.Join(p.probe, "\x00") + "\x00"))
	assert.Equal(t, hex.EncodeToString(sum[:]), digest(p.probe)["sha256"])

	rules := (&redactor{}).rules()
	assert.Equal(t, packRedactions{Rules: rules, URLCredentials: 4, EnvValues: 1, LastApplied: 1, Commands: commandsDigest, CommandDigests: 3}, m.Redactions,
		"the git URL, the CI run URL, the AuditEvent message in auditevents.ndjson and its OCSF copy; command, args and the probe")
}

// TestEvidencePack_IncludeCommands (#2127, QA-2): --include-commands exports
// the commands and arguments with the values of credential-named flags and
// NAME=value assignments (export included) redacted, also in a shell
// string, and the Authorization header's token; --path and --author stay.
// The manifest records the mode.
func TestEvidencePack_IncludeCommands(t *testing.T) {
	p := newRedactProbe(t)
	files, _, container, m := p.export(t, true)
	for name, content := range files {
		for _, secret := range p.secrets {
			assert.NotContains(t, string(content), secret, "%s holds a credential", name)
		}
	}
	assert.Equal(t, []interface{}{"sh", "-c", "GITHUB_TOKEN=<redacted> curl --token=<redacted> -H 'X: 1' --api-key <redacted> https://example.com; " +
		"export API_KEY=<redacted>; DB_PASS=<redacted> run --path /etc --author bob"}, container["command"], "only the credentials go")
	assert.Equal(t, []interface{}{"--password", "<redacted>", "--verbose", "--name", "smoke", "-H", "Authorization: Bearer <redacted>",
		"--passphrase", "<redacted>", "--pwd=<redacted>", "--auth", "<redacted>", "--bearer", "<redacted>", "--github-pat", "<redacted>",
		"-u", "admin:<redacted>", "-H", "X-Auth-Token: <redacted>", "-H", "PRIVATE-TOKEN: <redacted>"}, container["args"])
	assert.Equal(t, commandsRedacted, m.Redactions.Commands)
	assert.Equal(t, 16, m.Redactions.FlagValues,
		"5 in the command (GITHUB_TOKEN, --token, --api-key, API_KEY, DB_PASS), 9 in args (6 flags, -u, 3 headers), the probe's --db-password")
	assert.Zero(t, m.Redactions.CommandDigests)
	assert.Equal(t, (&redactor{includeCommands: true}).rules(), m.Redactions.Rules)
	assert.Contains(t, strings.Join(m.Redactions.Rules, " "), "best-effort")
}

// TestRedactor_TypedEnv: a typed container's literal env value is replaced
// and its name kept; a valueFrom is kept.
func TestRedactor_TypedEnv(t *testing.T) {
	r := &redactor{}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Env: []corev1.EnvVar{
		{Name: "A", Value: "secret"}, {Name: "B", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "k"}}},
	}}}}}
	r.object(pod)
	assert.Equal(t, []corev1.EnvVar{{Name: "A", Value: redactedValue},
		{Name: "B", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "k"}}}}, pod.Spec.Containers[0].Env)
	assert.Equal(t, 1, r.counts.EnvValues)
}

// TestRedactor_Args (--include-commands): the value of a flag or a
// NAME=value assignment whose name says it is a credential is redacted, in
// --flag=value and --flag value forms, with or without export, and inside
// one shell string; other flags, names and values stay (--path, --author).
func TestRedactor_Args(t *testing.T) {
	for _, tc := range []struct {
		in, want []string
		n        int
	}{
		{[]string{"--token=abc", "--verbose"}, []string{"--token=<redacted>", "--verbose"}, 1},
		{[]string{"--password", "abc", "--user", "bob"}, []string{"--password", "<redacted>", "--user", "bob"}, 1},
		{[]string{"-api-key", "abc"}, []string{"-api-key", "<redacted>"}, 1},
		{[]string{"--GITHUB_TOKEN=abc"}, []string{"--GITHUB_TOKEN=<redacted>"}, 1},
		{[]string{"--client-secret", `"a b"`}, []string{"--client-secret", "<redacted>"}, 1},
		{[]string{"sh", "-c", "login --user=bob --passwd=abc && run --apikey 'x y'"},
			[]string{"sh", "-c", "login --user=bob --passwd=<redacted> && run --apikey <redacted>"}, 2},
		{[]string{"--key", "--verbose"}, []string{"--key", "--verbose"}, 0},
		{[]string{"sh", "-c", "run --key --verbose"}, []string{"sh", "-c", "run --key --verbose"}, 0},
		{[]string{"--ssh-key", "abc"}, []string{"--ssh-key", "<redacted>"}, 1},
		{[]string{"--credential"}, []string{"--credential"}, 0},
		{[]string{"-p", "abc", "deploy", "abc"}, []string{"-p", "abc", "deploy", "abc"}, 0},
		{[]string{"--passphrase", "x", "--pwd=y", "--auth", "z", "--bearer", "w", "--github-pat", "v", "--pat=u"},
			[]string{"--passphrase", "<redacted>", "--pwd=<redacted>", "--auth", "<redacted>", "--bearer", "<redacted>", "--github-pat", "<redacted>", "--pat=<redacted>"}, 6},
		{[]string{"--path", "/etc", "--author", "bob", "--patch", "p"}, []string{"--path", "/etc", "--author", "bob", "--patch", "p"}, 0},
		{[]string{"sh", "-c", "GITHUB_TOKEN=a run; export API_KEY=\"b c\"; HOME=/root x; export PATH=/bin"},
			[]string{"sh", "-c", "GITHUB_TOKEN=<redacted> run; export API_KEY=<redacted>; HOME=/root x; export PATH=/bin"}, 2},
		{[]string{"GH_PAT=abc", "AUTH_HEADER=def", "AUTHOR=me"}, []string{"GH_PAT=<redacted>", "AUTH_HEADER=<redacted>", "AUTHOR=me"}, 2},
		{[]string{"curl", "-u", "bob:hunter2", "--user=amy:pw", "-H", "Authorization: Bearer abcdefgh123", "-H", "Accept: application/json"},
			[]string{"curl", "-u", "bob:<redacted>", "--user=amy:<redacted>", "-H", "Authorization: Bearer <redacted>", "-H", "Accept: application/json"}, 3},
		{[]string{"sh", "-c", "curl -u bob:hunter2 -H 'PRIVATE-TOKEN: x1' -H 'X-Api-Key: x2' https://h; DB_PASS=x3 PASSIVE=1 run"},
			[]string{"sh", "-c", "curl -u bob:<redacted> -H 'PRIVATE-TOKEN: <redacted>' -H 'X-Api-Key: <redacted>' https://h; DB_PASS=<redacted> PASSIVE=1 run"}, 4},
		{[]string{"run", "--header", "Bearer eyJhbGciOiJIUzI1NiJ9.e30"}, []string{"run", "--header", "Bearer <redacted>"}, 1},
		{[]string{"echo", "Authorization: approved"}, []string{"echo", "Authorization: <redacted>"}, 1},
	} {
		r := &redactor{}
		got := append([]string(nil), tc.in...)
		r.args(got)
		assert.Equal(t, tc.want, got, "%q", tc.in)
		assert.Equal(t, tc.n, r.counts.FlagValues, "%q", tc.in)
	}
	r := &redactor{includeCommands: true}
	pod := &corev1.Pod{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "i", Command: []string{"app", "--token", "abc"}}}}}
	r.object(pod)
	assert.Equal(t, []string{"app", "--token", "<redacted>"}, pod.Spec.InitContainers[0].Command, "typed containers too")
	// By default a typed container's command and args are dropped: a
	// []string cannot hold a digest.
	r = &redactor{}
	pod = &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Command: []string{"app", "--token", "abc"}, Args: []string{"x"}}}}}
	r.object(pod)
	assert.Nil(t, pod.Spec.Containers[0].Command)
	assert.Nil(t, pod.Spec.Containers[0].Args)
	assert.Equal(t, 2, r.counts.CommandDigests)
}

// TestEvidencePackCmd_IncludeCommands: the command exports digests by
// default; --include-commands warns that its redaction is best-effort and
// the manifest says which mode ran.
func TestEvidencePackCmd_IncludeCommands(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(packScheme(t)).WithObjects(packWorld(t, 20)...).Build()
	setForTest(t, &newClient, func() (sigs_client.Client, string, error) { return c, "pay", nil })
	setForTest(t, &identityOf, func(context.Context, sigs_client.Client) (Identity, error) { return Identity{Username: "auditor"}, nil })
	setForTest(t, &globalNamespace, "")
	for _, tc := range []struct {
		args    []string
		mode    string
		warning bool
	}{
		{nil, commandsDigest, false},
		{[]string{"--include-commands"}, commandsRedacted, true},
	} {
		out := filepath.Join(t.TempDir(), "pack.tar.gz")
		var stderr bytes.Buffer
		keepStyle(t)
		root := NewRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(&stderr)
		root.SetArgs(append([]string{"audit", "evidence-pack", "--since", "2026-01-01", "--until", "2026-12-01", "-o", out}, tc.args...))
		require.NoError(t, root.Execute(), stderr.String())
		assert.Equal(t, tc.warning, strings.Contains(stderr.String(),
			"warning: --include-commands exports container commands and arguments; their redaction is best-effort"), stderr.String())
		b, err := os.ReadFile(out)
		require.NoError(t, err)
		files, _ := readPack(t, b)
		var m packManifest
		require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
		assert.Equal(t, tc.mode, m.Redactions.Commands)
		assert.Equal(t, "auditor", m.ExportedBy)
	}
}

// TestEvidencePack_Receipts (#2052, spec 22 section 4.3): the pack holds
// the period's PromotionReceipts with their signed envelopes, exported as
// signed. Each one verifies offline from the pack with kardinal
// verify-receipt --file; a receipt changed in the pack does not. A receipt
// whose signed statement holds a credential has its statement and envelope
// withheld (they cannot be redacted without breaking the signature), and
// the manifest counts it.
//
// Covers EVIDENCE-PACK-RECEIPTS-01.
func TestEvidencePack_Receipts(t *testing.T) {
	keepStyle(t)
	rc, bundle, key := signedReceipt(t)
	in := packPeriodOpts.Since.Add(24 * time.Hour)
	bundle.CreationTimestamp = metav1.NewTime(in)
	rc.CreationTimestamp = metav1.NewTime(in)
	// Receipts whose signed record must stay in the cluster, their
	// statements built with json.Marshal as the controller builds them: it
	// escapes "&" (\u0026) and quotes, so a credential after the first URL
	// parameter, or in an escaped string, does not show in the JSON text.
	var stmt map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(rc.Spec.Statement), &stmt))
	unsafe := map[string]func(map[string]interface{}){
		"first-param": func(m map[string]interface{}) { m["ciRunURL"] = "https://ci.example.com/run/1?token=SECRETCI1" },
		"later-param": func(m map[string]interface{}) { m["ciRunURL"] = "https://ci.example.com/run/1?a=1&b=2&token=SECRETCI2" },
		"nested": func(m map[string]interface{}) {
			m["deep"] = []interface{}{map[string]interface{}{"x": []interface{}{"see https://u:SECRETCI3@ci.example.com/x"}}}
		},
		"escaped-quote": func(m map[string]interface{}) {
			m["note"] = `said "https://ci.example.com/r?a=1&access_token=SECRETCI4" twice`
		},
	}
	var objs []sigs_client.Object
	for _, name := range []string{"first-param", "later-param", "nested", "escaped-quote"} {
		m := map[string]interface{}{}
		for k, v := range stmt {
			m[k] = v
		}
		unsafe[name](m)
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		leaky := rc.DeepCopy()
		leaky.Name = "leaky-" + name
		leaky.Labels = map[string]string{"kardinal.io/pipeline": "checkout", "kardinal.io/bundle": bundle.Name, "kardinal.io/environment": "uat"}
		leaky.Spec.Statement = string(raw)
		objs = append(objs, leaky)
	}
	// A clean statement whose bundle signs something else (a status edited
	// by hand): the check saw the statement, not what the bundle signs.
	swapped := rc.DeepCopy()
	swapped.Name = "swapped"
	swapped.Labels = map[string]string{"kardinal.io/pipeline": "checkout", "kardinal.io/bundle": bundle.Name, "kardinal.io/environment": "uat"}
	swapped.Spec.Statement = strings.Replace(rc.Spec.Statement, "carol@example.com", "dave@example.com", 1)
	objs = append(objs, swapped)
	other := rc.DeepCopy()
	other.Name, other.Labels = "old-prod", map[string]string{"kardinal.io/bundle": "older-version", "kardinal.io/environment": "prod"}
	event := &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ev"},
		Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(packPeriodOpts.Since.Add(-time.Hour)), Action: "PromotionSucceeded", Outcome: "Success"}}

	opts := packPeriodOpts
	opts.Namespace = "default"
	b, stderr, err := buildPack(t, append(objs, bundle, rc, other, event), &pager{}, opts)
	require.NoError(t, err)
	files, _ := readPack(t, b)
	recs := packLines(files["receipts.ndjson"])
	require.Equal(t, []string{"default/checkout-1-5-0-prod", "default/leaky-escaped-quote", "default/leaky-first-param",
		"default/leaky-later-param", "default/leaky-nested", "default/swapped"}, names(recs),
		"the receipts of the period's version, not another version's")
	for name, content := range files {
		assert.NotContains(t, string(content), "SECRETCI", "%s holds a credential", name)
	}
	for _, want := range []string{"leaky-later-param: its signed statement and bundle are left out: the signed statement holds a credential (statement.ciRunURL)",
		"leaky-nested: its signed statement and bundle are left out: the signed statement holds a credential (statement.deep[0].x[0])",
		"swapped: its signed statement and bundle are left out: the Sigstore bundle's payload is not the statement"} {
		assert.Contains(t, stderr, want)
	}

	// Verify the receipt offline, as docs/compliance.md shows: its envelope
	// written to a file, kardinal verify-receipt --file with the public key.
	envelope := func(r map[string]interface{}) string { return r["status"].(map[string]interface{})["bundle"].(string) }
	file := filepath.Join(t.TempDir(), "receipt.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(envelope(recs[0])), 0o600))
	verify := []string{"verify-receipt", "--file", file, "--key", key, "-n", "default", "--pipeline", "checkout", "--env", "prod",
		"--subject", receiptDigest, "--cluster", "c1"}
	out, _, err := runOutput(t, receiptClient(t), verify...)
	require.NoError(t, err)
	assert.Contains(t, out, "VERIFIED receipt checkout-1-5-0-prod")
	assert.Equal(t, rc.Spec.Statement, recs[0]["spec"].(map[string]interface{})["statement"], "the statement is exported as signed")

	// A receipt changed in the pack does not verify.
	var sb map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(envelope(recs[0])), &sb))
	dsse := sb["dsseEnvelope"].(map[string]interface{})
	payload, err := base64.StdEncoding.DecodeString(dsse["payload"].(string))
	require.NoError(t, err)
	dsse["payload"] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), "carol@example.com", "mallory@example.com", 1)))
	tampered, err := json.Marshal(sb)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, tampered, 0o600))
	_, _, err = runOutput(t, receiptClient(t), verify...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOT VERIFIED")

	// The receipts whose signed record must stay in the cluster.
	for _, withheld := range recs[1:] {
		assert.Empty(t, withheld["spec"].(map[string]interface{})["statement"], names([]map[string]interface{}{withheld}))
		assert.Empty(t, withheld["status"].(map[string]interface{})["bundle"])
		assert.Contains(t, withheld["metadata"].(map[string]interface{})["annotations"], withheldAnnotation)
	}
	var m packManifest
	require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
	assert.Equal(t, 5, m.Redactions.ReceiptsWithheld)
}

// TestEvidencePack_Cosign (#2052, spec 22 section 4.3): the exporter signs
// the pack with cosign, as docs/compliance.md shows, in key mode: the
// signature verifies, and a changed pack does not. It runs where cosign is
// installed; CI installs it and sets KARDINAL_REQUIRE_COSIGN=1, so the test
// cannot be skipped there. Offline: no transparency log (--tlog-upload=false
// when signing, --insecure-ignore-tlog when verifying).
//
// Covers EVIDENCE-PACK-COSIGN-01.
func TestEvidencePack_Cosign(t *testing.T) {
	cosign, err := exec.LookPath("cosign")
	if err != nil {
		if os.Getenv("KARDINAL_REQUIRE_COSIGN") == "1" {
			t.Fatalf("cosign is not installed, and KARDINAL_REQUIRE_COSIGN=1: %v", err)
		}
		t.Skip("cosign is not installed")
	}
	b, _, err := buildPack(t, packWorld(t, 50), &pager{}, packPeriodOpts)
	require.NoError(t, err)
	dir := t.TempDir()
	pack := filepath.Join(dir, "q3.tar.gz")
	require.NoError(t, os.WriteFile(pack, b, 0o600))
	run := func(args ...string) (string, error) {
		cmd := exec.Command(cosign, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("generate-key-pair")
	require.NoError(t, err, out)
	out, err = run("sign-blob", "--yes", "--key", "cosign.key", "--tlog-upload=false", "--bundle", "q3.tar.gz.sigstore.json", "q3.tar.gz")
	require.NoError(t, err, out)
	out, err = run("verify-blob", "--key", "cosign.pub", "--bundle", "q3.tar.gz.sigstore.json", "--insecure-ignore-tlog", "q3.tar.gz")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Verified OK")

	f, err := os.OpenFile(pack, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("x")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	out, err = run("verify-blob", "--key", "cosign.pub", "--bundle", "q3.tar.gz.sigstore.json", "--insecure-ignore-tlog", "q3.tar.gz")
	require.Error(t, err, "a changed pack does not verify:\n%s", out)
}

// TestReceiptUnsafe (#2161, QA-2): the receipt check decodes the statement
// and checks each value, fails closed on a statement or bundle that does not
// decode, and requires the bundle to sign the statement.
func TestReceiptUnsafe(t *testing.T) {
	stmt := `{"a":"https://ci.example.com/r?x=1&token=T","b":"fine"}`
	bundleWith := func(payload string, extra map[string]interface{}) string {
		m := map[string]interface{}{"dsseEnvelope": map[string]interface{}{"payload": []byte(payload)}}
		for k, v := range extra {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	bundleOf := func(payload string) string { return bundleWith(payload, nil) }
	for _, tc := range []struct {
		statement, bundle, want string
	}{
		{`{"a":"https://ci.example.com/r?x=1","b":["ok"]}`, "", ""},
		{`{"a":"https://ci.example.com/r?x=1","b":["ok"]}`, bundleOf(`{"a":"https://ci.example.com/r?x=1","b":["ok"]}`), ""},
		{stmt, "", "the signed statement holds a credential (statement.a)"},
		{`{"a":{"b":[1,"https://u:p@host/x"]}}`, "", "the signed statement holds a credential (statement.a.b[1])"},
		{`not json`, "", "the signed statement is not JSON"},
		{`{"a":"ok"}`, `not json`, "the Sigstore bundle is not JSON"},
		{`{"a":"ok"}`, bundleOf(`{"a":"other"}`), "the Sigstore bundle's payload is not the statement"},
		// QA-2's note on #2161: a key, and a bundle field other than the payload.
		{`{"https://u:SECRETKEY@ci.example.com/x":"ok"}`, "", "the signed statement holds a credential (statement (a key))"},
		{`{"a":{"see https://ci.example.com/r?x=1&token=T":1}}`, "", "the signed statement holds a credential (statement.a (a key))"},
		{`{"a":"ok"}`, bundleWith(`{"a":"ok"}`, map[string]interface{}{"verificationMaterial": map[string]interface{}{
			"note": "https://u:SECRETMAT@rekor.example.com"}}), "the Sigstore bundle holds a credential (bundle.verificationMaterial.note)"},
	} {
		got := receiptUnsafe(tc.statement, tc.bundle)
		if tc.want == "" {
			assert.Empty(t, got, tc.statement)
		} else {
			assert.Contains(t, got, tc.want, tc.statement)
		}
	}
	// json.Marshal writes "&" as \u0026: the JSON text alone hides the
	// credential from scm.RedactText, which is why the check decodes.
	raw, err := json.Marshal(map[string]string{"a": "https://ci.example.com/r?x=1&token=T"})
	require.NoError(t, err)
	assert.Equal(t, string(raw), scm.RedactText(string(raw)))
	assert.Contains(t, receiptUnsafe(string(raw), ""), "the signed statement holds a credential (statement.a)")
}
