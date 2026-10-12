// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// FailedVersion returns status.failedVersion of the Pipeline named pipeline:
// why its newest Bundle failed, or nil when the newest Bundle is not Failed.
// It reads only the Bundles' status (their InvalidSpec, Failed and Ready
// conditions and status.environments), written by the Bundle reconciler.
// Display only: no controller decision reads it.
func FailedVersion(pipeline string, bundles []kardinalv1alpha1.Bundle) *kardinalv1alpha1.PipelineFailedVersion {
	var newest *kardinalv1alpha1.Bundle
	for i := range bundles {
		b := &bundles[i]
		if b.Spec.Pipeline != pipeline {
			continue
		}
		if newest == nil || newest.CreationTimestamp.Before(&b.CreationTimestamp) ||
			(newest.CreationTimestamp.Equal(&b.CreationTimestamp) && newest.Name < b.Name) {
			newest = b
		}
	}
	if newest == nil || newest.Status.Phase != "Failed" {
		return nil
	}
	f := &kardinalv1alpha1.PipelineFailedVersion{Bundle: newest.Name, Version: scm.BundleVersion(newest.Spec), Reason: "Failed"}
	conds := newest.Status.Conditions
	switch c := meta.FindStatusCondition(conds, "InvalidSpec"); {
	case c != nil && c.Status == "True":
		f.Reason, f.Message = c.Reason, c.Message
	default:
		if c := meta.FindStatusCondition(conds, "Failed"); c != nil && c.Status == "True" {
			f.Reason, f.Message = c.Reason, c.Message
		} else if c := meta.FindStatusCondition(conds, "Ready"); c != nil {
			f.Message = strings.TrimPrefix(c.Message, "promotion failed: ")
		}
	}
	f.Message = failureLine(f.Message)
	if c := meta.FindStatusCondition(conds, "Ready"); c != nil && !c.LastTransitionTime.IsZero() {
		t := c.LastTransitionTime
		f.Since = &t
	}
	for _, e := range newest.Status.Environments {
		if e.Phase == "Failed" {
			f.Environment = e.Name
			break
		}
	}
	return f
}

// failedVersionEqual reports whether a and b say the same; Since aside it
// is compared in full.
func failedVersionEqual(a, b *kardinalv1alpha1.PipelineFailedVersion) bool {
	if a == nil || b == nil {
		return a == b
	}
	as, bs := *a, *b
	as.Since, bs.Since = nil, nil
	return as == bs && a.Since.Equal(b.Since)
}

// WithFailure is summary with the failed newest version added when no
// environment shows it (it failed before any step):
// "...; sha-9349a3f (demo-bj579) failed before it started (GraphRejected)".
// A version that failed in an environment is in summary already ("failed
// in uat").
func WithFailure(summary string, f *kardinalv1alpha1.PipelineFailedVersion, envs []kardinalv1alpha1.PipelineEnvironmentStatus) string {
	if f == nil {
		return summary
	}
	for _, e := range envs {
		if e.Bundle == f.Bundle || e.IncomingBundle == f.Bundle {
			return summary
		}
	}
	where := " failed before it started"
	if f.Environment != "" {
		where = " failed in " + f.Environment
	}
	clause := VersionLabel(f.Version, f.Bundle) + where + " (" + f.Reason + ")"
	if summary == "" {
		return upperFirst(clause)
	}
	return summary + "; " + clause
}

// VersionLabel is a version as a person reads it: the tag, then the Bundle
// ("1.5.0 (my-app-x7k2p)"); the Bundle alone when the version says nothing
// more.
func VersionLabel(version, bundle string) string {
	switch {
	case bundle == "":
		return version
	case version == "" || version == bundle:
		return bundle
	}
	return version + " (" + bundle + ")"
}

// failureMessageMax bounds status.failedVersion.message (runes), so a
// compiler error cannot fill the CLI, the UI and the Pipeline object.
const failureMessageMax = 300

// failureLine is a condition message as status.failedVersion keeps it: its
// first line only (kro's CEL errors go on with the expression echoed and a
// caret line under it, " | (A + B).filter(...)" and " | ....^"), trimmed,
// and at most failureMessageMax runes, cut with "…".
func failureLine(msg string) string {
	line, _, _ := strings.Cut(msg, "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > failureMessageMax {
		line = strings.TrimSpace(string(r[:failureMessageMax-1])) + "…"
	}
	return line
}
