// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"fmt"
	"sort"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// Translator handles the full pipeline-to-graph creation flow:
// reads PolicyGates from the cluster, calls graph.Builder, creates the Graph CR.
type Translator struct {
	graphClient *graph.GraphClient
	builder     *graph.Builder
	k8s         client.Reader // for listing PolicyGates
	policyNS    []string      // namespaces to scan for org-level PolicyGates
	log         zerolog.Logger

	// identity provisions the ServiceAccount kro impersonates for the Graph.
	// Nil skips provisioning (unit tests, or an operator-managed identity).
	identity *graph.IdentityProvisioner
	// mapper, when set, says which kinds the cluster serves (servedKind): kro
	// resolves the CRD schema of every static-GVK node when it compiles a
	// Graph, so one missing CRD would reject the whole Graph.
	mapper meta.RESTMapper
	// providers checks a Pipeline's spec.git.providerRef (WithProviders).
	providers *scm.Registry
	// controllerSecret is the controller's own SCM token Secret: a Pipeline
	// whose git.secretRef names it gets no Graph (WithControllerSecret), so
	// no RenderRun mounts it in a render Job.
	controllerSecret *scm.ControllerTokenSecret
}

// WithControllerSecret sets the controller's own SCM token Secret, which no
// Pipeline's Graph may use.
func (t *Translator) WithControllerSecret(c *scm.ControllerTokenSecret) *Translator {
	t.controllerSecret = c
	return t
}

// New creates a new Translator.
// policyNS is the list of org policy namespaces: they are always scanned for
// PolicyGates, and only gates in them may grant a skip permission. Empty
// means graph.DefaultPolicyNamespace.
func New(
	graphClient *graph.GraphClient,
	builder *graph.Builder,
	k8s client.Reader,
	policyNS []string,
	log zerolog.Logger,
) *Translator {
	if len(policyNS) == 0 {
		policyNS = []string{graph.DefaultPolicyNamespace}
	}
	return &Translator{
		graphClient: graphClient,
		builder:     builder,
		k8s:         k8s,
		policyNS:    policyNS,
		log:         log,
	}
}

// WithIdentity sets the provisioner that ensures the Graph ServiceAccount
// and its RoleBindings exist before each Graph is created.
func (t *Translator) WithIdentity(p *graph.IdentityProvisioner) *Translator {
	t.identity = p
	return t
}

// WithRESTMapper sets the mapper servedKind asks which kinds the cluster
// serves (for example AnalysisRuns without Argo Rollouts).
func (t *Translator) WithRESTMapper(m meta.RESTMapper) *Translator {
	t.mapper = m
	return t
}

