// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// APIServer is the API server's ValidatingAdmissionPolicy admission plugin
// (k8s.io/apiserver/pkg/admission/plugin/policy/validating), fed policies,
// bindings and namespaces through informers and params (the AgentIndex)
// through a dynamic client, as in a cluster: a policy decides only through a
// binding, with the binding's validationActions (an Audit-only binding denies
// nothing), matchResources and paramRef, and the policy's matchConstraints.
type APIServer struct {
	plugin *validating.Plugin
	scheme *k8sruntime.Scheme
	kube   *kubefake.Clientset
	dyn    *dynfake.FakeDynamicClient

	mu         sync.Mutex
	seq        int
	namespaces map[string]bool
	// matched are the resources some policy's matchConstraints name.
	matched map[string]bool
	// policies are the names of the policies served, but the probe's.
	policies []string
}

// The resources the plugin knows, with their kinds and scope: the kinds
// the chart's policies match.
var apiResources = []struct {
	group, resource, kind string
	cluster               bool
}{
	{"kardinal.io", "bundles", "Bundle", false}, {"kardinal.io", "approvals", "Approval", false},
	{"kardinal.io", "policygates", "PolicyGate", false}, {"kardinal.io", "auditevents", "AuditEvent", false},
	{"kardinal.io", "pipelines", "Pipeline", false}, {"kardinal.io", "promotionsteps", "PromotionStep", false},
	{"kardinal.io", "prstatuses", "PRStatus", false}, {"kardinal.io", "hookruns", "HookRun", false},
	{"kardinal.io", "renderruns", "RenderRun", false}, {"kardinal.io", "imageverifications", "ImageVerification", false},
	{"kardinal.io", "metricchecks", "MetricCheck", false},
	{"kardinal.io", "agentidentities", "AgentIdentity", true}, {"kardinal.io", "agentindexes", "AgentIndex", true},
	{"kardinal.io", "changewindows", "ChangeWindow", true}, {"kardinal.io", "verdicts", "Verdict", false},
	{"kardinal.io", "agentruns", "AgentRun", false}, {"kardinal.io", "steptemplates", "StepTemplate", true},
	{"argoproj.io", "applications", "Application", false}, {"argoproj.io", "analysisruns", "AnalysisRun", false},
	{"kustomize.toolkit.fluxcd.io", "kustomizations", "Kustomization", false},
	{"helm.toolkit.fluxcd.io", "helmreleases", "HelmRelease", false},
	{"source.toolkit.fluxcd.io", "gitrepositories", "GitRepository", false},
}

// apiVersion is the version the scenario cluster serves group at.
func apiVersion(group string) string {
	switch group {
	case "kustomize.toolkit.fluxcd.io", "source.toolkit.fluxcd.io", "helm.toolkit.fluxcd.io":
		return "v1"
	}
	return "v1alpha1"
}

// GVR is the kardinal.io/v1alpha1 resource.
func GVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: resource}
}

var agentIndexGVR = GVR("agentindexes")

var quietKlog sync.Once

// The probe policy: it tells when the plugin's param informer holds the
// AgentIndex SetAgentIndex wrote (seq annotation), or none.
const (
	probePolicy = "scenario-agent-index-probe"
	probeLabel  = "scenario.kardinal.io/probe"
	seqKey      = "scenario.kardinal.io/seq"
	noParams    = "no params found"
)

// readyTimeout is how long NewAPIServer waits for the plugin to evaluate
// its first request, and SetAgentIndex for it to see a param. Compiling
// every chart policy's CEL takes seconds under -race on a loaded CI runner
// (#2264): the wait is on what the plugin does, this only bounds a plugin
// that never gets there.
const readyTimeout = 60 * time.Second

