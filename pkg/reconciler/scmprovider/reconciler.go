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
// checks that each can be used (scm.Registry.Validate: the apiURL scheme,
// the allowedRepositories globs, the allowedNamespaces selector, and
// Secrets labeled kardinal.io/referenceable with their keys) and writes the
// Ready condition. It calls no SCM. A provider that is deleted is evicted
// from the registry, so its client and token leave memory.
package scmprovider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
	// Registry validates the provider and drops a deleted one's client.
	Registry *scm.Registry
	Cluster  bool
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
	reg := r.Registry
	if reg == nil {
		reg = &scm.Registry{Client: r.Client}
	}
	kind := v1alpha1.KindScmProvider
	var obj client.Object
	var status *v1alpha1.ScmProviderStatus
	if r.Cluster {
		kind = v1alpha1.KindClusterScmProvider
		var p v1alpha1.ClusterScmProvider
		if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &p); err != nil {
			return r.gone(reg, kind, req, err)
		}
		obj, status = &p, &p.Status
	} else {
		var p v1alpha1.ScmProvider
		if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
			return r.gone(reg, kind, req, err)
		}
		obj, status = &p, &p.Status
	}
	spec, err := scm.GetProvider(ctx, r.Client, req.Namespace, kind, req.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("read %s %s: %w", kind, req.Name, err)
	}
	base := obj.DeepCopyObject().(client.Object)

	problems := reg.Validate(ctx, spec)
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

// gone evicts a provider that no longer exists.
func (r *Reconciler) gone(reg *scm.Registry, kind string, req ctrl.Request, err error) (ctrl.Result, error) {
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	reg.Evict(kind, req.Namespace, req.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler for its kind.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)
	if r.Cluster {
		return b.For(&v1alpha1.ClusterScmProvider{}).Named("clusterscmprovider").Complete(r)
	}
	return b.For(&v1alpha1.ScmProvider{}).Named("scmprovider").Complete(r)
}