// Translate translates a Pipeline+Bundle pair to a Graph CR and applies it:
// it creates the Graph, or updates the spec of the Graph this Bundle already
// controls, and refuses a Graph of the same name controlled by another owner
// (graph.ErrGraphOwnedByOther). Returns the name of the Graph.
func (t *Translator) Translate(ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline,
	bundle *kardinalv1alpha1.Bundle) (string, error) {
	log := zerolog.Ctx(ctx).With().
		Str("pipeline", pipeline.Name).
		Str("bundle", bundle.Name).
		Logger()
	ctx = log.WithContext(ctx)

	// The controller's token Secret is never a Pipeline's git Secret: the
	// Graph's RenderRuns would mount it in a render Job. Permanent until the
	// Pipeline changes.
	if err := t.controllerSecret.Refuses(pipeline); err != nil {
		return "", graph.Invalid(fmt.Errorf("translator.Translate: %w", err))
	}

	// Collect PolicyGates from the org policy namespaces, the Pipeline's
	// spec.policyNamespaces, and the Pipeline namespace.
	gates, err := t.collectGates(ctx, pipeline)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: collect gates: %w", err)
	}

	log.Debug().Int("gates", len(gates)).Msg("collected policy gates")

	// Argo Rollouts analysis templates named by spec.verification
	// (analysis.go). An API error is retried; a missing template or kind is
	// recorded and fails Build for an environment that verifies.
	analyses, err := t.collectAnalyses(ctx, pipeline)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: collect analysis templates: %w", err)
	}
	// Per-promotion MetricChecks of the Pipeline namespace: the builder adds
	// an instance for each one a gate reads.
	metricChecks, err := t.collectMetricTemplates(ctx, pipeline.Namespace)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: collect metric checks: %w", err)
	}
	// Per-promotion ChangeRequests of the Pipeline namespace: the builder adds
	// an instance for each one a gate reads, in the environments it applies to.
	changeRequests, err := t.collectChangeRequestTemplates(ctx, pipeline.Namespace)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: collect change requests: %w", err)
	}

	// Build validates the input (names, skip permissions, node IDs) and
	// returns the Graph spec. Only the controller's org namespaces count as
	// org policy: a Pipeline's own spec.policyNamespaces adds gates but can
	// never grant a skip.
	// Build, identity.Ensure and graphClient.Create prefix their own errors
	// ("build: ", "graph identity: ", "graph.Create "), so they are wrapped
	// with the Translate context only.
	// spec.git.providerRef: resolved once here and written into every
	// PromotionStep template, so the steps and their PRStatuses keep the
	// provider they started with (DESIGN §10 D3, no Watch node). A provider
	// that is missing or refuses this Pipeline is a TranslationError, retried
	// until it is created or changed.
	providerID, err := t.resolveProvider(ctx, pipeline)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: %w", err)
	}

	shape, err := t.existingShape(ctx, pipeline, bundle)
	if err != nil {
		return "", fmt.Errorf("translator.Translate: %w", err)
	}
	result, err := t.builder.Build(graph.BuildInput{
		Pipeline:         pipeline,
		Bundle:           bundle,
		PolicyGates:      gates,
		PolicyNamespaces: t.policyNS,
		ScmProvider:      providerID,
		Analyses:         analyses,
		Shape:            shape,
		MetricChecks:     metricChecks,
		ChangeRequests:   changeRequests,
	})
	if err != nil {
		return "", &BuildError{Err: fmt.Errorf("translator.Translate: %w", err), Gates: gates}
	}

	log.Debug().
		Int("nodes", result.NodeCount).
		Str("graph", result.Graph.Name).
		Msg("graph spec built")

	if err := graph.ValidateNodeIDs(result.Graph.Spec.Nodes); err != nil {
		return "", fmt.Errorf("translator.Translate: %w", err)
	}
	// A Graph is one etcd object (1.5 MiB). Refuse it here, with a reason on
	// the Bundle, instead of letting the API server fail every write
	// (ledger gap G10).
	if err := graph.CheckSize(result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: %w", err)
	}

	// kro applies the Graph as spec.serviceAccountName; it must exist and be
	// bound before kro's first reconcile or every apply is forbidden. A Graph
	// reads nothing outside its own namespace (the health ref nodes and their
	// reader RoleBindings are gone, #1283), so the applier binding is all it
	// needs.
	if err := t.identity.Ensure(ctx, result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: %w", err)
	}

	if err := t.graphClient.Create(ctx, result.Graph); err != nil {
		return "", fmt.Errorf("translator.Translate: %w", err)
	}

	log.Info().
		Str("graph", result.Graph.Name).
		Int("nodes", len(result.Graph.Spec.Nodes)).
		Msg("translation complete: graph applied")

	return result.Graph.Name, nil
}

// existingShape returns the shape of the Bundle's Graph when it exists, so a
// re-translation keeps it. It returns "" when the Bundle has no Graph yet.
// Switching the shape of a Graph in flight would make kro prune every
// PromotionStep the old shape's nodes created.
//
// The shape is read from the Graph's spec (graph.ShapeOf: a PromotionSteps
// collection node means compact), not from its kardinal.io/graph-shape label,
// which anyone may edit: the label is informational, and the builder writes
// it again from the shape it keeps.
func (t *Translator) existingShape(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline,
	bundle *kardinalv1alpha1.Bundle) (string, error) {
	g, err := t.graphClient.Get(ctx, pipeline.Namespace, graph.GraphNameFrom(pipeline.Name, bundle.Name))
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the Bundle's Graph shape: %w", err)
	}
	if !metav1.IsControlledBy(g, bundle) {
		// Another owner's Graph: Create refuses it (ErrGraphOwnedByOther).
		return "", nil
	}
	return graph.ShapeOf(g), nil
}

