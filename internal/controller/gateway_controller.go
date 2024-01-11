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
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const finalizer = "finalizers.buraksekili.github.io/gateway-api-tyk"

// GatewayReconciler reconciles a Gateway object
type GatewayReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;create;update
//+kubebuilder:rbac:groups=apps,resources=configmaps,verbs=get
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

	// Consider how to implement gw.Spec.Addresses
	for _, listener := range gw.Spec.Listeners {
		if !validListenerProtocol(listener.Protocol) {
			l.Info(
				"skip reconciling Gateway with unsupported protocol type",
				"protocol", listener.Protocol,
			)

			continue
		}
	}

	tykGwConf := v1alpha1.GatewayConfiguration{}
	if gwClass.Spec.ParametersRef != nil {
		if !validParametersRef(gwClass.Spec.ParametersRef) {
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

func (r *GatewayReconciler) reconcileDelete(ctx context.Context, gw *gwv1.Gateway) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(gw, finalizer) {
		controllerutil.RemoveFinalizer(gw, finalizer)
	}

	return ctrl.Result{}, nil
}

const (
	gatewayNameLabel      = "tyk.io/gateway-name"
	gatewayNamespaceLabel = "tyk.io/gateway-ns"
	tykManagedBy          = "tyk.tyk.io/managed-by"
)

func (r *GatewayReconciler) reconcile(ctx context.Context, l logr.Logger, gw *gwv1.Gateway, conf v1alpha1.GatewayConfiguration) (ctrl.Result, error) {
	controllerutil.AddFinalizer(gw, finalizer)

	svcPorts := make(map[string]corev1.ServicePort)
	for _, listener := range gw.Spec.Listeners {
		svcPorts[string(listener.Protocol)] = corev1.ServicePort{
			Name: string(listener.Name),
			Port: int32(listener.Port),
		}
	}

	deploy, err := r.reconcileDeployment(ctx, gw, &conf, svcPorts)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.reconcileSvc(ctx, svcReconcileReq{
		svcType:        regularSvcType,
		ownerGw:        gw,
		selectorLabels: deploy.Labels,
		svcPorts:       svcPorts,
	})
	if err != nil {
		l.Error(err, "failed to create Tyk Gateway Service")
		return ctrl.Result{}, err
	}

	controlAPISvcReq := svcReconcileReq{
		svcType:        controlSvcType,
		ownerGw:        gw,
		selectorLabels: deploy.Labels,
		svcPorts:       svcPorts,
	}
	if err = r.reconcileControlApiSvc(ctx, controlAPISvcReq); err != nil {
		return ctrl.Result{}, err
	}

	//auth := ""
	//if conf.Spec.Tyk.Auth != "" {
	//	auth = conf.Spec.Tyk.Auth
	//}
	//org := ""
	//if conf.Spec.Tyk.Org != "" {
	//	auth = conf.Spec.Tyk.Org
	//}

	//r.reconcileOperatorContexts(ctx)

	// TODO: check if tls enabled
	//svcURL := fmt.Sprintf("http://%s.%s.svc:%v", generateSvcName(gw.Name, regularSvcType), gw.Namespace, svc.Spec.Ports[0].Port)
	//if controlApiEnabled {
	//	svcURL = fmt.Sprintf("http://%s.%s.svc:%v", svcControlApi.Name, svcControlApi.Namespace, svcControlApi.Spec.Ports[0].Port)
	//}
	// we can do it another controller as well
	//c := tykV1Alpha1.OperatorContext{
	//	ObjectMeta: metav1.ObjectMeta{
	//		Name:      fmt.Sprintf("%s-context", gw.Name),
	//		Namespace: gw.Namespace,
	//		Labels: map[string]string{
	//			gatewayNameLabel:      gw.ObjectMeta.Name,
	//			gatewayNamespaceLabel: gw.ObjectMeta.Namespace,
	//		},
	//	},
	//}
	//
	//err = r.createOrUpdate(ctx, &c, func() error {
	//	c.Spec = tykV1Alpha1.OperatorContextSpec{
	//		Env: &tykV1Alpha1.Environment{
	//			Mode: "ce",
	//			URL:  svcURL,
	//			Auth: auth,
	//			Org:  org,
	//		},
	//	}
	//
	//	return nil
	//})

	return ctrl.Result{}, nil
}

func (r *GatewayReconciler) reconcileDeployment(
	ctx context.Context,
	gw *gwv1.Gateway,
	conf *v1alpha1.GatewayConfiguration,
	svcPorts map[string]corev1.ServicePort,
) (v1.Deployment, error) {
	tykConfigMap := &corev1.ConfigMap{}
	if conf.Spec.Tyk.ConfigMapRef.Name != "" {
		err := r.Client.Get(ctx, conf.Spec.Tyk.ConfigMapRef.NamespacedName(), tykConfigMap)
		if err != nil {
			// TODO: add events
			return v1.Deployment{}, err
		}
	}

	labels := map[gwv1.AnnotationKey]gwv1.AnnotationValue{}
	annotations := map[gwv1.AnnotationKey]gwv1.AnnotationValue{}

	if gw.Spec.Infrastructure != nil {
		labels = gw.Spec.Infrastructure.Labels
		annotations = gw.Spec.Infrastructure.Annotations
	}

	labels[tykManagedBy] = gwv1.AnnotationValue(fmt.Sprintf("%s-%s", gw.Namespace, gw.Name))

	deploy := v1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:        fmt.Sprintf("%s-tyk-gateway", gw.Name),
		Namespace:   gw.Namespace,
		Labels:      getRawMap(labels),
		Annotations: getRawMap(annotations),
	}}
	reconcileDeployment(&deploy, tykConfigMap)

	err := r.createOrUpdate(ctx, &deploy, func() error {
		if err := ctrl.SetControllerReference(gw, &deploy, r.Scheme); err != nil {
			return err
		}

		lpSvcPort := corev1.ServicePort{Port: 8080}
		if lp, ok := svcPorts[string(ListenerListenPort)]; ok {
			lpSvcPort = lp
		}

		handleConfigMap(tykConfigMap, &deploy)

		openPortOnDeploy(&deploy, ListenerListenPort, lpSvcPort)

		if controlApi, controlApiEnabled := svcPorts[string(ListenerControlAPI)]; controlApiEnabled {
			openPortOnDeploy(&deploy, ListenerControlAPI, controlApi)
		} else {
			// delete controlapiport
		}

		return nil
	})
	if err != nil {
		return v1.Deployment{}, nil
	}

	return deploy, err
}