// probeBundle is a Bundle the probe policy evaluates, carrying seq.
func probeBundle(seq string) map[string]interface{} {
	return map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
		"metadata": map[string]interface{}{"namespace": ReleaseNamespace, "name": "probe",
			"labels": map[string]interface{}{probeLabel: "true"}, "annotations": map[string]interface{}{seqKey: seq}},
		"spec": map[string]interface{}{"type": "image", "pipeline": "probe"}}
}

// NewAPIServer starts the plugin with objs (namespaces, policies, bindings)
// served by its informers. authz nil allows everything.
func NewAPIServer(objs []k8sruntime.Object, authz authorizer.UnconditionalAuthorizer) (*APIServer, func(), error) {
	// The plugin's informers log every sync.
	quietKlog.Do(func() { klog.SetLogger(logr.Discard()) })
	objs = append(objs, probeObjects()...)
	objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ReleaseNamespace,
		Labels: map[string]string{"kubernetes.io/metadata.name": ReleaseNamespace}}})
	kube := kubefake.NewClientset(objs...)
	factory := informers.NewSharedInformerFactory(kube, 0)
	scheme := k8sruntime.NewScheme()
	mapper := meta.NewDefaultRESTMapper(nil)
	listKinds := map[schema.GroupVersionResource]string{}
	for _, r := range apiResources {
		gvr := schema.GroupVersionResource{Group: r.group, Version: apiVersion(r.group), Resource: r.resource}
		gvk := gvr.GroupVersion().WithKind(r.kind)
		scope := meta.RESTScopeNamespace
		if r.cluster {
			scope = meta.RESTScopeRoot
		}
		mapper.AddSpecific(gvk, gvr, gvr, scope)
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvr.GroupVersion().WithKind(r.kind+"List"), &unstructured.UnstructuredList{})
		listKinds[gvr] = r.kind + "List"
	}
	if authz == nil {
		authz = allowAll{}
	}
	p, err := validating.NewPlugin(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("admission plugin: %w", err)
	}
	stop := make(chan struct{})
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	p.SetExternalKubeInformerFactory(factory)
	p.SetExternalKubeClientSet(kube)
	p.SetRESTMapper(mapper)
	p.SetDynamicClient(dyn)
	p.SetDrainedNotification(stop)
	p.SetUnconditionalAuthorizer(authz)
	if err := p.ValidateInitialization(); err != nil {
		close(stop)
		return nil, nil, fmt.Errorf("admission plugin: %w", err)
	}
	factory.Start(stop)
	factory.WaitForCacheSync(stop)
	s := &APIServer{plugin: p, scheme: scheme, kube: kube, dyn: dyn, namespaces: map[string]bool{}, matched: map[string]bool{}}
	// Ready is when the plugin evaluates a request: its policies compiled
	// (all of objs's, in one pass), the probe's binding matched and its param
	// informer synced, so the probe is denied for want of the AgentIndex
	// param, which is not served yet. Until then a request is refused as
	// not ready, or after the plugin's own wait (10s) for its informers.
	if err := s.waitForProbe("0", func(err error) bool { return err != nil && strings.Contains(err.Error(), noParams) }); err != nil {
		close(stop)
		return nil, nil, fmt.Errorf("admission plugin not ready: %w", err)
	}
	for _, o := range objs {
		switch x := o.(type) {
		case *corev1.Namespace:
			s.namespaces[x.Name] = true
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			if x.Name == probePolicy || x.Spec.MatchConstraints == nil {
				continue
			}
			s.policies = append(s.policies, x.Name)
			for _, r := range x.Spec.MatchConstraints.ResourceRules {
				for _, g := range r.APIGroups {
					for _, res := range r.Resources {
						s.matched[g+"/"+res] = true
					}
				}
			}
		}
	}
	return s, func() { close(stop) }, nil
}

// allowAll authorizes everything: a policy's authorizer checks pass.
type allowAll struct{}

func (allowAll) Authorize(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionAllow, "", nil
}

