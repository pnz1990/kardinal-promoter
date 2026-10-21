// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/compliance"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// packFormat names the evidence pack's layout, in manifest.json.
const packFormat = "kardinal.io/evidence-pack/v1"

// packDir is the directory every file of the pack is under.
const packDir = "evidence-pack/"

// chartNameLabel selects the chart's admission policies for admission.yaml.
const chartNameLabel = "app.kubernetes.io/name"

// evidencePackOptions are the filters and period of one pack.
type evidencePackOptions struct {
	Namespace   string // "" for every namespace
	Pipeline    string
	Environment string
	Since       time.Time
	Until       time.Time
	ExportedBy  string
	// IncludeCommands exports container command and args, redacted
	// best-effort, instead of their digests.
	IncludeCommands bool
	// TempDir holds the per-kind files while they are hashed.
	TempDir string
}

func newAuditEvidencePackCmd() *cobra.Command {
	var (
		since, until, pipeline, environment, out string
		allNamespaces, includeCommands           bool
	)
	cmd := &cobra.Command{
		Use:   "evidence-pack --since <time> [--until <time>] [-A | -n <ns>] [--pipeline <name>] [--environment <env>] [-o pack.tar.gz]",
		Short: "Write the records of a period, with a manifest of hashes and the control mapping, as one tar.gz",
		Long: `Write every kardinal record of a period into one gzipped tar for an auditor:
the AuditEvents, the versions created in the period with their Approvals,
gate instances and Verdicts, the policy in force, the control mapping and a
manifest with each file's SHA-256. The checksums are not a signature: sign
the pack with your own identity (docs/compliance.md).`,
		Annotations: map[string]string{ReferenceAnnotation: evidencePackReference},
		Example: `  kardinal audit evidence-pack -A --since 2026-07-01 --until 2026-10-01 -o q3.tar.gz
  kardinal audit evidence-pack -n payments --pipeline checkout --since 30d -o checkout.tar.gz`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now().UTC()
			from, err := parsePackTime(since, now)
			if err != nil {
				return fmt.Errorf("audit evidence-pack: --since: %w", err)
			}
			to := now
			if until != "" {
				if to, err = parsePackTime(until, now); err != nil {
					return fmt.Errorf("audit evidence-pack: --until: %w", err)
				}
			}
			if !from.Before(to) {
				return fmt.Errorf("audit evidence-pack: --since %s is not before --until %s",
					from.Format(time.RFC3339), to.Format(time.RFC3339))
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("audit evidence-pack: %w", err)
			}
			if allNamespaces {
				ns = ""
			}
			ctx := contextOf(cmd)
			opts := evidencePackOptions{Namespace: ns, Pipeline: pipeline, Environment: environment, Since: from, Until: to,
				IncludeCommands: includeCommands}
			if includeCommands {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: --include-commands exports container commands and arguments; "+
					"their redaction is best-effort: a secret it does not recognise is exported")
			}
			if id, err := callerIdentity(ctx, c); err == nil {
				opts.ExportedBy = id.Username
			} else {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: the manifest names no exporter: %v\n", err)
			}
			return writeEvidencePack(ctx, c, opts, out, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Start of the period: a date (2026-07-01), an RFC 3339 time, or a duration before now (30d)")
	cmd.Flags().StringVar(&until, "until", "", "End of the period (excluded), as --since; default now. Set it for a pack that does not change between exports")
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Only this pipeline's records")
	cmd.Flags().StringVar(&environment, "environment", "", "Only this environment's AuditEvents, gate instances, Approvals and Verdicts")
	cmd.Flags().StringVarP(&out, "output", "o", "", "Write the pack to this file instead of standard output")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "Records of every namespace")
	cmd.Flags().BoolVar(&includeCommands, "include-commands", false,
		"Export container commands and arguments, with credential-named values redacted best-effort, instead of their SHA-256")
	_ = cmd.MarkFlagRequired("since")
	return cmd
}

// parsePackTime reads a date (2006-01-02, UTC midnight), an RFC 3339 time,
// or a positive duration before now (30d, 12h).
func parsePackTime(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), nil
	}
	d, err := parseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a date (2026-07-01), an RFC 3339 time or a duration (30d)", s)
	}
	return now.Add(-d).Truncate(time.Second), nil
}

