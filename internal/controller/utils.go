package controller

import (
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func reconcileService(svc *v1.Service, ls map[string]string, ports []v1.ContainerPort) {
	if svc == nil {
		return
	}

	var servicePorts []v1.ServicePort

	for _, containerPort := range ports {
		servicePorts = append(servicePorts, v1.ServicePort{
			Name:       containerPort.Name,
			Port:       containerPort.ContainerPort,
			TargetPort: intstr.FromInt32(containerPort.ContainerPort),
			Protocol:   containerPort.Protocol,
		})
	}

	svc.Spec = v1.ServiceSpec{
		Selector: ls,
		Ports:    servicePorts,
	}
}

func reconcileDeployment(deploy *appsv1.Deployment, configMap *v1.ConfigMap) {
	if deploy == nil {
		return
	}

	replica := int32(1)

	deploy.Spec = appsv1.DeploymentSpec{
		Replicas: &replica,
		Selector: &metav1.LabelSelector{
			MatchLabels: deploy.Labels,
		},
		Template: v1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      deploy.Labels,
				Annotations: deploy.Annotations,
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:  "tyk-gateway",
						Image: "docker.tyk.io/tyk-gateway/tyk-gateway:v5.2.3",
					},
				},
			},
		},
	}

	if configMap != nil {
		anns := deploy.Spec.Template.GetAnnotations()
		addToAnnotations(anns, ConfigMapResourceVersionAnnKey, configMap.ResourceVersion)
		deploy.Spec.Template.SetAnnotations(anns)

		deploy.Spec.Template.Spec.Volumes = []v1.Volume{
			{
				Name: "config-volume",
				VolumeSource: v1.VolumeSource{
					ConfigMap: &v1.ConfigMapVolumeSource{
						LocalObjectReference: v1.LocalObjectReference{
							Name: configMap.ObjectMeta.Name,
						},
					},
				},
			},
		}

		deploy.Spec.Template.Spec.Containers[0].VolumeMounts = []v1.VolumeMount{
			{
				Name:      "config-volume",
				MountPath: "/opt/tyk-gateway/tyk.conf",
				SubPath:   "tyk.conf",
			},
		}
	}
}

func addToAnnotations(anns map[string]string, key, value string) map[string]string {
	if anns == nil {
		anns = make(map[string]string)
	}

	anns[key] = value

	return anns
}

// consider using json marshaling
func getRawMap(data map[gwv1.AnnotationKey]gwv1.AnnotationValue) map[string]string {
	d := make(map[string]string)
	for k, v := range data {
		d[string(k)] = string(v)
	}

	return d
}

func getEnv(envs []v1.EnvVar, envName string) v1.EnvVar {
	for _, env := range envs {
		if env.Name == envName {
			return env
		}
	}

	return v1.EnvVar{}
}

var (
	ErrNilGWClass = errors.New("Invalid Gateway Class provided; nil value")
)

func setGwClassConditionAccepted(gwClass *gwv1.GatewayClass) error {
	if gwClass == nil {
		return ErrNilGWClass
	}

	meta.SetStatusCondition(&gwClass.Status.Conditions, metav1.Condition{
		Type:               string(gwv1.GatewayClassConditionStatusAccepted),
		ObservedGeneration: gwClass.Generation,
		Status:             "True",
		Message:            string(gwv1.GatewayClassReasonAccepted),
		Reason:             string(gwv1.GatewayClassReasonAccepted),
	})

	return nil
}

func validParametersRef(ref *gwv1.ParametersReference) bool {
	if ref == nil {
		return true
	}

	return ref.Name != "" && ref.Group == "gateway" && ref.Kind == "GatewayConfiguration"
}

type updateResults string

const (
	mapUnchanged updateResults = "unchanged"
	mapUpdated   updateResults = "updated"
)

func updateAnnototation(object client.Object, key, val string) updateResults {
	if object == nil {
		return mapUnchanged
	}

	anns := object.GetAnnotations()
	if anns == nil {
		anns = make(map[string]string)
	}

	existingVal, ok := anns[key]
	if !ok || existingVal != val {
		anns[key] = val
		object.SetAnnotations(anns)
		return mapUpdated
	}

	return mapUnchanged
}

func updateLabels(object client.Object, key, val string) updateResults {
	if object == nil {
		return mapUnchanged
	}

	labels := object.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	existingVal, ok := labels[key]
	if !ok || existingVal != val {
		labels[key] = val
		object.SetLabels(labels)
		return mapUpdated
	}

	return mapUnchanged
}
