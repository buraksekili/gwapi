package controller

import (
	"strconv"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func deployment(l logr.Logger, envs []v1.EnvVar, configMap *v1.ConfigMap, labels, annotations map[gwv1.AnnotationKey]gwv1.AnnotationValue) appsv1.Deployment {
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

	deploy := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tyk-gateway",
			Namespace:   "default",
			Labels:      getRawMap(labels),
			Annotations: getRawMap(annotations),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replica,
			Selector: &metav1.LabelSelector{
				MatchLabels: getRawMap(labels),
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      getRawMap(labels),
					Annotations: getRawMap(annotations),
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
		},
	}

	if configMap != nil {
		anns := deploy.GetAnnotations()
		addToAnnotations(anns, "tyk.tyk.io/tyk-gateway-configmap-resourceVersion", configMap.ResourceVersion)
		deploy.SetAnnotations(anns)

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
				MountPath: "/etc/tyk-gateway",
			},
		}
	}

	return deploy
}

func addToAnnotations(anns map[string]string, key, value string) map[string]string {
	if anns == nil {
		anns = make(map[string]string)
	}

	anns[key] = value

	return anns
}

const indexedField = ".spec.tyk.configMapRef"

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
