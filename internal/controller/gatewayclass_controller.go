/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"github.com/buraksekili/gateway-api-tyk/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	"time"
)

// GatewayClassReconciler reconciles a GatewayClass object
type GatewayClassReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// <domain>/<name>
const controllerName = "buraksekili.github.com/gateway-api-controller-tyk"

//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GatewayClass object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *GatewayClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("GatewayClass", req.Name)

	l.Info("reconciling gateway class")

	gwClass := &gwv1.GatewayClass{}
	if err := r.Client.Get(ctx, req.NamespacedName, gwClass); err != nil {
		l.Info("gateway not found", "name", req.Name)
		return ctrl.Result{}, nil
	}

	// can we handle it in predicate?
	if gwClass.Spec.ControllerName != controllerName {
		return ctrl.Result{}, nil
	}

	if !gwClass.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if gwClass.Spec.ParametersRef != nil {
		if !validParameters(gwClass.Spec.ParametersRef) {
			// TODO: add event record
			// TODO: updated status
			return ctrl.Result{}, fmt.Errorf("invalid paramaters ref in GatewayClass")
		}
		ns := ""
		if gwClass.Spec.ParametersRef.Namespace != nil {
			ns = string(*gwClass.Spec.ParametersRef.Namespace)
		}

		gwConf := v1alpha1.GatewayConfiguration{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: gwClass.Spec.ParametersRef.Name, Namespace: ns}, &gwConf); err != nil {
			l.Info("failed to find GatewayConfiguration", "name", req.Name)
			return ctrl.Result{}, nil
		}

		anns := gwClass.Annotations
		if val, ok := anns["tyk.tyk.io/gatewayconfiguration-resourceversion"]; ok && val == gwConf.ResourceVersion {
			l.Info("no need to update gwclass")
		} else {
			anns = addToAnnotations(anns, "tyk.tyk.io/gatewayconfiguration-resourceversion", gwConf.ResourceVersion)
			gwClass.SetAnnotations(anns)

			if err := r.Client.Update(ctx, gwClass); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, nil
		}
	}

	// todo: no need to update whole status object
	gwClassOld := gwClass.DeepCopy()
	gwClass.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now())
	gwClass.Status.Conditions[0].ObservedGeneration = gwClass.Generation
	gwClass.Status.Conditions[0].Status = "True"
	gwClass.Status.Conditions[0].Message = string(gwv1.GatewayClassReasonAccepted)
	gwClass.Status.Conditions[0].Reason = string(gwv1.GatewayClassReasonAccepted)

	if err := r.Client.Status().Patch(ctx, gwClass, client.MergeFrom(gwClassOld)); err != nil {
		l.Info("failed to reconcile gw class")
		return ctrl.Result{}, errors.Wrapf(err, "failed to update gatewayclass status")
	}

	l.Info("reconciled gw class successfully")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwv1.GatewayClass{}).
		Watches(
			&v1alpha1.GatewayConfiguration{},
			handler.EnqueueRequestsFromMapFunc(r.listGatewayConfigurations),
			builder.WithPredicates(
				predicate.Funcs{
					DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
						return false
					},
					CreateFunc: func(createEvent event.CreateEvent) bool {
						return false
					},
					UpdateFunc: func(e event.UpdateEvent) bool {
						if e.ObjectOld == nil {
							r.Log.Error(nil, "Update event has no old object to update", "event", e)
							return false
						}
						if e.ObjectNew == nil {
							r.Log.Error(nil, "Update event has no new object to update", "event", e)
							return false
						}

						labels := e.ObjectNew.GetLabels()
						if labels == nil {
							return false
						}

						val, ok := labels[tykManagedBy]
						if ok && val == "tyk-operator" {
							return true
						}

						return false
					},
				},
			),
		).
		WithEventFilter(predicate.Or(predicate.AnnotationChangedPredicate{}, predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *GatewayClassReconciler) listGatewayConfigurations(ctx context.Context, gwConfig client.Object) []reconcile.Request {
	var requests []reconcile.Request

	gwClasses := &gwv1.GatewayClassList{}
	if err := r.Client.List(ctx, gwClasses); err != nil {
		return requests
	}

	for _, gwClass := range gwClasses.Items {
		if gwClass.Spec.ControllerName == controllerName {
			if validParameters(gwClass.Spec.ParametersRef) {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: gwClass.Name}})
			}
		}
	}

	return requests
}
