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
	"github.com/buraksekili/gateway-api-tyk/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// GatewayConfigurationReconciler reconciles a GatewayConfiguration object
type GatewayConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=gateway.buraksekili.github.io,resources=gatewayconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.buraksekili.github.io,resources=gatewayconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gateway.buraksekili.github.io,resources=gatewayconfigurations/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GatewayConfiguration object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *GatewayConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("GatewayConfiguration", req.String())

	gwConfig := &v1alpha1.GatewayConfiguration{}
	if err := r.Client.Get(ctx, req.NamespacedName, gwConfig); err != nil {
		l.Info("gateway not found", "name", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	labels := gwConfig.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// TODO: no need to run update if there is no change

	labels[tykManagedBy] = "tyk-operator"
	gwConfig.SetLabels(labels)

	if gwConfig.Spec.Tyk.ConfigMapRef.Name != "" {
		configMap := v1.ConfigMap{}

		err := r.Client.Get(ctx, gwConfig.Spec.Tyk.ConfigMapRef.NamespacedName(), &configMap)
		if err != nil {
			l.Info("failed to find ConfigMap from this GatewayConfiguration")
			return ctrl.Result{}, err
		}

		anns := gwConfig.GetAnnotations()
		if anns == nil {
			anns = make(map[string]string)
		}

		anns["tyk.tyk.io/tyk-gateway-configmap-resourceVersion"] = configMap.ResourceVersion
		gwConfig.SetAnnotations(anns)

		cmLabels := configMap.GetLabels()
		if cmLabels == nil {
			cmLabels = make(map[string]string)
		}

		cmLabels[tykManagedBy] = "tyk-operator"
		configMap.SetLabels(cmLabels)

		if err := r.Client.Update(ctx, &configMap); err != nil {
			l.Error(err, "failed to update configMap")
			return ctrl.Result{}, err
		}
	}

	if err := r.Client.Update(ctx, gwConfig); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

const _gwConfToConfigMapIdx = "_gwConfToConfigMap"

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&v1alpha1.GatewayConfiguration{},
		_gwConfToConfigMapIdx,
		func(rawObj client.Object) []string {
			gatewayConfiguration := rawObj.(*v1alpha1.GatewayConfiguration)
			if gatewayConfiguration.Spec.Tyk.ConfigMapRef.Name == "" {
				return nil
			}

			return []string{gatewayConfiguration.Spec.Tyk.ConfigMapRef.Name}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&v1alpha1.GatewayConfiguration{}).
		Watches(
			&v1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewayClassesFromConfigMap),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

func (r *GatewayConfigurationReconciler) findGatewayClassesFromConfigMap(ctx context.Context, configMapObj client.Object) []reconcile.Request {
	var requests []reconcile.Request

	gwConfigs := v1alpha1.GatewayConfigurationList{}
	listOpts := client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(_gwConfToConfigMapIdx, configMapObj.GetName()),
		Namespace:     configMapObj.GetNamespace(),
	}

	err := r.Client.List(context.Background(), &gwConfigs, &listOpts)
	if err != nil {
		return requests
	}

	for _, gwConfig := range gwConfigs.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: gwConfig.Name, Namespace: gwConfig.Namespace}})
	}

	return requests
}