func openPortOnDeploy(deploy *v1.Deployment, portType ListenerPortType, svcPort corev1.ServicePort) {
	ports := deploy.Spec.Template.Spec.Containers[0].Ports
	envs := deploy.Spec.Template.Spec.Containers[0].Env

	portIdx, envIdx := -1, -1

	switch portType {
	case ListenerListenPort:
		for i := range ports {
			if svcPort.Port == ports[i].ContainerPort {
				portIdx = i
				break
			}
		}

		for i := range envs {
			if "TYK_GW_LISTENPORT" == envs[i].Name {
				envIdx = i
				break
			}
		}

		if envIdx > -1 {
			envs[envIdx] = corev1.EnvVar{Name: "TYK_GW_LISTENPORT", Value: int32ToStr(svcPort.Port)}
		} else {
			envs = append(envs, corev1.EnvVar{Name: "TYK_GW_LISTENPORT", Value: int32ToStr(svcPort.Port)})
		}
	case ListenerControlAPI:
		for i := range ports {
			if svcPort.Port == ports[i].ContainerPort {
				portIdx = i
				break
			}
		}

		for i := range envs {
			if "TYK_GW_CONTROLAPIPORT" == envs[i].Name {
				envIdx = i
				break
			}
		}

		if envIdx > -1 {
			envs[envIdx] = corev1.EnvVar{Name: "TYK_GW_CONTROLAPIPORT", Value: int32ToStr(svcPort.Port)}
		} else {
			envs = append(envs, corev1.EnvVar{Name: "TYK_GW_CONTROLAPIPORT", Value: int32ToStr(svcPort.Port)})
		}
	}

	if portIdx == -1 {
		deploy.Spec.Template.Spec.Containers[0].Ports = append(deploy.Spec.Template.Spec.Containers[0].Ports, corev1.ContainerPort{ContainerPort: svcPort.Port, Name: svcPort.Name})
	}
	deploy.Spec.Template.Spec.Containers[0].Env = envs
}

type svcReconcileReq struct {
	ownerGw        *gwv1.Gateway
	svcPort        corev1.ServicePort
	svcPorts       map[string]corev1.ServicePort
	selectorLabels map[string]string
	svcType        int
}

func (r *GatewayReconciler) reconcileSvc(ctx context.Context, req svcReconcileReq) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateSvcName(req.ownerGw.Name, req.svcType),
			Namespace: req.ownerGw.Namespace,
			Labels: map[string]string{
				gatewayNameLabel:      req.ownerGw.Name,
				gatewayNamespaceLabel: req.ownerGw.Namespace,
			},
		},
	}

	err := r.createOrUpdate(ctx, svc, func() error {
		if err := ctrl.SetControllerReference(req.ownerGw, svc, r.Scheme); err != nil {
			return err
		}

		ports := []corev1.ServicePort{}

		switch req.svcType {
		case regularSvcType:
			for _, v := range []string{string(gwv1.HTTPProtocolType), string(ListenerListenPort)} {
				if p, exists := req.svcPorts[v]; exists {
					ports = append(ports, p)
				}
			}
		case controlSvcType:
			if p, exists := req.svcPorts[string(ListenerControlAPI)]; exists {
				ports = append(ports, p)
			}
		}

		reconcileService(svc, req.selectorLabels, ports)

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwv1.Gateway{}).
		Watches(
			&gwv1.GatewayClass{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewaysFromGatewayClass),
		).
		Complete(r)
}

func (r *GatewayReconciler) findGatewaysFromGatewayClass(ctx context.Context, gwClassObj client.Object) []reconcile.Request {
	gwClass := gwClassObj.(*gwv1.GatewayClass)
	if gwClass == nil {
		return nil
	}

	if gwClass.Spec.ControllerName != controllerName {
		return nil
	}

	gatewayList := &gwv1.GatewayList{}
	if err := r.Client.List(ctx, gatewayList); err != nil {
		fmt.Println("failed to list gatewaylists")
		return nil
	}

	var requests []reconcile.Request
	for _, gateway := range gatewayList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      gateway.Name,
				Namespace: gateway.Namespace,
			},
		})
	}

	// todo: metadata listing
	return requests
}

func (r *GatewayReconciler) createOrUpdate(ctx context.Context, object client.Object, fn controllerutil.MutateFn) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, object, fn)
	return err
}

func (r *GatewayReconciler) reconcileControlApiSvc(ctx context.Context, req svcReconcileReq) error {
	if _, ok := req.svcPorts[string(ListenerControlAPI)]; ok {
		if err := r.reconcileSvc(ctx, req); err != nil {
			return err
		}

		return nil
	}

	orphanSvc := &corev1.Service{}

	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("%s-tyk-gateway-control-api-service", req.ownerGw.Name),
		Namespace: req.ownerGw.Namespace,
	}, orphanSvc)
	if err == nil {
		if err = r.Client.Delete(ctx, orphanSvc); err != nil {
			return nil
		}
	}

	return nil
}
