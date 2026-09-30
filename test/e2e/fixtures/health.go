// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fixtures

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// PodinfoDeployment is a podinfo Deployment a test creates itself, outside
// git: the same probe and progress deadline as the repo fixtures, labelled
// with labels (on the Deployment, for health.labelSelector) and selecting its
// own pods by app=name.
func PodinfoDeployment(ns, name, image string, labels map[string]string) *appsv1.Deployment {
	replicas, deadline := int32(1), int32(60)
	pods := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas:                &replicas,
			ProgressDeadlineSeconds: &deadline,
			Selector:                &metav1.LabelSelector{MatchLabels: pods},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: pods},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "podinfo",
					Image: image,
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9898}},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
							Path: "/readyz", Port: intstr.FromInt32(9898)}},
						PeriodSeconds: 2,
					},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("16Mi"),
					}},
				}}},
			},
		},
	}
}