// probeObjects are the probe policy and its binding: a Bundle labelled
// probeLabel is admitted only when the AgentIndex param carries the seq
// annotation the Bundle carries.
func probeObjects() []k8sruntime.Object {
	deny := admissionregistrationv1.DenyAction
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: probePolicy},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			ParamKind: &admissionregistrationv1.ParamKind{APIVersion: "kardinal.io/v1alpha1", Kind: "AgentIndex"},
			MatchConstraints: &admissionregistrationv1.MatchResources{
				ObjectSelector: &metav1.LabelSelector{MatchLabels: map[string]string{probeLabel: "true"}},
				ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{{RuleWithOperations: admissionregistrationv1.RuleWithOperations{
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
					Rule: admissionregistrationv1.Rule{APIGroups: []string{"kardinal.io"}, APIVersions: []string{"*"},
						Resources: []string{"bundles"}}}}},
			},
			Validations: []admissionregistrationv1.Validation{{
				Expression: "has(params.metadata.annotations) && '" + seqKey + "' in params.metadata.annotations && " +
					"params.metadata.annotations['" + seqKey + "'] == object.metadata.annotations['" + seqKey + "']",
				Message: "stale param"}},
		},
	}
	defaultPolicy(&policy.Spec)
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: probePolicy},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: probePolicy,
			ParamRef:          &admissionregistrationv1.ParamRef{Name: Release, ParameterNotFoundAction: &deny},
			ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}},
	}
	return []k8sruntime.Object{policy, binding}
}

// Matches reports whether a policy matches resource (with subresource,
// when not empty) of group.
func (s *APIServer) Matches(group, resource, subresource string) bool {
	if subresource != "" {
		resource += "/" + subresource
	}
	return s.matched[group+"/"+resource]
}

// EnsureNamespace serves namespace ns (labelled with its name, as the API
// server does) to the plugin's namespace informer.
func (s *APIServer) EnsureNamespace(ns string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ns == "" || s.namespaces[ns] {
		return nil
	}
	_, err := s.kube.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns,
		Labels: map[string]string{"kubernetes.io/metadata.name": ns}}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("namespace %s: %w", ns, err)
	}
	s.namespaces[ns] = true
	return nil
}

