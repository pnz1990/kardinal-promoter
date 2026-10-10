// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"context"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// gateReasonUnblocked is the PolicyGate Ready=True reason of a gate that was
// blocking before (written by the PolicyGate reconciler).
const gateReasonUnblocked = "Unblocked"

// Event ranks order events with the same time: a rollback starts before it
// completes, and a PR is opened before the step waits on it.
const (
	rankStart = iota
	rankProgress
	rankEnd
)

// isRollback reports whether b is a rollback Bundle. lifecycle.PlanRollback
// sets both the label and spec.provenance.rollbackOf.
func isRollback(b *v1alpha1.Bundle) bool {
	return b.Labels[lifecycle.LabelRollback] == "true" ||
		(b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "")
}

// rollbackDetail describes what rollback Bundle b restores, for messages.
func rollbackDetail(b *v1alpha1.Bundle) (env, detail string) {
	if b.Spec.Intent != nil {
		env = b.Spec.Intent.TargetEnvironment
	}
	target := ""
	if b.Spec.Provenance != nil {
		target = b.Spec.Provenance.RollbackOf
	}
	switch {
	case target != "" && env != "":
		detail = fmt.Sprintf("restores Bundle %s in %s", target, env)
	case target != "":
		detail = fmt.Sprintf("restores Bundle %s", target)
	case env != "":
		detail = fmt.Sprintf("rolls back %s", env)
	default:
		detail = "is a rollback"
	}
	if from := b.Annotations[lifecycle.AnnotationRollbackFrom]; from != "" {
		detail += fmt.Sprintf(" (rolling back %s)", from)
	}
	return env, detail
}

// qualifyingEvents returns the events that match hook, oldest first, limited
// to the newest maxTrackedEvents.
//
// Events are derived from the current state of Bundles, PolicyGates and
// PromotionSteps (level-triggered): each event exists while the state that
// defines it holds, and its key is deterministic, so the same event found
// again on a later reconcile, or by a restarted controller, has the same key.
func (r *Reconciler) qualifyingEvents(ctx context.Context, hook *v1alpha1.NotificationHook) ([]pendingEvent, error) {
	want := make(map[v1alpha1.NotificationHookEventType]bool, len(hook.Spec.Events))
	for _, e := range hook.Spec.Events {
		want[e] = true
	}
	selector := hook.Spec.PipelineSelector
	var events []pendingEvent
	add := func(ev pendingEvent) {
		if want[ev.eventType] {
			ev.payload.Event = string(ev.eventType)
			events = append(events, ev)
		}
	}

	if want[v1alpha1.NotificationEventBundleVerified] || want[v1alpha1.NotificationEventBundleFailed] ||
		want[v1alpha1.NotificationEventBundleSuperseded] || want[v1alpha1.NotificationEventBundleRollbackStarted] ||
		want[v1alpha1.NotificationEventBundleRolledBack] {
		if err := r.bundleEvents(ctx, hook.Namespace, selector, add); err != nil {
			return nil, err
		}
	}
	if want[v1alpha1.NotificationEventPolicyGateBlocked] || want[v1alpha1.NotificationEventPolicyGateUnblocked] {
		if err := r.gateEvents(ctx, hook.Namespace, selector, add); err != nil {
			return nil, err
		}
	}
	if want[v1alpha1.NotificationEventPromotionStepFailed] || want[v1alpha1.NotificationEventPromotionStepPROpened] ||
		want[v1alpha1.NotificationEventPromotionStepWaitingForApproval] {
		if err := r.stepEvents(ctx, hook.Namespace, selector, add); err != nil {
			return nil, err
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		if events[i].rank != events[j].rank {
			return events[i].rank < events[j].rank
		}
		return events[i].eventKey < events[j].eventKey
	})
	if len(events) > maxTrackedEvents {
		events = events[len(events)-maxTrackedEvents:]
	}
	return events, nil
}

