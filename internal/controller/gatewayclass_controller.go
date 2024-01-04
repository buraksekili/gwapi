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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayClassReconciler reconciles a GatewayClass object
type GatewayClassReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder
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
	// TODO: check why we have two gatewayclass field in the logs
	l := log.FromContext(ctx).WithValues("GatewayClass", req.Name)

	l.Info("Reconciling GatewayClass")

	gwClass := &gwv1.GatewayClass{}
	if err := r.Client.Get(ctx, req.NamespacedName, gwClass); err != nil {
		l.Info("GatewayClass not found", "name", req.Name)

		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if gwClass.Spec.ControllerName != controllerName {
		l.Info(
			"Ignore GatewayClass resources with different controller names",
			"controllerName", gwClass.Spec.ControllerName,
		)

		return ctrl.Result{}, nil
	}

	if !gwClass.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if gwClass.Spec.ParametersRef != nil {
		if !validParameters(gwClass.Spec.ParametersRef) {
			r.Recorder.Eventf(gwClass, v1.EventTypeWarning, GWClassInvalidParametersRef,
				"invalid ParametersRef provided for controllerName: %s", controllerName,
			)

			return ctrl.Result{}, fmt.Errorf("invalid paramaters ref in GatewayClass")
		}

		ns := ""
		if gwClass.Spec.ParametersRef.Namespace != nil {
			ns = string(*gwClass.Spec.ParametersRef.Namespace)
		}

		gwConf := v1alpha1.GatewayConfiguration{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: gwClass.Spec.ParametersRef.Name, Namespace: ns}, &gwConf); err != nil {
			l.Info(
				"failed to find GatewayConfiguration",
				"GatewayConfiguration Name", gwClass.Spec.ParametersRef.Name,
				"GatewayConfiguration Namespace", ns,
			)

			return ctrl.Result{}, nil
		}

		anns := gwClass.Annotations
		if val, ok := anns[GWClassGWConfigurationAnnKey]; ok && val == gwConf.ResourceVersion {
			l.Info("no need to update GatewayClass annotations")
		} else {
			anns = addToAnnotations(anns, GWClassGWConfigurationAnnKey, gwConf.ResourceVersion)
			gwClass.SetAnnotations(anns)

			if err := r.Client.Update(ctx, gwClass); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, nil
		}
	}

	if err := setGwClassConditionAccepted(gwClass); err != nil {
		r.Recorder.Event(gwClass, v1.EventTypeWarning, GWClassFailedToSetAcceptedStatus,
			"failed to set GatewayClass Status Condition",
		)
		l.Error(err, "failed to set GatewayClass status conditions")

		return ctrl.Result{}, err
	}

	if err := r.Client.Status().Update(ctx, gwClass); err != nil {
		//if err := r.Client.Status().Patch(ctx, gwClass, client.MergeFrom(gwClass.DeepCopy())); err != nil {
		// TODO: do we need to print error here as returning error also prints out the error logs.
		r.Recorder.Event(gwClass, v1.EventTypeWarning, FailedToUpdateStatus,
			"failed to update GatewayClass Status",
		)

		l.Error(err, "failed to patch GatewayClass status")

		return ctrl.Result{}, errors.Wrapf(err, "failed to patch gatewayclass status")
	}

	l.Info("reconciled GatewayClass successfully")

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