// writeEvidencePack builds the pack and writes it to the file out, or to
// stdout when out is empty. A file is written beside out and renamed into
// place at the end, so an interrupted export leaves no file that looks
// complete. The pack's SHA-256 is printed to stderr.
func writeEvidencePack(ctx context.Context, c sigs_client.Client, opts evidencePackOptions, out string, stdout, stderr io.Writer) error {
	dir, err := os.MkdirTemp(opts.TempDir, "kardinal-evidence-pack-")
	if err != nil {
		return fmt.Errorf("audit evidence-pack: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	opts.TempDir = dir
	p, err := collectEvidence(ctx, c, opts)
	if err != nil {
		return err
	}
	for _, w := range p.manifest.Coverage.Warnings {
		_, _ = fmt.Fprintf(stderr, "warning: %s\n", w)
	}

	dest, name := stdout, "-"
	var f *os.File
	if out != "" {
		if f, err = os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".partial-"); err != nil {
			return fmt.Errorf("audit evidence-pack: %w", err)
		}
		defer func() {
			_ = f.Close()
			_ = os.Remove(f.Name()) // gone once renamed
		}()
		dest, name = f, out
	}
	sum := sha256.New()
	if err := p.writeTar(io.MultiWriter(dest, sum)); err != nil {
		return fmt.Errorf("audit evidence-pack: write: %w", err)
	}
	if f != nil {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("audit evidence-pack: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("audit evidence-pack: %w", err)
		}
		if err := os.Rename(f.Name(), out); err != nil {
			return fmt.Errorf("audit evidence-pack: %w", err)
		}
	}
	_, _ = fmt.Fprintf(stderr, "sha256:%s  %s (%d files, %d records)\n",
		hex.EncodeToString(sum.Sum(nil)), name, len(p.manifest.Files)+2, p.records())
	return nil
}

// packManifest is manifest.json.
type packManifest struct {
	Format          string         `json:"format"`
	KardinalVersion string         `json:"kardinalVersion"`
	ExportedBy      string         `json:"exportedBy,omitempty"`
	Period          packPeriod     `json:"period"`
	Filters         packFilters    `json:"filters"`
	Files           []packFile     `json:"files"`
	Coverage        packCoverage   `json:"coverage"`
	Redactions      packRedactions `json:"redactions"`
	Integrity       string         `json:"integrity"`
}

type packPeriod struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

type packFilters struct {
	Namespace   string `json:"namespace"`
	Pipeline    string `json:"pipeline,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type packFile struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Records int    `json:"records"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
}

type packCoverage struct {
	// Oldest is the creation of the oldest record of each kind the cluster
	// still holds in the namespaces asked for (an AuditEvent's
	// spec.timestamp), whatever the period.
	Oldest map[string]time.Time `json:"oldest"`
	// Missing are the kinds the cluster does not serve (an older chart).
	Missing []string `json:"missing"`
	// Forbidden are the kinds the exporter may not read.
	Forbidden []string `json:"forbidden"`
	Warnings  []string `json:"warnings"`
}

// integrityNote is manifest.json's integrity line.
const integrityNote = "files[].sha256 and SHA256SUMS are checksums: they detect a change after export but are not a " +
	"signature. Sign the pack with cosign under your own identity. A record deleted before the export is not in the pack."

// evidence is a pack being built: the hashed per-kind files on disk and
// the manifest.
type evidence struct {
	dir      string
	until    time.Time
	manifest packManifest
	red      redactor
}

func (p *evidence) records() int {
	n := 0
	for _, f := range p.manifest.Files {
		n += f.Records
	}
	return n
}

// kindFile is a pack file being written: NDJSON hashed as it goes.
type kindFile struct {
	f    *os.File
	h    hash.Hash
	enc  *json.Encoder
	meta packFile
	cw   *countWriter
}

type countWriter struct{ n int64 }

func (w *countWriter) Write(b []byte) (int, error) { w.n += int64(len(b)); return len(b), nil }

func (p *evidence) create(name, kind string) (*kindFile, error) {
	f, err := os.Create(filepath.Join(p.dir, name))
	if err != nil {
		return nil, err
	}
	kf := &kindFile{f: f, h: sha256.New(), meta: packFile{Name: name, Kind: kind}, cw: &countWriter{}}
	kf.enc = json.NewEncoder(io.MultiWriter(f, kf.h, kf.cw))
	return kf, nil
}

func (kf *kindFile) write(v interface{}) error {
	kf.meta.Records++
	return kf.enc.Encode(v)
}

// done closes the file and records it in the manifest.
func (p *evidence) done(kf *kindFile) error {
	if err := kf.f.Close(); err != nil {
		return err
	}
	kf.meta.Bytes = kf.cw.n
	kf.meta.SHA256 = hex.EncodeToString(kf.h.Sum(nil))
	p.manifest.Files = append(p.manifest.Files, kf.meta)
	return nil
}

// abandon closes and removes a file whose kind could not be read.
func (p *evidence) abandon(kf *kindFile) {
	_ = kf.f.Close()
	_ = os.Remove(kf.f.Name())
}

// readable sorts a List error: nil when the list worked, false with the
// kind recorded in coverage when the cluster does not serve the kind or
// the exporter may not read it, else the error.
func (p *evidence) readable(kind string, err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case apimeta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) || apierrors.IsNotFound(err):
		p.manifest.Coverage.Missing = append(p.manifest.Coverage.Missing, kind)
		return false, nil
	case apierrors.IsForbidden(err):
		p.manifest.Coverage.Forbidden = append(p.manifest.Coverage.Forbidden, kind)
		if p.manifest.Filters.Namespace == "*" && len(p.manifest.Coverage.Forbidden) == 1 {
			p.manifest.Coverage.Warnings = append(p.manifest.Coverage.Warnings,
				"you may not read some kinds in every namespace (-A): a namespaced reader exports one namespace with -n <namespace>")
		}
		return false, nil
	}
	return false, fmt.Errorf("audit evidence-pack: list %s: %w", kind, err)
}