// collectMetricTemplates returns the MetricChecks in ns with
// spec.perPromotion, sorted by name so the rendered Graph is stable.
func (t *Translator) collectMetricTemplates(ctx context.Context, ns string) ([]kardinalv1alpha1.MetricCheck, error) {
	var list kardinalv1alpha1.MetricCheckList
	if err := t.k8s.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list metricchecks in %s: %w", ns, err)
	}
	var out []kardinalv1alpha1.MetricCheck
	for _, mc := range list.Items {
		if mc.Spec.PerPromotion {
			out = append(out, mc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// collectChangeRequestTemplates returns the ChangeRequests in ns with
// spec.perPromotion, sorted by name.
func (t *Translator) collectChangeRequestTemplates(ctx context.Context, ns string) ([]kardinalv1alpha1.ChangeRequest, error) {
	var list kardinalv1alpha1.ChangeRequestList
	if err := t.k8s.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list changerequests in %s: %w", ns, err)
	}
	var out []kardinalv1alpha1.ChangeRequest
	for _, cr := range list.Items {
		if cr.Spec.PerPromotion {
			out = append(out, cr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// servedKind reports whether mapper, the cluster's, serves apiVersion's
// kind. Without a mapper every kind is assumed served. Only a "no match"
// from discovery means the kind is not served; any other discovery error
// (a timeout, a refused or forbidden request) is returned, so a discovery
// outage is retried instead of read as Argo Rollouts not installed (#2171).
func servedKind(mapper meta.RESTMapper, apiVersion, kind string) (bool, error) {
	if mapper == nil {
		return true, nil
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return false, nil
	}
	_, err = mapper.RESTMapping(gv.WithKind(kind).GroupKind(), gv.Version)
	switch {
	case err == nil:
		return true, nil
	case meta.IsNoMatchError(err):
		return false, nil
	}
	return false, fmt.Errorf("discover %s %s: %w", apiVersion, kind, err)
}

// collectGates lists PolicyGate templates from the org policy namespaces
// (t.policyNS), the Pipeline's spec.policyNamespaces, and the Pipeline's
// namespace. De-duplicates by name+namespace and skips gate instances
// (labelled kardinal.io/gate-template) and every gate with spec.generated.
//
// spec.policyNamespaces only adds namespaces. The org namespaces are always
// scanned, so a Pipeline cannot opt out of org policy by listing other
// namespaces.
func (t *Translator) collectGates(ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline, opts ...client.ListOption) ([]kardinalv1alpha1.PolicyGate, error) {
	seen := make(map[string]bool)
	var gates []kardinalv1alpha1.PolicyGate

	var namespaces []string
	scanned := map[string]bool{}
	for _, list := range [][]string{t.policyNS, pipeline.Spec.PolicyNamespaces, {pipeline.Namespace}} {
		for _, ns := range list {
			if ns != "" && !scanned[ns] {
				scanned[ns] = true
				namespaces = append(namespaces, ns)
			}
		}
	}

	for _, ns := range namespaces {
		var list kardinalv1alpha1.PolicyGateList
		if err := t.k8s.List(ctx, &list, append([]client.ListOption{client.InNamespace(ns)}, opts...)...); err != nil {
			return nil, fmt.Errorf("list policy gates in %s: %w", ns, err)
		}
		for _, g := range list.Items {
			// Gate instances live in the pipeline namespace too and would
			// otherwise be re-stamped as templates for the next Bundle.
			if !IsTemplate(&g) {
				continue
			}
			key := g.Namespace + "/" + g.Name
			if !seen[key] {
				seen[key] = true
				gates = append(gates, g)
			}
		}
	}

	// The cached List comes back in map order. Sort so the rendered Graph is
	// identical between translations and a no-op Pipeline change does not
	// produce a spurious Graph update.
	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Namespace != gates[j].Namespace {
			return gates[i].Namespace < gates[j].Namespace
		}
		return gates[i].Name < gates[j].Name
	})
	return gates, nil
}

// CollectGates returns the PolicyGate templates Translate passes to the Graph
// builder for pipeline. policyNS is the controller's --policy-namespaces list
// (nil means the controller default). The CLI uses it so `kardinal policy
// simulate` selects exactly the gates the controller would. opts narrow each
// namespace's List, for example to a cache index of templates (IsTemplate);
// the result is the same, as the templates are selected again.
func CollectGates(ctx context.Context, k8s client.Reader, policyNS []string,
	pipeline *kardinalv1alpha1.Pipeline, opts ...client.ListOption) ([]kardinalv1alpha1.PolicyGate, error) {
	return New(nil, nil, k8s, policyNS, zerolog.Nop()).collectGates(ctx, pipeline, opts...)
}

// IsTemplate reports whether g is a PolicyGate template: one a person or a
// policy repository wrote, which the Translator copies into each Bundle's
// Graph. A gate instance (it has kardinal.io/gate-template) and every gate
// with spec.generated (instances, freeze gates) is not: only such a gate may
// have a name too long for the gate-template label, so it must never be a
// template, whoever set the field.
func IsTemplate(g *kardinalv1alpha1.PolicyGate) bool {
	_, isInstance := g.Labels["kardinal.io/gate-template"]
	return !isInstance && !g.Spec.Generated
}
