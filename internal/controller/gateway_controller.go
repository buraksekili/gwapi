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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const finalizer = "finalizers.buraksekili.github.io/gateway-api-tyk"

// GatewayReconciler reconciles a Gateway object
type GatewayReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=deployment,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Gateway object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("Gateway", req.String())

	l.Info("reconciling gateway")

	gw := gwv1.Gateway{}
	if err := r.Client.Get(ctx, req.NamespacedName, &gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Gateway objects MUST refer in the spec.gatewayClassName field to a GatewayClass that exists and is Accepted
	// by an implementation for that implementation to reconcile them.
	// For reference: https: //gateway-api.sigs.k8s.io/reference/implementers-guide/#gateway
	gwClass := gwv1.GatewayClass{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(gw.Spec.GatewayClassName)}, &gwClass); err != nil {
		l.Info("failed to find gatewayclass", "gw class name", gw.Spec.GatewayClassName, "err", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if gwClass.Spec.ControllerName != controllerName {
		l.Info("gw class does not suitable to use in this controller",
			"gw class controller", gwClass.Spec.ControllerName,
			"controller", controllerName,
		)
		return ctrl.Result{}, nil
	}

	tykGwConf := v1alpha1.GatewayConfiguration{}
	if gwClass.Spec.ParametersRef != nil {
		if !validParameters(gwClass.Spec.ParametersRef) {
			return ctrl.Result{}, fmt.Errorf("invalid paramaters ref")
		}

		err := r.Client.Get(
			ctx,
			types.NamespacedName{Name: gwClass.Spec.ParametersRef.Name, Namespace: req.Namespace},
			&tykGwConf,
		)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if !gw.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &gw)
	}

	return r.reconcile(ctx, l, &gw, tykGwConf)
}

func validParameters(ref *gwv1.ParametersReference) bool {
	return ref.Name != "" && ref.Group == "gateway" && ref.Kind == "GatewayConfiguration"
}

func (r *GatewayReconciler) reconcileDelete(ctx context.Context, gw *gwv1.Gateway) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(gw, finalizer) {
		controllerutil.RemoveFinalizer(gw, finalizer)
	}

	return ctrl.Result{}, nil
}

func (r *GatewayReconciler) reconcile(ctx context.Context, l logr.Logger, gw *gwv1.Gateway, conf v1alpha1.GatewayConfiguration) (ctrl.Result, error) {
	controllerutil.AddFinalizer(gw, finalizer)

	labels := map[gwv1.AnnotationKey]gwv1.AnnotationValue{}
	labels["myoperator"] = "tyk"
	annotations := map[gwv1.AnnotationKey]gwv1.AnnotationValue{}

	if gw.Spec.Infrastructure != nil {
		labels = gw.Spec.Infrastructure.Labels
		annotations = gw.Spec.Infrastructure.Annotations
	}

	// Consider how to implement gw.Spec.Addresses

	var envs []corev1.EnvVar

	for _, listener := range gw.Spec.Listeners {
		if listener.Protocol != "HTTP" && listener.Protocol != "controlapi" {
			l.Info("unsupported protocol type defined in Gateway", "protocol", listener.Protocol)
			return ctrl.Result{}, nil
		}

		// TODO: Configure Tyk Gateway here based on listener options.

		listenerPort := intstr.FromInt32(int32(listener.Port))
		envs = append(envs, corev1.EnvVar{Name: "TYK_GW_LISTENPORT", Value: listenerPort.String()})
	}

	deploy := deployment(l, envs, conf.Spec.Tyk.ConfigMapRef.Name, labels, annotations)
	deploy.Namespace = gw.Namespace

	l.Info("prepared deployment", "deploy meta", deploy.ObjectMeta)

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, &deploy, func() error {
		return nil
	})

	if err != nil {
		l.Info("resource could not be created / updated", "result", result)
		return ctrl.Result{}, err
	}

	l.Info("resource has created / updated", "result", result)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&gwv1.Gateway{}).
		Complete(r)
}