// SetAgentIndex serves idx as the AgentIndex param (nil: none), and returns
// once the plugin's param informer holds it.
func (s *APIServer) SetAgentIndex(idx *v1alpha1.AgentIndex) error {
	s.mu.Lock()
	s.seq++
	seq := strconv.Itoa(s.seq)
	s.mu.Unlock()
	ctx := context.Background()
	res := s.dyn.Resource(agentIndexGVR)
	if idx == nil {
		if err := res.Delete(ctx, Release, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete agent index param: %w", err)
		}
	} else {
		m, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(idx)
		if err != nil {
			return fmt.Errorf("agent index param: %w", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetAPIVersion("kardinal.io/v1alpha1")
		u.SetKind("AgentIndex")
		u.SetResourceVersion("")
		u.SetManagedFields(nil)
		ann := u.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[seqKey] = seq
		u.SetAnnotations(ann)
		if _, err := res.Update(ctx, u, metav1.UpdateOptions{}); apierrors.IsNotFound(err) {
			_, err = res.Create(ctx, u, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("create agent index param: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("update agent index param: %w", err)
		}
	}
	if err := s.waitForProbe(seq, func(err error) bool {
		return (idx == nil && err != nil && strings.Contains(err.Error(), noParams)) || (idx != nil && err == nil)
	}); err != nil {
		return fmt.Errorf("the admission plugin did not see agent index %s: %w", seq, err)
	}
	return nil
}

// waitForProbe admits the probe Bundle carrying seq until seen says the
// plugin's answer is the one wanted, for at most readyTimeout; it returns
// the last answer when that never comes.
func (s *APIServer) waitForProbe(seq string, seen func(error) bool) error {
	probe := probeBundle(seq)
	controller := &user.DefaultInfo{Name: ControllerUser}
	for deadline := time.Now().Add(readyTimeout); ; time.Sleep(time.Millisecond) {
		err := s.Admit(admission.Create, GVR("bundles"), probe, nil, controller)
		if seen(err) {
			return nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = errors.New("admitted")
			}
			return fmt.Errorf("after %s, the probe: %w", readyTimeout, err)
		}
	}
}

// Admit runs a request through the plugin: nil when admitted, else the
// API server's Forbidden error.
func (s *APIServer) Admit(op admission.Operation, gvr schema.GroupVersionResource, obj, old map[string]interface{},
	u user.Info) error {
	return s.AdmitSub(op, gvr, "", obj, old, u)
}

// AdmitSub is Admit for a write of subresource (status) of the object.
func (s *APIServer) AdmitSub(op admission.Operation, gvr schema.GroupVersionResource, subresource string,
	obj, old map[string]interface{}, u user.Info) error {
	var newObj, oldObj k8sruntime.Object
	src := obj
	if obj != nil {
		newObj = &unstructured.Unstructured{Object: obj}
	}
	if old != nil {
		oldObj = &unstructured.Unstructured{Object: old}
		if src == nil {
			src = old
		}
	}
	o := unstructured.Unstructured{Object: src}
	attrs := admission.NewAttributesRecord(newObj, oldObj, o.GroupVersionKind(), o.GetNamespace(), o.GetName(), gvr, subresource, op, nil, false, u)
	return s.plugin.Validate(context.Background(), attrs, admission.NewObjectInterfacesFromScheme(s.scheme))
}

// PolicyObjects returns the namespaces, and the named policies with their
// bindings, as objects for the plugin's informers, with the API server's
// defaults applied. edit, when set, changes each binding before it is served.
func PolicyObjects(namespaces []string, c Chart, names []string,
	edit func(*admissionregistrationv1.ValidatingAdmissionPolicyBinding)) []k8sruntime.Object {
	var out []k8sruntime.Object
	for _, ns := range namespaces {
		out = append(out, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"kubernetes.io/metadata.name": ns}}})
	}
	for _, n := range names {
		v := c.VAPs[n].DeepCopy()
		defaultPolicy(&v.Spec)
		out = append(out, v)
		for _, b := range c.Bindings {
			if b.Spec.PolicyName != n {
				continue
			}
			bc := b.DeepCopy()
			if bc.Spec.MatchResources != nil {
				defaultMatchResources(bc.Spec.MatchResources)
			}
			if edit != nil {
				edit(bc)
			}
			out = append(out, bc)
		}
	}
	return out
}

// defaultPolicy and defaultMatchResources apply the API server's defaults
// (kubernetes v1.37.1 pkg/apis/admissionregistration/v1/defaults.go
// SetDefaults_ValidatingAdmissionPolicySpec, SetDefaults_MatchResources,
// SetDefaults_Rule), which a policy read back from the API server has.
func defaultPolicy(spec *admissionregistrationv1.ValidatingAdmissionPolicySpec) {
	if spec.FailurePolicy == nil {
		f := admissionregistrationv1.Fail
		spec.FailurePolicy = &f
	}
	if spec.MatchConstraints != nil {
		defaultMatchResources(spec.MatchConstraints)
	}
}

func defaultMatchResources(m *admissionregistrationv1.MatchResources) {
	if m.MatchPolicy == nil {
		p := admissionregistrationv1.Equivalent
		m.MatchPolicy = &p
	}
	if m.NamespaceSelector == nil {
		m.NamespaceSelector = &metav1.LabelSelector{}
	}
	if m.ObjectSelector == nil {
		m.ObjectSelector = &metav1.LabelSelector{}
	}
	for _, rules := range [][]admissionregistrationv1.NamedRuleWithOperations{m.ResourceRules, m.ExcludeResourceRules} {
		for i := range rules {
			if rules[i].Scope == nil {
				s := admissionregistrationv1.AllScopes
				rules[i].Scope = &s
			}
		}
	}
}