func (p *evidence) oldest(kind string, t time.Time) {
	if t.IsZero() {
		return
	}
	if o, ok := p.manifest.Coverage.Oldest[kind]; !ok || t.Before(o) {
		p.manifest.Coverage.Oldest[kind] = t.UTC()
	}
}

// eachPage lists list's kind page by page (auditExportPage objects each),
// calling fn on each page. The list is reused between pages.
func eachPage(ctx context.Context, c sigs_client.Reader, list sigs_client.ObjectList, opts []sigs_client.ListOption, fn func() error) error {
	cont := ""
	for {
		if err := c.List(ctx, list, append(opts, sigs_client.Limit(auditExportPage), sigs_client.Continue(cont))...); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
		if cont = list.GetContinue(); cont == "" {
			return nil
		}
	}
}

// inPeriod reports whether t is in [since, until).
func (o evidencePackOptions) inPeriod(t time.Time) bool {
	return !t.Before(o.Since) && t.Before(o.Until)
}

// bundleKey is a version's namespace and name.
type bundleKey struct{ ns, name string }

// collectEvidence lists every kind into its hashed file and builds the
// manifest. Memory is bounded by one List page, and by the names of the
// period's versions, which select their Approvals, gate instances and
// Verdicts.
func collectEvidence(ctx context.Context, c sigs_client.Client, opts evidencePackOptions) (*evidence, error) {
	ns := opts.Namespace
	nsName := ns
	if nsName == "" {
		nsName = "*"
	}
	p := &evidence{dir: opts.TempDir, until: opts.Until, red: redactor{includeCommands: opts.IncludeCommands}, manifest: packManifest{
		Format:          packFormat,
		KardinalVersion: CLIVersion,
		ExportedBy:      opts.ExportedBy,
		Period:          packPeriod{Since: opts.Since, Until: opts.Until},
		Filters:         packFilters{Namespace: nsName, Pipeline: opts.Pipeline, Environment: opts.Environment},
		Coverage:        packCoverage{Oldest: map[string]time.Time{}, Missing: []string{}, Forbidden: []string{}, Warnings: []string{}},
		Integrity:       integrityNote,
	}}
	var nsOpts []sigs_client.ListOption
	if ns != "" {
		nsOpts = append(nsOpts, sigs_client.InNamespace(ns))
	}
	envOK := func(env string) bool { return opts.Environment == "" || env == opts.Environment }
	pipeOK := func(pl string) bool { return opts.Pipeline == "" || pl == opts.Pipeline }

	if err := p.auditEvents(ctx, c, nsOpts, opts, envOK, pipeOK); err != nil {
		return nil, err
	}
	bundles, err := p.bundles(ctx, c, nsOpts, opts, pipeOK)
	if err != nil {
		return nil, err
	}
	ofBundle := func(namespace, bundle string) bool { return bundles[bundleKey{namespace, bundle}] }

	// The period's Approvals, gate instances and Verdicts: those of its versions.
	if err := p.kind(ctx, c, "Approval", &v1alpha1.ApprovalList{}, nsOpts, func(o runtime.Object) bool {
		a := o.(*v1alpha1.Approval)
		return ofBundle(a.Namespace, a.Spec.Bundle) && envOK(a.Spec.Environment)
	}); err != nil {
		return nil, err
	}
	if err := p.kind(ctx, c, "PolicyGate", &v1alpha1.PolicyGateList{}, nsOpts, func(o runtime.Object) bool {
		g := o.(*v1alpha1.PolicyGate)
		b, ok := g.Labels["kardinal.io/bundle"]
		return ok && ofBundle(g.Namespace, b) && envOK(g.Labels["kardinal.io/environment"])
	}); err != nil {
		return nil, err
	}
	// runs are the AgentRuns the period's Verdicts name (spec.run): a Verdict
	// an in-cluster verifier wrote has no actor stamp, and its AgentRun names
	// the agent and its sponsor (#2295).
	runs := map[bundleKey]bool{}
	if err := p.kind(ctx, c, "Verdict", &v1alpha1.VerdictList{}, nsOpts, func(o runtime.Object) bool {
		v := o.(*v1alpha1.Verdict)
		keep := ofBundle(v.Namespace, v.Spec.Bundle) && envOK(v.Spec.Environment)
		if keep && v.Spec.Run != nil {
			runs[bundleKey{v.Namespace, v.Spec.Run.Name}] = true
		}
		return keep
	}); err != nil {
		return nil, err
	}
	if err := p.agentRuns(ctx, c, nsOpts, runs); err != nil {
		return nil, err
	}
	if err := p.kind(ctx, c, "PromotionReceipt", &v1alpha1.PromotionReceiptList{}, nsOpts, func(o runtime.Object) bool {
		rc := o.(*v1alpha1.PromotionReceipt)
		b, ok := rc.Labels["kardinal.io/bundle"]
		return ok && ofBundle(rc.Namespace, b) && envOK(rc.Labels["kardinal.io/environment"])
	}); err != nil {
		return nil, err
	}
	// Their change request tickets: the ChangeRequest instances.
	if err := p.kind(ctx, c, "ChangeRequest", &v1alpha1.ChangeRequestList{}, nsOpts, func(o runtime.Object) bool {
		cr := o.(*v1alpha1.ChangeRequest)
		_, instance := cr.Labels[graph.LabelChangeRequestTemplate]
		b, ok := cr.Labels["kardinal.io/bundle"]
		return instance && ok && ofBundle(cr.Namespace, b) && envOK(cr.Labels["kardinal.io/environment"])
	}); err != nil {
		return nil, err
	}
	// The procedure and policy in force at export.
	if err := p.kind(ctx, c, "Pipeline", &v1alpha1.PipelineList{}, nsOpts, func(o runtime.Object) bool {
		return pipeOK(o.(*v1alpha1.Pipeline).Name)
	}); err != nil {
		return nil, err
	}
	if err := p.kindAs(ctx, c, "PolicyGate", "policygate-templates.ndjson", "PolicyGateTemplate", &v1alpha1.PolicyGateList{}, nsOpts, func(o runtime.Object) bool {
		g := o.(*v1alpha1.PolicyGate)
		_, instance := g.Labels["kardinal.io/gate-template"]
		return !instance && !g.Spec.Generated
	}); err != nil {
		return nil, err
	}
	if err := p.kindAs(ctx, c, "ChangeRequest", "changerequest-templates.ndjson", "ChangeRequestTemplate", &v1alpha1.ChangeRequestList{}, nsOpts, func(o runtime.Object) bool {
		_, instance := o.(*v1alpha1.ChangeRequest).Labels[graph.LabelChangeRequestTemplate]
		return !instance
	}); err != nil {
		return nil, err
	}
	if err := p.kind(ctx, c, "ChangeWindow", &v1alpha1.ChangeWindowList{}, nil, func(runtime.Object) bool { return true }); err != nil {
		return nil, err
	}
	if err := p.kind(ctx, c, "AgentIdentity", &v1alpha1.AgentIdentityList{}, nil, func(runtime.Object) bool { return true }); err != nil {
		return nil, err
	}
	if err := p.admission(ctx, c); err != nil {
		return nil, err
	}
	return p, p.controls()
}

