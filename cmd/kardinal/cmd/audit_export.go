// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// auditExportPage is how many AuditEvents each List page holds: the export
// streams page by page, so a large log is never held in memory.
const auditExportPage = 500

// OCSF identifiers of the API Activity class (OCSF 1.3.0,
// events/application/api_activity.json).
const (
	ocsfVersion     = "1.3.0"
	ocsfCategoryUID = 6
	ocsfClassUID    = 6003
)

func newAuditExportCmd() *cobra.Command {
	var (
		pipeline, since, format, file string
		allNamespaces                 bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write AuditEvents as JSON lines or OCSF API Activity events, for a SIEM",
		Long: `Write the AuditEvents of the last --since as newline-delimited JSON, one
event per line: --format json (the AuditEvent spec, with its namespace, name
and uid) or --format ocsf (OCSF 1.3.0 API Activity, class 6003). Each event
names its actor: who caused it, verified, and what the client asserted about
itself, kept apart (docs/actors.md).`,
		Annotations: map[string]string{ReferenceAnnotation: auditExportReference},
		Example: `  kardinal audit export --since 7d > audit.ndjson
  kardinal audit export -A --format ocsf --file audit.ocsf.ndjson
  kardinal audit export --pipeline checkout --since 1h`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "json" && format != "ocsf" {
				return fmt.Errorf("audit export: --format is json or ocsf, not %q", format)
			}
			dur, err := parseSinceDuration(since)
			if err != nil {
				return fmt.Errorf("audit export: invalid --since %q: %w", since, err)
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("audit export: %w", err)
			}
			if allNamespaces {
				ns = ""
			}
			out := cmd.OutOrStdout()
			if file != "" {
				f, err := os.Create(file)
				if err != nil {
					return fmt.Errorf("audit export: %w", err)
				}
				defer func() { _ = f.Close() }()
				out = f
			}
			n, err := auditExportFn(contextOf(cmd), out, c, ns, pipeline, format, time.Now().Add(-dur))
			if err != nil {
				return err
			}
			if file != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d events to %s\n", n, file)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Only this pipeline's events")
	cmd.Flags().StringVar(&since, "since", "24h", "Time window: a positive duration (90m, 24h, 7d)")
	cmd.Flags().StringVar(&format, "format", "json", "json (the AuditEvent spec) or ocsf (OCSF 1.3.0 API Activity)")
	cmd.Flags().StringVar(&file, "file", "", "Write to this file instead of standard output")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "Events of every namespace")
	return cmd
}

// auditExportFn writes the AuditEvents in ns ("" for all) since cutoff, one
// JSON line each, in the order the API server lists them, and returns how
// many it wrote.
func auditExportFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, format string, cutoff time.Time) (int, error) {
	opts := []sigs_client.ListOption{sigs_client.Limit(auditExportPage)}
	if ns != "" {
		opts = append(opts, sigs_client.InNamespace(ns))
	}
	if pipeline != "" {
		opts = append(opts, sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline})
	}
	enc := json.NewEncoder(w)
	n := 0
	cont := ""
	for {
		var page v1alpha1.AuditEventList
		if err := c.List(ctx, &page, append(opts, sigs_client.Continue(cont))...); err != nil {
			return n, fmt.Errorf("audit export: list auditevents: %w", err)
		}
		for i := range page.Items {
			ev := &page.Items[i]
			if ev.Spec.Timestamp.Time.Before(cutoff) {
				continue
			}
			var rec interface{} = jsonAuditRecord(ev)
			if format == "ocsf" {
				rec = ocsfAuditRecord(ev)
			}
			if err := enc.Encode(rec); err != nil {
				return n, fmt.Errorf("audit export: write: %w", err)
			}
			n++
		}
		if cont = page.Continue; cont == "" {
			return n, nil
		}
	}
}

// auditJSON is one event of --format json.
type auditJSON struct {
	Namespace string                  `json:"namespace"`
	Name      string                  `json:"name"`
	UID       string                  `json:"uid"`
	Spec      v1alpha1.AuditEventSpec `json:"spec"`
}

func jsonAuditRecord(ev *v1alpha1.AuditEvent) auditJSON {
	return auditJSON{Namespace: ev.Namespace, Name: ev.Name, UID: string(ev.UID), Spec: ev.Spec}
}

