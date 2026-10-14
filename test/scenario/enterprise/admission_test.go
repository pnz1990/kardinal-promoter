// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package enterprise

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
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
)

// admissionServer is the API server's ValidatingAdmissionPolicy admission
// plugin (k8s.io/apiserver/pkg/admission/plugin/policy/validating), fed the
// policies and bindings through informers as in a cluster: a policy decides
// only through a binding, with the binding's validationActions (an Audit-only
// binding denies nothing) and matchResources, and the policy's
// matchConstraints.
type admissionServer struct {
	plugin *validating.Plugin
	scheme *k8sruntime.Scheme
}

var (
	approvalsGVR = schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: "approvals"}
	bundlesGVR   = schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: "bundles"}
)

func newAdmissionServer(t *testing.T, objs []k8sruntime.Object, authz authorizer.Authorizer) *admissionServer {
	t.Helper()
	client := kubefake.NewClientset(objs...)
	factory := informers.NewSharedInformerFactory(client, 0)
	scheme := k8sruntime.NewScheme()
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvr := range []schema.GroupVersionResource{approvalsGVR, bundlesGVR} {
		gvk := gvr.GroupVersion().WithKind(map[string]string{"approvals": "Approval", "bundles": "Bundle"}[gvr.Resource])
		mapper.AddSpecific(gvk, gvr, gvr, meta.RESTScopeNamespace)
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	}
	p, err := validating.NewPlugin(nil)
	require.NoError(t, err)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	p.SetExternalKubeInformerFactory(factory)
	p.SetExternalKubeClientSet(client)
	p.SetRESTMapper(mapper)
	p.SetDynamicClient(dynfake.NewSimpleDynamicClient(scheme))
	p.SetDrainedNotification(stop)
	p.SetUnconditionalAuthorizer(authz)
	require.NoError(t, p.ValidateInitialization())
	factory.Start(stop)
	factory.WaitForCacheSync(stop)
	deadline := time.Now().Add(10 * time.Second)
	for !p.WaitForReady() {
		require.True(t, time.Now().Before(deadline), "admission plugin not ready")
	}
	return &admissionServer{plugin: p, scheme: scheme}
}

// admit runs a request through the plugin: nil when admitted.
func (s *admissionServer) admit(op admission.Operation, gvr schema.GroupVersionResource, obj, old map[string]interface{},
	username string, groups []string) error {
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
	u := unstructured.Unstructured{Object: src}
	gvk := u.GroupVersionKind()
	attrs := admission.NewAttributesRecord(newObj, oldObj, gvk, u.GetNamespace(), u.GetName(), gvr, "", op, nil, false,
		&user.DefaultInfo{Name: username, Groups: groups})
	return s.plugin.Validate(context.Background(), attrs, admission.NewObjectInterfacesFromScheme(s.scheme))
}

// policyObjects returns the namespaces, and the named policies with their
// bindings, as objects for the plugin's informers. edit, when set, changes
// each binding before it is served.
func policyObjects(namespaces []string, vaps map[string]*admissionregistrationv1.ValidatingAdmissionPolicy,
	bindings map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding, names []string,
	edit func(*admissionregistrationv1.ValidatingAdmissionPolicyBinding)) []k8sruntime.Object {
	var out []k8sruntime.Object
	for _, ns := range namespaces {
		out = append(out, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"kubernetes.io/metadata.name": ns}}})
	}
	for _, n := range names {
		v := vaps[n].DeepCopy()
		defaultPolicy(&v.Spec)
		out = append(out, v)
		for _, b := range bindings {
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