// agentRuns writes the AgentRuns the period's Verdicts name, and warns of
// those the cluster no longer holds.
func (p *evidence) agentRuns(ctx context.Context, c sigs_client.Reader, nsOpts []sigs_client.ListOption, runs map[bundleKey]bool) error {
	found := map[bundleKey]bool{}
	readable := true
	if err := p.kind(ctx, c, "AgentRun", &v1alpha1.AgentRunList{}, nsOpts, func(o runtime.Object) bool {
		r := o.(*v1alpha1.AgentRun)
		k := bundleKey{r.Namespace, r.Name}
		found[k] = runs[k]
		return runs[k]
	}); err != nil {
		return err
	}
	for _, kind := range append(p.manifest.Coverage.Missing, p.manifest.Coverage.Forbidden...) {
		readable = readable && kind != "AgentRun"
	}
	gone := 0
	for k := range runs {
		if !found[k] {
			gone++
		}
	}
	if readable && gone > 0 {
		p.manifest.Coverage.Warnings = append(p.manifest.Coverage.Warnings, fmt.Sprintf(
			"%d Verdicts name an AgentRun the cluster no longer holds: their agent is named by the gate instance's "+
				"spec.verdicts[].actor, the VerdictRecorded AuditEvent and the version's receipt", gone))
	}
	return nil
}