// bundleEvents: Bundle.Verified, Bundle.Failed, Bundle.Superseded, and for
// rollback Bundles Bundle.RollbackStarted and Bundle.RolledBack. The pipeline
// is matched on spec.pipeline: Bundles created by `kardinal create bundle` or
// kubectl do not carry the kardinal.io/pipeline label.
func (r *Reconciler) bundleEvents(ctx context.Context, ns, selector string, add func(pendingEvent)) error {
	var bundles v1alpha1.BundleList
	if selector != "" {
		// One Pipeline's Bundles, through the spec.pipeline index (#1654).
		items, err := lifecycle.ListPipelineBundles(ctx, r.Client, ns, selector)
		if err != nil {
			return err
		}
		bundles.Items = items
	} else if err := r.List(ctx, &bundles, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	for i := range bundles.Items {
		b := &bundles.Items[i]
		if selector != "" && b.Spec.Pipeline != selector {
			continue
		}
		base := pendingEvent{
			at:      b.CreationTimestamp.Time,
			payload: notificationPayload{Pipeline: b.Spec.Pipeline, Bundle: b.Name},
		}
		phase := b.Status.Phase
		bundleEvent := func(t v1alpha1.NotificationHookEventType, rank int, msg string) {
			ev := base
			ev.eventType, ev.rank = t, rank
			ev.eventKey = string(t) + "/" + b.Name
			ev.payload.Message = msg
			add(ev)
		}
		switch phase {
		case "Verified":
			bundleEvent(v1alpha1.NotificationEventBundleVerified, rankEnd, fmt.Sprintf("Bundle %s is Verified", b.Name))
		case "Failed":
			bundleEvent(v1alpha1.NotificationEventBundleFailed, rankEnd, fmt.Sprintf("Bundle %s is Failed", b.Name))
		case "Superseded":
			bundleEvent(v1alpha1.NotificationEventBundleSuperseded, rankEnd, fmt.Sprintf("Bundle %s is Superseded", b.Name))
		}
		if !isRollback(b) {
			continue
		}
		env, detail := rollbackDetail(b)
		base.payload.Environment = env
		// Started once it promotes, also when the hook first sees it
		// already finished.
		if phase == "Promoting" || phase == "Verified" || phase == "Failed" {
			bundleEvent(v1alpha1.NotificationEventBundleRollbackStarted, rankStart,
				fmt.Sprintf("Rollback Bundle %s started: it %s", b.Name, detail))
		}
		if phase == "Verified" {
			bundleEvent(v1alpha1.NotificationEventBundleRolledBack, rankEnd,
				fmt.Sprintf("Rollback Bundle %s is Verified: it %s", b.Name, detail))
		}
	}
	return nil
}

// gateEvents: PolicyGate.Blocked, one event per blocking episode of a gate
// instance, and PolicyGate.Unblocked, one per allowed episode that follows a
// block. Templates (no kardinal.io/bundle label) are never evaluated, so they
// neither block nor unblock anything. The episode is identified by the Ready
// condition's lastTransitionTime, which only moves when the gate flips;
// status.lastEvaluatedAt moves on every status write (a changed result, a
// step waiting for a fresh one, or the --gate-status-heartbeat).
func (r *Reconciler) gateEvents(ctx context.Context, ns, selector string, add func(pendingEvent)) error {
	var gates v1alpha1.PolicyGateList
	if err := r.List(ctx, &gates, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list policygates: %w", err)
	}
	for i := range gates.Items {
		g := &gates.Items[i]
		if g.Labels[labelBundle] == "" {
			continue
		}
		if selector != "" && g.Labels[labelPipeline] != selector {
			continue
		}
		cond := meta.FindStatusCondition(g.Status.Conditions, gateReadyCondition)
		if cond == nil {
			continue
		}
		ev := pendingEvent{
			at: cond.LastTransitionTime.Time,
			payload: notificationPayload{
				Pipeline:    g.Labels[labelPipeline],
				Bundle:      g.Labels[labelBundle],
				Environment: g.Labels[labelEnvironment],
			},
		}
		episode := "/" + g.Name + "/" + cond.LastTransitionTime.UTC().Format(time.RFC3339)
		switch {
		case !g.Status.Ready && cond.Status == metav1.ConditionFalse:
			ev.eventType = v1alpha1.NotificationEventPolicyGateBlocked
			ev.legacyKey = string(ev.eventType) + "/" + g.Name
			ev.payload.Message = fmt.Sprintf("PolicyGate %s is blocking: %s", g.Name, g.Status.Reason)
		case g.Status.Ready && cond.Status == metav1.ConditionTrue && cond.Reason == gateReasonUnblocked:
			ev.eventType = v1alpha1.NotificationEventPolicyGateUnblocked
			ev.payload.Message = fmt.Sprintf("PolicyGate %s is no longer blocking: %s", g.Name, g.Status.Reason)
		default:
			continue
		}
		ev.eventKey = string(ev.eventType) + episode
		add(ev)
	}
	return nil
}

// stepEvents: PromotionStep.Failed, PromotionStep.PROpened (status.prURL is
// set) and PromotionStep.WaitingForApproval (state WaitingForMerge). A step
// opens at most one PR and waits for its merge once, so the step name is the
// key.
func (r *Reconciler) stepEvents(ctx context.Context, ns, selector string, add func(pendingEvent)) error {
	var steps v1alpha1.PromotionStepList
	if err := r.List(ctx, &steps, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list promotionsteps: %w", err)
	}
	for i := range steps.Items {
		ps := &steps.Items[i]
		if selector != "" && ps.Spec.PipelineName != selector {
			continue
		}
		base := pendingEvent{
			at: ps.CreationTimestamp.Time,
			payload: notificationPayload{
				Pipeline:    ps.Spec.PipelineName,
				Bundle:      ps.Spec.BundleName,
				Environment: ps.Spec.Environment,
			},
		}
		stepEvent := func(t v1alpha1.NotificationHookEventType, rank int, msg string) {
			ev := base
			ev.eventType, ev.rank = t, rank
			ev.eventKey = string(t) + "/" + ps.Name
			ev.payload.Message = msg
			add(ev)
		}
		if ps.Status.State == "Failed" {
			stepEvent(v1alpha1.NotificationEventPromotionStepFailed, rankEnd,
				fmt.Sprintf("PromotionStep %s failed: %s", ps.Name, ps.Status.Message))
		}
		if ps.Status.PRURL != "" {
			base.payload.PRURL = ps.Status.PRURL
			stepEvent(v1alpha1.NotificationEventPromotionStepPROpened, rankStart,
				fmt.Sprintf("PromotionStep %s opened a pull request for %s: %s", ps.Name, ps.Spec.Environment, ps.Status.PRURL))
		}
		if ps.Status.State == "WaitingForMerge" {
			msg := fmt.Sprintf("PromotionStep %s is waiting for approval: merge its pull request to promote to %s",
				ps.Name, ps.Spec.Environment)
			if ps.Status.PRURL != "" {
				msg += ": " + ps.Status.PRURL
			}
			stepEvent(v1alpha1.NotificationEventPromotionStepWaitingForApproval, rankProgress, msg)
		}
	}
	return nil
}
