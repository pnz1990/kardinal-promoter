// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// LabelBudgetExemptions is on an AgentIdentity exactly while it has
// spec.budgetExemptions entries: the chart's actor-budgets admission policy
// checks their stamps by it (docs/design/24-agent-budgets.md section 12).
const LabelBudgetExemptions = "kardinal.io/budget-exemptions"

// DefaultBudgetExemption is how long a budget exemption lasts when the
// request names no expiry. The controller caps it at 24 hours from when it
// first sees it.
const DefaultBudgetExemption = time.Hour

// BudgetExemptRequest lets one Pipeline environment, optionally one
// version, through its agent's budget (kardinal override --budget).
type BudgetExemptRequest struct {
	Namespace, Pipeline, Environment string
	// Bundle, when set, names the version, and through it the agent;
	// otherwise the agent is the one whose step waits in Environment.
	Bundle string
	Reason string
	// ExpiresIn is how long it lasts; zero is DefaultBudgetExemption.
	ExpiresIn time.Duration
	// CreatedBy and Actor are the requester, as admission checks them.
	CreatedBy string
	Actor     *v1alpha1.Actor
	Now       time.Time
}

// ExemptFromBudget adds a budget exemption to the AgentIdentity of the agent
// whose version waits in req's environment (or of req.Bundle's agent), with
// the label the admission policy wants, and returns the agent and the
// exemption's name. Expired exemptions are removed on the way, so the list
// stays under its limit of 32.
func ExemptFromBudget(ctx context.Context, c client.Client, req BudgetExemptRequest) (string, string, error) {
	switch {
	case req.Reason == "":
		return "", "", fmt.Errorf("a budget exemption needs a reason: %w", ErrInvalid)
	case req.Namespace == "" || req.Pipeline == "" || req.Environment == "":
		return "", "", fmt.Errorf("a budget exemption names a pipeline, its namespace and an environment: %w", ErrInvalid)
	case req.ExpiresIn < 0:
		return "", "", fmt.Errorf("an exemption cannot expire before it starts: %w", ErrInvalid)
	}
	d := req.ExpiresIn
	if d == 0 {
		d = DefaultBudgetExemption
	}
	agent, err := budgetAgent(ctx, c, req)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(req.Namespace + "\x00" + req.Pipeline + "\x00" + req.Environment + "\x00" + req.Bundle +
		"\x00" + req.CreatedBy + "\x00" + req.Now.UTC().Format(time.RFC3339Nano)))
	name := "x-" + hex.EncodeToString(sum[:])[:12]
	ex := v1alpha1.AgentBudgetExemption{Name: name, Namespace: req.Namespace, Pipeline: req.Pipeline,
		Environment: req.Environment, Bundle: req.Bundle, Reason: req.Reason, CreatedBy: req.CreatedBy, Actor: req.Actor,
		ExpiresAt: metav1.NewTime(req.Now.Add(d).UTC().Truncate(time.Second))}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var id v1alpha1.AgentIdentity
		if err := c.Get(ctx, types.NamespacedName{Name: agent}, &id); err != nil {
			return err
		}
		base := id.DeepCopy()
		kept := make([]v1alpha1.AgentBudgetExemption, 0, len(id.Spec.BudgetExemptions)+1)
		for _, e := range id.Spec.BudgetExemptions {
			if req.Now.Before(e.ExpiresAt.Time) {
				kept = append(kept, e)
			}
		}
		kept = append(kept, ex)
		id.Spec.BudgetExemptions = kept
		if id.Labels == nil {
			id.Labels = map[string]string{}
		}
		id.Labels[LabelBudgetExemptions] = "true"
		return c.Patch(ctx, &id, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return agent, "", fmt.Errorf("add a budget exemption to AgentIdentity %s: %w", agent, err)
	}
	return agent, name, nil
}

// budgetAgent is the agent the exemption is for: req.Bundle's, else the
// one whose step of the Pipeline waits for its budget in the environment.
func budgetAgent(ctx context.Context, c client.Client, req BudgetExemptRequest) (string, error) {
	if req.Bundle != "" {
		var b v1alpha1.Bundle
		if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Bundle}, &b); err != nil {
			return "", fmt.Errorf("get bundle %s: %w", req.Bundle, err)
		}
		if b.Spec.Actor == nil || b.Spec.Actor.Kind != v1alpha1.ActorAgent || b.Spec.Actor.Agent == "" {
			return "", fmt.Errorf("version %s was not made by an agent, so no budget holds it: %w", req.Bundle, ErrInvalid)
		}
		return b.Spec.Actor.Agent, nil
	}
	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps, client.InNamespace(req.Namespace),
		client.MatchingLabels{"kardinal.io/pipeline": req.Pipeline}); err != nil {
		return "", fmt.Errorf("list the steps of pipeline %s: %w", req.Pipeline, err)
	}
	agents := map[string]bool{}
	for _, s := range steps.Items {
		target := s.Spec.Environment == req.Environment || s.Labels["kardinal.io/fleet"] == req.Environment
		if target && s.Status.Budget != nil && s.Status.Budget.State == "Waiting" {
			agents[s.Status.Budget.Agent] = true
		}
	}
	switch len(agents) {
	case 0:
		return "", fmt.Errorf("no version of %s waits for an agent's budget in %s: name one with --bundle: %w",
			req.Pipeline, req.Environment, ErrNotFound)
	case 1:
		for a := range agents {
			return a, nil
		}
	}
	names := make([]string, 0, len(agents))
	for a := range agents {
		names = append(names, a)
	}
	sort.Strings(names)
	return "", fmt.Errorf("versions of agents %s wait for their budgets in %s: name one with --bundle: %w",
		strings.Join(names, ", "), req.Environment, ErrInvalid)
}