// auditEvents writes the period's AuditEvents twice, as kardinal audit
// export writes them: JSON lines and OCSF. A period that ends before the
// oldest AuditEvent the cluster holds fails: retention deleted it.
func (p *evidence) auditEvents(ctx context.Context, c sigs_client.Reader, nsOpts []sigs_client.ListOption,
	opts evidencePackOptions, envOK, pipeOK func(string) bool) error {
	js, err := p.create("auditevents.ndjson", "AuditEvent")
	if err != nil {
		return err
	}
	ocsf, err := p.create("auditevents.ocsf.ndjson", "AuditEvent")
	if err != nil {
		return err
	}
	var list v1alpha1.AuditEventList
	err = eachPage(ctx, c, &list, nsOpts, func() error {
		for i := range list.Items {
			ev := &list.Items[i]
			if !pipeOK(ev.Spec.PipelineName) {
				continue
			}
			p.oldest("AuditEvent", ev.Spec.Timestamp.Time)
			if !opts.inPeriod(ev.Spec.Timestamp.Time) || !envOK(ev.Spec.Environment) {
				continue
			}
			ev = ev.DeepCopy()
			if err := js.write(p.red.value(jsonAuditRecord(ev))); err != nil {
				return err
			}
			if err := ocsf.write(p.red.value(ocsfAuditRecord(ev))); err != nil {
				return err
			}
		}
		return nil
	})
	if ok, err := p.readable("AuditEvent", err); !ok {
		p.abandon(js)
		p.abandon(ocsf)
		return err
	}
	if err := p.done(js); err != nil {
		return err
	}
	if err := p.done(ocsf); err != nil {
		return err
	}
	oldest, ok := p.manifest.Coverage.Oldest["AuditEvent"]
	switch {
	case ok && !opts.Until.After(oldest):
		return fmt.Errorf("audit evidence-pack: the period ends at %s, before the oldest AuditEvent the cluster holds (%s): "+
			"retention deleted its records. Export continuously into your SIEM with kardinal audit export (docs/compliance.md#keep-the-records)",
			opts.Until.Format(time.RFC3339), oldest.Format(time.RFC3339))
	case ok && opts.Since.Before(oldest):
		p.manifest.Coverage.Warnings = append(p.manifest.Coverage.Warnings, fmt.Sprintf(
			"the oldest AuditEvent the cluster holds is from %s, after the period's start (%s): retention "+
				"(--audit-retention-max-age, --audit-retention-max-per-pipeline) may have deleted records of the period",
			oldest.Format(time.RFC3339), opts.Since.Format(time.RFC3339)))
	}
	return nil
}

// bundles writes the versions created in the period and returns their
// names.
func (p *evidence) bundles(ctx context.Context, c sigs_client.Reader, nsOpts []sigs_client.ListOption,
	opts evidencePackOptions, pipeOK func(string) bool) (map[bundleKey]bool, error) {
	kf, err := p.create(compliance.PackFiles["Bundle"], "Bundle")
	if err != nil {
		return nil, err
	}
	in := map[bundleKey]bool{}
	var list v1alpha1.BundleList
	err = eachPage(ctx, c, &list, nsOpts, func() error {
		for i := range list.Items {
			b := &list.Items[i]
			if !pipeOK(b.Spec.Pipeline) {
				continue
			}
			p.oldest("Bundle", b.CreationTimestamp.Time)
			if !opts.inPeriod(b.CreationTimestamp.Time) {
				continue
			}
			in[bundleKey{b.Namespace, b.Name}] = true
			if err := kf.write(p.record(b, "Bundle")); err != nil {
				return err
			}
		}
		return nil
	})
	if ok, err := p.readable("Bundle", err); !ok {
		p.abandon(kf)
		if err == nil && slices.Contains(p.manifest.Coverage.Forbidden, "Bundle") {
			p.manifest.Coverage.Warnings = append(p.manifest.Coverage.Warnings,
				"without read on bundles the pack holds no Approvals, gate instances or Verdicts: they are selected by their version")
		}
		return in, err
	}
	return in, p.done(kf)
}

