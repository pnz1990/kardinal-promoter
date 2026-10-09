// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package scmprovider reconciles ScmProviders and ClusterScmProviders: it
// checks that each can be used (Secrets with their keys, a valid
// allowedNamespaces selector) and writes the Ready condition. It calls no
// SCM: the token is used the first time a Pipeline needs it.
package scmprovider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
)

// recheck is how often a provider's Secrets are read again: Secrets are not
// watched (the controller may only get them), so a Secret created or fixed
// later turns the provider Ready within this time.
const recheck = 5 * time.Minute

// ConditionReady is the provider's condition, with reason Valid or Invalid.
const ConditionReady = "Ready"

// Reconciler reconciles one kind: ScmProvider (Cluster false) or
// ClusterScmProvider (Cluster true).
type Reconciler struct {
	client.Client
	Cluster bool
}

var (
	scmProvidersResource        = v1alpha1.GroupVersion.WithResource("scmproviders").GroupResource()
	clusterScmProvidersResource = v1alpha1.GroupVersion.WithResource("clusterscmproviders").GroupResource()
)

// Reconcile checks one provider. A provider deleted while it is reconciled
// ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	res := scmProvidersResource
	if r.Cluster {
		res = clusterScmProvidersResource
	}
	return objectgone.Reconcile(ctx, req, res, r.reconcile)
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("scmprovider", req.String()).Logger()
	var (
		obj        client.Object
		spec       v1alpha1.ScmProviderSpec
		status     *v1alpha1.ScmProviderStatus
		secretNS   string
		selectorOK = true
		selErr     error
	)
	if r.Cluster {
		var p v1alpha1.ClusterScmProvider
		if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &p); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		obj, spec, status, secretNS = &p, p.Spec.ScmProviderSpec, &p.Status, p.Spec.SecretRef.Namespace
		if p.Spec.AllowedNamespaces != nil {
			if _, selErr = metav1.LabelSelectorAsSelector(p.Spec.AllowedNamespaces); selErr != nil {
				selectorOK = false
			}
		}
	} else {
		var p v1alpha1.ScmProvider
		if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		obj, spec, status, secretNS = &p, p.Spec, &p.Status, p.Namespace
	}
	base := obj.DeepCopyObject().(client.Object)

	var problems []string
	if !selectorOK {
		problems = append(problems, fmt.Sprintf("spec.allowedNamespaces: %v", selErr))
	}
	if msg := r.checkSecret(ctx, secretNS, spec.SecretRef, "token"); msg != "" {
		problems = append(problems, "spec.secretRef: "+msg)
	}
	if ref := spec.WebhookSecretRef; ref != nil {
		ns := secretNS
		if ref.Namespace != "" {
			ns = ref.Namespace
		}
		if msg := r.checkSecret(ctx, ns, *ref, "secret"); msg != "" {
			problems = append(problems, "spec.webhookSecretRef: "+msg)
		}
	}
	cond := metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "Valid",
		Message: "the provider can be used", ObservedGeneration: obj.GetGeneration()}
	if len(problems) > 0 {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Invalid", strings.Join(problems, "; ")
	}
	changed := meta.SetStatusCondition(&status.Conditions, cond)
	if status.ObservedGeneration != obj.GetGeneration() {
		status.ObservedGeneration, changed = obj.GetGeneration(), true
	}
	if changed {
		if err := r.Status().Patch(ctx, obj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch scm provider status: %w", err)
		}
		log.Info().Str("ready", string(cond.Status)).Str("message", cond.Message).Msg("SCM provider checked")
	}
	return ctrl.Result{RequeueAfter: recheck}, nil
}

// checkSecret says what is wrong with the Secret key ref names in ns, or ""
// when it has a non-empty value.
func (r *Reconciler) checkSecret(ctx context.Context, ns string, ref v1alpha1.ScmSecretKeyRef, defaultKey string) string {
	key := ref.Key
	if key == "" {
		key = defaultKey
	}
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return fmt.Sprintf("Secret %s/%s: %v", ns, ref.Name, err)
	}
	if strings.TrimSpace(string(s.Data[key])) == "" {
		return fmt.Sprintf("Secret %s/%s has no %s key", ns, ref.Name, key)
	}
	return ""
}

// SetupWithManager registers the reconciler for its kind.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)
	if r.Cluster {
		return b.For(&v1alpha1.ClusterScmProvider{}).Named("clusterscmprovider").Complete(r)
	}
	return b.For(&v1alpha1.ScmProvider{}).Named("scmprovider").Complete(r)
}
