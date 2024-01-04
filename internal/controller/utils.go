package controller

import (
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	"strconv"
)

func service(svc *v1.Service, deployments *appsv1.Deployment) {
	if deployments == nil || svc == nil {
		return
	}

	targetPort := intstr.IntOrString{Type: intstr.Int}
	for _, container := range deployments.Spec.Template.Spec.Containers {
		lpEnv := getEnv(container.Env, "TYK_GW_LISTENPORT")
		if lpEnv.Name == "" {
			lpEnv = v1.EnvVar{Name: "TYK_GW_LISTENPORT", Value: "8080"}
		}

		lp, err := strconv.Atoi(lpEnv.Value)
		if err != nil {
			lp = 8080
		}

		targetPort.IntVal = int32(lp)
	}

	svc.Spec = v1.ServiceSpec{
		Selector: deployments.Spec.Template.ObjectMeta.Labels,
		Ports: []v1.ServicePort{
			{
				Protocol:   v1.ProtocolTCP,
				Port:       8080,
				TargetPort: targetPort,
			},
		},
	}
}

func deployment(l logr.Logger, deploy *appsv1.Deployment, envs []v1.EnvVar, configMap *v1.ConfigMap) {
	if deploy == nil {
		return
	}

	replica := int32(1)
	lpEnv := getEnv(envs, "TYK_GW_LISTENPORT")
	if lpEnv.Name == "" {
		l.Info("failed to find listen port, using default 8080")
		lpEnv = v1.EnvVar{Name: "TYK_GW_LISTENPORT", Value: "8080"}
	}

	lp, err := strconv.Atoi(lpEnv.Value)
	if err != nil {
		l.Info("failed to convert listen port to integer, using default 8080", "error", err)
		lp = 8080
	}

	listenPort := int32(lp)

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
						Ports: []v1.ContainerPort{{ContainerPort: listenPort}},
						Env:   envs,
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