// kind writes the objects of a kind that keep passes to its pack file.
func (p *evidence) kind(ctx context.Context, c sigs_client.Reader, kind string, list sigs_client.ObjectList,
	opts []sigs_client.ListOption, keep func(runtime.Object) bool) error {
	return p.kindAs(ctx, c, kind, compliance.PackFiles[kind], kind, list, opts, keep)
}

// kindAs writes the objects of kind that keep passes to file, recording
// coverage under label.
func (p *evidence) kindAs(ctx context.Context, c sigs_client.Reader, kind, file, label string, list sigs_client.ObjectList,
	opts []sigs_client.ListOption, keep func(runtime.Object) bool) error {
	kf, err := p.create(file, label)
	if err != nil {
		return err
	}
	err = eachPage(ctx, c, list, opts, func() error {
		items, err := apimeta.ExtractList(list)
		if err != nil {
			return err
		}
		for _, o := range items {
			if !keep(o) {
				continue
			}
			obj := o.(sigs_client.Object)
			p.oldest(label, obj.GetCreationTimestamp().Time)
			if err := kf.write(p.record(obj, kind)); err != nil {
				return err
			}
		}
		return nil
	})
	if ok, err := p.readable(label, err); !ok {
		p.abandon(kf)
		return err
	}
	return p.done(kf)
}

// record is obj as written to the pack: a redacted copy (redactor), with
// its kind and without managedFields.
func (p *evidence) record(obj sigs_client.Object, kind string) sigs_client.Object {
	o := obj.DeepCopyObject().(sigs_client.Object)
	o.SetManagedFields(nil)
	o.GetObjectKind().SetGroupVersionKind(v1alpha1.GroupVersion.WithKind(kind))
	if rc, ok := o.(*v1alpha1.PromotionReceipt); ok {
		p.receipt(rc)
		return rc
	}
	p.red.object(o)
	return o
}

// withheldAnnotation marks a receipt whose signed statement the pack left
// out, and says why.
const withheldAnnotation = "kardinal.io/evidence-pack-withheld"

// receipt redacts a PromotionReceipt but its signed record: the statement
// and its Sigstore bundle are exported as signed, or they would not verify
// (kardinal verify-receipt --file). The pack withholds both, and says why,
// when it cannot show them safe (receiptUnsafe): a signed statement that
// holds a credential cannot be redacted without breaking its signature
// (#2158), so it is never exported.
func (p *evidence) receipt(rc *v1alpha1.PromotionReceipt) {
	statement, bundle := rc.Spec.Statement, rc.Status.Bundle
	rc.Spec.Statement, rc.Status.Bundle = "", ""
	p.red.object(rc)
	if why := receiptUnsafe(statement, bundle); why != "" {
		if rc.Annotations == nil {
			rc.Annotations = map[string]string{}
		}
		rc.Annotations[withheldAnnotation] = why + "; verify it from the cluster or its sinks"
		p.red.counts.ReceiptsWithheld++
		p.manifest.Coverage.Warnings = append(p.manifest.Coverage.Warnings, fmt.Sprintf(
			"receipt %s/%s: its signed statement and bundle are left out: %s", rc.Namespace, rc.Name, why))
		return
	}
	rc.Spec.Statement, rc.Status.Bundle = statement, bundle
}

