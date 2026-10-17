// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/labelvalue"
)

// buildChangeRequestContext is changeRequests.* (docs/design/23-change-requests.md
// section 6): for each per-promotion ChangeRequest template in ns, the
// instance the Graph made from it for this gate's Bundle and environment, as
// {state, ticket, url, stale, timedOut, approvers, verified, providerState}.
// The ChangeRequest reconciler wrote all of it to the instance's status; the
// gate only compares now with status.validUntil.
//
//   - No instance yet (the upstreams are not Verified, or kro has not
//     applied it), or more than one claiming to be it: state "", stale.
//   - A reading past validUntil: state "unknown", stale; the ticket and its
//     link stay, so the reason can name them. An expression reading only
//     state == "approved" fails closed.
//   - A name with no template: no entry; the expression's evaluation error
//     fails the gate closed.
func (r *Reconciler) buildChangeRequestContext(ctx context.Context, ns string, now time.Time,
	bundle, env string) (map[string]interface{}, error) {
	var list kardinalv1alpha1.ChangeRequestList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list changerequests: %w", err)
	}
	instances := map[string][]*kardinalv1alpha1.ChangeRequest{}
	for i := range list.Items {
		cr := &list.Items[i]
		if tmpl, ok := cr.Labels[graph.LabelChangeRequestTemplate]; ok && bundle != "" &&
			cr.Labels[labelBundle] == bundle && cr.Labels[labelEnvironment] == env {
			instances[tmpl] = append(instances[tmpl], cr)
		}
	}
	out := map[string]interface{}{}
	for i := range list.Items {
		cr := &list.Items[i]
		if _, isInstance := cr.Labels[graph.LabelChangeRequestTemplate]; isInstance || !cr.Spec.PerPromotion {
			continue
		}
		var inst *kardinalv1alpha1.ChangeRequest
		if found := instances[labelvalue.Fit(cr.Name)]; len(found) == 1 {
			inst = found[0]
		}
		out[cr.Name] = changeRequestEntry(inst, now)
	}
	return out, nil
}

// changeRequestEntry is one changeRequests.<name> value.
func changeRequestEntry(cr *kardinalv1alpha1.ChangeRequest, now time.Time) map[string]interface{} {
	e := map[string]interface{}{"state": "", "ticket": "", "url": "", "stale": true, "timedOut": false,
		"approvers": []interface{}{}, "verified": false, "verifiedReason": "", "providerState": ""}
	if cr == nil {
		return e
	}
	st := cr.Status
	if st.Ticket != nil {
		e["ticket"], e["url"] = st.Ticket.Key, st.Ticket.URL
	}
	e["timedOut"] = st.TimedOut
	e["providerState"] = st.ProviderState
	// Open mode created the ticket with the correlation id; a link says.
	e["verified"] = st.Ticket != nil && (st.Verified == nil || *st.Verified)
	e["verifiedReason"] = st.VerifiedReason
	approvers := make([]interface{}, 0, len(st.Approvers))
	for _, a := range st.Approvers {
		approvers = append(approvers, a.Name)
	}
	e["approvers"] = approvers
	if st.ValidUntil == nil || !now.Before(st.ValidUntil.Time) {
		if st.Ticket != nil {
			e["state"] = kardinalv1alpha1.ChangeRequestUnknown
		}
		return e
	}
	e["state"], e["stale"] = st.State, false
	return e
}

// changeRequestNotes explains a blocked gate's change requests that expr
// reads: "change request prod-change: ticket CHG0031337 is pending
// (Authorize / requested)", "... has no ticket yet", "... is stale". It only
// adds to status.reason.
func changeRequestNotes(expr string, celCtx map[string]interface{}) string {
	crs, _ := celCtx["changeRequests"].(map[string]interface{})
	var notes []string
	for name, v := range crs {
		e, _ := v.(map[string]interface{})
		if e == nil || !graph.ExprReadsChangeRequest(expr, name) {
			continue
		}
		ticket, _ := e["ticket"].(string)
		state, _ := e["state"].(string)
		raw, _ := e["providerState"].(string)
		stale, _ := e["stale"].(bool)
		var note string
		switch {
		case ticket == "":
			note = fmt.Sprintf("change request %s has no ticket yet", name)
		case stale:
			note = fmt.Sprintf("change request %s: ticket %s was not read since its validUntil (stale)", name, ticket)
		default:
			note = fmt.Sprintf("change request %s: ticket %s is %s", name, ticket, state)
			if raw != "" {
				note += " (" + raw + ")"
			}
		}
		if verified, _ := e["verified"].(bool); ticket != "" && !verified {
			note += ", not verified"
			if why, _ := e["verifiedReason"].(string); why != "" {
				note += " (" + why + ")"
			}
		}
		notes = append(notes, note)
	}
	sort.Strings(notes)
	return strings.Join(notes, "; ")
}

// changeRequestRequests enqueues the gate instances of the ChangeRequest's
// namespace whose expression reads changeRequests.
func (r *Reconciler) changeRequestRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.instanceGateRequests(ctx, "ChangeRequest", "changeRequests", client.InNamespace(obj.GetNamespace()))
}

// changeRequestChanged passes ChangeRequest updates that change what a gate
// reads: the state, the ticket, timedOut, verified, the approvers, or a
// reading that makes a stale one fresh. The reconciler writes observedAt on
// every poll; those writes alone pass nothing. Create and delete events pass.
var changeRequestChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		o, okOld := e.ObjectOld.(*kardinalv1alpha1.ChangeRequest)
		n, okNew := e.ObjectNew.(*kardinalv1alpha1.ChangeRequest)
		if !okOld || !okNew {
			return true
		}
		os, ns := o.Status, n.Status
		key := func(t *kardinalv1alpha1.ChangeRequestTicket) string {
			if t == nil {
				return ""
			}
			return t.Key + "\x00" + t.URL
		}
		verified := func(v *bool) string {
			if v == nil {
				return ""
			}
			return fmt.Sprint(*v)
		}
		approvers := func(a []kardinalv1alpha1.ChangeRequestApprover) string {
			var names []string
			for _, x := range a {
				names = append(names, x.Name)
			}
			return strings.Join(names, "\x00")
		}
		staleToFresh := os.ValidUntil == nil || (ns.ObservedAt != nil && !ns.ObservedAt.Before(os.ValidUntil))
		return o.Generation != n.Generation || os.State != ns.State || key(os.Ticket) != key(ns.Ticket) ||
			os.TimedOut != ns.TimedOut || verified(os.Verified) != verified(ns.Verified) ||
			approvers(os.Approvers) != approvers(ns.Approvers) || os.ProviderState != ns.ProviderState ||
			(ns.ValidUntil != nil && staleToFresh)
	},
}
