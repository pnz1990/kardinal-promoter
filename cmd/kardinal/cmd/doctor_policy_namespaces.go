// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// Controller versions for checkPolicyNamespaces (#2286): from
// policyNamespacesWarnVersion the controller says, in the condition
// PolicyNamespacesShared=False, which spec.policyNamespaces entries name a
// namespace that has not agreed to share its PolicyGates; from
// policyNamespacesRefuseVersion it refuses them (new versions of such a
// Pipeline wait). Each is compared from its lowest pre-release (-0), so a
// release candidate (v0.14.0-rc.1) counts as its release, and so does a
// git-describe version (v0.14.0-3-gabc1234, commits after the tag): at
// least its base. A dev build or a Go pseudo-version is unknown
// (releaseVersion), and warns.
const (
	policyNamespacesWarnVersion   = "v0.13.0"
	policyNamespacesRefuseVersion = "v0.14.0"
)

// atLeast reports whether release version v is release or later, a
// pre-release of release included.
func atLeast(v, release string) bool { return semver.Compare(v, release+"-0") >= 0 }

// policyNamespacesHint is what to do for a namespace doctor names.
const policyNamespacesHint = "Label each namespace kardinal.io/policy-shareable=true, list it in the Helm value " +
	"controller.policyShareableNamespaces, or remove it from spec.policyNamespaces (docs/policy-gates.md#sharing-gates-with-other-namespaces)"

// checkPolicyNamespaces lists the Pipelines whose spec.policyNamespaces
// names a namespace that has not agreed to share its PolicyGates (their
// PolicyNamespacesShared condition is False), so an operator can fix them
// before a controller that refuses them. With a controller from
// policyNamespacesRefuseVersion they are refused (FAIL: their new versions
// wait); before it they are warned of (WARN). A controller older than
// policyNamespacesWarnVersion does not check, so finding none proves
// nothing there (WARN). The Pipelines of every namespace, or of ns when the
// user may not list them cluster-wide; then a clean result is a WARN too
// (only ns was checked). The result is the condition as the controller last
// wrote it: it reads the named Namespaces again every 5 minutes.
func checkPolicyNamespaces(ctx context.Context, client sigs_client.Reader, ns, controller string) doctorResult {
	r := doctorResult{label: "Policy namespaces shared"}
	ver := releaseVersion(controller)
	if ver != "" && !atLeast(ver, policyNamespacesWarnVersion) {
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("controller %s does not check spec.policyNamespaces consent", controller)
		r.hint = fmt.Sprintf("Upgrade to %s first and run kardinal doctor again before %s, which refuses a namespace "+
			"that has not agreed to share its PolicyGates", policyNamespacesWarnVersion, policyNamespacesRefuseVersion)
		return r
	}
	var list v1alpha1.PipelineList
	scope := "all namespaces"
	if err := client.List(ctx, &list); err != nil {
		scope = "namespace " + ns
		if err := client.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
			r.icon, r.warned = doctorWarn, true
			r.detail = fmt.Sprintf("could not list Pipelines: %v", err)
			return r
		}
	}
	var affected []string
	for i := range list.Items {
		p := &list.Items[i]
		if flagged, names := pipelinerec.NotSharedPolicyNamespaces(p); flagged {
			entry := p.Namespace + "/" + p.Name
			if len(names) > 0 {
				entry += " (" + strings.Join(names, ", ") + ")"
			}
			affected = append(affected, entry)
		}
	}
	sort.Strings(affected)
	if len(affected) == 0 {
		r.detail = fmt.Sprintf("every spec.policyNamespaces entry agreed (%d Pipelines, %s, as the controller last checked)",
			len(list.Items), scope)
		if scope != "all namespaces" {
			// A namespaced user's clean result is not the cluster's.
			r.icon, r.warned = doctorWarn, true
			r.detail = fmt.Sprintf("checked %s only: %s", scope, r.detail)
			r.hint = "Run kardinal doctor as a user who may list Pipelines in every namespace to check them all"
			return r
		}
		r.icon = doctorPass
		return r
	}
	refuses := ver != "" && atLeast(ver, policyNamespacesRefuseVersion)
	if refuses {
		r.icon, r.failed = doctorFail, true
		r.detail = fmt.Sprintf("refused, their new versions wait: %s (%s)", strings.Join(affected, "; "), scope)
	} else {
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("%s refuses them: %s (%s)", policyNamespacesRefuseVersion, strings.Join(affected, "; "), scope)
	}
	r.hint = policyNamespacesHint
	return r
}