// receiptUnsafe says why a receipt's signed record must not leave the
// cluster, or "" when it may. It fails closed:
//   - the statement is decoded and every string in it is checked with
//     scm.RedactText, the redaction of status, CLI output and logs: the
//     statement's JSON text escapes "&" (\u0026) and quotes, so a
//     credential parameter after the first, or a URL in an escaped string,
//     does not show in the text (#2161, QA-2);
//   - a statement that does not decode is not shown safe;
//   - the bundle's DSSE payload must be the statement, byte for byte: the
//     check above saw the statement, and a bundle that signs anything else
//     (a status edited by hand) would be exported unchecked;
//   - every other string of the bundle (its verification material, a log
//     entry) and every key of either is checked as the statement's values
//     are.
func receiptUnsafe(statement, bundle string) string {
	var tree interface{}
	if err := json.Unmarshal([]byte(statement), &tree); err != nil {
		return "the signed statement is not JSON, so it cannot be checked for credentials"
	}
	if path := credentialAt(tree, "statement"); path != "" {
		return "the signed statement holds a credential (" + path + ")"
	}
	if bundle == "" {
		return ""
	}
	var b struct {
		DSSE struct {
			Payload []byte `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal([]byte(bundle), &b); err != nil {
		return "the Sigstore bundle is not JSON, so its payload cannot be checked"
	}
	if string(b.DSSE.Payload) != statement {
		return "the Sigstore bundle's payload is not the statement"
	}
	var whole interface{}
	if err := json.Unmarshal([]byte(bundle), &whole); err != nil {
		return "the Sigstore bundle is not JSON, so its payload cannot be checked"
	}
	if path := credentialAt(whole, "bundle"); path != "" {
		return "the Sigstore bundle holds a credential (" + path + ")"
	}
	return ""
}

// credentialAt is the path of the first string in tree, a map's key or a
// value, that scm.RedactText would change (a URL's userinfo, a credential
// parameter), or "".
func credentialAt(tree interface{}, path string) string {
	switch t := tree.(type) {
	case string:
		if scm.RedactText(t) != t {
			return path
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if scm.RedactText(k) != k {
				return path + " (a key)"
			}
			if p := credentialAt(t[k], path+"."+k); p != "" {
				return p
			}
		}
	case []interface{}:
		for i, v := range t {
			if p := credentialAt(v, fmt.Sprintf("%s[%d]", path, i)); p != "" {
				return p
			}
		}
	}
	return ""
}

// admission writes admission.yaml: the chart's ValidatingAdmissionPolicies
// and their bindings, the checks that keep the records' names from being
// forged.
func (p *evidence) admission(ctx context.Context, c sigs_client.Client) error {
	sel := sigs_client.MatchingLabels{chartNameLabel: "kardinal-promoter"}
	var docs []string
	var policies admissionregistrationv1.ValidatingAdmissionPolicyList
	err := eachPage(ctx, c, &policies, []sigs_client.ListOption{sel}, func() error {
		for i := range policies.Items {
			d, err := p.admissionDoc(&policies.Items[i], c.Scheme())
			if err != nil {
				return err
			}
			docs = append(docs, d)
		}
		return nil
	})
	if ok, err := p.readable("ValidatingAdmissionPolicy", err); !ok {
		return err
	}
	var bindings admissionregistrationv1.ValidatingAdmissionPolicyBindingList
	err = eachPage(ctx, c, &bindings, []sigs_client.ListOption{sel}, func() error {
		for i := range bindings.Items {
			d, err := p.admissionDoc(&bindings.Items[i], c.Scheme())
			if err != nil {
				return err
			}
			docs = append(docs, d)
		}
		return nil
	})
	if ok, err := p.readable("ValidatingAdmissionPolicyBinding", err); !ok {
		return err
	}
	sort.Strings(docs)
	return p.writeRaw("admission.yaml", "", len(docs), []byte(strings.Join(docs, "---\n")))
}

// admissionDoc is obj as one YAML document, with its apiVersion and kind
// and without managedFields.
func (p *evidence) admissionDoc(obj sigs_client.Object, scheme *runtime.Scheme) (string, error) {
	o := obj.DeepCopyObject().(sigs_client.Object)
	o.SetManagedFields(nil)
	p.red.object(o)
	gvk, err := apiutil.GVKForObject(o, scheme)
	if err != nil {
		return "", err
	}
	o.GetObjectKind().SetGroupVersionKind(gvk)
	b, err := yaml.Marshal(o)
	return string(b), err
}

// controls writes controls.json, the mapping of each control to the
// pack's files.
func (p *evidence) controls() error {
	b, err := json.MarshalIndent(struct {
		Frameworks []compliance.PackFramework `json:"frameworks"`
	}{compliance.ForPack(compliance.Frameworks())}, "", "  ")
	if err != nil {
		return err
	}
	return p.writeRaw("controls.json", "", 0, append(b, '\n'))
}

// writeRaw writes a pack file whose content is in memory.
func (p *evidence) writeRaw(name, kind string, records int, b []byte) error {
	if err := os.WriteFile(filepath.Join(p.dir, name), b, 0o600); err != nil {
		return err
	}
	s := sha256.Sum256(b)
	p.manifest.Files = append(p.manifest.Files, packFile{Name: name, Kind: kind, Records: records, Bytes: int64(len(b)), SHA256: hex.EncodeToString(s[:])})
	return nil
}

// writeTar writes the pack: manifest.json, SHA256SUMS, then the files in
// name order. Every header has the period's end as its time, so a frozen
// cluster gives the same bytes.
func (p *evidence) writeTar(w io.Writer) error {
	sort.Slice(p.manifest.Files, func(i, j int) bool { return p.manifest.Files[i].Name < p.manifest.Files[j].Name })
	sort.Strings(p.manifest.Coverage.Missing)
	sort.Strings(p.manifest.Coverage.Forbidden)
	p.manifest.Redactions = p.red.counts
	p.manifest.Redactions.Rules = p.red.rules()
	p.manifest.Redactions.Commands = commandsDigest
	if p.red.includeCommands {
		p.manifest.Redactions.Commands = commandsRedacted
	}
	manifest, err := json.MarshalIndent(p.manifest, "", "  ")
	if err != nil {
		return err
	}
	manifest = append(manifest, '\n')
	ms := sha256.Sum256(manifest)
	sums := []string{hex.EncodeToString(ms[:]) + "  manifest.json"}
	for _, f := range p.manifest.Files {
		sums = append(sums, f.SHA256+"  "+f.Name)
	}
	sumsFile := []byte(strings.Join(sums, "\n") + "\n")

	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	mtime := p.until.Truncate(time.Second)
	header := func(name string, size int64) *tar.Header {
		return &tar.Header{Name: packDir + name, Mode: 0o644, Size: size, ModTime: mtime, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	}
	if err := tw.WriteHeader(&tar.Header{Name: packDir, Mode: 0o755, ModTime: mtime, Typeflag: tar.TypeDir, Format: tar.FormatPAX}); err != nil {
		return err
	}
	for _, mem := range []struct {
		name string
		b    []byte
	}{{"manifest.json", manifest}, {"SHA256SUMS", sumsFile}} {
		if err := tw.WriteHeader(header(mem.name, int64(len(mem.b)))); err != nil {
			return err
		}
		if _, err := tw.Write(mem.b); err != nil {
			return err
		}
	}
	for _, f := range p.manifest.Files {
		if err := p.copyInto(tw, header(f.Name, f.Bytes)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func (p *evidence) copyInto(tw *tar.Writer, h *tar.Header) error {
	f, err := os.Open(filepath.Join(p.dir, strings.TrimPrefix(h.Name, packDir)))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// evidencePackReference is the detail of the command for its reference page.
const evidencePackReference = `The cluster holds only what retention and each Pipeline's historyLimit kept.
A period that ends before the oldest AuditEvent fails: export continuously
with kardinal audit export.

The period is the versions (Bundles) created in [--since, --until) and the
AuditEvents timestamped in it. A version brings its Approvals, gate instances,
Verdicts, the AgentRuns that wrote them, receipts and change request tickets.
The Pipelines, gate and change request
templates, ChangeWindows, AgentIdentities and admission policies are the ones
in force at export.

    manifest.json                format, kardinal version, exporter, period,
                                 filters, each file's records, bytes and
                                 SHA-256, coverage, redactions, integrity note
    SHA256SUMS                   sha256sum -c input for every other file
    controls.json                each control, its records and their files
    auditevents.ndjson           as kardinal audit export --format json
    auditevents.ocsf.ndjson      as kardinal audit export --format ocsf
    bundles.ndjson               the versions created in the period
    approvals.ndjson             their Approvals
    policygates.ndjson           their gate instances (overrides, results)
    verdicts.ndjson              their Verdicts
    agentruns.ndjson             the AgentRuns those Verdicts name
                                 (spec.run): each names the agent and its
                                 sponsor (status.agent, status.sponsor)
    receipts.ndjson              their PromotionReceipts, statement and
                                 Sigstore bundle as signed (verify one
                                 with kardinal verify-receipt --file)
    changerequests.ndjson        their ChangeRequest instances (ticket,
                                 state, approvers, whether it was verified)
    pipelines.ndjson             the Pipelines
    policygate-templates.ndjson  the gate templates
    changerequest-templates.ndjson
                                 the ChangeRequest templates
    changewindows.ndjson         windows and freezes, with exemptions
    agentidentities.ndjson       the agent registry
    admission.yaml               the chart's ValidatingAdmissionPolicies
                                 and bindings

Every record is redacted: the userinfo of each URL and secret query
parameters (as for status, CLI output and logs), literal env values (<redacted>, names and valueFrom kept, hook Jobs
included), and kubectl's last-applied-configuration annotation. Container
command and args, probes and lifecycle handlers included, are exported as
{"sha256": ..., "length": N}: SHA-256 of each argument followed by a NUL
(printf '%s\0' ARG... | sha256sum); the digest is unkeyed, so a short or
guessable secret could be found from it. --include-commands exports them
with credential-named flags, NAME=value assignments and headers, Bearer
tokens and -u user:password redacted, best-effort.
manifest.json's redactions records the mode, the rules and the counts.

coverage.oldest is the oldest record of each kind the cluster still holds,
coverage.missing the kinds it does not serve, coverage.forbidden the kinds
you may not read: those files are left out, not written empty. The read
needs get and list on these kinds, and list on the admission policies.

The pack is written beside -o and renamed into place when complete. Its
SHA-256 is printed to standard error. Memory is bounded by one List page
(500 objects).`