// ocsfAuditRecord maps ev to an OCSF 1.3.0 API Activity event. What OCSF has
// no place for (the actor's kind, sponsor, via and asserted fields, the
// outcome, the request) is under unmapped.kardinal.
func ocsfAuditRecord(ev *v1alpha1.AuditEvent) map[string]interface{} {
	activity, activityName := ocsfActivity(ev)
	status, statusName := 99, ev.Spec.Outcome
	switch ev.Spec.Outcome {
	case "Success":
		status = 1
	case "Failure":
		status = 2
	}
	rec := map[string]interface{}{
		"category_uid":  ocsfCategoryUID,
		"category_name": "Application Activity",
		"class_uid":     ocsfClassUID,
		"class_name":    "API Activity",
		"activity_id":   activity,
		"activity_name": activityName,
		"type_uid":      ocsfClassUID*100 + activity,
		"severity_id":   1,
		"severity":      "Informational",
		"time":          ev.Spec.Timestamp.UnixMilli(),
		"status_id":     status,
		"status":        statusName,
		"message":       ev.Spec.Message,
		"metadata": map[string]interface{}{
			"version": ocsfVersion,
			"uid":     string(ev.UID),
			"product": map[string]interface{}{"name": "kardinal-promoter", "vendor_name": "kardinal-promoter"},
		},
		"api": map[string]interface{}{
			"operation": ev.Spec.Action,
			"service":   map[string]interface{}{"name": "kardinal-promoter"},
			"request":   map[string]interface{}{"uid": string(ev.UID)},
		},
		"src_endpoint": map[string]interface{}{"svc_name": "kardinal-controller"},
		"actor":        ocsfActor(ev.Spec.Actor),
		"resources":    ocsfResources(ev),
	}
	if r := ev.Spec.Request; r != nil {
		rec["http_request"] = map[string]interface{}{"http_method": r.Method, "url": map[string]interface{}{"path": r.Route}}
	}
	kardinal := map[string]interface{}{"action": ev.Spec.Action, "outcome": ev.Spec.Outcome,
		"namespace": ev.Namespace, "name": ev.Name}
	if a := ev.Spec.Actor; a != nil {
		kardinal["actor"] = a
	}
	if ev.Spec.Request != nil {
		kardinal["request"] = ev.Spec.Request
	}
	rec["unmapped"] = map[string]interface{}{"kardinal": kardinal}
	return rec
}

// ocsfActivity is the API Activity activity of ev: Create, Update, Delete,
// or Other (99) for promotion progress.
func ocsfActivity(ev *v1alpha1.AuditEvent) (int, string) {
	switch ev.Spec.Action {
	case "ApprovalRecorded", "HoldCreated", "RollbackStarted":
		return 1, "Create"
	case "ApprovalRevoked", "HoldReleased":
		return 4, "Delete"
	case "GateOverridden", "BundleRejected":
		return 3, "Update"
	case "APIWrite":
		if r := ev.Spec.Request; r != nil {
			for _, s := range []string{"/bundles", "/promote", "/rollback", "/approvals"} {
				if strings.HasSuffix(r.Route, s) {
					return 1, "Create"
				}
			}
		}
		return 3, "Update"
	}
	return 99, "Other"
}

// ocsfActor maps the actor: the principal is actor.user, the agent
// actor.app_name.
func ocsfActor(a *v1alpha1.Actor) map[string]interface{} {
	if a == nil {
		return map[string]interface{}{"user": map[string]interface{}{"type_id": 0, "type": "Unknown"}}
	}
	typeID, typ := 0, "Unknown"
	switch a.Kind {
	case v1alpha1.ActorHuman:
		typeID, typ = 1, "User"
	case v1alpha1.ActorBot, v1alpha1.ActorController:
		typeID, typ = 3, "System"
	case v1alpha1.ActorAgent:
		typeID, typ = 99, "Agent"
	}
	out := map[string]interface{}{"user": map[string]interface{}{"name": a.Principal, "type_id": typeID, "type": typ}}
	if a.Agent != "" {
		out["app_name"] = a.Agent
	}
	return out
}

func ocsfResources(ev *v1alpha1.AuditEvent) []map[string]interface{} {
	var out []map[string]interface{}
	for _, r := range []struct{ typ, name string }{
		{"Bundle", ev.Spec.BundleName}, {"Pipeline", ev.Spec.PipelineName}, {"Environment", ev.Spec.Environment},
	} {
		if r.name != "" {
			out = append(out, map[string]interface{}{"type": r.typ, "name": r.name, "namespace": ev.Namespace})
		}
	}
	return out
}

// auditExportReference is the detail of the command for its reference page.
const auditExportReference = `Events are written in the order the API server lists them, page by page
(500 a page), not sorted: sort by time downstream (spec.timestamp, OCSF time).
Run it on a schedule more often than AuditEvent retention, and deduplicate by
uid. The OCSF mapping: actor.user.name is the principal, actor.user.type
User (human), System (bot, controller) or Agent, actor.app_name the agent;
api.operation is the AuditEvent action; resources are the Bundle, Pipeline
and environment. The actor's sponsor and asserted fields are in
unmapped.kardinal.actor.`
