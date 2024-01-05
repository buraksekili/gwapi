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
	"crypto/sha256"
	"fmt"
	tykApiModel "github.com/TykTechnologies/tyk-operator/api/model"
	"github.com/TykTechnologies/tyk-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	v1 "sigs.k8s.io/gateway-api/apis/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// HTTPRouteReconciler reconciles a HTTPRoute object
type HTTPRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the HTTPRoute object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *HTTPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("HTTPRoute", req.NamespacedName.String())

	l.Info("reconciling HTTPRoute")

	desired := &v1.HTTPRoute{}
	if err := r.Client.Get(ctx, req.NamespacedName, desired); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := validateRouteParentRefs(desired.Spec.ParentRefs); err != nil {
		return ctrl.Result{}, err
	}

	//for _, ref := range desired.Spec.ParentRefs {
	//	oc := v1alpha1.OperatorContext{
	//		Spec: v1alpha1.OperatorContextSpec{
	//			Env: &v1alpha1.Environment{
	//				URL:
	//			},
	//		},
	//	}
	//}

	for _, rule := range desired.Spec.Rules {
		for _, backend := range rule.BackendRefs {
			api := v1alpha1.ApiDefinition{
				Spec: v1alpha1.APIDefinitionSpec{
					APIDefinitionSpec: tykApiModel.APIDefinitionSpec{
						Proxy: tykApiModel.Proxy{TargetURL: generateTargetURL(req.Namespace, backend)},
					},
				},
			}

			for _, match := range rule.Matches {
				apiDef := api
				apiDef.ObjectMeta = metav1.ObjectMeta{
					Name:      generateApiName(desired.ObjectMeta, match, backend, rule),
					Namespace: desired.Namespace,
				}

				_, err := controllerutil.CreateOrUpdate(ctx, r.Client, &apiDef, func() error {
					if err := controllerutil.SetOwnerReference(desired, &apiDef, r.Scheme); err != nil {
						l.Error(err, "failed to set owner reference")
						return err
					}

					reconcileApiDefinition(&apiDef, match)

					return nil
				})
				if err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	return ctrl.Result{}, nil
}

func validateRouteParentRefs(refs []v1.ParentReference) error {
	for i := range refs {
		if refs[i].Kind != nil && *refs[i].Kind != "Gateway" {
			return fmt.Errorf("invalid kind for HTTPRoute, expected Gateway got %v", *refs[i].Kind)
		}
	}

	return nil
}

func generateTargetURL(ns string, backend v1.HTTPBackendRef) string {
	// TODO: check if service exists, if not update status accordingly
	// after getting the service, you can also know the service protocol. as of today, go with http
	if !svcExists(backend) {
		return ""
	}

	if backend.Namespace != nil && *backend.Namespace != "" {
		// requires checking ReferenceGrant
		ns = string(*backend.Namespace)
	}

	backendPort := int32(80)
	if backend.Port != nil && *backend.Port != 0 {
		backendPort = int32(*backend.Port)
	}

	return fmt.Sprintf("http://%s.%s.svc:%d", backend.Name, ns, backendPort)
}

func svcExists(backend v1.HTTPBackendRef) bool {
	return true
}

// SetupWithManager sets up the controller with the Manager.
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&v1.HTTPRoute{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

func reconcileApiDefinition(apiDef *v1alpha1.ApiDefinition, match v1.HTTPRouteMatch) {
	apiDef.Spec.Name = apiDef.Name

	lp := listenPath(match.Path)
	apiDef.Spec.Proxy.ListenPath = &lp

	active := true
	apiDef.Spec.Active = &active
}

func listenPath(path *v1.HTTPPathMatch) string {
	if path == nil || path.Value == nil {
		return "/"
	}

	return *path.Value
}

func generateApiName(meta metav1.ObjectMeta, match v1.HTTPRouteMatch, backend v1.HTTPBackendRef, rule v1.HTTPRouteRule) string {
	/*
		1- route.Namespace / route.name
		2- if no field specified
			generate-random-stuff as follows;
				"<namespace>/<name>/<random>"
		3- Ifj
	*/

	/*
		no need to take parentsRef name into consideration while generating a name for ApiDefinition since
		i do not know from httproute_controller that which parentRef reflects to Tyk Gateway.
	*/

	name := fmt.Sprintf("%s/%s", meta.Name, meta.Namespace)
	if match.Path == nil {
		// TODO: generate random string here
		name = fmt.Sprintf("%s/%s", name, "generaterandomstring")
	} else {
		if match.Path.Value != nil {
			name = combine(name, *match.Path.Value)
		}
		if match.Path.Type != nil {
			name = combine(name, string(*match.Path.Type))
		}
	}

	namespace := meta.Namespace
	if backend.Namespace != nil && *backend.Namespace != "" {
		namespace = string(*backend.Namespace)
	}

	name = combine(name, string(backend.Name))
	name = combine(name, namespace)

	for _, ref := range rule.BackendRefs {
		name = combine(name, string(ref.Name))
	}

	hashedName := shortHash(name)
	resourceName := fmt.Sprintf("%s-%s-%s", namespace, backend.Name, hashedName)
	return resourceName
}

func combine(base string, additions ...string) string {
	for _, addition := range additions {
		base = fmt.Sprintf("%s/%s", base, addition)
	}

	return base
}

func shortHash(txt string) string {
	h := sha256.New()
	h.Write([]byte(txt))

	return fmt.Sprintf("%x", h.Sum(nil))[:9]
}
